// Package idempotency remembers the Idempotency-Key of inbound webhook
// requests per connection (plans/webhook-idempotency.md), so a sender whose
// outbox delivers at least once gets the same answer for a retry without the
// message being published twice. Durable in Postgres; in memory for tests and
// runs without a database.
package idempotency

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

// Retention is how long a key is remembered. Bifrost asked for at least seven
// days; a till that was boxed up for a fortnight still gets the right answer.
const Retention = 30 * 24 * time.Hour

// MaxKeyLength is the longest key accepted (the column width).
const MaxKeyLength = 64

// Store is what the webhook consumer needs.
type Store interface {
	// Seen reports whether key was remembered for this connection, and the
	// envelope id the first delivery became.
	Seen(ctx context.Context, tenantID, connectionID, key string) (envelopeID string, seen bool, err error)
	// Remember records key → envelopeID. A key already present is left as it
	// is: the first delivery wins, and remembering twice is not an error.
	Remember(ctx context.Context, tenantID, connectionID, key, envelopeID string) error
	// Expire forgets keys remembered before olderThan; returns how many.
	Expire(ctx context.Context, olderThan time.Time) (int64, error)
}

// PostgresStore keeps keys in webhook_idempotency_keys (migration 000027).
type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Seen(ctx context.Context, tenantID, connectionID, key string) (string, bool, error) {
	var envelopeID string
	err := s.db.QueryRowContext(ctx, `
		SELECT envelope_id::text FROM webhook_idempotency_keys
		 WHERE tenant_id = $1 AND connection_id = $2 AND idempotency_key = $3`,
		tenantID, connectionID, key).Scan(&envelopeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return envelopeID, true, nil
}

func (s *PostgresStore) Remember(ctx context.Context, tenantID, connectionID, key, envelopeID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO webhook_idempotency_keys (tenant_id, connection_id, idempotency_key, envelope_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (connection_id, idempotency_key) DO NOTHING`,
		tenantID, connectionID, key, envelopeID)
	return err
}

func (s *PostgresStore) Expire(ctx context.Context, olderThan time.Time) (int64, error) {
	// lint:tenant-ok — retention sweep over every workspace's keys; nothing is read back.
	res, err := s.db.ExecContext(ctx, `DELETE FROM webhook_idempotency_keys WHERE created_at < $1`, olderThan)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MemoryStore is the in-memory Store.
type MemoryStore struct {
	mu   sync.Mutex
	rows map[string]memoryRow
	now  func() time.Time
}

type memoryRow struct {
	tenantID, envelopeID string
	at                   time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[string]memoryRow{}, now: time.Now}
}

func (s *MemoryStore) Seen(_ context.Context, tenantID, connectionID, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[connectionID+"\x00"+key]
	if !ok || r.tenantID != tenantID {
		return "", false, nil
	}
	return r.envelopeID, true, nil
}

func (s *MemoryStore) Remember(_ context.Context, tenantID, connectionID, key, envelopeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := connectionID + "\x00" + key
	if _, ok := s.rows[k]; !ok {
		s.rows[k] = memoryRow{tenantID: tenantID, envelopeID: envelopeID, at: s.now()}
	}
	return nil
}

func (s *MemoryStore) Expire(_ context.Context, olderThan time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, r := range s.rows {
		if r.at.Before(olderThan) {
			delete(s.rows, k)
			n++
		}
	}
	return n, nil
}

// Len is how many keys the store holds (tests).
func (s *MemoryStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}
