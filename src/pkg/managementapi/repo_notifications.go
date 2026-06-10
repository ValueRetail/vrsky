package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ValueRetail/vrsky/pkg/crypto"
)

// Phase 3A (#84): per-tenant notification targets — where a tenant's alerts
// are delivered. Secrets (Slack webhook URL, PagerDuty routing key, webhook
// HMAC key) are encrypted into the secrets table and referenced by secret_id,
// mirroring the OAuth client-secret pattern (repo_oauth.go).

// ErrNotificationTargetNotFound is returned for lookups of missing targets.
var ErrNotificationTargetNotFound = errors.New("notification target not found")

// NotificationTargetConfig is the non-secret per-target configuration stored
// as JSONB. Which fields apply depends on Type: email → Email; webhook → URL.
type NotificationTargetConfig struct {
	Email       string `json:"email,omitempty"`        // email: recipient address
	URL         string `json:"url,omitempty"`          // webhook: destination URL (non-secret)
	Platform    bool   `json:"platform,omitempty"`     // also receive platform-level alerts (no tenant_id label)
	MinSeverity string `json:"min_severity,omitempty"` // "" | info | warning | critical
}

// NotificationTarget is one delivery destination for alerts.
type NotificationTarget struct {
	ID        string
	TenantID  string
	Name      string
	Type      string // slack | email | pagerduty | webhook
	Config    NotificationTargetConfig
	SecretID  string // "" when the type carries no secret
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// notificationSecretName labels the encrypted secret row for a target.
func notificationSecretName(targetName string) string {
	return "notification-target:" + targetName
}

// CreateNotificationTarget inserts a target; when secret is non-empty it is
// encrypted and stored first (same transaction).
func (r *PostgresRepository) CreateNotificationTarget(ctx context.Context, t *NotificationTarget, secret string) error {
	cfgJSON, err := json.Marshal(t.Config)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var secretID sql.NullString
	if secret != "" {
		key, err := crypto.Key()
		if err != nil {
			return err
		}
		ct, err := crypto.Encrypt(secret, key)
		if err != nil {
			return fmt.Errorf("encrypt notification secret: %w", err)
		}
		id, err := createSecretTx(ctx, tx, t.TenantID, notificationSecretName(t.Name), ct)
		if err != nil {
			return fmt.Errorf("persist notification secret: %w", err)
		}
		secretID = sql.NullString{String: id, Valid: true}
		t.SecretID = id
	}

	const q = `
		INSERT INTO notification_targets (tenant_id, name, type, config, secret_id, enabled)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`
	if err := tx.QueryRowContext(ctx, q,
		t.TenantID, t.Name, t.Type, cfgJSON, secretID, t.Enabled,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// scanNotificationTarget reads one row (column order of selectTargetCols).
const selectTargetCols = `id, tenant_id, name, type, config, COALESCE(secret_id::text, ''), enabled, created_at, updated_at`

func scanNotificationTarget(scan func(dest ...interface{}) error) (*NotificationTarget, error) {
	var t NotificationTarget
	var cfgJSON []byte
	if err := scan(&t.ID, &t.TenantID, &t.Name, &t.Type, &cfgJSON, &t.SecretID, &t.Enabled, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	if len(cfgJSON) > 0 {
		if err := json.Unmarshal(cfgJSON, &t.Config); err != nil {
			return nil, fmt.Errorf("decode config: %w", err)
		}
	}
	return &t, nil
}

// ListNotificationTargets returns every target owned by a tenant.
func (r *PostgresRepository) ListNotificationTargets(ctx context.Context, tenantID string) ([]*NotificationTarget, error) {
	q := `SELECT ` + selectTargetCols + ` FROM notification_targets WHERE tenant_id = $1 ORDER BY created_at`
	rows, err := r.db.QueryContext(ctx, q, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NotificationTarget
	for rows.Next() {
		t, err := scanNotificationTarget(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetNotificationTarget returns one target scoped to a tenant.
func (r *PostgresRepository) GetNotificationTarget(ctx context.Context, tenantID, id string) (*NotificationTarget, error) {
	q := `SELECT ` + selectTargetCols + ` FROM notification_targets WHERE tenant_id = $1 AND id = $2`
	t, err := scanNotificationTarget(r.db.QueryRowContext(ctx, q, tenantID, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotificationTargetNotFound
	}
	return t, err
}

// UpdateNotificationTarget rewrites the mutable fields; a non-empty secret is
// re-encrypted into a fresh secrets row (the old reference is replaced).
func (r *PostgresRepository) UpdateNotificationTarget(ctx context.Context, t *NotificationTarget, secret string) error {
	cfgJSON, err := json.Marshal(t.Config)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if secret != "" {
		key, err := crypto.Key()
		if err != nil {
			return err
		}
		ct, err := crypto.Encrypt(secret, key)
		if err != nil {
			return fmt.Errorf("encrypt notification secret: %w", err)
		}
		id, err := createSecretTx(ctx, tx, t.TenantID, notificationSecretName(t.Name), ct)
		if err != nil {
			return fmt.Errorf("persist notification secret: %w", err)
		}
		t.SecretID = id
	}

	var secretID sql.NullString
	if t.SecretID != "" {
		secretID = sql.NullString{String: t.SecretID, Valid: true}
	}
	const q = `
		UPDATE notification_targets
		SET name = $1, type = $2, config = $3, secret_id = $4, enabled = $5, updated_at = NOW()
		WHERE tenant_id = $6 AND id = $7`
	res, err := tx.ExecContext(ctx, q, t.Name, t.Type, cfgJSON, secretID, t.Enabled, t.TenantID, t.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotificationTargetNotFound
	}
	return tx.Commit()
}

// DeleteNotificationTarget removes one target scoped to a tenant.
func (r *PostgresRepository) DeleteNotificationTarget(ctx context.Context, tenantID, id string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM notification_targets WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotificationTargetNotFound
	}
	return nil
}

// ListNotificationTargetsForDispatch returns the enabled targets an incoming
// alert should fan out to. Tenant alerts (tenantID != "") go to that tenant's
// targets; platform alerts (tenantID == "") go to every enabled target flagged
// platform=true across tenants — that cross-tenant read is the intended
// routing model for platform-level alerts (disk, NATS, mgmt-api, certs).
func (r *PostgresRepository) ListNotificationTargetsForDispatch(ctx context.Context, tenantID string) ([]*NotificationTarget, error) {
	var (
		q    string
		args []interface{}
	)
	if tenantID != "" {
		q = `SELECT ` + selectTargetCols + ` FROM notification_targets WHERE tenant_id = $1 AND enabled`
		args = []interface{}{tenantID}
	} else {
		// lint:tenant-ok — platform alerts intentionally fan out to every
		// tenant's platform-flagged targets (see routing model above).
		q = `SELECT ` + selectTargetCols + ` FROM notification_targets WHERE enabled AND config->>'platform' = 'true'`
	}
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NotificationTarget
	for rows.Next() {
		t, err := scanNotificationTarget(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ResolveNotificationSecret decrypts a target's secret (webhook URL / routing
// key / HMAC key). Returns "" when the target has none.
func (r *PostgresRepository) ResolveNotificationSecret(ctx context.Context, t *NotificationTarget) (string, error) {
	if t.SecretID == "" {
		return "", nil
	}
	ct, err := r.GetSecretCiphertext(ctx, t.TenantID, t.SecretID)
	if err != nil {
		return "", err
	}
	key, err := crypto.Key()
	if err != nil {
		return "", err
	}
	return crypto.Decrypt(ct, key)
}
