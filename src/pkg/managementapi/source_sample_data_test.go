package managementapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// SQL fragments used to match the queries GetSourceSampleData issues. The
// default sqlmock matcher is regexp-based and matches anywhere in the query,
// so these distinctive substrings are enough to disambiguate the three queries.
const (
	qApproval     = "FROM tenant_data_connections"
	qSpecificConn = "FROM connections WHERE id"
	qFallback     = "ORDER BY updated_at DESC"
)

// setupSourceSampleHandler returns a handler wired to a mock DB plus the mock
// controller so each test can program the expected queries.
func setupSourceSampleHandler(t *testing.T) (*Handler, sqlmock.Sqlmock, func()) {
	t.Helper()
	handler, _ := setupTestHandler()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	handler.SetDB(db)
	return handler, mock, func() { _ = db.Close() }
}

// callSourceSampleData drives the handler with the given query string and a
// valid tenant context, returning the recorder.
func callSourceSampleData(handler *Handler, query string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/v1/sample-data/source"+query, nil)
	r = r.WithContext(contextWithTenant("tenant-1"))
	w := httptest.NewRecorder()
	handler.GetSourceSampleData(w, r)
	return w
}

// storedEnvelope mimics how connections.last_payload is persisted: a serialized
// envelope whose `payload` field holds the raw bytes (base64 in JSON).
func storedEnvelope(t *testing.T, payload []byte) []byte {
	t.Helper()
	b, err := json.Marshal(struct {
		Payload []byte `json:"payload"`
	}{Payload: payload})
	if err != nil {
		t.Fatalf("failed to marshal envelope: %v", err)
	}
	return b
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body %q: %v", w.Body.String(), err)
	}
	return body
}

// (1) Missing source_tenant_id must 400 before any DB access.
func TestGetSourceSampleData_MissingSourceTenantID(t *testing.T) {
	handler, mock, cleanup := setupSourceSampleHandler(t)
	defer cleanup()

	w := callSourceSampleData(handler, "?source_connection_id=conn-1")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB access: %v", err)
	}
}

// (2) No active tenant_data_connection must 403.
func TestGetSourceSampleData_NoActiveConnection(t *testing.T) {
	handler, mock, cleanup := setupSourceSampleHandler(t)
	defer cleanup()

	mock.ExpectQuery(qApproval).
		WithArgs("tenant-1", "src-tenant").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	w := callSourceSampleData(handler, "?source_tenant_id=src-tenant")

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", w.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// (2b) A DB error on the approval check is a server error (500), not a 403 -
// transient outages must not be masked as authorization failures.
func TestGetSourceSampleData_ApprovalQueryError(t *testing.T) {
	handler, mock, cleanup := setupSourceSampleHandler(t)
	defer cleanup()

	mock.ExpectQuery(qApproval).
		WithArgs("tenant-1", "src-tenant").
		WillReturnError(sql.ErrConnDone)

	w := callSourceSampleData(handler, "?source_tenant_id=src-tenant")

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", w.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// (3) Active connection but no last_payload anywhere -> ok=false.
func TestGetSourceSampleData_NoPayload(t *testing.T) {
	handler, mock, cleanup := setupSourceSampleHandler(t)
	defer cleanup()

	mock.ExpectQuery(qApproval).
		WithArgs("tenant-1", "src-tenant").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	// No source_connection_id given, so it goes straight to the fallback query.
	mock.ExpectQuery(qFallback).
		WithArgs("src-tenant").
		WillReturnError(sql.ErrNoRows)

	w := callSourceSampleData(handler, "?source_tenant_id=src-tenant")

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if body := decodeBody(t, w); body["ok"] != false {
		t.Errorf("expected ok=false, got %v", body["ok"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// (4) A source_connection_id that doesn't exist falls back to the most recent
// connection with a payload, and returns it.
func TestGetSourceSampleData_UnknownConnectionFallsBack(t *testing.T) {
	handler, mock, cleanup := setupSourceSampleHandler(t)
	defer cleanup()

	payload := storedEnvelope(t, []byte(`{"order_id":42}`))
	mock.ExpectQuery(qApproval).
		WithArgs("tenant-1", "src-tenant").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(qSpecificConn).
		WithArgs("missing-conn", "src-tenant").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(qFallback).
		WithArgs("src-tenant").
		WillReturnRows(sqlmock.NewRows([]string{"last_payload"}).AddRow(payload))

	w := callSourceSampleData(handler, "?source_tenant_id=src-tenant&source_connection_id=missing-conn")

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	body := decodeBody(t, w)
	if body["ok"] != true {
		t.Errorf("expected ok=true after fallback, got %v", body["ok"])
	}
	data, _ := body["data"].(map[string]interface{})
	if data["order_id"] != float64(42) {
		t.Errorf("expected fallback payload order_id=42, got %v", body["data"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// (4b) Unknown source_connection_id with no fallback payload -> ok=false.
func TestGetSourceSampleData_UnknownConnectionNoFallback(t *testing.T) {
	handler, mock, cleanup := setupSourceSampleHandler(t)
	defer cleanup()

	mock.ExpectQuery(qApproval).
		WithArgs("tenant-1", "src-tenant").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(qSpecificConn).
		WithArgs("missing-conn", "src-tenant").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(qFallback).
		WithArgs("src-tenant").
		WillReturnError(sql.ErrNoRows)

	w := callSourceSampleData(handler, "?source_tenant_id=src-tenant&source_connection_id=missing-conn")

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if body := decodeBody(t, w); body["ok"] != false {
		t.Errorf("expected ok=false, got %v", body["ok"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// (5) A stored payload that isn't a valid envelope -> ok=false.
func TestGetSourceSampleData_MalformedEnvelope(t *testing.T) {
	handler, mock, cleanup := setupSourceSampleHandler(t)
	defer cleanup()

	mock.ExpectQuery(qApproval).
		WithArgs("tenant-1", "src-tenant").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(qFallback).
		WithArgs("src-tenant").
		WillReturnRows(sqlmock.NewRows([]string{"last_payload"}).AddRow([]byte("this is not json")))

	w := callSourceSampleData(handler, "?source_tenant_id=src-tenant")

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	body := decodeBody(t, w)
	if body["ok"] != false {
		t.Errorf("expected ok=false for malformed envelope, got %v", body["ok"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// Happy path: a specific connection with a valid JSON envelope -> ok=true and
// the decoded payload is returned.
func TestGetSourceSampleData_ValidPayload(t *testing.T) {
	handler, mock, cleanup := setupSourceSampleHandler(t)
	defer cleanup()

	payload := storedEnvelope(t, []byte(`{"customer":"acme","amount":99}`))
	mock.ExpectQuery(qApproval).
		WithArgs("tenant-1", "src-tenant").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(qSpecificConn).
		WithArgs("conn-1", "src-tenant").
		WillReturnRows(sqlmock.NewRows([]string{"last_payload"}).AddRow(payload))

	w := callSourceSampleData(handler, "?source_tenant_id=src-tenant&source_connection_id=conn-1")

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	body := decodeBody(t, w)
	if body["ok"] != true {
		t.Errorf("expected ok=true, got %v", body["ok"])
	}
	data, _ := body["data"].(map[string]interface{})
	if data["customer"] != "acme" || data["amount"] != float64(99) {
		t.Errorf("unexpected decoded payload: %v", body["data"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
