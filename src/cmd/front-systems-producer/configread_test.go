package main

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A failed config or secret read must not ack the message (#704); a
// connection that isn't ours still acks.
func TestConfigRead_FailureIsNotAcked(t *testing.T) {
	harness.RunConfigReadCases(t, harness.ConfigReadSpec{
		New: func(t *testing.T, db *sql.DB) sdk.Producer {
			p := testProducer()
			p.db = db
			return p
		},
		Columns:          []string{"nodes"},
		OwnNode:          `{"type":"front_systems","front_systems":{"subscription_key_secret_id":"00000000-0000-0000-0000-000000000001"}}`,
		OwnNodeHasSecret: true,
		PlaceholderField: "front_systems.resource",
		ForeignNode:      `{"type":"http","http":{"url":"https://example.com"},"auth_value_secret_id":"00000000-0000-0000-0000-000000000002"}`,
		Positive: func(t *testing.T, db *sql.DB) (sdk.Producer, string, func() bool) {
			u := harness.NewUpstream(t)
			p := testProducer()
			p.db = db
			p.allowPrivate = true // the fake target is on 127.0.0.1
			return p, fmt.Sprintf(`{"type":"front_systems","front_systems":{"base_url":%q,"subscription_key_secret_id":%q,"api_key":"k","resource":"/api/Products"}}`, u.URL, harness.TestSecretID), u.Delivered
		},
	})
}
