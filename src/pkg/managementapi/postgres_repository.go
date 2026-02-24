package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgconn"
)

// PostgresRepository implements Repository interface using PostgreSQL
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository creates a new PostgreSQL repository
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// CreateConnection creates a new connection in the database
func (r *PostgresRepository) CreateConnection(ctx context.Context, connection *Connection) error {
	query := `
		INSERT INTO connections (
			id, tenant_id, name, description,
			source_config, converter_config, filter_config, destination_config,
			status, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11
		)
	`

	_, err := r.db.ExecContext(
		ctx,
		query,
		connection.ID,
		connection.TenantID,
		connection.Name,
		connection.Description,
		connection.SourceConfig,
		connection.ConverterConfig,
		connection.FilterConfig,
		connection.DestinationConfig,
		connection.Status,
		connection.CreatedAt,
		connection.UpdatedAt,
	)

	if err != nil {
		// Check for unique constraint violation (error code 23505)
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" {
			return &ConflictError{Message: "Connection with this name already exists for this tenant"}
		}
		return fmt.Errorf("failed to create connection: %w", err)
	}

	return nil
}

// GetConnection retrieves a connection by ID
func (r *PostgresRepository) GetConnection(ctx context.Context, id string) (*Connection, error) {
	query := `
		SELECT
			id, tenant_id, name, description,
			source_config, converter_config, filter_config, destination_config,
			status, created_at, updated_at, started_at, stopped_at, last_error
		FROM connections
		WHERE id = $1
	`

	conn := &Connection{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&conn.ID,
		&conn.TenantID,
		&conn.Name,
		&conn.Description,
		&conn.SourceConfig,
		&conn.ConverterConfig,
		&conn.FilterConfig,
		&conn.DestinationConfig,
		&conn.Status,
		&conn.CreatedAt,
		&conn.UpdatedAt,
		&conn.StartedAt,
		&conn.StoppedAt,
		&conn.LastError,
	)

	if err == sql.ErrNoRows {
		return nil, &NotFoundError{ResourceType: "Connection", ResourceID: id}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	return conn, nil
}

// ListConnections retrieves connections for a tenant with optional filtering
func (r *PostgresRepository) ListConnections(ctx context.Context, tenantID string, filters *ListFilters) ([]*Connection, int64, error) {
	if filters == nil {
		filters = &ListFilters{Limit: 20, Offset: 0}
	}

	// Validate and set defaults
	if filters.Limit <= 0 || filters.Limit > 100 {
		filters.Limit = 20
	}
	if filters.Offset < 0 {
		filters.Offset = 0
	}

	// Build query
	whereConditions := []string{"tenant_id = $1"}
	args := []interface{}{tenantID}
	argIndex := 2

	if filters.Status != "" {
		whereConditions = append(whereConditions, fmt.Sprintf("status = $%d", argIndex))
		args = append(args, filters.Status)
		argIndex++
	}

	if filters.Search != "" {
		whereConditions = append(whereConditions, fmt.Sprintf("name ILIKE $%d", argIndex))
		args = append(args, "%"+filters.Search+"%")
		argIndex++
	}

	whereClause := strings.Join(whereConditions, " AND ")

	// Get total count
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM connections WHERE %s", whereClause)
	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count connections: %w", err)
	}

	// Get paginated results
	listQuery := fmt.Sprintf(`
		SELECT
			id, tenant_id, name, description,
			source_config, converter_config, filter_config, destination_config,
			status, created_at, updated_at, started_at, stopped_at, last_error
		FROM connections
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, argIndex, argIndex+1)

	args = append(args, filters.Limit, filters.Offset)

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list connections: %w", err)
	}
	defer rows.Close()

	var connections []*Connection
	for rows.Next() {
		conn := &Connection{}
		err := rows.Scan(
			&conn.ID,
			&conn.TenantID,
			&conn.Name,
			&conn.Description,
			&conn.SourceConfig,
			&conn.ConverterConfig,
			&conn.FilterConfig,
			&conn.DestinationConfig,
			&conn.Status,
			&conn.CreatedAt,
			&conn.UpdatedAt,
			&conn.StartedAt,
			&conn.StoppedAt,
			&conn.LastError,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan connection: %w", err)
		}
		connections = append(connections, conn)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("error iterating connections: %w", err)
	}

	return connections, total, nil
}

// UpdateConnection updates an existing connection
func (r *PostgresRepository) UpdateConnection(ctx context.Context, connection *Connection) error {
	query := `
		UPDATE connections
		SET
			name = $2,
			description = $3,
			source_config = $4,
			converter_config = $5,
			filter_config = $6,
			destination_config = $7,
			updated_at = $8
		WHERE id = $1
	`

	result, err := r.db.ExecContext(
		ctx,
		query,
		connection.ID,
		connection.Name,
		connection.Description,
		connection.SourceConfig,
		connection.ConverterConfig,
		connection.FilterConfig,
		connection.DestinationConfig,
		time.Now().UTC(),
	)

	if err != nil {
		return fmt.Errorf("failed to update connection: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return &NotFoundError{ResourceType: "Connection", ResourceID: connection.ID}
	}

	return nil
}

// DeleteConnection deletes a connection and its related events
func (r *PostgresRepository) DeleteConnection(ctx context.Context, id string) error {
	query := "DELETE FROM connections WHERE id = $1"

	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete connection: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return &NotFoundError{ResourceType: "Connection", ResourceID: id}
	}

	return nil
}

// UpdateConnectionStatus updates the status of a connection
func (r *PostgresRepository) UpdateConnectionStatus(ctx context.Context, id string, status string, lastError *string) error {
	query := `
		UPDATE connections
		SET
			status = $2,
			last_error = $3,
			updated_at = $4
		WHERE id = $1
	`

	result, err := r.db.ExecContext(
		ctx,
		query,
		id,
		status,
		lastError,
		time.Now().UTC(),
	)

	if err != nil {
		return fmt.Errorf("failed to update connection status: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return &NotFoundError{ResourceType: "Connection", ResourceID: id}
	}

	return nil
}

// CreateConnectionEvent creates a new connection event
func (r *PostgresRepository) CreateConnectionEvent(ctx context.Context, event *ConnectionEvent) error {
	query := `
		INSERT INTO connection_events (
			id, connection_id, tenant_id, event_type, event_data, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6
		)
	`

	_, err := r.db.ExecContext(
		ctx,
		query,
		event.ID,
		event.ConnectionID,
		event.TenantID,
		event.EventType,
		event.EventData,
		event.CreatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to create connection event: %w", err)
	}

	return nil
}

// GetConnectionEvents retrieves events for a connection
func (r *PostgresRepository) GetConnectionEvents(ctx context.Context, connectionID string) ([]*ConnectionEvent, error) {
	query := `
		SELECT
			id, connection_id, tenant_id, event_type, event_data, created_at
		FROM connection_events
		WHERE connection_id = $1
		ORDER BY created_at DESC
		LIMIT 1000
	`

	rows, err := r.db.QueryContext(ctx, query, connectionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get connection events: %w", err)
	}
	defer rows.Close()

	var events []*ConnectionEvent
	for rows.Next() {
		event := &ConnectionEvent{}
		err := rows.Scan(
			&event.ID,
			&event.ConnectionID,
			&event.TenantID,
			&event.EventType,
			&event.EventData,
			&event.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating events: %w", err)
	}

	return events, nil
}

// Close closes the repository connection
func (r *PostgresRepository) Close() error {
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}

// Helper function to marshal JSONB for database storage
func marshalJSONB(v interface{}) (interface{}, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Helper function to unmarshal JSONB from database
func unmarshalJSONB(data []byte, v interface{}) error {
	if data == nil {
		return nil
	}
	return json.Unmarshal(data, v)
}
