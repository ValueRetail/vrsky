package managementapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const secretsTestKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func setupSecretsHandler(t *testing.T) (*Handler, *MockRepository) {
	t.Setenv("ENCRYPTION_KEY", secretsTestKey)
	h, repo := setupTestHandler()
	return h, repo
}

func doRequest(t *testing.T, h http.HandlerFunc, method, target, tenantID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, target, &buf)
	if tenantID != "" {
		req = req.WithContext(ContextWithTenantID(req.Context(), tenantID))
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func mustDecodeData(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, body)
	}
	return env.Data
}

func TestSecrets_CreateGetList(t *testing.T) {
	h, _ := setupSecretsHandler(t)

	// Create
	w := doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", "tenant-A",
		CreateSecretRequest{Name: "pg-pwd", Value: "hunter2"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d (%s)", w.Code, w.Body.String())
	}
	data := mustDecodeData(t, w.Body.Bytes())
	id, _ := data["id"].(string)
	if id == "" {
		t.Fatalf("create: missing id in response")
	}
	if _, exists := data["value"]; exists {
		t.Fatalf("create: response must not contain plaintext value")
	}
	if _, exists := data["ciphertext"]; exists {
		t.Fatalf("create: response must not contain ciphertext")
	}

	// Get
	w = doRequest(t, h.SecretsItem, http.MethodGet, "/api/v1/secrets/"+id, "tenant-A", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	got := mustDecodeData(t, w.Body.Bytes())
	if got["name"] != "pg-pwd" {
		t.Fatalf("get: wrong name %v", got["name"])
	}

	// List
	w = doRequest(t, h.SecretsCollection, http.MethodGet, "/api/v1/secrets", "tenant-A", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", w.Code)
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data) != 1 || env.Data[0]["id"] != id {
		t.Fatalf("list: want 1 secret, got %+v", env.Data)
	}
}

func TestSecrets_CreateRequiresFields(t *testing.T) {
	h, _ := setupSecretsHandler(t)
	w := doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", "tenant-A",
		CreateSecretRequest{Name: "", Value: ""})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestSecrets_CreateRequiresTenant(t *testing.T) {
	h, _ := setupSecretsHandler(t)
	w := doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", "",
		CreateSecretRequest{Name: "x", Value: "y"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 on missing tenant, got %d", w.Code)
	}
}

func TestSecrets_MissingMasterKey(t *testing.T) {
	// Don't set ENCRYPTION_KEY for this test.
	t.Setenv("ENCRYPTION_KEY", "")
	h, _ := setupTestHandler()
	w := doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", "tenant-A",
		CreateSecretRequest{Name: "x", Value: "y"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on missing master key, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ConfigError") {
		t.Fatalf("want ConfigError, got %s", w.Body.String())
	}
}

func TestSecrets_TenantIsolation(t *testing.T) {
	h, _ := setupSecretsHandler(t)

	// Tenant A creates secret.
	w := doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", "tenant-A",
		CreateSecretRequest{Name: "shared-name", Value: "A-secret"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create A: %d", w.Code)
	}
	idA := mustDecodeData(t, w.Body.Bytes())["id"].(string)

	// Tenant B cannot read it.
	w = doRequest(t, h.SecretsItem, http.MethodGet, "/api/v1/secrets/"+idA, "tenant-B", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("B should get 404, got %d", w.Code)
	}

	// Tenant B cannot delete it.
	w = doRequest(t, h.SecretsItem, http.MethodDelete, "/api/v1/secrets/"+idA, "tenant-B", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("B delete: want 404, got %d", w.Code)
	}

	// Tenant B cannot list A's secrets.
	w = doRequest(t, h.SecretsCollection, http.MethodGet, "/api/v1/secrets", "tenant-B", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list B: %d", w.Code)
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data) != 0 {
		t.Fatalf("B should see no secrets, got %+v", env.Data)
	}

	// Tenant B can create a secret with the same name (per-tenant unique).
	w = doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", "tenant-B",
		CreateSecretRequest{Name: "shared-name", Value: "B-secret"})
	if w.Code != http.StatusCreated {
		t.Fatalf("B create same name: %d (%s)", w.Code, w.Body.String())
	}
}

func TestSecrets_DuplicateName(t *testing.T) {
	h, _ := setupSecretsHandler(t)
	_ = doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", "tenant-A",
		CreateSecretRequest{Name: "dup", Value: "v1"})
	w := doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", "tenant-A",
		CreateSecretRequest{Name: "dup", Value: "v2"})
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 on duplicate, got %d", w.Code)
	}
}

func TestSecrets_UpdateRotatesTimestamp(t *testing.T) {
	h, _ := setupSecretsHandler(t)
	w := doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", "tenant-A",
		CreateSecretRequest{Name: "x", Value: "v1"})
	id := mustDecodeData(t, w.Body.Bytes())["id"].(string)

	v2 := "v2"
	w = doRequest(t, h.SecretsItem, http.MethodPut, "/api/v1/secrets/"+id, "tenant-A",
		UpdateSecretRequest{Value: &v2})
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d (%s)", w.Code, w.Body.String())
	}
	out := mustDecodeData(t, w.Body.Bytes())
	if out["rotated_at"] == nil {
		t.Fatalf("rotated_at should be populated after value change")
	}
}

func TestSecrets_RotateRewrapsCiphertext(t *testing.T) {
	h, repo := setupSecretsHandler(t)
	w := doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", "tenant-A",
		CreateSecretRequest{Name: "x", Value: "v1"})
	id := mustDecodeData(t, w.Body.Bytes())["id"].(string)

	before, _ := repo.GetSecretCiphertext(nil, "tenant-A", id)

	w = doRequest(t, h.SecretsItem, http.MethodPost, "/api/v1/secrets/"+id+"/rotate", "tenant-A", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("rotate: %d (%s)", w.Code, w.Body.String())
	}
	after, _ := repo.GetSecretCiphertext(nil, "tenant-A", id)
	if before == after {
		t.Fatalf("ciphertext should change after rotate (new nonce)")
	}
}

func TestSecrets_DeleteRejectedWhenReferenced(t *testing.T) {
	h, repo := setupSecretsHandler(t)
	w := doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", "tenant-A",
		CreateSecretRequest{Name: "x", Value: "v"})
	id := mustDecodeData(t, w.Body.Bytes())["id"].(string)

	// Attach a connection whose JSON contains the secret id.
	cfg, _ := json.Marshal(map[string]any{"password_secret_id": id})
	repo.connections["conn-1"] = &Connection{
		ID:       "conn-1",
		TenantID: "tenant-A",
		Name:     "uses-secret",
		Nodes:    []*Node{{ID: "n", Type: "consumer", Config: cfg, Enabled: true}},
	}

	w = doRequest(t, h.SecretsItem, http.MethodDelete, "/api/v1/secrets/"+id, "tenant-A", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 when referenced, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestSecrets_DeleteOK(t *testing.T) {
	h, _ := setupSecretsHandler(t)
	w := doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", "tenant-A",
		CreateSecretRequest{Name: "x", Value: "v"})
	id := mustDecodeData(t, w.Body.Bytes())["id"].(string)

	w = doRequest(t, h.SecretsItem, http.MethodDelete, "/api/v1/secrets/"+id, "tenant-A", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d (%s)", w.Code, w.Body.String())
	}

	w = doRequest(t, h.SecretsItem, http.MethodGet, "/api/v1/secrets/"+id, "tenant-A", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("get-after-delete: want 404, got %d", w.Code)
	}
}
