package managementapi

import (
	"strings"
	"testing"
)

// The API consumer's base_url and endpoint paths are tenant config and the
// node's credential goes with every request (#639). The consumer enforces at
// dial time; this is the early error the user sees when starting the
// connection. http is allowed, since public APIs use it.
func TestValidateNodeConfigs_APIConsumerURLs(t *testing.T) {
	tests := []struct {
		name    string
		api     string // the api block
		wantErr string // substring; empty = accepted
	}{
		{"https public", `{"base_url":"https://api.example.org","endpoints":[{"path":"/forecast/compact","params":"lat=59.9&lon=10.7"}]}`, ""},
		{"http public", `{"base_url":"http://api.example.com","endpoints":[{"path":"/v1/items"}]}`, ""},
		{"no endpoints defaults to /", `{"base_url":"https://api.example.com"}`, ""},
		{"full url in the path", `{"base_url":"","endpoints":[{"path":"https://api.example.com/v1"}]}`, ""},
		{"hostname", `{"base_url":"http://internal.example.com","endpoints":[{"path":"/"}]}`, ""}, // the dial decides
		{"loopback", `{"base_url":"http://127.0.0.1:8080","endpoints":[{"path":"/"}]}`, "private"},
		{"loopback v6", `{"base_url":"http://[::1]/","endpoints":[{"path":"/"}]}`, "private"},
		{"metadata", `{"base_url":"http://169.254.169.254","endpoints":[{"path":"/latest/meta-data/"}]}`, "private"},
		{"metadata, no endpoints", `{"base_url":"http://169.254.169.254"}`, "private"},
		{"private 10", `{"base_url":"https://10.0.0.8","endpoints":[{"path":"/"}]}`, "private"},
		{"private full url in the second endpoint", `{"base_url":"https://api.example.com","endpoints":[{"path":"/ok"},{"path":"http://192.168.1.5/admin"}]}`, "private"},
		{"relative path, no base", `{"base_url":"","endpoints":[{"path":"/v1"}]}`, "not an absolute URL"},
		{"not http", `{"base_url":"ftp://api.example.com","endpoints":[{"path":"/v1"}]}`, "http or https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := `{"type":"api","api":` + tt.api + `}`
			err := NewValidator().ValidateNodeConfigs(pipeline(cfg, okHTTPDest))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected an error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), "api-consumer"):
				t.Fatalf("error %q does not name the service", err)
			}
		})
	}
}

// Several endpoints with the same problem give one message, not one each.
func TestValidateNodeConfigs_APIConsumerURLs_OneMessagePerProblem(t *testing.T) {
	cfg := `{"type":"api","api":{"base_url":"https://api.example.com","endpoints":[{"path":"http://10.0.0.1/a"},{"path":"http://10.0.0.1/a"},{"path":"http://10.0.0.2/b"}]}}`
	err := NewValidator().ValidateNodeConfigs(pipeline(cfg, okHTTPDest))
	if err == nil {
		t.Fatal("expected an error")
	}
	if n := strings.Count(err.Error(), "private"); n != 2 {
		t.Errorf("got %d messages, want 2 (one per distinct URL): %v", n, err)
	}
}

func TestValidateNodeConfigs_APIConsumerURLs_DevFlag(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
	cfg := `{"type":"api","api":{"base_url":"http://127.0.0.1:9000","endpoints":[{"path":"/v1"}]}}`
	if err := NewValidator().ValidateNodeConfigs(pipeline(cfg, okHTTPDest)); err != nil {
		t.Fatalf("the operator's dev flag should allow a local mock: %v", err)
	}
}
