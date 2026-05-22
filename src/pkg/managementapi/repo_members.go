package managementapi

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ===== Types =====

// TenantMember is the joined projection of (user_tenant_roles, users) used
// by the members admin UI (#69).
type TenantMember struct {
	UserID    string     `json:"user_id"`
	TenantID  string     `json:"tenant_id"`
	Email     string     `json:"email"`
	FullName  string     `json:"full_name,omitempty"`
	Role      string     `json:"role"`
	InvitedAt time.Time  `json:"invited_at"`
	JoinedAt  *time.Time `json:"joined_at,omitempty"`
}

// ===== Repository surface =====

// ErrLastOwner indicates a role change or removal would leave the tenant
// with zero owners — the caller should refuse with 409.
var ErrLastOwner = errors.New("operation would leave the tenant without an owner")

// ListTenantMembers returns every (user, role) tuple for one tenant.
func (r *PostgresRepository) ListTenantMembers(ctx context.Context, tenantID string) ([]*TenantMember, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT utr.user_id, utr.tenant_id, u.email, COALESCE(u.full_name, ''),
		       utr.role, utr.invited_at, utr.joined_at
		FROM user_tenant_roles utr
		JOIN users u ON u.id = utr.user_id
		WHERE utr.tenant_id = $1 AND u.deleted_at IS NULL
		ORDER BY utr.invited_at ASC
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TenantMember
	for rows.Next() {
		m := &TenantMember{}
		var joined sql.NullTime
		if err := rows.Scan(
			&m.UserID, &m.TenantID, &m.Email, &m.FullName,
			&m.Role, &m.InvitedAt, &joined,
		); err != nil {
			return nil, err
		}
		if joined.Valid {
			t := joined.Time
			m.JoinedAt = &t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetTenantMemberRole changes a user's role in a tenant. Refuses if the
// change would leave the tenant with zero owners.
func (r *PostgresRepository) SetTenantMemberRole(ctx context.Context, tenantID, userID, newRole string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Read the current row so we can detect owner-demotion.
	var current string
	err = tx.QueryRowContext(ctx, `
		SELECT role FROM user_tenant_roles
		WHERE tenant_id = $1 AND user_id = $2
	`, tenantID, userID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("user is not a member of this tenant")
	}
	if err != nil {
		return err
	}
	if current == newRole {
		return nil
	}
	// If demoting away from owner, ensure another owner exists.
	if current == "owner" && newRole != "owner" {
		var owners int
		err = tx.QueryRowContext(ctx,
			`SELECT count(*) FROM user_tenant_roles WHERE tenant_id = $1 AND role = 'owner'`,
			tenantID,
		).Scan(&owners)
		if err != nil {
			return err
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE user_tenant_roles SET role = $3
		WHERE tenant_id = $1 AND user_id = $2
	`, tenantID, userID, newRole); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveTenantMember deletes the (user, tenant) row. Refuses if it would
// leave zero owners.
func (r *PostgresRepository) RemoveTenantMember(ctx context.Context, tenantID, userID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var current string
	err = tx.QueryRowContext(ctx, `
		SELECT role FROM user_tenant_roles
		WHERE tenant_id = $1 AND user_id = $2
	`, tenantID, userID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // already gone — idempotent
	}
	if err != nil {
		return err
	}
	if current == "owner" {
		var owners int
		err = tx.QueryRowContext(ctx,
			`SELECT count(*) FROM user_tenant_roles WHERE tenant_id = $1 AND role = 'owner'`,
			tenantID,
		).Scan(&owners)
		if err != nil {
			return err
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM user_tenant_roles WHERE tenant_id = $1 AND user_id = $2
	`, tenantID, userID); err != nil {
		return err
	}
	return tx.Commit()
}
