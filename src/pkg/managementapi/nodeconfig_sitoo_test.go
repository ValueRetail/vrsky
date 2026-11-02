package managementapi

import (
	"fmt"
	"strings"
	"testing"
)

// A Sitoo base_url is tenant config and the connector sends the node's
// credential to it (#616). The connector enforces at dial time; this is the
// early error the user sees when starting the connection.
func TestValidateNodeConfigs_SitooBaseURL(t *testing.T) {
	sitoo := func(baseURL string) string {
		return fmt.Sprintf(`{"type":"sitoo","sitoo":{"account_id":1,"site_id":2,"api_id":"x","base_url":%q}}`, baseURL)
	}
	tests := []struct {
		name    string
		baseURL string
		wantErr string // substring; empty = accepted
	}{
		{"unset uses the default", "", ""},
		{"default host", "https://api.mysitoo.com/v2", ""},
		{"http", "http://api.mysitoo.com/v2", "must use https"},
		{"loopback", "https://127.0.0.1/v2", "private"},
		{"private", "https://10.0.0.8/v2", "private"},
		{"metadata", "https://169.254.169.254/", "private"},
		{"ipv6 loopback", "https://[::1]/", "private"},
		{"userinfo", "https://u:p@api.mysitoo.com/v2", "user name"},
	}
	for _, tt := range tests {
		for _, side := range []string{"consumer", "producer"} {
			t.Run(side+"/"+tt.name, func(t *testing.T) {
				v := NewValidator()
				var conn *Connection
				if side == "consumer" {
					conn = pipeline(sitoo(tt.baseURL), okHTTPDest)
				} else {
					conn = pipeline(okFileSource, sitoo(tt.baseURL))
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

func TestValidateNodeConfigs_SitooBaseURL_DevFlag(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
	v := NewValidator()
	cfg := `{"type":"sitoo","sitoo":{"account_id":1,"site_id":2,"api_id":"x","base_url":"http://sitoo-mock:8080/v2"}}`
	if err := v.ValidateNodeConfigs(pipeline(cfg, okHTTPDest)); err != nil {
		t.Fatalf("the operator's dev flag should allow a mock Sitoo: %v", err)
	}
}
