package managementapi

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// GenerateSlug creates a URL-safe slug from a name with a unique suffix.
// "Acme Corp" -> "acme-corp-a3f2"
func GenerateSlug(name string) string {
	slug := strings.ToLower(name)
	// Replace non-alphanumeric with dashes
	reg := regexp.MustCompile(`[^a-z0-9]+`)
	slug = reg.ReplaceAllString(slug, "-")
	// Trim leading/trailing dashes
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "workspace"
	}
	// Append 4-char UUID suffix for uniqueness
	suffix := uuid.New().String()[:4]
	return slug + "-" + suffix
}

// CreateTenant creates a new tenant and assigns the user as owner in a single transaction
func (r *PostgresRepository) CreateTenant(ctx context.Context, userID, name, slug string) (*Tenant, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var tenant Tenant
	err = tx.QueryRowContext(ctx, `
		INSERT INTO tenants (name, slug, owner_id, subscription_plan, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'free', 'provisioning', NOW(), NOW())
		RETURNING id, name, slug, owner_id, subscription_plan, is_verified,
		          max_integrations, max_messages_per_month, status, nats_slug, created_at, updated_at
	`, name, slug, userID).Scan(
		&tenant.ID, &tenant.Name, &tenant.Slug, &tenant.OwnerID,
		&tenant.SubscriptionPlan, &tenant.IsVerified,
		&tenant.MaxIntegrations, &tenant.MaxMessagesPerMonth,
		&tenant.Status, &tenant.NATSSlug,
		&tenant.CreatedAt, &tenant.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "23505") {
			return nil, ErrSlugAlreadyExists
		}
		return nil, err
	}

	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO user_tenant_roles (user_id, tenant_id, role, invited_at, joined_at)
		VALUES ($1, $2, 'owner', $3, $3)
	`, userID, tenant.ID, now)
	if err != nil {
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}

	return &tenant, nil
}

// GetTenantByID fetches a tenant by ID
func (r *PostgresRepository) GetTenantByID(ctx context.Context, tenantID string) (*Tenant, error) {
	var t Tenant
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, slug, owner_id, subscription_plan, is_verified,
		       max_integrations, max_messages_per_month, status, nats_slug, created_at, updated_at
		FROM tenants
		WHERE id = $1 AND deleted_at IS NULL
	`, tenantID).Scan(
		&t.ID, &t.Name, &t.Slug, &t.OwnerID,
		&t.SubscriptionPlan, &t.IsVerified,
		&t.MaxIntegrations, &t.MaxMessagesPerMonth,
		&t.Status, &t.NATSSlug,
		&t.CreatedAt, &t.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetUserTenants returns all tenants a user has access to, including their role
func (r *PostgresRepository) GetUserTenants(ctx context.Context, userID string) ([]*TenantResponse, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT t.id, t.name, t.slug, t.owner_id, t.subscription_plan, t.is_verified,
		       t.max_integrations, t.max_messages_per_month, t.status, t.nats_slug, utr.role,
		       t.created_at, t.updated_at
		FROM tenants t
		JOIN user_tenant_roles utr ON t.id = utr.tenant_id
		WHERE utr.user_id = $1 AND t.deleted_at IS NULL
		ORDER BY t.created_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tenants []*TenantResponse
	for rows.Next() {
		var tr TenantResponse
		if err := rows.Scan(
			&tr.ID, &tr.Name, &tr.Slug, &tr.OwnerID,
			&tr.SubscriptionPlan, &tr.IsVerified,
			&tr.MaxIntegrations, &tr.MaxMessagesPerMonth, &tr.Status, &tr.NATSSlug, &tr.UserRole,
			&tr.CreatedAt, &tr.UpdatedAt,
		); err != nil {
			return nil, err
		}
		tenants = append(tenants, &tr)
	}
	return tenants, rows.Err()
}

// GetUserTenantRole returns the user's role in a tenant, or empty string if not a member
func (r *PostgresRepository) GetUserTenantRole(ctx context.Context, userID, tenantID string) (string, error) {
	var role string
	err := r.db.QueryRowContext(ctx, `
		SELECT role FROM user_tenant_roles
		WHERE user_id = $1 AND tenant_id = $2
	`, userID, tenantID).Scan(&role)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return role, nil
}

// DeleteTenant soft-deletes a tenant
func (r *PostgresRepository) DeleteTenant(ctx context.Context, tenantID string) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE tenants SET deleted_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
	`, tenantID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrTenantNotFound
	}
	return nil
}

// ============================================
// Phase 2: Provisioning & API Keys
// ============================================

// UpdateTenantStatus updates the tenant's status and optional NATS slug
func (r *PostgresRepository) UpdateTenantStatus(ctx context.Context, tenantID, status string, natsSlug *string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE tenants SET status = $2, nats_slug = $3, updated_at = NOW()
		WHERE id = $1
	`, tenantID, status, natsSlug)
	return err
}

