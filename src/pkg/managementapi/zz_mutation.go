package managementapi

import (
	"context"
	"database/sql"
)

// MutationTenantUnsafe is a mutation for #446: it deletes a secret without a
// tenant_id filter, which make lint-tenant must reject.
func MutationTenantUnsafe(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM secrets WHERE id = $1`, id)
	return err
}
