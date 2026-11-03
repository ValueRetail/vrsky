package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
			return &salesforceProducer{
				db:        db,
				logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				cache:     map[string][]*sfTarget{},
				cacheTime: map[string]time.Time{},
				cacheTTL:  time.Minute,
			}
		},
		Columns:          []string{"nodes", "edges"},
		OwnNode:          `{"type":"salesforce","salesforce":{"object":"Account"}}`,
		OwnNodeHasSecret: false,
		ForeignNode:      `{"type":"http","http":{"url":"https://example.com"},"auth_value_secret_id":"00000000-0000-0000-0000-000000000002"}`,
		Positive: func(t *testing.T, db *sql.DB) (sdk.Producer, string, func() bool) {
			u := harness.NewUpstream(t)
			p := &salesforceProducer{
				db:            db,
				logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
				httpClient:    &http.Client{Timeout: 10 * time.Second},
				bulkThreshold: 200,
				cache:         map[string][]*sfTarget{},
				cacheTime:     map[string]time.Time{},
				cacheTTL:      time.Minute,
				resolveToken:  func(context.Context, string, string, bool) (string, error) { return "tok", nil },
			}
			node := fmt.Sprintf(`{"type":"salesforce","salesforce":{"instance_url":%q,"oauth_grant_id":"g1","object":"Account"}}`, u.URL)
			return p, node, func() bool { return u.Writes() == 1 }
		},
	})
}
