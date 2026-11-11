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

// The HTTP producer's url is tenant config and the node's headers and OAuth
// token go with the request (#660). The producer enforces at dial time; this is
// the early error the user sees when starting the connection. http is allowed.
func TestValidateNodeConfigs_HTTPProducerURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string // substring; empty = accepted
	}{
		{"https public", "https://example.com/ingest", ""},
		{"http public", "http://example.com/ingest?x=1", ""},
		{"userinfo", "https://u:p@example.com/ingest", ""},
		{"hostname", "http://internal.example.com/ingest", ""}, // the dial decides
		{"loopback", "http://127.0.0.1:8080/hook", "private"},
		{"loopback v6", "http://[::1]/hook", "private"},
		{"metadata", "http://169.254.169.254/metadata/instance", "private"},
		{"azure wire server", "http://168.63.129.16/", "private"},
		{"v4-mapped metadata", "http://[::ffff:169.254.169.254]/", "private"},
		{"private 10", "https://10.0.0.8/hook", "private"},
		{"private 192", "http://192.168.1.5/admin", "private"},
		{"no scheme", "example.com/hook", "not an absolute URL"},
		{"not http", "ftp://example.com/hook", "http or https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := `{"type":"http","http":{"url":"` + tt.url + `","method":"POST"}}`
			err := NewValidator().ValidateNodeConfigs(pipeline(okFileSource, dest))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected an error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), "http-producer"):
				t.Fatalf("error %q does not name the service", err)
			}
		})
	}
}

// The webhook consumer is also type "http" but has no url; only the producer's
// url is checked.
func TestValidateNodeConfigs_HTTPProducerURL_ConsumerUnaffected(t *testing.T) {
	if err := NewValidator().ValidateNodeConfigs(pipeline(`{"type":"http"}`, okHTTPDest)); err != nil {
		t.Fatalf("webhook consumer rejected: %v", err)
	}
}

func TestValidateNodeConfigs_HTTPProducerURL_DevFlag(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
	dest := `{"type":"http","http":{"url":"http://127.0.0.1:9000/hook"}}`
	if err := NewValidator().ValidateNodeConfigs(pipeline(okFileSource, dest)); err != nil {
		t.Fatalf("the operator's dev flag should allow a local mock: %v", err)
	}
}

// #850: a bearer token goes in the secrets vault. A node holding it in the
// clear, or auth_type bearer with no stored token, is refused at start, and
// the error never quotes the token.
func TestValidateNodeConfigs_HTTPProducerBearer(t *testing.T) {
	const tok = "test-bearer-not-real-0123"
	tests := []struct {
		name    string
		block   string
		wantErr string // substring; empty = accepted
	}{
		{"stored token", `"auth_type":"bearer","token_secret_id":"00000000-0000-0000-0000-000000000850"`, ""},
		{"no auth", ``, ""},
		{"oauth", `"auth_type":"oauth","oauth_grant_id":"g-1"`, ""},
		{"inline token", `"auth_type":"bearer","token":"` + tok + `"`, "http.token is written into the node"},
		{"inline token without auth_type", `"token":"` + tok + `"`, "http.token is written into the node"},
		{"bearer without a stored token", `"auth_type":"bearer"`, "http.token_secret_id is missing"},
		{"bearer with an empty stored token", `"auth_type":"bearer","token_secret_id":""`, "http.token_secret_id is missing"},
		// The producer decodes keys without regard to case, so the check does too.
		{"inline token, mixed-case key", `"Auth_Type":"bearer","Token":"` + tok + `"`, "http.token is written into the node"},
		{"bearer, mixed-case auth_type, no stored token", `"AUTH_TYPE":"bearer"`, "http.token_secret_id is missing"},
		{"bearer with a stored token id that is not a string", `"auth_type":"bearer","token_secret_id":123`, "http.token_secret_id is missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := `"url":"https://example.com/ingest","method":"POST"`
			if tt.block != "" {
				block += "," + tt.block
			}
			err := NewValidator().ValidateNodeConfigs(pipeline(okFileSource, `{"type":"http","http":{`+block+`}}`))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected an error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			case err != nil && strings.Contains(err.Error(), tok):
				t.Fatalf("the start error quotes the token: %q", err)
			}
		})
	}
}
