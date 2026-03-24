package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ============================================
// Phase 3: Data Sharing Repository
// ============================================

var (
	ErrConnectionRequestNotFound = errors.New("connection request not found")
	ErrDataConnectionNotFound    = errors.New("data connection not found")
)

func (r *PostgresRepository) CreateConnectionRequest(ctx context.Context, req *DataConnectionRequest) error {
	allowedJSON, _ := json.Marshal(req.AllowedFields)
	deniedJSON, _ := json.Marshal(req.DeniedFields)
	if req.AllowedFields == nil {
		allowedJSON = nil
	}
	if req.DeniedFields == nil {
		deniedJSON = nil
	}

	return r.db.QueryRowContext(ctx, `
		INSERT INTO tenant_connection_requests
			(requester_tenant_id, target_tenant_id, permission_type, status, message, allowed_fields, denied_fields)
		VALUES ($1, $2, $3, 'pending', $4, $5, $6)
		RETURNING id, created_at, updated_at
	`, req.RequesterTenantID, req.TargetTenantID, req.PermissionType, req.Message, allowedJSON, deniedJSON).Scan(
		&req.ID, &req.CreatedAt, &req.UpdatedAt,
	)
}

func (r *PostgresRepository) GetConnectionRequest(ctx context.Context, requestID string) (*DataConnectionRequest, error) {
	var req DataConnectionRequest
	var allowedJSON, deniedJSON []byte
	var message sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT id, requester_tenant_id, target_tenant_id, permission_type, status,
		       message, allowed_fields, denied_fields, created_at, updated_at, responded_at
		FROM tenant_connection_requests WHERE id = $1
	`, requestID).Scan(
		&req.ID, &req.RequesterTenantID, &req.TargetTenantID, &req.PermissionType, &req.Status,
		&message, &allowedJSON, &deniedJSON, &req.CreatedAt, &req.UpdatedAt, &req.RespondedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrConnectionRequestNotFound
	}
	if err != nil {
		return nil, err
	}
	req.Message = message.String
	if allowedJSON != nil {
		_ = json.Unmarshal(allowedJSON, &req.AllowedFields)
	}
	if deniedJSON != nil {
		_ = json.Unmarshal(deniedJSON, &req.DeniedFields)
	}
	return &req, nil
}

func (r *PostgresRepository) ListIncomingConnectionRequests(ctx context.Context, targetTenantID string) ([]*DataConnectionRequest, error) {
	return r.listConnectionRequests(ctx, `
		SELECT tcr.id, tcr.requester_tenant_id, tcr.target_tenant_id, tcr.permission_type, tcr.status,
		       COALESCE(tcr.message, ''), tcr.allowed_fields, tcr.denied_fields,
		       tcr.created_at, tcr.updated_at, tcr.responded_at,
		       t.name
		FROM tenant_connection_requests tcr
		JOIN tenants t ON t.id = tcr.requester_tenant_id
		WHERE tcr.target_tenant_id = $1
		ORDER BY tcr.created_at DESC
	`, targetTenantID, true)
}

func (r *PostgresRepository) ListOutgoingConnectionRequests(ctx context.Context, requesterTenantID string) ([]*DataConnectionRequest, error) {
	return r.listConnectionRequests(ctx, `
		SELECT tcr.id, tcr.requester_tenant_id, tcr.target_tenant_id, tcr.permission_type, tcr.status,
		       COALESCE(tcr.message, ''), tcr.allowed_fields, tcr.denied_fields,
		       tcr.created_at, tcr.updated_at, tcr.responded_at,
		       t.name
		FROM tenant_connection_requests tcr
		JOIN tenants t ON t.id = tcr.target_tenant_id
		WHERE tcr.requester_tenant_id = $1
		ORDER BY tcr.created_at DESC
	`, requesterTenantID, false)
}

func (r *PostgresRepository) listConnectionRequests(ctx context.Context, query, tenantID string, isIncoming bool) ([]*DataConnectionRequest, error) {
	rows, err := r.db.QueryContext(ctx, query, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*DataConnectionRequest
	for rows.Next() {
		var req DataConnectionRequest
		var allowedJSON, deniedJSON []byte
		var otherTenantName string
		if err := rows.Scan(
			&req.ID, &req.RequesterTenantID, &req.TargetTenantID, &req.PermissionType, &req.Status,
			&req.Message, &allowedJSON, &deniedJSON,
			&req.CreatedAt, &req.UpdatedAt, &req.RespondedAt,
			&otherTenantName,
		); err != nil {
			return nil, err
		}
		if allowedJSON != nil {
			_ = json.Unmarshal(allowedJSON, &req.AllowedFields)
		}
		if deniedJSON != nil {
			_ = json.Unmarshal(deniedJSON, &req.DeniedFields)
		}
		if isIncoming {
			req.RequesterTenantName = otherTenantName
		} else {
			req.TargetTenantName = otherTenantName
		}
		results = append(results, &req)
	}
	return results, rows.Err()
}

func (r *PostgresRepository) ApproveConnectionRequest(ctx context.Context, requestID string, allowedFields, deniedFields []string) (*TenantDataConnection, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	allowedJSON, _ := json.Marshal(allowedFields)
	deniedJSON, _ := json.Marshal(deniedFields)
	if allowedFields == nil {
		allowedJSON = nil
	}
	if deniedFields == nil {
		deniedJSON = nil
	}

	// Update request status
	var requesterID, targetID, permType string
	err = tx.QueryRowContext(ctx, `
		UPDATE tenant_connection_requests
		SET status = 'approved', responded_at = NOW(), updated_at = NOW(),
		    allowed_fields = $2, denied_fields = $3
		WHERE id = $1 AND status = 'pending'
		RETURNING requester_tenant_id, target_tenant_id, permission_type
	`, requestID, allowedJSON, deniedJSON).Scan(&requesterID, &targetID, &permType)
	if err == sql.ErrNoRows {
		return nil, ErrConnectionRequestNotFound
	}
	if err != nil {
		return nil, err
	}

	// Create data connection
	var conn TenantDataConnection
	var aJSON, dJSON []byte
	err = tx.QueryRowContext(ctx, `
		INSERT INTO tenant_data_connections
			(request_id, requester_tenant_id, target_tenant_id, permission_type, allowed_fields, denied_fields)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, request_id, requester_tenant_id, target_tenant_id, permission_type,
		          allowed_fields, denied_fields, rate_limit_per_hour, status, created_at, updated_at
	`, requestID, requesterID, targetID, permType, allowedJSON, deniedJSON).Scan(
		&conn.ID, &conn.RequestID, &conn.RequesterTenantID, &conn.TargetTenantID, &conn.PermissionType,
		&aJSON, &dJSON, &conn.RateLimitPerHour, &conn.Status, &conn.CreatedAt, &conn.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if aJSON != nil {
		_ = json.Unmarshal(aJSON, &conn.AllowedFields)
	}
	if dJSON != nil {
		_ = json.Unmarshal(dJSON, &conn.DeniedFields)
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &conn, nil
}

func (r *PostgresRepository) DenyConnectionRequest(ctx context.Context, requestID string) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE tenant_connection_requests
		SET status = 'denied', responded_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status = 'pending'
	`, requestID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrConnectionRequestNotFound
	}
	return nil
}

