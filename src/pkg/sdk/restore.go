package sdk

import (
	"context"
	"database/sql"
	"log/slog"
)

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
// "not mine" and "already active" checks, so restoring is idempotent and a
// start command arriving during the scan is harmless. Call it after the
// command subscriptions are in place, so nothing published meanwhile is
// missed. Returns how many rows were offered to start.
func RestoreRunning(ctx context.Context, db *sql.DB, logger *slog.Logger, typeHint string,
	start func(ctx context.Context, connectionID, tenantID string)) int {
	if db == nil {
		return 0
	}
	if logger == nil {
		logger = slog.Default()
	}
	// lint:tenant-ok — fleet-wide boot scan; each row's own tenant scopes everything start does.
	rows, err := db.QueryContext(ctx, `
		SELECT id::text, tenant_id FROM connections
		 WHERE status = 'running' AND nodes::text LIKE '%"' || $1 || '"%'`, typeHint)
	if err != nil {
		logger.Error("Could not list running pipelines to restore", "error", err)
		return 0
	}
	type pair struct{ id, tenant string }
	var todo []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.tenant); err != nil {
			logger.Error("Could not read a running pipeline to restore", "error", err)
			continue
		}
		todo = append(todo, p)
	}
	_ = rows.Close()
	for _, p := range todo {
		start(ctx, p.id, p.tenant)
	}
	logger.Info("Restored running pipelines", "type", typeHint, "count", len(todo))
	return len(todo)
}
