package managementapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A stored secret may be referenced only from a credential setting (#853).
const placeholderID = "0f0f0f0f-1111-2222-3333-444444444444"

func placeholder() string { return "{secret:" + placeholderID + "}" }

// pipelineWith is a valid two-node pipeline: an inbound webhook into dst.
func pipelineWith(dst string) *Connection {
	return &Connection{
		Nodes: []*Node{
			{ID: "src", Type: "consumer", Config: json.RawMessage(`{"type":"http","http":{}}`)},
			{ID: "dst", Type: "producer", Config: json.RawMessage(dst)},
		},
		Edges: []*Edge{{ID: "e", Source: "src", Target: "dst"}},
	}
}

func TestValidateSecretPlaceholders(t *testing.T) {
	ph := placeholder()
	cases := []struct {
		name    string
		dst     string
		wantErr string // "" means the pipeline passes
	}{
		{"credential reference", `{"type":"sftp","sftp":{"host":"h","password_secret_id":"x"}}`, ""},
		{"stray reference is dropped, not refused", `{"type":"sftp","sftp":{"host":"h","mode_secret_id":"x"}}`, ""},
		{"header placeholder", `{"type":"http","http":{"url":"https://example.com","headers":{"X-Key":"k ` + ph + `"}}}`, ""},
		{"Azure connection string reference", `{"type":"cloud_storage","cloud_storage":{"provider":"azure","bucket":"b","connection_string_secret_id":"x"}}`, ""},
		{"Azure connection string placeholder", `{"type":"cloud_storage","cloud_storage":{"provider":"azure","bucket":"b","connection_string":"BlobEndpoint=http://127.0.0.1:1/` + ph + `;SharedAccessSignature=sv=x"}}`, "cloud_storage.connection_string holds a {secret:…} placeholder; reference the stored secret as cloud_storage.connection_string_secret_id instead"},
		{"http method", `{"type":"http","http":{"url":"https://example.com","method":"` + ph + `"}}`, "node dst: http.method holds a {secret:…} placeholder"},
		{"database table", `{"type":"database","database":{"host":"h","table":"` + ph + `"}}`, "database.table holds"},
		{"kafka auth type, other case", `{"type":"kafka","kafka":{"Auth_Type":"` + ph + `"}}`, "kafka.Auth_Type holds"},
		{"rabbitmq exchange", `{"type":"rabbitmq","rabbitmq":{"exchange":"` + ph + `"}}`, "rabbitmq.exchange holds"},
		{"sftp filename", `{"type":"sftp","sftp":{"filename_template":"x-` + ph + `"}}`, "sftp.filename_template holds"},
		{"sitoo resource", `{"type":"sitoo","sitoo":{"resource":"` + ph + `"}}`, "sitoo.resource holds"},
		{"front systems resource", `{"type":"front_systems","front_systems":{"resource":"` + ph + `"}}`, "front_systems.resource holds"},
		{"business central company", `{"type":"business_central","business_central":{"company_id":"` + ph + `"}}`, "business_central.company_id holds"},
		{"sap entity set", `{"type":"sap_s4hana","sap_s4hana":{"entity_set":"` + ph + `"}}`, "sap_s4hana.entity_set holds"},
		{"credential that takes only a reference", `{"type":"database","database":{"host":"h","password":"` + ph + `"}}`, "database.password holds a {secret:…} placeholder; reference the stored secret as database.password_secret_id instead"},
		{"setting beside the block", `{"type":"http","http":{"url":"https://example.com"},"label":"` + ph + `"}`, "node dst: label holds"},
	}
	v := NewValidator()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := pipelineWith(tc.dst)
			for _, check := range []struct {
				name string
				run  func(*Connection) error
			}{{"save", v.ValidateDAG}, {"start", v.ValidateNodeConfigs}} {
				err := check.run(conn)
				got := ""
				if dagErr, ok := err.(*DAGValidationError); ok {
					got = strings.Join(dagErr.Errors, "\n")
				} else if err != nil {
					got = err.Error()
				}
				if tc.wantErr == "" {
					// Start may refuse a node for reasons of its own; only
					// the placeholder message matters here.
					if strings.Contains(got, "placeholder") {
						t.Errorf("%s: refused: %s", check.name, got)
					}
					continue
				}
				if !strings.Contains(got, tc.wantErr) {
					t.Errorf("%s: errors = %q, want %q", check.name, got, tc.wantErr)
				}
			}
		})
	}
}

