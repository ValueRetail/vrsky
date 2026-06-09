package managementapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildTestTarget_Dispatch(t *testing.T) {
	raw := map[string]json.RawMessage{
		"database": json.RawMessage(`{"host":"h","table":"t"}`),
	}
	url, body, ok, _ := buildTestTarget("database", "consumer", raw, "ten")
	if !ok || !strings.HasSuffix(url, ":9300/test-connection/") {
		t.Fatalf("database consumer url=%q ok=%v", url, ok)
	}
	if !strings.Contains(string(body), `"host":"h"`) {
		t.Errorf("body should forward the database sub-config, got %s", body)
	}

	url, _, ok, _ = buildTestTarget("database", "producer", raw, "ten")
	if !ok || !strings.HasSuffix(url, ":9500/test-connection/") {
		t.Errorf("database producer should target db-producer, got %q", url)
	}

	// Salesforce body must inject tenant_id.
	raw = map[string]json.RawMessage{"salesforce": json.RawMessage(`{"instance_url":"u","oauth_grant_id":"g","soql":"SELECT Id FROM Account"}`)}
	_, body, ok, _ = buildTestTarget("salesforce", "consumer", raw, "ten-42")
	if !ok || !strings.Contains(string(body), `"tenant_id":"ten-42"`) {
		t.Errorf("salesforce body must carry tenant_id, got %s", body)
	}

	// Inbound webhook + unknown types are not testable.
	if _, _, ok, hint := buildTestTarget("http", "consumer", nil, "t"); ok || hint == "" {
		t.Errorf("http (webhook) should be unsupported with a hint")
	}
	if _, _, ok, _ := buildTestTarget("mystery", "consumer", nil, "t"); ok {
		t.Errorf("unknown type should be unsupported")
	}
}

// TestTestConnection_RelaysWorkerResult drives the handler against a fake worker
// and checks it relays the worker's {ok,...} without touching the DB.
func TestTestConnection_RelaysWorkerResult(t *testing.T) {
	var gotPath string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"sample":["customers"]}`))
	}))
	defer worker.Close()
	t.Setenv("TEST_URL_DB_CONSUMER", worker.URL)

	h := &Handler{} // TestConnection touches no Handler fields (no DB write).
	body := `{"type":"database","role":"consumer","database":{"host":"h","table":"customers"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connections/test", strings.NewReader(body))
	req = req.WithContext(ContextWithTenantID(req.Context(), "tenant-x"))
	rec := httptest.NewRecorder()

	h.TestConnection(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/test-connection/" {
		t.Errorf("worker path = %q, want /test-connection/", gotPath)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("relayed resp = %+v, want ok:true", resp)
	}
}

func TestTestConnection_UnsupportedType(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connections/test", strings.NewReader(`{"type":"http"}`))
	req = req.WithContext(ContextWithTenantID(req.Context(), "t"))
	rec := httptest.NewRecorder()
	h.TestConnection(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["ok"] != false || resp["error"] == nil {
		t.Errorf("want ok:false + error hint, got %+v", resp)
	}
}
