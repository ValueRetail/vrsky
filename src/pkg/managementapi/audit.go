package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// AuditEntry is one row in the audit_log table (Phase 1G / #72).
type AuditEntry struct {
	ID           string                 `json:"id"`
	TenantID     string                 `json:"tenant_id"`
	UserID       *string                `json:"user_id,omitempty"`
	ActorKind    string                 `json:"actor_kind"`            // user | api_key | service | system
	ActorLabel   string                 `json:"actor_label,omitempty"` // email, key name, …
	Action       string                 `json:"action"`                // dotted verb, e.g. "connection.create"
	ResourceType string                 `json:"resource_type"`         // e.g. "connection"
	ResourceID   string                 `json:"resource_id,omitempty"`
	Method       string                 `json:"method"`
	Path         string                 `json:"path"`
	StatusCode   int                    `json:"status_code"`
	RequestID    string                 `json:"request_id,omitempty"`
	IPAddress    string                 `json:"ip_address,omitempty"`
	UserAgent    string                 `json:"user_agent,omitempty"`
	Details      map[string]interface{} `json:"details,omitempty"`
	OccurredAt   time.Time              `json:"occurred_at"`
}

// AuditFilters constrains list queries.
type AuditFilters struct {
	Action       string
	ResourceType string
	ResourceID   string
	UserID       string
	Since        *time.Time
	Until        *time.Time
}

// CreateAuditEntry inserts one immutable row.
func (r *PostgresRepository) CreateAuditEntry(ctx context.Context, e *AuditEntry) error {
	if e.ActorKind == "" {
		e.ActorKind = "user"
	}
	if e.Details == nil {
		e.Details = map[string]interface{}{}
	}
	detailsJSON, _ := json.Marshal(e.Details)

	var userID sql.NullString
	if e.UserID != nil && *e.UserID != "" {
		userID = sql.NullString{String: *e.UserID, Valid: true}
	}
	var resourceID, requestID, actorLabel, ip, ua sql.NullString
	if e.ResourceID != "" {
		resourceID = sql.NullString{String: e.ResourceID, Valid: true}
	}
	if e.RequestID != "" {
		requestID = sql.NullString{String: e.RequestID, Valid: true}
	}
	if e.ActorLabel != "" {
		actorLabel = sql.NullString{String: e.ActorLabel, Valid: true}
	}
	if e.IPAddress != "" {
		ip = sql.NullString{String: e.IPAddress, Valid: true}
	}
	if e.UserAgent != "" {
		ua = sql.NullString{String: e.UserAgent, Valid: true}
	}

	return r.db.QueryRowContext(ctx, `
		INSERT INTO audit_log (
			tenant_id, user_id, actor_kind, actor_label,
			action, resource_type, resource_id,
			method, path, status_code, request_id,
			ip_address, user_agent, details
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, '')::inet, $13, $14)
		RETURNING id, occurred_at
	`, e.TenantID, userID, e.ActorKind, actorLabel,
		e.Action, e.ResourceType, resourceID,
		e.Method, e.Path, e.StatusCode, requestID,
		ip, ua, detailsJSON,
	).Scan(&e.ID, &e.OccurredAt)
}

