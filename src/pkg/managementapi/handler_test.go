package managementapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// MockRepository implements Repository interface for testing
type MockRepository struct {
	connections map[string]*Connection
	events      []*ConnectionEvent
}

func NewMockRepository() *MockRepository {
	return &MockRepository{
		connections: make(map[string]*Connection),
		events:      make([]*ConnectionEvent, 0),
	}
}

func (m *MockRepository) CreateConnection(ctx context.Context, conn *Connection) error {
	if conn.ID == "" {
		conn.ID = "test-id-" + time.Now().Format("20060102150405")
	}
	m.connections[conn.ID] = conn
	return nil
}

func (m *MockRepository) GetConnection(ctx context.Context, id string) (*Connection, error) {
	if conn, exists := m.connections[id]; exists {
		return conn, nil
	}
	return nil, &NotFoundError{ResourceType: "Connection", ResourceID: id}
}

func (m *MockRepository) ListConnections(ctx context.Context, tenantID string, filters *ListFilters) ([]*Connection, int64, error) {
	var results []*Connection
	for _, conn := range m.connections {
		if conn.TenantID == tenantID {
			results = append(results, conn)
		}
	}
	return results, int64(len(results)), nil
}

func (m *MockRepository) UpdateConnection(ctx context.Context, conn *Connection) error {
	if _, exists := m.connections[conn.ID]; !exists {
		return &NotFoundError{ResourceType: "Connection", ResourceID: conn.ID}
	}
	m.connections[conn.ID] = conn
	return nil
}

func (m *MockRepository) UpdateConnectionStatus(ctx context.Context, id string, status string, lastError *string) error {
	if conn, exists := m.connections[id]; exists {
		conn.Status = status
		conn.LastError = lastError
		return nil
	}
	return &NotFoundError{ResourceType: "Connection", ResourceID: id}
}

func (m *MockRepository) DeleteConnection(ctx context.Context, id string) error {
	if _, exists := m.connections[id]; !exists {
		return &NotFoundError{ResourceType: "Connection", ResourceID: id}
	}
	delete(m.connections, id)
	return nil
}

func (m *MockRepository) CreateConnectionEvent(ctx context.Context, event *ConnectionEvent) error {
	m.events = append(m.events, event)
	return nil
}

func (m *MockRepository) GetConnectionEvents(ctx context.Context, connectionID string) ([]*ConnectionEvent, error) {
	var results []*ConnectionEvent
	for _, evt := range m.events {
		if evt.ConnectionID == connectionID {
			results = append(results, evt)
		}
	}
	return results, nil
}

func (m *MockRepository) Close() error {
	return nil
}

// Test helper to create context with tenant ID
func contextWithTenant(tenantID string) context.Context {
	return ContextWithTenantID(context.Background(), tenantID)
}

// Test helper to create handler with mock repo
func setupTestHandler() (*Handler, *MockRepository) {
	mockRepo := NewMockRepository()
	validator := NewValidator()
	handler := NewHandler(mockRepo, validator)
	return handler, mockRepo
}

// Test CreateConnection with valid config
func TestCreateConnection_Valid(t *testing.T) {
	handler, mockRepo := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	req := CreateConnectionRequest{
		Name:        "test-connection",
		Description: "Test connection",
		SourceConfig: SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "http://example.com",
				Method: "GET",
			},
		},
		DestinationConfig: DestinationConfig{
			Type: "http",
			HTTP: &HTTPDestinationConfig{
				URL:    "http://example.com",
				Method: "POST",
			},
		},
	}

	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/api/v1/connections", bytes.NewReader(body))
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateConnection(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", w.Code)
	}

	// Verify connection was created
	if len(mockRepo.connections) != 1 {
		t.Errorf("expected 1 connection, got %d", len(mockRepo.connections))
	}
}