// Transform nodes have no secrets resolved, so their text is left alone.
func TestValidateSecretPlaceholders_OnlyEdgeNodes(t *testing.T) {
	if got := validateSecretPlaceholders(&Node{ID: "f", Type: "filter", Config: json.RawMessage(`{"expression":"` + placeholder() + `"}`)}); got != nil {
		t.Errorf("filter node refused: %v", got)
	}
	if got := validateSecretPlaceholders(&Node{ID: "c", Type: "consumer", Config: json.RawMessage(`{"type":`)}); got != nil {
		t.Errorf("broken JSON reported here: %v (the node check reports it)", got)
	}
}

// Test Connection refuses the placeholder itself, before any worker sees it:
// the nats worker resolves stored secrets for the session's tenant.
func TestTestConnection_PlaceholderRefusedBeforeTheWorker(t *testing.T) {
	var hits atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer worker.Close()
	t.Setenv("TEST_URL_NATS", worker.URL)
	t.Setenv("TEST_URL_SFTP", worker.URL)

	h := NewHandler(nil, NewValidator())
	post := func(body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/connections/test", strings.NewReader(body))
		req = req.WithContext(ContextWithTenantID(req.Context(), "tenant-a"))
		rr := httptest.NewRecorder()
		h.TestConnection(rr, req)
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("answer %q: %v", rr.Body.String(), err)
		}
		return out
	}

	out := post(`{"type":"nats","role":"consumer","nats":{"servers":["tls://h:4222"],"auth_type":"` + placeholder() + `"}}`)
	if out["ok"] != false || !strings.Contains(out["error"].(string), "nats.auth_type holds a {secret:…} placeholder") {
		t.Fatalf("answer = %v", out)
	}
	out = post(`{"type":"sftp","sftp":{"host":"h","remote_dir":"` + placeholder() + `"}}`)
	if out["ok"] != false || !strings.Contains(out["error"].(string), "sftp.remote_dir holds") {
		t.Fatalf("answer = %v", out)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the worker was called %d times", n)
	}

	// A credential reference still goes through.
	if out := post(`{"type":"nats","nats":{"servers":["tls://h:4222"],"auth_type":"user_password","password_secret_id":"x"}}`); out["ok"] != true {
		t.Fatalf("credential reference: %v", out)
	}
	if hits.Load() != 1 {
		t.Fatalf("the worker was called %d times, want 1", hits.Load())
	}
}

func TestDiscoverSchema_PlaceholderRefused(t *testing.T) {
	h := NewHandler(nil, NewValidator())
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/schema-discovery/{source}", h.DiscoverSchema)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/schema-discovery/sftp",
		bytes.NewReader([]byte(`{"host":"h","remote_dir":"`+placeholder()+`"}`)))
	req = req.WithContext(ContextWithTenantID(req.Context(), "tenant-a"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "sftp.remote_dir holds") {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}

	// The #868 review's case: the endpoint part of an Azure connection
	// string becomes the URL a transport error quotes.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/schema-discovery/cloud_storage",
		bytes.NewReader([]byte(`{"provider":"azure","bucket":"b","connection_string":"BlobEndpoint=http://127.0.0.1:1/`+placeholder()+`;SharedAccessSignature=sv=x"}`)))
	req = req.WithContext(ContextWithTenantID(req.Context(), "tenant-a"))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "cloud_storage.connection_string holds") {
		t.Fatalf("connection string: status %d body %s", rr.Code, rr.Body.String())
	}
}