// ListAuditEntries returns paginated entries for a tenant, ordered newest-first.
func (r *PostgresRepository) ListAuditEntries(ctx context.Context, tenantID string, f AuditFilters, limit, offset int) ([]*AuditEntry, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	// Build a dynamic WHERE so missing filters fall through. Tenant filter
	// is always present.
	where := "WHERE tenant_id = $1"
	args := []interface{}{tenantID}
	add := func(clause string, val interface{}) {
		args = append(args, val)
		where += " AND " + clause + " $" + itoa(len(args))
	}
	// Action and resource_type filter as substring (case-insensitive) so
	// the UI's free-text search behaves naturally — typing "c" matches
	// "connection.start", "secret.create", etc.
	if f.Action != "" {
		add("action ILIKE", "%"+f.Action+"%")
	}
	if f.ResourceType != "" {
		add("resource_type ILIKE", "%"+f.ResourceType+"%")
	}
	// Resource ID stays exact — it's a UUID; substring search would just
	// surface accidental collisions.
	if f.ResourceID != "" {
		add("resource_id =", f.ResourceID)
	}
	if f.UserID != "" {
		add("user_id =", f.UserID)
	}
	if f.Since != nil {
		add("occurred_at >=", *f.Since)
	}
	if f.Until != nil {
		add("occurred_at <=", *f.Until)
	}

	var total int64
	// lint:tenant-ok — `where` always begins with "WHERE tenant_id = $1".
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	// lint:tenant-ok — `where` always begins with "WHERE tenant_id = $1".
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, user_id, actor_kind, actor_label,
		       action, resource_type, resource_id,
		       method, path, status_code, request_id,
		       host(ip_address), user_agent, details, occurred_at
		FROM audit_log `+where+`
		ORDER BY occurred_at DESC
		LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []*AuditEntry
	for rows.Next() {
		e, err := scanAuditRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// StreamAuditEntries iterates every entry for a tenant matching the filters,
// invoking emit for each. Used by the JSONL export endpoint to avoid
// loading the whole result set into memory.
func (r *PostgresRepository) StreamAuditEntries(ctx context.Context, tenantID string, f AuditFilters, emit func(*AuditEntry) error) error {
	where := "WHERE tenant_id = $1"
	args := []interface{}{tenantID}
	add := func(clause string, val interface{}) {
		args = append(args, val)
		where += " AND " + clause + " $" + itoa(len(args))
	}
	// Action and resource_type filter as substring (case-insensitive) so
	// the UI's free-text search behaves naturally — typing "c" matches
	// "connection.start", "secret.create", etc.
	if f.Action != "" {
		add("action ILIKE", "%"+f.Action+"%")
	}
	if f.ResourceType != "" {
		add("resource_type ILIKE", "%"+f.ResourceType+"%")
	}
	// Resource ID stays exact — it's a UUID; substring search would just
	// surface accidental collisions.
	if f.ResourceID != "" {
		add("resource_id =", f.ResourceID)
	}
	if f.Since != nil {
		add("occurred_at >=", *f.Since)
	}
	if f.Until != nil {
		add("occurred_at <=", *f.Until)
	}

	// lint:tenant-ok — `where` always begins with "WHERE tenant_id = $1".
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, user_id, actor_kind, actor_label,
		       action, resource_type, resource_id,
		       method, path, status_code, request_id,
		       host(ip_address), user_agent, details, occurred_at
		FROM audit_log `+where+`
		ORDER BY occurred_at ASC
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanAuditRow(rows)
		if err != nil {
			return err
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

// scanAuditRow centralises NULL handling for the common SELECT shape.
func scanAuditRow(rows *sql.Rows) (*AuditEntry, error) {
	var (
		e          AuditEntry
		userID     sql.NullString
		actorLabel sql.NullString
		resourceID sql.NullString
		requestID  sql.NullString
		ipAddress  sql.NullString
		userAgent  sql.NullString
		details    []byte
	)
	if err := rows.Scan(
		&e.ID, &e.TenantID, &userID, &e.ActorKind, &actorLabel,
		&e.Action, &e.ResourceType, &resourceID,
		&e.Method, &e.Path, &e.StatusCode, &requestID,
		&ipAddress, &userAgent, &details, &e.OccurredAt,
	); err != nil {
		return nil, err
	}
	if userID.Valid {
		v := userID.String
		e.UserID = &v
	}
	e.ActorLabel = actorLabel.String
	e.ResourceID = resourceID.String
	e.RequestID = requestID.String
	e.IPAddress = ipAddress.String
	e.UserAgent = userAgent.String
	if len(details) > 0 {
		_ = json.Unmarshal(details, &e.Details)
	}
	if e.Details == nil {
		e.Details = map[string]interface{}{}
	}
	return &e, nil
}

// itoa is a tiny zero-alloc int-to-string helper used to build $N
// placeholders. We avoid strconv.Itoa just because the call sites are hot
// in tight loops and the impl is trivial.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [12]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
