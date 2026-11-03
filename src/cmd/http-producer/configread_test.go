package main

import (
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A failed config or secret read must not ack the message (#704); a
// connection that isn't ours still acks.
func TestConfigRead_FailureIsNotAcked(t *testing.T) {
	harness.RunConfigReadCases(t, harness.ConfigReadSpec{
		New: func(t *testing.T, db *sql.DB) sdk.Producer {
			return &httpProducer{
				db:              db,
				logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
				configCache:     map[string][]*HTTPConfig{},
				configCacheTime: map[string]time.Time{},
				configCacheTTL:  time.Minute,
			}
		},
		Columns:          []string{"nodes", "edges"},
		OwnNode:          `{"type":"http","http":{"url":"https://example.com/in"},"auth_value_secret_id":"00000000-0000-0000-0000-000000000001"}`,
		OwnNodeHasSecret: true,
		ForeignNode:      `{"type":"kafka","kafka":{"brokers":["b:9092"],"password_secret_id":"00000000-0000-0000-0000-000000000002"}}`,
	})
}
