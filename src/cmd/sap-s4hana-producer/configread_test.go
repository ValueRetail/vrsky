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
		OwnNode:          `{"type":"sap_s4hana","sap_s4hana":{"password_secret_id":"00000000-0000-0000-0000-000000000001"}}`,
		OwnNodeHasSecret: true,
		ForeignNode:      `{"type":"http","http":{"url":"https://example.com"},"auth_value_secret_id":"00000000-0000-0000-0000-000000000002"}`,
		Positive: func(t *testing.T, db *sql.DB) (sdk.Producer, string, func() bool) {
			u := harness.NewUpstream(t)
			p := testProducer()
			p.db = db
			p.allowPrivate = true // the fake target is on 127.0.0.1
			return p, fmt.Sprintf(`{"type":"sap_s4hana","sap_s4hana":{"api_base_url":%q,"entity_set":"A_SalesOrder","username":"u","password_secret_id":%q}}`, u.URL+"/sap/opu/odata/sap/API_SALES_ORDER_SRV", harness.TestSecretID), u.Delivered
		},
	})
}
