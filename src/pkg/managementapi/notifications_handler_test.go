package managementapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func createTargetForTest(t *testing.T, h *Handler, tenant string, body map[string]interface{}) notificationTargetResponse {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/api/v1/notifications/targets", bytes.NewReader(b)).
		WithContext(contextWithTenant(tenant))
	w := httptest.NewRecorder()
	h.CreateNotificationTarget(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create target: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp notificationTargetResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestCreateNotificationTarget_SuccessAndNoSecretLeak(t *testing.T) {
	handler, _ := setupTestHandler()
	resp := createTargetForTest(t, handler, "tenant-1", map[string]interface{}{
		"name":   "ops-slack",
		"type":   "slack",
		"secret": "https://hooks.slack.com/services/T0/B0/supersecret",
	})
	if resp.ID == "" || !resp.HasSecret || !resp.Enabled {
		t.Errorf("unexpected response: %+v", resp)
	}
	// The webhook URL is a secret and must never appear in any response.
	list := httptest.NewRequest("GET", "/api/v1/notifications/targets", nil).
		WithContext(contextWithTenant("tenant-1"))
	w := httptest.NewRecorder()
	handler.ListNotificationTargets(w, list)
	if strings.Contains(w.Body.String(), "supersecret") {
		t.Errorf("response leaks the secret: %s", w.Body.String())
	}
}

func TestCreateNotificationTarget_Validation(t *testing.T) {
	handler, _ := setupTestHandler()
	cases := []map[string]interface{}{
		{"type": "slack", "secret": "https://hooks..."},                          // no name
		{"name": "x", "type": "slack"},                                           // slack without secret
		{"name": "x", "type": "email", "email": "not-an-email"},                  // bad email
		{"name": "x", "type": "pagerduty"},                                       // pd without routing key
		{"name": "x", "type": "webhook", "url": "ftp://nope"},                    // non-http url
		{"name": "x", "type": "carrier-pigeon"},                                  // unknown type
		{"name": "x", "type": "email", "email": "a@b.c", "min_severity": "loud"}, // bad severity
	}
	for i, body := range cases {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/api/v1/notifications/targets", bytes.NewReader(b)).
			WithContext(contextWithTenant("tenant-1"))
		w := httptest.NewRecorder()
		handler.CreateNotificationTarget(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("case %d: status=%d, want 400 (body=%s)", i, w.Code, w.Body.String())
		}
	}
}

func TestNotificationTarget_TenantIsolation(t *testing.T) {
	handler, _ := setupTestHandler()
	created := createTargetForTest(t, handler, "tenant-1", map[string]interface{}{
		"name": "mail", "type": "email", "email": "ops@a.com",
	})
	// Another tenant cannot read, update, or delete it.
	r := httptest.NewRequest("DELETE", "/api/v1/notifications/targets/"+created.ID, nil).
		WithContext(contextWithTenant("tenant-2"))
	r.SetPathValue("id", created.ID)
	w := httptest.NewRecorder()
	handler.DeleteNotificationTarget(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant delete: status=%d, want 404", w.Code)
	}
}

func TestUpdateNotificationTarget_KeepsSecretWhenOmitted(t *testing.T) {
	handler, repo := setupTestHandler()
	created := createTargetForTest(t, handler, "tenant-1", map[string]interface{}{
		"name": "ops-slack", "type": "slack", "secret": "hook-url-1",
	})
	body, _ := json.Marshal(map[string]interface{}{
		"name": "ops-slack-renamed", "type": "slack", // no secret in PUT
	})
	r := httptest.NewRequest("PUT", "/api/v1/notifications/targets/"+created.ID, bytes.NewReader(body)).
		WithContext(contextWithTenant("tenant-1"))
	r.SetPathValue("id", created.ID)
	w := httptest.NewRecorder()
	handler.UpdateNotificationTarget(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("update: status=%d body=%s", w.Code, w.Body.String())
	}
	tgt, err := repo.GetNotificationTarget(contextWithTenant("tenant-1"), "tenant-1", created.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if tgt.SecretID == "" {
		t.Error("secret reference lost on PUT without a new secret")
	}
	if tgt.Name != "ops-slack-renamed" {
		t.Errorf("name = %q", tgt.Name)
	}
}

func TestAlertsWebhook_AuthAndDispatch(t *testing.T) {
	t.Setenv("ALERTS_WEBHOOK_TOKEN", "tok-123")
	handler, _ := setupTestHandler()

	// A webhook target catches the dispatched alert (no secret → plain POST).
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	createTargetForTest(t, handler, "tenant-1", map[string]interface{}{
		"name": "catcher", "type": "webhook", "url": srv.URL,
	})

	payload := map[string]interface{}{
		"status": "firing",
		"alerts": []map[string]interface{}{{
			"status": "firing",
			"labels": map[string]string{
				"alertname": "PipelineDown", "severity": "critical", "tenant_id": "tenant-1",
			},
			"annotations": map[string]string{"summary": "no messages for 10m"},
		}},
	}
	b, _ := json.Marshal(payload)

	// Wrong token → 401, nothing delivered.
	r := httptest.NewRequest("POST", "/api/v1/alerts/webhook", bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	handler.AlertsWebhook(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: status=%d, want 401", w.Code)
	}

	// Correct token → dispatched to the tenant's webhook target.
	r = httptest.NewRequest("POST", "/api/v1/alerts/webhook", bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer tok-123")
	w = httptest.NewRecorder()
	handler.AlertsWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("webhook: status=%d body=%s", w.Code, w.Body.String())
	}
	var res struct {
		Delivered int `json:"delivered"`
		Failed    int `json:"failed"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res.Delivered != 1 || res.Failed != 0 {
		t.Errorf("delivered/failed = %d/%d, want 1/0", res.Delivered, res.Failed)
	}
	if !strings.Contains(string(received), "PipelineDown") {
		t.Errorf("target did not receive the alert: %s", received)
	}
}

func TestAlertsWebhook_PlatformRoutingAndSeverityFilter(t *testing.T) {
	t.Setenv("ALERTS_WEBHOOK_TOKEN", "tok-123")
	handler, _ := setupTestHandler()

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Platform-flagged target with min_severity=critical.
	createTargetForTest(t, handler, "tenant-1", map[string]interface{}{
		"name": "platform-hook", "type": "webhook", "url": srv.URL,
		"platform": true, "min_severity": "critical",
	})

	send := func(severity string) {
		payload := map[string]interface{}{
			"alerts": []map[string]interface{}{{
				"status":      "firing",
				"labels":      map[string]string{"alertname": "DiskUsageHigh", "severity": severity},
				"annotations": map[string]string{"summary": "disk > 80%"},
			}},
		}
		b, _ := json.Marshal(payload)
		r := httptest.NewRequest("POST", "/api/v1/alerts/webhook", bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer tok-123")
		w := httptest.NewRecorder()
		handler.AlertsWebhook(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("webhook: status=%d", w.Code)
		}
	}

	send("warning") // below min_severity — filtered out
	if hits != 0 {
		t.Errorf("warning alert should be filtered, hits=%d", hits)
	}
	send("critical") // platform alert (no tenant_id) reaches the platform target
	if hits != 1 {
		t.Errorf("critical platform alert should deliver, hits=%d", hits)
	}
}

func TestAlertsWebhook_NotConfigured(t *testing.T) {
	t.Setenv("ALERTS_WEBHOOK_TOKEN", "")
	handler, _ := setupTestHandler()
	r := httptest.NewRequest("POST", "/api/v1/alerts/webhook", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	handler.AlertsWebhook(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503 when token unset", w.Code)
	}
}