func (r *PostgresRepository) ListDataConnections(ctx context.Context, tenantID string) ([]*TenantDataConnection, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, request_id, requester_tenant_id, target_tenant_id, permission_type,
		       allowed_fields, denied_fields, rate_limit_per_hour, status, created_at, updated_at, revoked_at
		FROM tenant_data_connections
		WHERE requester_tenant_id = $1 OR target_tenant_id = $1
		ORDER BY created_at DESC
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*TenantDataConnection
	for rows.Next() {
		conn, err := scanDataConnection(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, conn)
	}
	return results, rows.Err()
}

func (r *PostgresRepository) GetDataConnectionByID(ctx context.Context, id string) (*TenantDataConnection, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, request_id, requester_tenant_id, target_tenant_id, permission_type,
		       allowed_fields, denied_fields, rate_limit_per_hour, status, created_at, updated_at, revoked_at
		FROM tenant_data_connections WHERE id = $1
	`, id)
	conn, err := scanDataConnectionRow(row)
	if err == sql.ErrNoRows {
		return nil, ErrDataConnectionNotFound
	}
	return conn, err
}

func (r *PostgresRepository) GetActiveDataConnection(ctx context.Context, requesterID, targetID string) (*TenantDataConnection, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, request_id, requester_tenant_id, target_tenant_id, permission_type,
		       allowed_fields, denied_fields, rate_limit_per_hour, status, created_at, updated_at, revoked_at
		FROM tenant_data_connections
		WHERE requester_tenant_id = $1 AND target_tenant_id = $2 AND status = 'active'
		LIMIT 1
	`, requesterID, targetID)
	conn, err := scanDataConnectionRow(row)
	if err == sql.ErrNoRows {
		return nil, ErrDataConnectionNotFound
	}
	return conn, err
}

func (r *PostgresRepository) RevokeDataConnection(ctx context.Context, connectionID string) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE tenant_data_connections
		SET status = 'revoked', revoked_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status = 'active'
	`, connectionID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrDataConnectionNotFound
	}
	return nil
}

func (r *PostgresRepository) CreateDataAccessLog(ctx context.Context, entry *DataAccessLogEntry) error {
	fieldsJSON, _ := json.Marshal(entry.FieldsAccessed)
	if entry.FieldsAccessed == nil {
		fieldsJSON = nil
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO tenant_data_access_log
			(connection_id, requester_tenant_id, target_tenant_id, fields_accessed, bytes_received, status_code, ip_address)
		VALUES ($1, $2, $3, $4, $5, $6, $7::inet)
	`, entry.ConnectionID, entry.RequesterTenantID, entry.TargetTenantID,
		fieldsJSON, entry.BytesReceived, entry.StatusCode, nullIfEmpty(entry.IPAddress))
	return err
}

