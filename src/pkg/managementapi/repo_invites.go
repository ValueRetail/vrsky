package managementapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Invite lifecycle for member onboarding (#130). A pending invite lets an
// owner invite an email that has not registered yet; it can be listed, resent
// (new token + expiry), revoked, and accepted once the invitee signs up.

// InviteTTL is how long a freshly created/resent invite stays valid.
const InviteTTL = 7 * 24 * time.Hour

// ErrInviteNotFound is returned when no matching invite row exists.
var ErrInviteNotFound = errors.New("invite not found")

// ErrInvitePending is returned when an outstanding pending invite already
// exists for the same (tenant, email).
var ErrInvitePending = errors.New("a pending invite already exists for that email")

// TenantInvite mirrors one row of tenant_invites. Token is omitted from JSON
// except on the create/resend responses (where it forms the accept link).
type TenantInvite struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	Email      string     `json:"email"`
	Role       string     `json:"role"`
	Token      string     `json:"token,omitempty"`
	Status     string     `json:"status"`
	InvitedBy  string     `json:"invited_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
}

// InviteStore is the narrow persistence surface the invite handlers need. It is
// satisfied by *PostgresRepository; the handlers obtain it via a type assertion
// on h.repo, so the broad Repository interface (and its many test mocks) is
// left untouched.
type InviteStore interface {
	CreateInvite(ctx context.Context, tenantID, email, role, invitedBy string) (*TenantInvite, error)
	ListInvites(ctx context.Context, tenantID string) ([]*TenantInvite, error)
	GetInvite(ctx context.Context, tenantID, inviteID string) (*TenantInvite, error)
	GetInviteByToken(ctx context.Context, token string) (*TenantInvite, error)
	ResendInvite(ctx context.Context, tenantID, inviteID string) (*TenantInvite, error)
	RevokeInvite(ctx context.Context, tenantID, inviteID string) error
	MarkInviteAccepted(ctx context.Context, inviteID string) error
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505), matching the string-based detection used
// elsewhere in this package (e.g. repo_user.go).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key value") ||
		strings.Contains(msg, "unique constraint")
}

func newInviteToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CreateInvite inserts a pending invite. Returns ErrInvitePending if an
// outstanding pending invite already exists for (tenant, email).
func (r *PostgresRepository) CreateInvite(ctx context.Context, tenantID, email, role, invitedBy string) (*TenantInvite, error) {
	token, err := newInviteToken()
	if err != nil {
		return nil, err
	}
	var invitedByArg any
	if strings.TrimSpace(invitedBy) != "" {
		invitedByArg = invitedBy
	}
	inv := &TenantInvite{}
	// lint:tenant-ok — INSERT carries tenant_id in the row.
	err = r.db.QueryRowContext(ctx, `
		INSERT INTO tenant_invites (tenant_id, email, role, token, status, invited_by, expires_at)
		VALUES ($1, lower($2), $3, $4, 'pending', $5, NOW() + ($6 || ' seconds')::interval)
		RETURNING id, tenant_id, email, role, token, status, COALESCE(invited_by::text,''), created_at, expires_at
	`, tenantID, email, role, token, invitedByArg, fmt.Sprintf("%d", int(InviteTTL.Seconds()))).Scan(
		&inv.ID, &inv.TenantID, &inv.Email, &inv.Role, &inv.Token, &inv.Status,
		&inv.InvitedBy, &inv.CreatedAt, &inv.ExpiresAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrInvitePending
		}
		return nil, err
	}
	return inv, nil
}

// ListInvites returns all invites for a tenant, newest first.
func (r *PostgresRepository) ListInvites(ctx context.Context, tenantID string) ([]*TenantInvite, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, email, role, status, COALESCE(invited_by::text,''),
		       created_at, expires_at, accepted_at
		FROM tenant_invites
		WHERE tenant_id = $1
		ORDER BY created_at DESC
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*TenantInvite
	for rows.Next() {
		inv := &TenantInvite{}
		if err := rows.Scan(&inv.ID, &inv.TenantID, &inv.Email, &inv.Role, &inv.Status,
			&inv.InvitedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// GetInvite fetches one invite scoped to its tenant.
func (r *PostgresRepository) GetInvite(ctx context.Context, tenantID, inviteID string) (*TenantInvite, error) {
	inv := &TenantInvite{}
	err := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, email, role, status, COALESCE(invited_by::text,''),
		       created_at, expires_at, accepted_at
		FROM tenant_invites
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, inviteID).Scan(&inv.ID, &inv.TenantID, &inv.Email, &inv.Role, &inv.Status,
		&inv.InvitedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInviteNotFound
	}
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// GetInviteByToken fetches an invite by its opaque token (used by accept). Not
// tenant-scoped by design — the token IS the capability, and the tenant is
// recovered from the row.
func (r *PostgresRepository) GetInviteByToken(ctx context.Context, token string) (*TenantInvite, error) {
	inv := &TenantInvite{}
	// lint:tenant-ok — lookup by unique capability token; tenant recovered from row.
	err := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, email, role, status, COALESCE(invited_by::text,''),
		       created_at, expires_at, accepted_at
		FROM tenant_invites
		WHERE token = $1
	`, token).Scan(&inv.ID, &inv.TenantID, &inv.Email, &inv.Role, &inv.Status,
		&inv.InvitedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInviteNotFound
	}
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// ResendInvite regenerates the token and extends expiry for a pending invite,
// returning the refreshed row (with the new token). Only pending invites can be
// resent.
func (r *PostgresRepository) ResendInvite(ctx context.Context, tenantID, inviteID string) (*TenantInvite, error) {
	token, err := newInviteToken()
	if err != nil {
		return nil, err
	}
	inv := &TenantInvite{}
	err = r.db.QueryRowContext(ctx, `
		UPDATE tenant_invites
		   SET token = $3, expires_at = NOW() + ($4 || ' seconds')::interval, created_at = NOW()
		 WHERE tenant_id = $1 AND id = $2 AND status = 'pending'
		RETURNING id, tenant_id, email, role, token, status, COALESCE(invited_by::text,''), created_at, expires_at
	`, tenantID, inviteID, token, fmt.Sprintf("%d", int(InviteTTL.Seconds()))).Scan(
		&inv.ID, &inv.TenantID, &inv.Email, &inv.Role, &inv.Token, &inv.Status,
		&inv.InvitedBy, &inv.CreatedAt, &inv.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInviteNotFound
	}
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// RevokeInvite marks a pending invite revoked. Idempotent-ish: a missing or
// non-pending invite returns ErrInviteNotFound.
func (r *PostgresRepository) RevokeInvite(ctx context.Context, tenantID, inviteID string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE tenant_invites SET status = 'revoked'
		 WHERE tenant_id = $1 AND id = $2 AND status = 'pending'
	`, tenantID, inviteID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrInviteNotFound
	}
	return nil
}

// MarkInviteAccepted flips an invite to accepted. Not tenant-scoped: the caller
// (accept handler) has already validated the invite via its token.
func (r *PostgresRepository) MarkInviteAccepted(ctx context.Context, inviteID string) error {
	// lint:tenant-ok — invite already authorized by token in the accept handler.
	_, err := r.db.ExecContext(ctx, `
		UPDATE tenant_invites SET status = 'accepted', accepted_at = NOW()
		 WHERE id = $1
	`, inviteID)
	return err
}