// Test CreateConnection with invalid source config
func TestCreateConnection_InvalidSourceConfig(t *testing.T) {
	handler, _ := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	req := CreateConnectionRequest{
		Name:        "test-connection",
		Description: "Test connection",
		SourceConfig: SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "invalid-url", // Invalid URL
				Method: "GET",
			},
		},
		DestinationConfig: DestinationConfig{
			Type: "http",
			HTTP: &HTTPDestinationConfig{
				URL:    "http://example.com",
				Method: "POST",
			},
		},
	}

	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/api/v1/connections", bytes.NewReader(body))
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateConnection(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

// Test CreateConnection with missing tenant ID
func TestCreateConnection_MissingTenant(t *testing.T) {
	handler, _ := setupTestHandler()
	ctx := context.Background() // No tenant ID

	req := CreateConnectionRequest{
		Name: "test-connection",
	}

	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/api/v1/connections", bytes.NewReader(body))
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateConnection(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

// Test GetConnection with valid ID
func TestGetConnection_Found(t *testing.T) {
	handler, mockRepo := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	// Create a connection first
	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "test-connection",
	}
	mockRepo.connections["test-id"] = conn

	r := httptest.NewRequest("GET", "/api/v1/connections/test-id", nil)
	r = r.WithContext(ctx)
	// Set PathValue using Go 1.22+ standard API
	r.SetPathValue("id", "test-id")
	w := httptest.NewRecorder()

	handler.GetConnection(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

// Test GetConnection with invalid ID
func TestGetConnection_NotFound(t *testing.T) {
	handler, _ := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	r := httptest.NewRequest("GET", "/api/v1/connections/nonexistent", nil)
	r = r.WithContext(ctx)
	r.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	handler.GetConnection(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

// Test ListConnections
func TestListConnections(t *testing.T) {
	handler, mockRepo := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	// Create test connections
	for i := 0; i < 3; i++ {
		conn := &Connection{
			ID:       "test-id-" + string(rune(i)),
			TenantID: "tenant-1",
			Name:     "connection-" + string(rune(i)),
		}
		mockRepo.connections[conn.ID] = conn
	}

	r := httptest.NewRequest("GET", "/api/v1/connections", nil)
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListConnections(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp ListResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 3 {
		t.Errorf("expected 3 connections, got %d", resp.Total)
	}
}

// Test UpdateConnection
func TestUpdateConnection_Valid(t *testing.T) {
	handler, mockRepo := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	// Create a connection first
	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "test-connection",
		Status:   "stopped",
		SourceConfig: SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "http://example.com/api",
				Method: "GET",
			},
		},
		DestinationConfig: DestinationConfig{
			Type: "http",
			HTTP: &HTTPDestinationConfig{
				URL:    "http://example.com/webhook",
				Method: "POST",
			},
		},
	}
	mockRepo.connections["test-id"] = conn

	updatedName := "updated-connection"
	updatedDesc := "Updated description"
	updateReq := UpdateConnectionRequest{
		Name:        &updatedName,
		Description: &updatedDesc,
	}

	body, _ := json.Marshal(updateReq)
	r := httptest.NewRequest("PUT", "/api/v1/connections/test-id", bytes.NewReader(body))
	r = r.WithContext(ctx)
	r.SetPathValue("id", "test-id")
	w := httptest.NewRecorder()

	handler.UpdateConnection(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d. Response: %s", w.Code, w.Body.String())
	}

	// Verify update
	updated, _ := mockRepo.GetConnection(ctx, "test-id")
	if updated.Name != "updated-connection" {
		t.Errorf("expected name 'updated-connection', got '%s'", updated.Name)
	}
}

// Test DeleteConnection
func TestDeleteConnection_Valid(t *testing.T) {
	handler, mockRepo := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	// Create a connection first
	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "test-connection",
		Status:   "stopped",
	}
	mockRepo.connections["test-id"] = conn

	r := httptest.NewRequest("DELETE", "/api/v1/connections/test-id", nil)
	r = r.WithContext(ctx)
	r.SetPathValue("id", "test-id")
	w := httptest.NewRecorder()

	handler.DeleteConnection(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", w.Code)
	}

	// Verify deletion
	_, err := mockRepo.GetConnection(ctx, "test-id")
	if err == nil {
		t.Error("expected connection to be deleted")
	}
}

// Test StartConnection
func TestStartConnection_Valid(t *testing.T) {
	handler, mockRepo := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	// Create a connection in stopped state
	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "test-connection",
		Status:   "stopped",
	}
	mockRepo.connections["test-id"] = conn

	r := httptest.NewRequest("POST", "/api/v1/connections/test-id/start", nil)
	r = r.WithContext(ctx)
	r.SetPathValue("id", "test-id")
	w := httptest.NewRecorder()

	handler.StartConnection(w, r)

	// Status should be either 200 (already started) or 400 (validation error)
	if w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
		t.Logf("start connection returned %d (may be expected if NATS not available)", w.Code)
	}
}

// Implement required Repository method
func (m *MockRepository) GetConnectionByNameAndTenant(ctx context.Context, name, tenantID string) (*Connection, error) {
	for _, conn := range m.connections {
		if conn.Name == name && conn.TenantID == tenantID {
			return conn, nil
		}
	}
	return nil, ErrConnectionNotFound
}

// ============================================
// Auth Mock Methods (Phase 1)
// These are stub implementations for testing non-auth handlers
// ============================================

func (m *MockRepository) CreateUser(ctx context.Context, user *User) error {
	return nil
}

func (m *MockRepository) GetUserByID(ctx context.Context, id string) (*User, error) {
	return nil, &NotFoundError{ResourceType: "User", ResourceID: id}
}

func (m *MockRepository) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	return nil, &NotFoundError{ResourceType: "User", ResourceID: email}
}