func (r *PostgresRepository) ListDataAccessLog(ctx context.Context, targetTenantID string, filters *ListFilters) ([]*DataAccessLogEntry, int64, error) {
	// Count query
	countQuery := `SELECT COUNT(*) FROM tenant_data_access_log WHERE target_tenant_id = $1`
	var total int64
	if err := r.db.QueryRowContext(ctx, countQuery, targetTenantID).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := 20
	offset := 0
	if filters != nil {
		if filters.Limit > 0 && filters.Limit <= 100 {
			limit = filters.Limit
		}
		if filters.Offset >= 0 {
			offset = filters.Offset
		}
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, connection_id, requester_tenant_id, target_tenant_id,
		       request_time, fields_accessed, COALESCE(bytes_received, 0), status_code,
		       COALESCE(host(ip_address), '')
		FROM tenant_data_access_log
		WHERE target_tenant_id = $1
		ORDER BY request_time DESC
		LIMIT $2 OFFSET $3
	`, targetTenantID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var results []*DataAccessLogEntry
	for rows.Next() {
		var entry DataAccessLogEntry
		var fieldsJSON []byte
		if err := rows.Scan(
			&entry.ID, &entry.ConnectionID, &entry.RequesterTenantID, &entry.TargetTenantID,
			&entry.RequestTime, &fieldsJSON, &entry.BytesReceived, &entry.StatusCode,
			&entry.IPAddress,
		); err != nil {
			return nil, 0, err
		}
		if fieldsJSON != nil {
			_ = json.Unmarshal(fieldsJSON, &entry.FieldsAccessed)
		}
		results = append(results, &entry)
	}
	return results, total, rows.Err()
}

func (r *PostgresRepository) PauseConnectionsByDataConnection(ctx context.Context, tenantID, dataConnectionID string) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE connections
		SET status = 'stopped', updated_at = NOW(), last_error = 'Tenant data connection revoked'
		WHERE tenant_id = $1
		  AND status = 'running'
		  AND source_config->>'type' = 'tenant'
		  AND source_config->>'connection_id' = $2
	`, tenantID, dataConnectionID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *PostgresRepository) GetTenantByAPIKeyHash(ctx context.Context, keyHash string) (*Tenant, error) {
	var t Tenant
	err := r.db.QueryRowContext(ctx, `
		SELECT t.id, t.name, t.slug, t.owner_id, t.subscription_plan, t.is_verified,
		       t.max_integrations, t.max_messages_per_month, t.status, t.nats_slug, t.created_at, t.updated_at
		FROM tenants t
		JOIN tenant_api_keys tak ON t.id = tak.tenant_id
		WHERE tak.api_key_hash = $1 AND tak.is_active = true AND t.deleted_at IS NULL
	`, keyHash).Scan(
		&t.ID, &t.Name, &t.Slug, &t.OwnerID,
		&t.SubscriptionPlan, &t.IsVerified,
		&t.MaxIntegrations, &t.MaxMessagesPerMonth,
		&t.Status, &t.NATSSlug,
		&t.CreatedAt, &t.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no tenant found for API key")
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// helpers

type scannable interface {
	Scan(dest ...interface{}) error
}

func scanDataConnection(s scannable) (*TenantDataConnection, error) {
	var conn TenantDataConnection
	var aJSON, dJSON []byte
	err := s.Scan(
		&conn.ID, &conn.RequestID, &conn.RequesterTenantID, &conn.TargetTenantID, &conn.PermissionType,
		&aJSON, &dJSON, &conn.RateLimitPerHour, &conn.Status, &conn.CreatedAt, &conn.UpdatedAt, &conn.RevokedAt,
	)
	if err != nil {
		return nil, err
	}
	if aJSON != nil {
		_ = json.Unmarshal(aJSON, &conn.AllowedFields)
	}
	if dJSON != nil {
		_ = json.Unmarshal(dJSON, &conn.DeniedFields)
	}
	return &conn, nil
}

func scanDataConnectionRow(row *sql.Row) (*TenantDataConnection, error) {
	var conn TenantDataConnection
	var aJSON, dJSON []byte
	err := row.Scan(
		&conn.ID, &conn.RequestID, &conn.RequesterTenantID, &conn.TargetTenantID, &conn.PermissionType,
		&aJSON, &dJSON, &conn.RateLimitPerHour, &conn.Status, &conn.CreatedAt, &conn.UpdatedAt, &conn.RevokedAt,
	)
	if err != nil {
		return nil, err
	}
	if aJSON != nil {
		_ = json.Unmarshal(aJSON, &conn.AllowedFields)
	}
	if dJSON != nil {
		_ = json.Unmarshal(dJSON, &conn.DeniedFields)
	}
	return &conn, nil
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
