package managementapi

import (
	"fmt"
	"strings"
	"testing"
)

// A Visma base_url and token_url are tenant config and the connectors send the
// client secret and the bearer token to them (#661). The connectors enforce at
// dial time; this is the early error the user sees when starting the
// connection.
func TestValidateNodeConfigs_VismaURLs(t *testing.T) {
	visma := func(baseURL, tokenURL string) string {
		return fmt.Sprintf(`{"type":"visma","visma":{"base_url":%q,"token_url":%q,"scope":"s","client_id":"id","resource":"SalesOrders"}}`, baseURL, tokenURL)
	}
	const okBase = "https://salesorder.visma.net/api/v3"
	tests := []struct {
		name, baseURL, tokenURL string
		wantErr                 string // substring; empty = accepted
	}{
		{"real defaults", okBase, "", ""},
		{"real defaults, explicit token url", okBase, "https://connect.visma.com/connect/token", ""},
		{"base http", "http://salesorder.visma.net/api/v3", "", "must use https"},
		{"base loopback", "https://127.0.0.1/v3", "", "private"},
		{"base private", "https://10.0.0.8/v3", "", "private"},
		{"base metadata", "https://169.254.169.254/", "", "private"},
		{"base ipv6 loopback", "https://[::1]/", "", "private"},
		{"base userinfo", "https://u:p@salesorder.visma.net/api/v3", "", "user name"},
		{"token http", okBase, "http://connect.visma.com/connect/token", "visma.token_url"},
		{"token loopback", okBase, "https://127.0.0.1/token", "visma.token_url"},
		{"token metadata", okBase, "https://169.254.169.254/token", "visma.token_url"},
	}
	for _, tt := range tests {
		for _, side := range []string{"consumer", "producer"} {
			t.Run(side+"/"+tt.name, func(t *testing.T) {
				v := NewValidator()
				var conn *Connection
				if side == "consumer" {
					conn = pipeline(visma(tt.baseURL, tt.tokenURL), okHTTPDest)
				} else {
					conn = pipeline(okFileSource, visma(tt.baseURL, tt.tokenURL))
				}
				err := v.ValidateNodeConfigs(conn)
				switch {
				case tt.wantErr == "" && err != nil:
					t.Fatalf("unexpected error: %v", err)
				case tt.wantErr != "" && err == nil:
					t.Fatalf("expected an error containing %q", tt.wantErr)
				case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
					t.Fatalf("error %q does not contain %q", err, tt.wantErr)
				}
			})
		}
	}
}

func TestValidateNodeConfigs_VismaURLs_DevFlag(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
	v := NewValidator()
	cfg := `{"type":"visma","visma":{"base_url":"http://visma-mock:8080/api","token_url":"http://visma-mock:8080/token","scope":"s","client_id":"id","resource":"SalesOrders"}}`
	if err := v.ValidateNodeConfigs(pipeline(cfg, okHTTPDest)); err != nil {
		t.Fatalf("the operator's dev flag should allow a mock Visma: %v", err)
	}
}
