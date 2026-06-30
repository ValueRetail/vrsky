package managementapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// natsInstRepo is an in-memory Repository (embedding MockRepository) that also
// implements NATSInstanceStore, for driving the discovery handler + health
// monitor without a database.
type natsInstRepo struct {
	*MockRepository
	mu            sync.Mutex
	instances     []*NATSInstance
	connCounts    map[string]int // instance id -> connection count
	metricUpdates map[string]int // instance id -> last integrations recorded
	seq           int
}

func newNATSInstRepo() *natsInstRepo {
	return &natsInstRepo{MockRepository: NewMockRepository(), connCounts: map[string]int{}}
}

func (r *natsInstRepo) ListNATSInstances(_ context.Context, tenantID string) ([]*NATSInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*NATSInstance
	for _, n := range r.instances {
		if n.TenantID == tenantID && n.Status == "active" && n.DeletedAt == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

func (r *natsInstRepo) ListActiveNATSInstancesAllTenants(_ context.Context) ([]*NATSInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*NATSInstance
	for _, n := range r.instances {
		if (n.Status == "active" || n.Status == "unhealthy") && n.DeletedAt == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

func (r *natsInstRepo) RegisterNATSInstance(_ context.Context, tenantID string, num int, dns string) (*NATSInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	n := &NATSInstance{ID: "n" + string(rune('0'+r.seq)), TenantID: tenantID, InstanceNumber: num, DNSName: dns, Status: "provisioning"}
	r.instances = append(r.instances, n)
	return n, nil
}

func (r *natsInstRepo) SetNATSInstanceStatus(_ context.Context, id, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.instances {
		if n.ID == id {
			n.Status = status
			return nil
		}
	}
	return ErrNATSInstanceNotFound
}

func (r *natsInstRepo) SoftDeleteNATSInstance(_ context.Context, tenantID, id string) error {
	return nil
}

func (r *natsInstRepo) UpdateNATSInstanceMetrics(_ context.Context, id string, integrations, connections, memoryMB int, msgRate int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.metricUpdates == nil {
		r.metricUpdates = map[string]int{}
	}
	r.metricUpdates[id] = integrations
	return nil
}

func (r *natsInstRepo) MaxInstanceNumber(_ context.Context, tenantID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	max := 0
	for _, n := range r.instances {
		if n.TenantID == tenantID && n.DeletedAt == nil && n.InstanceNumber > max {
			max = n.InstanceNumber
		}
	}
	return max, nil
}

func (r *natsInstRepo) CountConnectionsPerInstance(_ context.Context, _ string) (map[string]int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for id, c := range r.connCounts {
		out[id] = c
	}
	return out, nil
}

func (r *natsInstRepo) AssignConnectionInstance(_ context.Context, _, _, _ string) error { return nil }

func (r *natsInstRepo) GetConnectionInstance(_ context.Context, _, _ string) (*NATSInstance, error) {
	return nil, ErrNATSInstanceNotFound
}

func TestHandleListNATSInstances(t *testing.T) {
	repo := newNATSInstRepo()
	repo.instances = []*NATSInstance{
		{ID: "n1", TenantID: "t-1", InstanceNumber: 1, DNSName: "nats-t-1-1.vrsky-tenants.svc.cluster.local", Status: "active"},
		{ID: "n2", TenantID: "t-1", InstanceNumber: 2, DNSName: "nats-t-1-2.vrsky-tenants.svc.cluster.local", Status: "active"},
		{ID: "n3", TenantID: "other", InstanceNumber: 1, DNSName: "nats-other-1.vrsky-tenants.svc.cluster.local", Status: "active"},
	}
	h := NewHandler(repo, NewValidator())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/t-1/nats-instances", nil)
	req.SetPathValue("tenant_id", "t-1")
	rec := httptest.NewRecorder()
	h.HandleListNATSInstances(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data natsInstancesResponse `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Data.URLs) != 2 {
		t.Fatalf("expected 2 URLs for t-1 (tenant-scoped), got %v", resp.Data.URLs)
	}
	if resp.Data.URLs[0] != "nats://nats-t-1-1.vrsky-tenants.svc.cluster.local:4222" {
		t.Fatalf("unexpected URL form: %q", resp.Data.URLs[0])
	}
}

func TestNATSHealthMonitor_MarksUnhealthyThenRecovers(t *testing.T) {
	// A toggleable fake NATS monitoring endpoint.
	healthy := true
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		ok := healthy
		mu.Unlock()
		if ok {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	// srv.Listener.Addr() gives host:port; the monitor builds http://<dns>:8222.
	// Point dns at the test server's host:port and override the probe URL by
	// using the server's address as the DNS (with its own port via a custom
	// client is overkill) — instead, test runOnce against a store whose probe
	// we steer by hostport. Simplest: use the server URL host as DNSName and a
	// monitor whose probe targets it directly.
	host := srv.Listener.Addr().String()

	repo := newNATSInstRepo()
	repo.instances = []*NATSInstance{{ID: "n1", TenantID: "t-1", DNSName: host, Status: "active"}}

	m := NewNATSHealthMonitor(repo, nil, nil)
	m.failThreshold = 2
	// Override probe to hit the test server's actual host:port (no :8222).
	m.client = srv.Client()
	probeURL := "http://" + host + "/healthz"
	m.probeFn = func(ctx context.Context, _ string) bool {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
		resp, err := m.client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}

	ctx := context.Background()
	// Healthy: stays active.
	m.runOnce(ctx)
	if repo.instances[0].Status != "active" {
		t.Fatalf("healthy probe should keep active, got %s", repo.instances[0].Status)
	}
	// Go unhealthy: needs failThreshold consecutive failures.
	mu.Lock()
	healthy = false
	mu.Unlock()
	m.runOnce(ctx) // fail 1 — still active (below threshold)
	if repo.instances[0].Status != "active" {
		t.Fatalf("one failure should not flip status yet, got %s", repo.instances[0].Status)
	}
	m.runOnce(ctx) // fail 2 — now unhealthy
	if repo.instances[0].Status != "unhealthy" {
		t.Fatalf("two failures should mark unhealthy, got %s", repo.instances[0].Status)
	}
	// Recover.
	mu.Lock()
	healthy = true
	mu.Unlock()
	m.runOnce(ctx)
	if repo.instances[0].Status != "active" {
		t.Fatalf("recovery should mark active, got %s", repo.instances[0].Status)
	}
}
