package managementapi

import (
	"context"
	"time"
)

// Paid plans (plans/paid-plans.md).
//
// A workspace is created on the trial plan with a clock running. When the
// clock runs out the billing sweep suspends it: its pipelines are stopped and
// anything that would run work answers 402 until a platform operator sets a
// plan. Nothing a tenant member can do changes the plan or the limits — the
// customer asks (a plan request, which reaches the operator in Teams), the
// operator invoices and activates. Payments live outside the product; these
// are the fields a Stripe webhook would set later.

// Billing states of a tenant.
const (
	BillingTrial     = "trial"
	BillingPaid      = "paid"
	BillingSuspended = "suspended"
)

// Plans. Their limits are rows of plan_limits, read at run time so an operator
// can retune a tier with one UPDATE; the names are fixed here because the
// handlers and the UI name them.
const (
	PlanTrial      = "trial"
	PlanPaid       = "paid"
	PlanEnterprise = "enterprise"
)

// TrialLength is how long a new workspace runs before it must have a plan.
const TrialLength = 14 * 24 * time.Hour

// PlanLimits is one tier's row of plan_limits. 0 means unlimited.
type PlanLimits struct {
	PlanName                 string `json:"plan_name"`
	MaxMsgPerSec             int    `json:"max_msg_per_sec"`
	MaxIntegrations          int    `json:"max_integrations"`
	MaxStorageBytes          int64  `json:"max_storage_bytes"`
	IncludedMessagesPerMonth int64  `json:"included_messages_per_month"`
	SortOrder                int    `json:"-"`
}

// PlanRequest is a customer asking for a plan. One open request per tenant;
// the operator closes it by setting the plan (outcome accepted) or by hand.
type PlanRequest struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenant_id"`
	RequestedPlan string     `json:"requested_plan"`
	Message       string     `json:"message"`
	RequestedBy   *string    `json:"requested_by,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	HandledAt     *time.Time `json:"handled_at,omitempty"`
	Outcome       string     `json:"outcome,omitempty"`
}

// TenantBilling is what a member of the workspace may see about its plan.
type TenantBilling struct {
	Plan          string       `json:"plan"`
	BillingStatus string       `json:"billing_status"`
	TrialEndsAt   *time.Time   `json:"trial_ends_at,omitempty"`
	Limits        *PlanLimits  `json:"limits"`
	Plans         []PlanLimits `json:"plans"`
	OpenRequest   *PlanRequest `json:"open_request,omitempty"`
}

// PlatformTenant is one row of the operator's workspace list.
type PlatformTenant struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Slug             string       `json:"slug"`
	OwnerEmail       string       `json:"owner_email"`
	Plan             string       `json:"plan"`
	BillingStatus    string       `json:"billing_status"`
	TrialEndsAt      *time.Time   `json:"trial_ends_at,omitempty"`
	BillingNote      string       `json:"billing_note"`
	CreatedAt        time.Time    `json:"created_at"`
	Messages30d      int64        `json:"messages_30d"`
	RunningPipelines int          `json:"running_pipelines"`
	OpenRequest      *PlanRequest `json:"open_request,omitempty"`
}

// TenantPlanChange is what the operator sets.
type TenantPlanChange struct {
	Plan        string     `json:"plan"`
	TrialEndsAt *time.Time `json:"trial_ends_at,omitempty"` // only read when Plan is trial
	Note        *string    `json:"note,omitempty"`
}

// BillingStore is the repository surface the paid-plans code needs.
type BillingStore interface {
	GetPlanLimits(ctx context.Context, plan string) (*PlanLimits, error)
	ListPlanLimits(ctx context.Context) ([]PlanLimits, error)
	// SetTenantPlan sets plan + billing state, copies the plan's limits into
	// the tenant's quotas, and closes open plan requests with the outcome.
	SetTenantPlan(ctx context.Context, tenantID string, change TenantPlanChange, billingStatus string, handledBy *string) error
	SetTenantBillingStatus(ctx context.Context, tenantID, status string) error
	ListExpiredTrials(ctx context.Context, now time.Time) ([]*Tenant, error)
	ListTenantsForPlatform(ctx context.Context) ([]*PlatformTenant, error)

	CreatePlanRequest(ctx context.Context, req *PlanRequest) error
	GetOpenPlanRequest(ctx context.Context, tenantID string) (*PlanRequest, error)
	ListOpenPlanRequests(ctx context.Context) ([]*PlanRequest, error)

	SetConnectionStoppedByBilling(ctx context.Context, tenantID, connectionID string, stopped bool) error
	ListConnectionsStoppedByBilling(ctx context.Context, tenantID string) ([]*Connection, error)
}
