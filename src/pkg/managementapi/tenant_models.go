package managementapi

import "time"

// Tenant represents a workspace/company
type Tenant struct {
	ID                  string     `json:"id" db:"id"`
	Name                string     `json:"name" db:"name"`
	Slug                string     `json:"slug" db:"slug"`
	OwnerID             string     `json:"owner_id" db:"owner_id"`
	SubscriptionPlan    string     `json:"subscription_plan" db:"subscription_plan"`
	IsVerified          bool       `json:"is_verified" db:"is_verified"`
	MaxIntegrations     int        `json:"max_integrations" db:"max_integrations"`
	MaxMessagesPerMonth int64      `json:"max_messages_per_month" db:"max_messages_per_month"`
	Status              string     `json:"status" db:"status"`       // provisioning|active|failed|terminating
	NATSSlug            *string    `json:"nats_slug" db:"nats_slug"` // set when NATS is provisioned
	CreatedAt           time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at" db:"updated_at"`
	DeletedAt           *time.Time `json:"-" db:"deleted_at"`
}

// UserTenantRole represents user membership in a tenant
type UserTenantRole struct {
	ID        string     `json:"id" db:"id"`
	UserID    string     `json:"user_id" db:"user_id"`
	TenantID  string     `json:"tenant_id" db:"tenant_id"`
	Role      string     `json:"role" db:"role"`
	InvitedBy *string    `json:"invited_by,omitempty" db:"invited_by"`
	InvitedAt time.Time  `json:"invited_at" db:"invited_at"`
	JoinedAt  *time.Time `json:"joined_at,omitempty" db:"joined_at"`
}

// TenantResponse is returned by API endpoints, includes the user's role
type TenantResponse struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Slug                string    `json:"slug"`
	OwnerID             string    `json:"owner_id"`
	SubscriptionPlan    string    `json:"subscription_plan"`
	IsVerified          bool      `json:"is_verified"`
	MaxIntegrations     int       `json:"max_integrations"`
	MaxMessagesPerMonth int64     `json:"max_messages_per_month"`
	Status              string    `json:"status"`
	NATSSlug            *string   `json:"nats_slug,omitempty"`
	UserRole            string    `json:"user_role"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// CreateTenantRequest is sent by the client to create a new tenant
type CreateTenantRequest struct {
	Name string `json:"name"`
}

// TenantAPIKey represents an API key for a tenant (Phase 3 preparation)
type TenantAPIKey struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenant_id"`
	CreatedAt time.Time  `json:"created_at"`
	RotatedAt *time.Time `json:"rotated_at,omitempty"`
	IsActive  bool       `json:"is_active"`
}

// ProvisioningJob tracks async NATS provisioning for a tenant
type ProvisioningJob struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenant_id"`
	Status      string     `json:"status"` // queued|running|completed|failed
	Progress    int        `json:"progress"`
	CurrentStep string     `json:"current_step"`
	ErrorMsg    string     `json:"error_message,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// ProvisioningStatusUpdate is broadcast via SSE to connected clients
type ProvisioningStatusUpdate struct {
	TenantID    string `json:"tenant_id"`
	Status      string `json:"status"`
	Progress    int    `json:"progress"`
	CurrentStep string `json:"current_step"`
	NATSUrl     string `json:"nats_url,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ProvisionJob is the internal job representation for the provisioning worker
type ProvisionJob struct {
	TenantID   string
	TenantSlug string
	JobID      string
}
