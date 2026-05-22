package managementapi

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ============================================
// Phase 1A (#66): Secrets Repository
// ============================================

// ErrSecretNotFound is returned by the Get/Update/Delete/Rotate paths when
// the (tenant_id, id) tuple does not exist.
var ErrSecretNotFound = errors.New("secret not found")

// Secret is the persisted form of an encrypted credential. The plaintext is
// never read from this struct — callers receive only metadata except in the
// worker decryption path, which uses GetSecretCiphertext.
type Secret struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"-"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	RotatedAt  *time.Time `json:"rotated_at,omitempty"`
}

// CreateSecret stores a new ciphertext for a tenant. The caller is responsible
// for encrypting plaintext through pkg/crypto before calling.
func (r *PostgresRepository) CreateSecret(ctx context.Context, tenantID, name, ciphertext string) (*Secret, error) {
	s := &Secret{TenantID: tenantID, Name: name}
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO secrets (tenant_id, name, ciphertext)
		VALUES ($1, $2, $3)
		RETURNING id, created_at, updated_at
	`, tenantID, name, ciphertext).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// GetSecret returns secret metadata (no ciphertext) for the given tenant.
func (r *PostgresRepository) GetSecret(ctx context.Context, tenantID, id string) (*Secret, error) {
	s := &Secret{TenantID: tenantID}
	var rotated sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, created_at, updated_at, rotated_at
		FROM secrets
		WHERE id = $1 AND tenant_id = $2
	`, id, tenantID).Scan(&s.ID, &s.Name, &s.CreatedAt, &s.UpdatedAt, &rotated)
	if err == sql.ErrNoRows {
		return nil, ErrSecretNotFound
	}
	if err != nil {
		return nil, err
	}
	if rotated.Valid {
		s.RotatedAt = &rotated.Time
	}
	return s, nil
}

// GetSecretCiphertext returns the raw ciphertext for a (tenant, id) pair.
// Used by workers when decrypting credentials at startup.
func (r *PostgresRepository) GetSecretCiphertext(ctx context.Context, tenantID, id string) (string, error) {
	var ct string
	err := r.db.QueryRowContext(ctx, `
		SELECT ciphertext FROM secrets WHERE id = $1 AND tenant_id = $2
	`, id, tenantID).Scan(&ct)
	if err == sql.ErrNoRows {
		return "", ErrSecretNotFound
	}
	return ct, err
}

// ListSecrets returns metadata for all secrets owned by a tenant.
func (r *PostgresRepository) ListSecrets(ctx context.Context, tenantID string, limit, offset int) ([]*Secret, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, created_at, updated_at, rotated_at
		FROM secrets
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, tenantID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Secret
	for rows.Next() {
		s := &Secret{TenantID: tenantID}
		var rotated sql.NullTime
		if err := rows.Scan(&s.ID, &s.Name, &s.CreatedAt, &s.UpdatedAt, &rotated); err != nil {
			return nil, err
		}
		if rotated.Valid {
			s.RotatedAt = &rotated.Time
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateSecret rewrites name and/or ciphertext. Passing empty strings leaves
// the corresponding column unchanged. updated_at is always bumped; rotated_at
// is set only when ciphertext changes.
func (r *PostgresRepository) UpdateSecret(ctx context.Context, tenantID, id, name, ciphertext string) (*Secret, error) {
	if name == "" && ciphertext == "" {
		return r.GetSecret(ctx, tenantID, id)
	}
	// Build a partial UPDATE without sprintf'ing user input.
	var (
		newName    sql.NullString
		newCipher  sql.NullString
		setRotated bool
	)
	if name != "" {
		newName = sql.NullString{String: name, Valid: true}
	}
	if ciphertext != "" {
		newCipher = sql.NullString{String: ciphertext, Valid: true}
		setRotated = true
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE secrets
		SET name       = COALESCE($3, name),
		    ciphertext = COALESCE($4, ciphertext),
		    updated_at = NOW(),
		    rotated_at = CASE WHEN $5 THEN NOW() ELSE rotated_at END
		WHERE id = $1 AND tenant_id = $2
	`, id, tenantID, newName, newCipher, setRotated)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrSecretNotFound
	}
	return r.GetSecret(ctx, tenantID, id)
}

// DeleteSecret removes a secret. Refuses if the secret is referenced from any
// connection's nodes config; the caller surfaces this as 409 Conflict.
// Returns the list of connection IDs that reference the secret, if any.
func (r *PostgresRepository) DeleteSecret(ctx context.Context, tenantID, id string) ([]string, error) {
	// 1. Find references. The JSONB query searches for any string value
	// (anywhere in nodes / edges) equal to the secret UUID, plus the DSN
	// placeholder form {secret:<uuid>}.
	rows, err := r.db.QueryContext(ctx, `
		SELECT id FROM connections
		WHERE tenant_id = $1
		  AND (nodes::text LIKE '%' || $2 || '%' OR edges::text LIKE '%' || $2 || '%')
	`, tenantID, id)
	if err != nil {
		return nil, err
	}
	var refs []string
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, cid)
	}
	rows.Close()
	if len(refs) > 0 {
		return refs, nil
	}
	// 2. No references — delete.
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM secrets WHERE id = $1 AND tenant_id = $2
	`, id, tenantID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrSecretNotFound
	}
	return nil, nil
}
