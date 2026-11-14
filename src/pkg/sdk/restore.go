package sdk

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrRestoreList means RestoreRunningErr could not list the running
// connections, as opposed to finding none (#725).
var ErrRestoreList = errors.New("could not list running pipelines to restore")

// RestoreRunning calls start for every connection the database says is
// running, so a standing consumer brings its pipelines back after a restart
// without anyone redeploying them (plans/stable-connections.md).
//
// Connection start/stop are NATS commands and are not persisted: before this,
// a connector rollout, crash or node move silently stopped every pipeline on
// that service until someone redeployed it from the builder — and a webhook
// source answered 404 meanwhile.
//
// typeHint is the consumer's node type ("http", "file", "business_central");
// rows whose graph does not mention it are skipped without being read in
// full. start is the same function the start command runs, with its own
// "not mine" check. start runs on the caller's goroutine while the
// start-command subscription can call it for the same connection at the same
// time, so start must claim the connection with a StartGuard before its slow
// part (#375, #397): then restoring is idempotent, a start command arriving
// during the scan starts nothing twice, and a stop during either start wins.
// Call it after the command subscriptions are in place, so nothing published
// meanwhile is missed. Returns how many rows were offered to start.
//
// A list query that fails because the database does not answer
// (IsTransientDBError) is tried again on the default RetryPolicy schedule
// until it works, ctx ends or the policy gives up (#725); it blocks the
// caller meanwhile. Any other list error ends it at once. A list that never
// worked is logged at Error and returns 0; RestoreRunningErr returns the
// error too, for a caller that wants to tell it from "nothing running".
func RestoreRunning(ctx context.Context, db *sql.DB, logger *slog.Logger, typeHint string,
	start func(ctx context.Context, connectionID, tenantID string)) int {
	n, _ := RestoreRunningErr(ctx, db, logger, typeHint, RetryPolicy{}, start)
	return n
}

// RestoreRunningErr is RestoreRunning with the retry timing given and the
// failure returned: an error wrapping ErrRestoreList (and the last query
// error) when the list could not be read before ctx ended or the policy gave
// up. It is logged at Error as well. A nil db restores nothing and is no
// error.
func RestoreRunningErr(ctx context.Context, db *sql.DB, logger *slog.Logger, typeHint string, policy RetryPolicy,
	start func(ctx context.Context, connectionID, tenantID string)) (int, error) {
	if db == nil {
		return 0, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	first := time.Now()
	var restore []restoreRow
	for attempt := 1; ; attempt++ {
		todo, lerr := listRunning(ctx, db, typeHint, logger)
		if lerr == nil {
			restore = todo
			break
		}
		err := fmt.Errorf("%w: %w", ErrRestoreList, lerr)
		if !IsTransientDBError(lerr) && ctx.Err() == nil {
			logger.Error("Could not list running pipelines to restore; nothing restored. The query failed for a reason a retry does not fix",
				"type", typeHint, "error", err, "attempts", attempt)
			return 0, err
		}
		if ctx.Err() != nil || policy.givenUp(first) {
			logger.Error("Could not list running pipelines to restore; nothing restored. Restart the consumer once the database answers",
				"type", typeHint, "error", err, "attempts", attempt)
			return 0, err
		}
		d := policy.Delay(attempt)
		logger.Error("Could not list running pipelines to restore; trying again", "type", typeHint, "error", err, "retry_in", d)
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			logger.Error("Could not list running pipelines to restore; nothing restored (shutting down)",
				"type", typeHint, "error", err, "attempts", attempt)
			return 0, err
		case <-t.C:
		}
	}
	for _, p := range restore {
		start(ctx, p.id, p.tenant)
	}
	logger.Info("Restored running pipelines", "type", typeHint, "count", len(restore))
	return len(restore), nil
}

type restoreRow struct{ id, tenant string }

// listRunning is one try at the restore list. An error while iterating the
// rows fails the whole list: a partial one would restore some pipelines and
// silently leave the rest.
func listRunning(ctx context.Context, db *sql.DB, typeHint string, logger *slog.Logger) ([]restoreRow, error) {
	// lint:tenant-ok — fleet-wide boot scan; each row's own tenant scopes everything start does.
	rows, err := db.QueryContext(ctx, `
		SELECT id::text, tenant_id FROM connections
		 WHERE status = 'running' AND nodes::text LIKE '%"' || $1 || '"%'`, typeHint)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var todo []restoreRow
	for rows.Next() {
		var p restoreRow
		if err := rows.Scan(&p.id, &p.tenant); err != nil {
			logger.Error("Could not read a running pipeline to restore", "error", err)
			continue
		}
		todo = append(todo, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return todo, nil
}