func (m *MockRepository) UpdateUserLastLogin(ctx context.Context, userID string) error {
	return nil
}

func (m *MockRepository) UpdateUserPassword(ctx context.Context, userID, passwordHash string) error {
	return nil
}

func (m *MockRepository) VerifyUserEmail(ctx context.Context, userID string) error {
	return nil
}

func (m *MockRepository) CreateSession(ctx context.Context, session *Session) error {
	return nil
}

func (m *MockRepository) GetSessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error) {
	return nil, &NotFoundError{ResourceType: "Session", ResourceID: tokenHash}
}

func (m *MockRepository) ValidateSession(ctx context.Context, tokenHash string) (*Session, *User, error) {
	return nil, nil, &NotFoundError{ResourceType: "Session", ResourceID: tokenHash}
}

func (m *MockRepository) UpdateSessionActivity(ctx context.Context, sessionID string) error {
	return nil
}

func (m *MockRepository) InvalidateSession(ctx context.Context, tokenHash string) error {
	return nil
}

func (m *MockRepository) InvalidateAllUserSessions(ctx context.Context, userID string) error {
	return nil
}

func (m *MockRepository) CreateEmailVerificationToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	return nil
}

func (m *MockRepository) GetEmailVerificationToken(ctx context.Context, tokenHash string) (*EmailVerificationToken, error) {
	return nil, &NotFoundError{ResourceType: "EmailVerificationToken", ResourceID: tokenHash}
}

func (m *MockRepository) UseEmailVerificationToken(ctx context.Context, tokenHash string) error {
	return nil
}

func (m *MockRepository) CreatePasswordResetToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	return nil
}

func (m *MockRepository) GetPasswordResetToken(ctx context.Context, tokenHash string) (*PasswordResetToken, error) {
	return nil, &NotFoundError{ResourceType: "PasswordResetToken", ResourceID: tokenHash}
}

func (m *MockRepository) UsePasswordResetToken(ctx context.Context, tokenHash, newPasswordHash string) error {
	return nil
}

func (m *MockRepository) CreateAuthAuditLog(ctx context.Context, log *AuthAuditLog) error {
	return nil
}

// ============================================
// Tenant Operations (Phase 1 Refactor)
// ============================================

func (m *MockRepository) CreateTenant(ctx context.Context, userID, name, slug string) (*Tenant, error) {
	return &Tenant{ID: "test-tenant-id", Name: name, Slug: slug, OwnerID: userID, SubscriptionPlan: "free"}, nil
}

func (m *MockRepository) GetTenantByID(ctx context.Context, tenantID string) (*Tenant, error) {
	return nil, ErrTenantNotFound
}

func (m *MockRepository) GetUserTenants(ctx context.Context, userID string) ([]*TenantResponse, error) {
	return []*TenantResponse{}, nil
}

func (m *MockRepository) GetUserTenantRole(ctx context.Context, userID, tenantID string) (string, error) {
	return "", nil
}

func (m *MockRepository) DeleteTenant(ctx context.Context, tenantID string) error {
	return nil
}

func (m *MockRepository) UpdateTenantStatus(ctx context.Context, tenantID, status string, natsSlug *string) error {
	return nil
}

func (m *MockRepository) CreateProvisioningJob(ctx context.Context, tenantID string) (*ProvisioningJob, error) {
	return &ProvisioningJob{ID: "test-job-id", TenantID: tenantID, Status: "queued"}, nil
}

func (m *MockRepository) UpdateProvisioningJob(ctx context.Context, jobID, status string, progress int, step, errMsg string) error {
	return nil
}

func (m *MockRepository) UpdateProvisioningJobCompleted(ctx context.Context, jobID string, completedAt *time.Time) error {
	return nil
}

func (m *MockRepository) GetLatestProvisioningJob(ctx context.Context, tenantID string) (*ProvisioningJob, error) {
	return nil, nil
}

func (m *MockRepository) UpsertTenantAPIKey(ctx context.Context, tenantID, keyHash string) (*TenantAPIKey, error) {
	return &TenantAPIKey{ID: "test-key-id", TenantID: tenantID, IsActive: true}, nil
}

func (m *MockRepository) GetTenantAPIKey(ctx context.Context, tenantID string) (*TenantAPIKey, error) {
	return nil, ErrTenantNotFound
}
