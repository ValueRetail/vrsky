package managementapi

import (
	"context"
	"database/sql"
	"hash/fnv"
)

// Cluster-wide singleton helpers for horizontal scaling (#138).
//
// When management-api runs N>=2 replicas, every replica boots the same
// background loops (the OAuth refresher, the usage rollup). Left unguarded
// they'd all fire at once: N concurrent OAuth refreshes of the same grant race
// on refresh-token rotation, and N rollups contend on the same usage_daily
// upserts. Postgres advisory locks give us a cluster-wide mutex with no extra
// infrastructure — the database is already the shared, HA (#137) coordination
// point, and this avoids pulling in Kubernetes leader-election (Leases + RBAC)
// just to gate two timers.
//
// We use *transaction-level* advisory locks (pg_try_advisory_xact_lock): the
// lock auto-releases when the surrounding transaction commits or rolls back, so
// there is no way to leak a lock on panic, context cancellation, or a dropped
// connection — unlike session-level locks, which must be explicitly released on
// the exact connection that took them.

// Fixed advisory-lock keys. These are arbitrary but must be stable and unique
// across all callers in this database. Keep them here so collisions are
// obvious at a glance.
const (
	// advisoryKeyUsageRollup gates UsageRollup.runOnce to one replica per tick.
	advisoryKeyUsageRollup int64 = 0x7635_0001
)

// advisoryKey hashes an arbitrary string (e.g. an OAuth grant ID) to a stable
// 63-bit key suitable for pg_*_advisory_lock. The top bit is cleared so the
// value is always a positive bigint.
func advisoryKey(s string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return int64(h.Sum64() & 0x7fff_ffff_ffff_ffff)
}

// withAdvisoryLock tries to acquire the transaction-scoped advisory lock for
// key, and runs fn only if it is acquired. The lock is held for the duration of
// fn and released automatically when the transaction ends.
//
// Return semantics:
//   - acquired=false, err=nil  → another replica/connection holds the lock;
//     fn was NOT run. Callers should treat this as "someone else is handling
//     it" and skip quietly.
//   - acquired=true,  err=nil  → fn ran and succeeded.
//   - acquired=true,  err!=nil → fn ran and returned err (or commit failed).
//
// fn does not need to use the locking transaction; the lock acts purely as a
// cross-replica mutex. db must be non-nil — callers with a nil db (e.g. unit
// tests with no database) should bypass this helper and run fn directly.
func withAdvisoryLock(ctx context.Context, db *sql.DB, key int64, fn func(context.Context) error) (acquired bool, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	// Rollback is a no-op after a successful Commit; on every other path it
	// ends the transaction and releases the advisory lock.
	defer func() { _ = tx.Rollback() }()

	var got bool
	if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", key).Scan(&got); err != nil {
		return false, err
	}
	if !got {
		return false, nil
	}

	if err := fn(ctx); err != nil {
		return true, err
	}
	// Commit releases the lock; the work fn did on its own connection(s) has
	// already been committed independently.
	return true, tx.Commit()
}
