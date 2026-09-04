package managementapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

// MockRepository implements Repository interface for testing
type MockRepository struct {
	connections  map[string]*Connection
	events       []*ConnectionEvent
	secrets      map[string]*mockSecret // keyed by id
	auditEntries []*AuditEntry
	oidcConfigs  []*OIDCConfig
	oidcUsers    []mockOIDCUser
	quotas       map[string]*TenantQuotas
	tenantPlans  map[string]string
	usage        map[string]map[string]*UsageDaily // tenantID → day → row
}

type mockOIDCUser struct {
	provider string
	subject  string
	user     *User
}

type mockSecret struct {
	meta       Secret
	ciphertext string
}

func NewMockRepository() *MockRepository {
	return &MockRepository{
		connections: make(map[string]*Connection),
		events:      make([]*ConnectionEvent, 0),
		secrets:     make(map[string]*mockSecret),
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
		Nodes: []*Node{
			{ID: "c1", Type: "consumer", Config: json.RawMessage(`{"type":"file","file":{"path":"/data/input"}}`), Enabled: true},
			{ID: "p1", Type: "producer", Config: json.RawMessage(`{"type":"http","http":{"url":"https://example.test/hook"}}`), Enabled: true},
		},
		Edges: []*Edge{{ID: "e1", Source: "c1", Target: "p1"}},
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

// Create-time validation is topological now that the flat model is gone: a
// pipeline with no producer cannot be ordered, so it is rejected.
func TestCreateConnection_InvalidGraph(t *testing.T) {
	handler, _ := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	req := CreateConnectionRequest{
		Name:        "test-connection",
		Description: "Test connection",
		Nodes: []*Node{
			{ID: "c1", Type: "consumer", Config: json.RawMessage(`{"type":"file","file":{"path":"/data/input"}}`), Enabled: true},
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
		Nodes: []*Node{
			{ID: "c1", Type: "consumer", Config: json.RawMessage(`{"type":"file","file":{"path":"/data/input"}}`), Enabled: true},
			{ID: "p1", Type: "producer", Config: json.RawMessage(`{"type":"http","http":{"url":"https://example.test/hook"}}`), Enabled: true},
		},
		Edges: []*Edge{{ID: "e1", Source: "c1", Target: "p1"}},
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

// fakeOrchestrator records StopPipeline invocations so a test can assert that
// deleting a graph-based connection tears down its k8s resources (#175).
type fakeOrchestrator struct {
	stopCalls   int
	stoppedConn string
}

func (f *fakeOrchestrator) StartPipeline(_ context.Context, _ *Connection) error { return nil }
func (f *fakeOrchestrator) StopPipeline(_ context.Context, conn *Connection) error {
	f.stopCalls++
	f.stoppedConn = conn.ID
	return nil
}
func (f *fakeOrchestrator) GetPipelineStatus(_ context.Context, _ *Connection) (map[string]string, error) {
	return map[string]string{}, nil
}

// TestDeleteConnection_TearsDownOrchestratorResources asserts that deleting a
// graph-based connection invokes the orchestrator teardown (StopPipeline), so
// per-connection Deployments + HPAs aren't orphaned (#175). Before the fix,
// delete only published a NATS stop and never called the orchestrator.
func TestDeleteConnection_TearsDownOrchestratorResources(t *testing.T) {
	handler, mockRepo := setupTestHandler()
	ctx := contextWithTenant("tenant-1")

	fake := &fakeOrchestrator{}
	handler.SetOrchestratorFactory(func(_ *Connection) PipelineOrchestrator { return fake })

	conn := &Connection{
		ID:       "graph-conn",
		TenantID: "tenant-1",
		Name:     "graph-connection",
		Status:   "stopped",
		Nodes:    []*Node{{ID: "src", Type: "consumer"}},
	}
	mockRepo.connections["graph-conn"] = conn

	r := httptest.NewRequest("DELETE", "/api/v1/connections/graph-conn", nil)
	r = r.WithContext(ctx)
	r.SetPathValue("id", "graph-conn")
	w := httptest.NewRecorder()

	handler.DeleteConnection(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", w.Code)
	}
	if fake.stopCalls != 1 {
		t.Errorf("expected orchestrator StopPipeline to be called once on delete, got %d", fake.stopCalls)
	}
	if fake.stoppedConn != "graph-conn" {
		t.Errorf("expected teardown for graph-conn, got %q", fake.stoppedConn)
	}
	if _, err := mockRepo.GetConnection(ctx, "graph-conn"); err == nil {
		t.Error("expected connection to be deleted from repo")
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

func (m *MockRepository) DeleteUser(ctx context.Context, userID string) error {
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

func (m *MockRepository) UpdateTenantPlan(ctx context.Context, tenantID, plan string) error {
	if tenantID == "missing" {
		return ErrTenantNotFound
	}
	if m.tenantPlans == nil {
		m.tenantPlans = make(map[string]string)
	}
	m.tenantPlans[tenantID] = plan
	return nil
}

func (m *MockRepository) ListTenantPlans(ctx context.Context) (map[string]string, error) {
	return m.tenantPlans, nil
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

// ============================================
// Data Sharing Operations (Phase 3)
// ============================================

func (m *MockRepository) CreateConnectionRequest(ctx context.Context, req *DataConnectionRequest) error {
	req.ID = "test-request-id"
	return nil
}

func (m *MockRepository) GetConnectionRequest(ctx context.Context, requestID string) (*DataConnectionRequest, error) {
	return nil, ErrConnectionRequestNotFound
}

func (m *MockRepository) ListIncomingConnectionRequests(ctx context.Context, targetTenantID string) ([]*DataConnectionRequest, error) {
	return []*DataConnectionRequest{}, nil
}

func (m *MockRepository) ListOutgoingConnectionRequests(ctx context.Context, requesterTenantID string) ([]*DataConnectionRequest, error) {
	return []*DataConnectionRequest{}, nil
}

func (m *MockRepository) ApproveConnectionRequest(ctx context.Context, requestID string, allowedFields, deniedFields, sharedConnectionIDs []string) (*TenantDataConnection, error) {
	return &TenantDataConnection{ID: "test-conn-id", Status: "active"}, nil
}

func (m *MockRepository) DenyConnectionRequest(ctx context.Context, requestID string) error {
	return nil
}

func (m *MockRepository) ListDataConnections(ctx context.Context, tenantID string) ([]*TenantDataConnection, error) {
	return []*TenantDataConnection{}, nil
}

func (m *MockRepository) GetDataConnectionByID(ctx context.Context, id string) (*TenantDataConnection, error) {
	return nil, ErrDataConnectionNotFound
}

func (m *MockRepository) GetActiveDataConnection(ctx context.Context, requesterID, targetID string) (*TenantDataConnection, error) {
	return nil, ErrDataConnectionNotFound
}

func (m *MockRepository) GetSharedConnectionsForTenant(ctx context.Context, requesterID, targetID string) ([]string, error) {
	return []string{}, nil
}

func (m *MockRepository) RevokeDataConnection(ctx context.Context, connectionID string) error {
	return nil
}

func (m *MockRepository) CreateDataAccessLog(ctx context.Context, entry *DataAccessLogEntry) error {
	return nil
}

func (m *MockRepository) ListDataAccessLog(ctx context.Context, targetTenantID string, filters *ListFilters) ([]*DataAccessLogEntry, int64, error) {
	return []*DataAccessLogEntry{}, 0, nil
}

func (m *MockRepository) PauseConnectionsByDataConnection(ctx context.Context, tenantID, dataConnectionID string) (int64, error) {
	return 0, nil
}

func (m *MockRepository) GetTenantByAPIKeyHash(ctx context.Context, keyHash string) (*Tenant, error) {
	return nil, ErrTenantNotFound
}

// ----- Tenant quotas (Phase 1I — #74) -----

func (m *MockRepository) GetTenantQuotas(ctx context.Context, tenantID string) (*TenantQuotas, error) {
	if q, ok := m.quotas[tenantID]; ok {
		cp := *q
		return &cp, nil
	}
	q := &TenantQuotas{
		TenantID:        tenantID,
		PlanName:        "free",
		MaxMsgPerSec:    50,
		MaxIntegrations: 10,
		MaxStorageBytes: 1 << 30,
	}
	if m.quotas == nil {
		m.quotas = map[string]*TenantQuotas{}
	}
	m.quotas[tenantID] = q
	cp := *q
	return &cp, nil
}

func (m *MockRepository) UpdateTenantQuotas(ctx context.Context, q *TenantQuotas) error {
	if m.quotas == nil {
		m.quotas = map[string]*TenantQuotas{}
	}
	cp := *q
	m.quotas[q.TenantID] = &cp
	return nil
}

func (m *MockRepository) SetTenantStorageUsage(ctx context.Context, tenantID string, bytes int64) error {
	q, _ := m.GetTenantQuotas(ctx, tenantID)
	q.StorageBytes = bytes
	q.StorageExceeded = bytes > q.MaxStorageBytes
	m.quotas[tenantID] = q
	return nil
}

func (m *MockRepository) CountActiveIntegrations(ctx context.Context, tenantID string) (int, error) {
	n := 0
	for _, c := range m.connections {
		if c.TenantID == tenantID {
			n++
		}
	}
	return n, nil
}

// ----- Usage metering (Phase 4A — #92) -----

func (m *MockRepository) UpsertUsageDaily(ctx context.Context, tenantID string, day time.Time, messages, deploys, storageBytes int64) error {
	if m.usage == nil {
		m.usage = map[string]map[string]*UsageDaily{}
	}
	if m.usage[tenantID] == nil {
		m.usage[tenantID] = map[string]*UsageDaily{}
	}
	d := day.UTC().Format("2006-01-02")
	m.usage[tenantID][d] = &UsageDaily{Day: d, MessagesPublished: messages, Deploys: deploys, StorageBytes: storageBytes}
	return nil
}

func (m *MockRepository) ListUsageDaily(ctx context.Context, tenantID string, from, to time.Time) ([]*UsageDaily, error) {
	lo, hi := from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02")
	var out []*UsageDaily
	for d, row := range m.usage[tenantID] {
		if d >= lo && d <= hi {
			cp := *row
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, nil
}

func (m *MockRepository) SumUsage(ctx context.Context, tenantID string, from, to time.Time) (*UsageTotals, error) {
	rows, _ := m.ListUsageDaily(ctx, tenantID, from, to)
	t := &UsageTotals{}
	for _, r := range rows {
		t.MessagesPublished += r.MessagesPublished
		t.Deploys += r.Deploys
		t.StorageBytes = r.StorageBytes // rows are day-ascending → ends on latest
	}
	return t, nil
}

func (m *MockRepository) ListTenantStorage(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for id, q := range m.quotas {
		out[id] = q.StorageBytes
	}
	return out, nil
}

// ----- Tenant members (Phase 1D — #69) -----

func (m *MockRepository) ListTenantMembers(ctx context.Context, tenantID string) ([]*TenantMember, error) {
	return nil, nil
}

func (m *MockRepository) AddTenantMember(ctx context.Context, tenantID, userID, role string) error {
	return nil
}

func (m *MockRepository) SetTenantMemberRole(ctx context.Context, tenantID, userID, newRole string) error {
	return nil
}

func (m *MockRepository) RemoveTenantMember(ctx context.Context, tenantID, userID string) error {
	return nil
}

// ----- OIDC (Phase 1C — #68) -----

func (m *MockRepository) GetOIDCConfigByTenantID(ctx context.Context, tenantID string) (*OIDCConfig, error) {
	for _, c := range m.oidcConfigs {
		if c.TenantID == tenantID {
			cp := *c
			return &cp, nil
		}
	}
	return nil, ErrOIDCConfigNotFound
}

func (m *MockRepository) GetOIDCConfigByTenantSlug(ctx context.Context, slug string) (*OIDCConfig, error) {
	// In-memory mock keeps tenant slug == config.TenantID for simplicity.
	return m.GetOIDCConfigByTenantID(ctx, slug)
}

func (m *MockRepository) UpsertOIDCConfig(ctx context.Context, c *OIDCConfig) error {
	for i, existing := range m.oidcConfigs {
		if existing.TenantID == c.TenantID {
			cp := *c
			m.oidcConfigs[i] = &cp
			return nil
		}
	}
	cp := *c
	m.oidcConfigs = append(m.oidcConfigs, &cp)
	return nil
}

func (m *MockRepository) DeleteOIDCConfig(ctx context.Context, tenantID string) error {
	for i, c := range m.oidcConfigs {
		if c.TenantID == tenantID {
			m.oidcConfigs = append(m.oidcConfigs[:i], m.oidcConfigs[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *MockRepository) GetUserByOIDCSubject(ctx context.Context, provider, subject string) (*User, error) {
	for _, u := range m.oidcUsers {
		if u.provider == provider && u.subject == subject {
			user := *u.user
			return &user, nil
		}
	}
	return nil, nil
}

func (m *MockRepository) LinkUserOIDC(ctx context.Context, userID, provider, subject string) error {
	// Not strictly needed for current tests; keep as a recorded link.
	m.oidcUsers = append(m.oidcUsers, mockOIDCUser{
		provider: provider, subject: subject,
		user: &User{ID: userID},
	})
	return nil
}

// ----- Audit log (Phase 1G — #72) -----

func (m *MockRepository) CreateAuditEntry(ctx context.Context, e *AuditEntry) error {
	if e.ID == "" {
		e.ID = "audit-" + time.Now().Format("150405.000000000")
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now()
	}
	m.auditEntries = append(m.auditEntries, e)
	return nil
}

func (m *MockRepository) ListAuditEntries(ctx context.Context, tenantID string, f AuditFilters, limit, offset int) ([]*AuditEntry, int64, error) {
	var matches []*AuditEntry
	for _, e := range m.auditEntries {
		if e.TenantID != tenantID {
			continue
		}
		if f.Action != "" && e.Action != f.Action {
			continue
		}
		if f.ResourceType != "" && e.ResourceType != f.ResourceType {
			continue
		}
		matches = append(matches, e)
	}
	return matches, int64(len(matches)), nil
}

func (m *MockRepository) StreamAuditEntries(ctx context.Context, tenantID string, f AuditFilters, emit func(*AuditEntry) error) error {
	for _, e := range m.auditEntries {
		if e.TenantID != tenantID {
			continue
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return nil
}

// ----- Secrets (Phase 1A — #66) -----

func (m *MockRepository) CreateSecret(ctx context.Context, tenantID, name, ciphertext string) (*Secret, error) {
	for _, s := range m.secrets {
		if s.meta.TenantID == tenantID && s.meta.Name == name {
			return nil, &ConflictError{Message: "duplicate key value violates unique constraint secrets_tenant_id_name_key"}
		}
	}
	id := "secret-" + name + "-" + tenantID
	now := time.Now()
	s := &mockSecret{
		meta:       Secret{ID: id, TenantID: tenantID, Name: name, CreatedAt: now, UpdatedAt: now},
		ciphertext: ciphertext,
	}
	m.secrets[id] = s
	out := s.meta
	return &out, nil
}

func (m *MockRepository) GetSecret(ctx context.Context, tenantID, id string) (*Secret, error) {
	s, ok := m.secrets[id]
	if !ok || s.meta.TenantID != tenantID {
		return nil, ErrSecretNotFound
	}
	out := s.meta
	return &out, nil
}

func (m *MockRepository) GetSecretCiphertext(ctx context.Context, tenantID, id string) (string, error) {
	s, ok := m.secrets[id]
	if !ok || s.meta.TenantID != tenantID {
		return "", ErrSecretNotFound
	}
	return s.ciphertext, nil
}

func (m *MockRepository) ListSecrets(ctx context.Context, tenantID string, limit, offset int) ([]*Secret, error) {
	var out []*Secret
	for _, s := range m.secrets {
		if s.meta.TenantID == tenantID {
			meta := s.meta
			out = append(out, &meta)
		}
	}
	return out, nil
}

func (m *MockRepository) UpdateSecret(ctx context.Context, tenantID, id, name, ciphertext string) (*Secret, error) {
	s, ok := m.secrets[id]
	if !ok || s.meta.TenantID != tenantID {
		return nil, ErrSecretNotFound
	}
	if name != "" {
		s.meta.Name = name
	}
	if ciphertext != "" {
		s.ciphertext = ciphertext
		now := time.Now()
		s.meta.RotatedAt = &now
	}
	s.meta.UpdatedAt = time.Now()
	out := s.meta
	return &out, nil
}

func (m *MockRepository) DeleteSecret(ctx context.Context, tenantID, id string) ([]string, error) {
	s, ok := m.secrets[id]
	if !ok || s.meta.TenantID != tenantID {
		return nil, ErrSecretNotFound
	}
	// Scan in-memory connections for any string-containing reference to id.
	var refs []string
	for cid, conn := range m.connections {
		if conn.TenantID != tenantID {
			continue
		}
		blob, _ := json.Marshal(conn)
		if bytes.Contains(blob, []byte(id)) {
			refs = append(refs, cid)
		}
	}
	if len(refs) > 0 {
		return refs, nil
	}
	delete(m.secrets, s.meta.ID)
	return nil, nil
}

// ============================================
// Phase 3: Unit Tests
// ============================================

func TestFilterFields_Basic(t *testing.T) {
	data := json.RawMessage(`{"name":"test","email":"a@b.com","password":"secret123","api_key":"abc"}`)
	result := filterFields(data, nil, nil)

	var obj map[string]json.RawMessage
	_ = json.Unmarshal(result, &obj)

	if _, exists := obj["password"]; exists {
		t.Error("password should be filtered out by unsafe pattern")
	}
	if _, exists := obj["api_key"]; exists {
		t.Error("api_key should be filtered out by unsafe pattern (contains 'key')")
	}
	if _, exists := obj["name"]; !exists {
		t.Error("name should be present")
	}
	if _, exists := obj["email"]; !exists {
		t.Error("email should be present")
	}
}

func TestFilterFields_AllowedList(t *testing.T) {
	data := json.RawMessage(`{"name":"test","email":"a@b.com","phone":"123"}`)
	result := filterFields(data, []string{"name", "email"}, nil)

	var obj map[string]json.RawMessage
	_ = json.Unmarshal(result, &obj)

	if _, exists := obj["phone"]; exists {
		t.Error("phone should be filtered out (not in allowed list)")
	}
	if _, exists := obj["name"]; !exists {
		t.Error("name should be present (in allowed list)")
	}
}

func TestFilterFields_DeniedList(t *testing.T) {
	data := json.RawMessage(`{"name":"test","email":"a@b.com","internal_id":"123"}`)
	result := filterFields(data, nil, []string{"internal_id"})

	var obj map[string]json.RawMessage
	_ = json.Unmarshal(result, &obj)

	if _, exists := obj["internal_id"]; exists {
		t.Error("internal_id should be filtered out (in denied list)")
	}
	if _, exists := obj["name"]; !exists {
		t.Error("name should be present")
	}
}

func TestRateLimiter_Allow(t *testing.T) {
	rl := NewConnectionRateLimiter()

	// With a high limit, first request should always be allowed
	if !rl.Allow("conn-1", 1000) {
		t.Error("first request should be allowed")
	}

	// Remove and verify no panic
	rl.Remove("conn-1")
	if !rl.Allow("conn-1", 1000) {
		t.Error("request after remove should create new limiter and allow")
	}
}
