// Package checkpoint provides functions for persisting and retrieving
// component checkpoints for resumable message processing.
package checkpoint

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Checkpoint represents the processing state of a component node
type Checkpoint struct {
	TenantID               string    `db:"tenant_id"`
	ConnectionID           string    `db:"connection_id"`
	NodeID                 string    `db:"node_id"`
	LastProcessedMessageID string    `db:"last_processed_message_id"`
	LastProcessedAt        time.Time `db:"last_processed_at"`
	MessageCount           int64     `db:"message_count"`
	UpdatedAt              time.Time `db:"updated_at"`
}

// Store provides checkpoint persistence operations
type Store interface {
	// Save persists a checkpoint (insert or update)
	Save(ctx context.Context, cp *Checkpoint) error

	// Get retrieves a checkpoint for a specific node
	Get(ctx context.Context, tenantID, connectionID, nodeID string) (*Checkpoint, error)

	// Delete removes a checkpoint
	Delete(ctx context.Context, tenantID, connectionID, nodeID string) error

	// DeleteForConnection removes all checkpoints for a connection
	DeleteForConnection(ctx context.Context, tenantID, connectionID string) error
}

// PostgresStore implements Store using PostgreSQL
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore creates a new PostgreSQL checkpoint store
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// Save persists a checkpoint using upsert
func (s *PostgresStore) Save(ctx context.Context, cp *Checkpoint) error {
	query := `
		INSERT INTO connection_node_checkpoints (
			tenant_id, connection_id, node_id,
			last_processed_message_id, last_processed_at, message_count, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, connection_id, node_id)
		DO UPDATE SET
			last_processed_message_id = EXCLUDED.last_processed_message_id,
			last_processed_at = EXCLUDED.last_processed_at,
			message_count = EXCLUDED.message_count,
			updated_at = EXCLUDED.updated_at
	`

	cp.UpdatedAt = time.Now()

	_, err := s.db.ExecContext(ctx, query,
		cp.TenantID,
		cp.ConnectionID,
		cp.NodeID,
		cp.LastProcessedMessageID,
		cp.LastProcessedAt,
		cp.MessageCount,
		cp.UpdatedAt,
	)

	if err != nil {
		return fmt.Errorf("save checkpoint: %w", err)
	}

	return nil
}

// Get retrieves a checkpoint for a specific node
func (s *PostgresStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*Checkpoint, error) {
	query := `
		SELECT tenant_id, connection_id, node_id,
			   last_processed_message_id, last_processed_at, message_count, updated_at
		FROM connection_node_checkpoints
		WHERE tenant_id = $1 AND connection_id = $2 AND node_id = $3
	`

	cp := &Checkpoint{}
	err := s.db.QueryRowContext(ctx, query, tenantID, connectionID, nodeID).Scan(
		&cp.TenantID,
		&cp.ConnectionID,
		&cp.NodeID,
		&cp.LastProcessedMessageID,
		&cp.LastProcessedAt,
		&cp.MessageCount,
		&cp.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil // No checkpoint exists yet
	}
	if err != nil {
		return nil, fmt.Errorf("get checkpoint: %w", err)
	}

	return cp, nil
}

// Delete removes a checkpoint for a specific node
func (s *PostgresStore) Delete(ctx context.Context, tenantID, connectionID, nodeID string) error {
	query := `
		DELETE FROM connection_node_checkpoints
		WHERE tenant_id = $1 AND connection_id = $2 AND node_id = $3
	`

	_, err := s.db.ExecContext(ctx, query, tenantID, connectionID, nodeID)
	if err != nil {
		return fmt.Errorf("delete checkpoint: %w", err)
	}

	return nil
}

// DeleteForConnection removes all checkpoints for a connection
func (s *PostgresStore) DeleteForConnection(ctx context.Context, tenantID, connectionID string) error {
	query := `
		DELETE FROM connection_node_checkpoints
		WHERE tenant_id = $1 AND connection_id = $2
	`

	_, err := s.db.ExecContext(ctx, query, tenantID, connectionID)
	if err != nil {
		return fmt.Errorf("delete checkpoints for connection: %w", err)
	}

	return nil
}

// InMemoryStore implements Store using in-memory storage (for testing)
type InMemoryStore struct {
	checkpoints map[string]*Checkpoint
}

// NewInMemoryStore creates a new in-memory checkpoint store
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		checkpoints: make(map[string]*Checkpoint),
	}
}

func (s *InMemoryStore) key(tenantID, connectionID, nodeID string) string {
	return fmt.Sprintf("%s:%s:%s", tenantID, connectionID, nodeID)
}

// Save persists a checkpoint
func (s *InMemoryStore) Save(ctx context.Context, cp *Checkpoint) error {
	cp.UpdatedAt = time.Now()
	key := s.key(cp.TenantID, cp.ConnectionID, cp.NodeID)
	s.checkpoints[key] = cp
	return nil
}

// Get retrieves a checkpoint
func (s *InMemoryStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*Checkpoint, error) {
	key := s.key(tenantID, connectionID, nodeID)
	cp, ok := s.checkpoints[key]
	if !ok {
		return nil, nil
	}
	return cp, nil
}

// Delete removes a checkpoint
func (s *InMemoryStore) Delete(ctx context.Context, tenantID, connectionID, nodeID string) error {
	key := s.key(tenantID, connectionID, nodeID)
	delete(s.checkpoints, key)
	return nil
}

// DeleteForConnection removes all checkpoints for a connection
func (s *InMemoryStore) DeleteForConnection(ctx context.Context, tenantID, connectionID string) error {
	prefix := fmt.Sprintf("%s:%s:", tenantID, connectionID)
	for key := range s.checkpoints {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(s.checkpoints, key)
		}
	}
	return nil
}
