package managementapi

import (
	"encoding/json"
	"time"

	"github.com/ValueRetail/vrsky/pkg/fieldfilter"
)

// DataConnectionRequest represents a request from one tenant to connect with another
type DataConnectionRequest struct {
	ID                string     `json:"id"`
	RequesterTenantID string     `json:"requester_tenant_id"`
	TargetTenantID    string     `json:"target_tenant_id"`
	PermissionType    string     `json:"permission_type"` // send|receive|both
	Status            string     `json:"status"`          // pending|approved|denied|revoked
	Message           string     `json:"message,omitempty"`
	AllowedFields     []string   `json:"allowed_fields,omitempty"`
	DeniedFields      []string   `json:"denied_fields,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	RespondedAt       *time.Time `json:"responded_at,omitempty"`
	// Enriched fields (populated by handlers, not stored)
	RequesterTenantName string `json:"requester_tenant_name,omitempty"`
	TargetTenantName    string `json:"target_tenant_name,omitempty"`
}

// TenantDataConnection represents an active data-sharing connection between tenants
type TenantDataConnection struct {
	ID                string     `json:"id"`
	RequestID         string     `json:"request_id"`
	RequesterTenantID string     `json:"requester_tenant_id"`
	TargetTenantID    string     `json:"target_tenant_id"`
	PermissionType    string     `json:"permission_type"`
	AllowedFields     []string   `json:"allowed_fields,omitempty"`
	DeniedFields      []string   `json:"denied_fields,omitempty"`
	RateLimitPerHour  int        `json:"rate_limit_per_hour"`
	SharedConnectionIDs []string `json:"shared_connection_ids,omitempty"`
	Status              string     `json:"status"` // active|paused|revoked
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
}

// DataAccessLogEntry records a single data access event for the audit trail
type DataAccessLogEntry struct {
	ID                string    `json:"id"`
	ConnectionID      string    `json:"connection_id"`
	RequesterTenantID string    `json:"requester_tenant_id"`
	TargetTenantID    string    `json:"target_tenant_id"`
	RequestTime       time.Time `json:"request_time"`
	FieldsAccessed    []string  `json:"fields_accessed,omitempty"`
	BytesReceived     int       `json:"bytes_received"`
	StatusCode        int       `json:"status_code"`
	IPAddress         string    `json:"ip_address,omitempty"`
}

// CreateConnectionRequestPayload is the request body for creating a connection request
type CreateConnectionRequestPayload struct {
	TargetTenantID string `json:"target_tenant_id"`
	TargetAPIKey   string `json:"target_api_key"`
	PermissionType string `json:"permission_type"`
	Message        string `json:"message,omitempty"`
}

// ApproveConnectionRequestPayload is the request body for approving a connection request
type ApproveConnectionRequestPayload struct {
	AllowedFields       []string `json:"allowed_fields,omitempty"`
	DeniedFields        []string `json:"denied_fields,omitempty"`
	SharedConnectionIDs []string `json:"shared_connection_ids,omitempty"`
}

// unsafeFieldPatterns re-exported for backward compatibility in this package.
var unsafeFieldPatterns = fieldfilter.UnsafeFieldPatterns

// filterFields delegates to the shared fieldfilter package.
func filterFields(data json.RawMessage, allowed, denied []string) json.RawMessage {
	return fieldfilter.FilterFields(data, allowed, denied)
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}
