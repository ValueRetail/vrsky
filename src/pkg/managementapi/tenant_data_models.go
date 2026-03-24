package managementapi

import (
	"encoding/json"
	"time"
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
	Status            string     `json:"status"` // active|paused|revoked
	CreatedAt         time.Time  `json:"created_at"`
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
	AllowedFields []string `json:"allowed_fields,omitempty"`
	DeniedFields  []string `json:"denied_fields,omitempty"`
}

// unsafeFieldPatterns are auto-denied during approval to prevent leaking sensitive data.
// Any JSON key containing one of these substrings (case-insensitive) is blocked.
var unsafeFieldPatterns = []string{
	"password", "secret", "token", "key", "price", "credential", "private",
}

// filterFields strips denied fields and restricts to allowed fields.
// Applies unsafeFieldPatterns auto-filter regardless of explicit lists.
func filterFields(data json.RawMessage, allowed, denied []string) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return data // not a JSON object, return as-is
	}

	// Build denied set (lowercase for case-insensitive comparison)
	deniedSet := make(map[string]bool, len(denied))
	for _, d := range denied {
		deniedSet[toLower(d)] = true
	}

	// Build allowed set (empty means allow all)
	allowedSet := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allowedSet[toLower(a)] = true
	}

	filtered := make(map[string]json.RawMessage)
	for k, v := range obj {
		kLower := toLower(k)

		// Check explicit denied list
		if deniedSet[kLower] {
			continue
		}

		// Check unsafe patterns
		if matchesUnsafePattern(kLower) {
			continue
		}

		// Check allowed list (empty = allow all)
		if len(allowedSet) > 0 && !allowedSet[kLower] {
			continue
		}

		filtered[k] = v
	}

	result, err := json.Marshal(filtered)
	if err != nil {
		return data
	}
	return result
}

func matchesUnsafePattern(fieldLower string) bool {
	for _, pattern := range unsafeFieldPatterns {
		if containsLower(fieldLower, pattern) {
			return true
		}
	}
	return false
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

func containsLower(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