// CreateProvisioningJob creates a new provisioning job record
func (r *PostgresRepository) CreateProvisioningJob(ctx context.Context, tenantID string) (*ProvisioningJob, error) {
	var job ProvisioningJob
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO provisioning_jobs (tenant_id, status, progress, current_step)
		VALUES ($1, 'queued', 0, 'Queued')
		RETURNING id, tenant_id, status, progress, current_step, error_message, created_at, completed_at
	`, tenantID).Scan(
		&job.ID, &job.TenantID, &job.Status, &job.Progress,
		&job.CurrentStep, &job.ErrorMsg, &job.CreatedAt, &job.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// UpdateProvisioningJob updates the status, progress, and step of a job
func (r *PostgresRepository) UpdateProvisioningJob(ctx context.Context, jobID, status string, progress int, step, errMsg string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE provisioning_jobs SET status = $2, progress = $3, current_step = $4, error_message = $5
		WHERE id = $1
	`, jobID, status, progress, step, errMsg)
	return err
}

// UpdateProvisioningJobCompleted sets the completed_at timestamp
func (r *PostgresRepository) UpdateProvisioningJobCompleted(ctx context.Context, jobID string, completedAt *time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE provisioning_jobs SET completed_at = $2
		WHERE id = $1
	`, jobID, completedAt)
	return err
}

// GetLatestProvisioningJob returns the most recent provisioning job for a tenant
func (r *PostgresRepository) GetLatestProvisioningJob(ctx context.Context, tenantID string) (*ProvisioningJob, error) {
	var job ProvisioningJob
	err := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, status, progress, current_step, COALESCE(error_message, ''), created_at, completed_at
		FROM provisioning_jobs
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(
		&job.ID, &job.TenantID, &job.Status, &job.Progress,
		&job.CurrentStep, &job.ErrorMsg, &job.CreatedAt, &job.CompletedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// UpsertTenantAPIKey creates or replaces an API key for a tenant
func (r *PostgresRepository) UpsertTenantAPIKey(ctx context.Context, tenantID, keyHash string) (*TenantAPIKey, error) {
	var key TenantAPIKey
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO tenant_api_keys (tenant_id, api_key_hash)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id) DO UPDATE
			SET api_key_hash = EXCLUDED.api_key_hash,
			    rotated_at = NOW(),
			    is_active = true
		RETURNING id, tenant_id, created_at, rotated_at, is_active
	`, tenantID, keyHash).Scan(
		&key.ID, &key.TenantID, &key.CreatedAt, &key.RotatedAt, &key.IsActive,
	)
	if err != nil {
		return nil, err
	}
	return &key, nil
}

// GetTenantAPIKey retrieves the current API key metadata for a tenant
func (r *PostgresRepository) GetTenantAPIKey(ctx context.Context, tenantID string) (*TenantAPIKey, error) {
	var key TenantAPIKey
	err := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, created_at, rotated_at, is_active
		FROM tenant_api_keys
		WHERE tenant_id = $1
	`, tenantID).Scan(
		&key.ID, &key.TenantID, &key.CreatedAt, &key.RotatedAt, &key.IsActive,
	)
	if err == sql.ErrNoRows {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, err
	}
	return &key, nil
}
