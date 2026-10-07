package managementapi

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const planLimitsCols = `plan_name, max_msg_per_sec, max_integrations, max_storage_bytes, included_messages_per_month, sort_order`

func scanPlanLimits(row interface{ Scan(...any) error }) (*PlanLimits, error) {
	var p PlanLimits
	err := row.Scan(&p.PlanName, &p.MaxMsgPerSec, &p.MaxIntegrations, &p.MaxStorageBytes, &p.IncludedMessagesPerMonth, &p.SortOrder)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ErrUnknownPlan is returned for a plan name that is not a row of plan_limits.
var ErrUnknownPlan = errors.New("unknown plan")

func (r *PostgresRepository) GetPlanLimits(ctx context.Context, plan string) (*PlanLimits, error) {
	p, err := scanPlanLimits(r.db.QueryRowContext(ctx, `SELECT `+planLimitsCols+` FROM plan_limits WHERE plan_name = $1`, plan))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnknownPlan
	}
	return p, err
}

func (r *PostgresRepository) ListPlanLimits(ctx context.Context) ([]PlanLimits, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+planLimitsCols+` FROM plan_limits ORDER BY sort_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanLimits
	for rows.Next() {
		p, err := scanPlanLimits(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// SetTenantPlan is the operator's one write: plan, billing state and the
// plan's limits move together, in one transaction, and any open request is
// closed as accepted. A plan that is not a tier is refused before anything
// changes.
func (r *PostgresRepository) SetTenantPlan(ctx context.Context, tenantID string, change TenantPlanChange, billingStatus string, handledBy *string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var trialEnds *time.Time
	if billingStatus == BillingTrial {
		trialEnds = change.TrialEndsAt
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE tenants
		   SET subscription_plan = $2, billing_status = $3, trial_ends_at = $4,
		       billing_note = COALESCE($5, billing_note), updated_at = NOW()
		 WHERE id = $1 AND deleted_at IS NULL
		   AND EXISTS (SELECT 1 FROM plan_limits WHERE plan_name = $2)`,
		tenantID, change.Plan, billingStatus, trialEnds, change.Note)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, perr := r.GetPlanLimits(ctx, change.Plan); perr != nil {
			return perr
		}
		return ErrTenantNotFound
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tenant_quotas (tenant_id, plan_name, max_msg_per_sec, max_integrations, max_storage_bytes)
		SELECT $1, plan_name, max_msg_per_sec, max_integrations, max_storage_bytes FROM plan_limits WHERE plan_name = $2
		ON CONFLICT (tenant_id) DO UPDATE
		   SET plan_name = EXCLUDED.plan_name, max_msg_per_sec = EXCLUDED.max_msg_per_sec,
		       max_integrations = EXCLUDED.max_integrations, max_storage_bytes = EXCLUDED.max_storage_bytes,
		       updated_at = NOW()`, tenantID, change.Plan); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE plan_requests SET handled_at = NOW(), handled_by = $2, outcome = 'accepted'
		 WHERE tenant_id = $1 AND handled_at IS NULL`, tenantID, handledBy); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *PostgresRepository) SetTenantBillingStatus(ctx context.Context, tenantID, status string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE tenants SET billing_status = $2, updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`, tenantID, status)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTenantNotFound
	}
	return nil
}

// ListExpiredTrials is what the billing sweep suspends: on trial, clock run
// out. Read with FOR UPDATE SKIP LOCKED would be overkill — the sweep is
// advisory-lock-gated to one replica, and suspending twice is harmless.
func (r *PostgresRepository) ListExpiredTrials(ctx context.Context, now time.Time) ([]*Tenant, error) {
	// lint:tenant-ok — platform-wide sweep over every workspace; not served to a tenant.
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, slug, owner_id, subscription_plan, billing_status, trial_ends_at
		  FROM tenants
		 WHERE deleted_at IS NULL AND billing_status = 'trial' AND trial_ends_at IS NOT NULL AND trial_ends_at <= $1
		 ORDER BY trial_ends_at`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Tenant
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.OwnerID, &t.SubscriptionPlan, &t.BillingStatus, &t.TrialEndsAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// ListTenantsForPlatform is the operator's view across every workspace.
func (r *PostgresRepository) ListTenantsForPlatform(ctx context.Context) ([]*PlatformTenant, error) {
	// lint:tenant-ok — the platform operator's list of ALL workspaces; the route is operator-only (RequireOperator).
	rows, err := r.db.QueryContext(ctx, `
		SELECT t.id, t.name, t.slug, COALESCE(u.email, ''), t.subscription_plan, t.billing_status, t.trial_ends_at,
		       COALESCE(t.billing_note, ''), t.created_at,
		       COALESCE((SELECT SUM(messages_published) FROM usage_daily d
		                  WHERE d.tenant_id = t.id AND d.day >= CURRENT_DATE - 30), 0),
		       (SELECT COUNT(*) FROM connections c WHERE c.tenant_id = t.id::text AND c.status = 'running'),
		       pr.id, pr.requested_plan, pr.message, pr.created_at
		  FROM tenants t
		  LEFT JOIN users u ON u.id = t.owner_id
		  LEFT JOIN plan_requests pr ON pr.tenant_id = t.id AND pr.handled_at IS NULL
		 WHERE t.deleted_at IS NULL
		 ORDER BY (pr.id IS NOT NULL) DESC, t.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PlatformTenant
	for rows.Next() {
		var pt PlatformTenant
		var reqID, reqPlan, reqMsg sql.NullString
		var reqAt sql.NullTime
		if err := rows.Scan(&pt.ID, &pt.Name, &pt.Slug, &pt.OwnerEmail, &pt.Plan, &pt.BillingStatus, &pt.TrialEndsAt,
			&pt.BillingNote, &pt.CreatedAt, &pt.Messages30d, &pt.RunningPipelines,
			&reqID, &reqPlan, &reqMsg, &reqAt); err != nil {
			return nil, err
		}
		if reqID.Valid {
			pt.OpenRequest = &PlanRequest{ID: reqID.String, TenantID: pt.ID, RequestedPlan: reqPlan.String, Message: reqMsg.String, CreatedAt: reqAt.Time}
		}
		out = append(out, &pt)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CreatePlanRequest(ctx context.Context, req *PlanRequest) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO plan_requests (tenant_id, requested_plan, message, requested_by)
		VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
		req.TenantID, req.RequestedPlan, req.Message, req.RequestedBy).Scan(&req.ID, &req.CreatedAt)
}

const planRequestCols = `id, tenant_id::text, requested_plan, message, requested_by::text, created_at, handled_at, COALESCE(outcome, '')`

func scanPlanRequest(row interface{ Scan(...any) error }) (*PlanRequest, error) {
	var pr PlanRequest
	if err := row.Scan(&pr.ID, &pr.TenantID, &pr.RequestedPlan, &pr.Message, &pr.RequestedBy, &pr.CreatedAt, &pr.HandledAt, &pr.Outcome); err != nil {
		return nil, err
	}
	return &pr, nil
}

// GetOpenPlanRequest returns nil, nil when there is none.
func (r *PostgresRepository) GetOpenPlanRequest(ctx context.Context, tenantID string) (*PlanRequest, error) {
	pr, err := scanPlanRequest(r.db.QueryRowContext(ctx, `
		SELECT `+planRequestCols+` FROM plan_requests WHERE tenant_id = $1 AND handled_at IS NULL`, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return pr, err
}

func (r *PostgresRepository) ListOpenPlanRequests(ctx context.Context) ([]*PlanRequest, error) {
	// lint:tenant-ok — the platform operator's inbox across workspaces; operator-only route.
	rows, err := r.db.QueryContext(ctx, `SELECT `+planRequestCols+` FROM plan_requests WHERE handled_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PlanRequest
	for rows.Next() {
		pr, err := scanPlanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) SetConnectionStoppedByBilling(ctx context.Context, tenantID, connectionID string, stopped bool) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE connections SET stopped_by_billing = $3 WHERE id = $1 AND tenant_id = $2`, connectionID, tenantID, stopped)
	return err
}

// ListConnectionsStoppedByBilling returns the pipelines a suspension stopped,
// with their graph, so activation can start them again.
func (r *PostgresRepository) ListConnectionsStoppedByBilling(ctx context.Context, tenantID string) ([]*Connection, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id FROM connections WHERE tenant_id = $1 AND stopped_by_billing AND status = 'stopped' ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []*Connection
	for _, id := range ids {
		c, err := r.GetConnection(ctx, id) // the full row, nodes and edges included
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
