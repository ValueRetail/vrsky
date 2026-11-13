package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A failed config or secret read must not ack the message (#704); a
// connection that isn't ours still acks.
func TestConfigRead_FailureIsNotAcked(t *testing.T) {
	harness.RunConfigReadCases(t, harness.ConfigReadSpec{
		New: func(t *testing.T, db *sql.DB) sdk.Producer {
			return &cloudProducer{
				db:        db,
				logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				cache:     map[string][]*cloudTarget{},
				cacheTime: map[string]time.Time{},
				cacheTTL:  time.Minute,
			}
		},
		Columns:          []string{"nodes", "edges"},
		OwnNode:          `{"type":"cloud_storage","cloud_storage":{"bucket":"b","secret_access_key_secret_id":"00000000-0000-0000-0000-000000000001"}}`,
		OwnNodeHasSecret: true,
		PlaceholderField: "cloud_storage.bucket",
		ForeignNode:      `{"type":"http","http":{"url":"https://example.com"},"auth_value_secret_id":"00000000-0000-0000-0000-000000000002"}`,
		Positive: func(t *testing.T, db *sql.DB) (sdk.Producer, string, func() bool) {
			fake := &fakeStore{}
			var mu sync.Mutex
			var secret string
			p := &cloudProducer{
				db:        db,
				logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				cache:     map[string][]*cloudTarget{},
				cacheTime: map[string]time.Time{},
				cacheTTL:  time.Minute,
				now:       time.Now,
				newStore: func(_ context.Context, cfg *objectstore.Config) (objectstore.ObjectStore, error) {
					mu.Lock()
					secret = cfg.SecretAccessKey
					mu.Unlock()
					return fake, nil
				},
			}
			node := fmt.Sprintf(`{"type":"cloud_storage","cloud_storage":{"provider":"s3","bucket":"b","access_key_id":"ak","secret_access_key_secret_id":%q}}`, harness.TestSecretID)
			return p, node, func() bool {
				mu.Lock()
				defer mu.Unlock()
				return len(fake.keys()) == 1 && secret == harness.TestSecret
			}
		},
	})
}
