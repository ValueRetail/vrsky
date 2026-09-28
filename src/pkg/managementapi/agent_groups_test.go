package managementapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Agent groups (#281 follow-up): one output node, many tills.

func TestValidGroupNames(t *testing.T) {
	got, err := validGroupNames([]string{" store-oslo ", "all-tills", "", "store-oslo", "A_1"})
	if err != nil || strings.Join(got, ",") != "A_1,all-tills,store-oslo" {
		t.Errorf("valid names: %v, %v — want trimmed, de-duplicated, sorted", got, err)
	}
	for _, bad := range []string{"all tills", "-x", "a/b", strings.Repeat("g", 65), "ø"} {
		if _, err := validGroupNames([]string{bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if got, err := validGroupNames(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("nil → empty list, got %v %v", got, err)
	}
}

func TestAgents_UpdateGroups_ValidatesAndIsTenantScoped(t *testing.T) {
	repo := newAgentRBACMock()
	repo.addUserSession("tokEd", "user-A", tenantA, "editor")
	repo.addUserSession("tokEdB", "user-B", tenantB, "editor")
	repo.agents["a1"] = &Agent{ID: "a1", TenantID: tenantA, Name: "till-1", Groups: []string{}}
	h := NewHandler(repo, NewValidator())

	w := runHTTPAs(t, h, http.MethodPatch, "/api/v1/agents/a1", tenantA, "tokEd",
		map[string]any{"groups": []string{"store-oslo", " all-tills "}})
	if w.Code != http.StatusOK {
		t.Fatalf("set groups: %d %s", w.Code, w.Body.String())
	}
	var a Agent
	decodeAgentData(t, w.Body.Bytes(), &a)
	if strings.Join(a.Groups, ",") != "all-tills,store-oslo" || a.Name != "till-1" {
		t.Errorf("agent after PATCH = %+v", a)
	}

	// Name and groups in one PATCH.
	w = runHTTPAs(t, h, http.MethodPatch, "/api/v1/agents/a1", tenantA, "tokEd",
		map[string]any{"name": "till-one", "groups": []string{"all-tills"}})
	decodeAgentData(t, w.Body.Bytes(), &a)
	if a.Name != "till-one" || strings.Join(a.Groups, ",") != "all-tills" {
		t.Errorf("rename + groups = %+v", a)
	}

	// "groups": [] leaves every group; no body at all is a 400.
	w = runHTTPAs(t, h, http.MethodPatch, "/api/v1/agents/a1", tenantA, "tokEd", map[string]any{"groups": []string{}})
	decodeAgentData(t, w.Body.Bytes(), &a)
	if len(a.Groups) != 0 {
		t.Errorf("groups: [] should clear, got %v", a.Groups)
	}
	if w := runHTTPAs(t, h, http.MethodPatch, "/api/v1/agents/a1", tenantA, "tokEd", map[string]any{}); w.Code != http.StatusBadRequest {
		t.Errorf("empty PATCH: %d, want 400", w.Code)
	}
	if w := runHTTPAs(t, h, http.MethodPatch, "/api/v1/agents/a1", tenantA, "tokEd",
		map[string]any{"groups": []string{"all tills"}}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "InvalidGroup") {
		t.Errorf("bad name: %d %s", w.Code, w.Body.String())
	}

	// Tenant B cannot touch A's agent — same 404 as a missing one.
	if w := runHTTPAs(t, h, http.MethodPatch, "/api/v1/agents/a1", tenantB, "tokEdB",
		map[string]any{"groups": []string{"x"}}); w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant PATCH: %d, want 404", w.Code)
	}
	if got := repo.agents["a1"].Groups; len(got) != 0 {
		t.Errorf("tenant B changed A's groups: %v", got)
	}
}

func TestAgents_ListGroups_CountsLiveMembersAndOnline(t *testing.T) {
	repo := newAgentRBACMock()
	repo.addUserSession("tokV", "user-A", tenantA, "viewer")
	now := time.Now()
	repo.agents["a1"] = &Agent{ID: "a1", TenantID: tenantA, Name: "t1", Online: true, Groups: []string{"all-tills", "store-oslo"}}
	repo.agents["a2"] = &Agent{ID: "a2", TenantID: tenantA, Name: "t2", Online: false, Groups: []string{"all-tills"}}
	repo.agents["a3"] = &Agent{ID: "a3", TenantID: tenantA, Name: "old", Online: false, RevokedAt: &now, Groups: []string{"all-tills"}}
	repo.agents["b1"] = &Agent{ID: "b1", TenantID: tenantB, Name: "other", Online: true, Groups: []string{"all-tills"}}
	h := NewHandler(repo, NewValidator())

	w := runHTTPAs(t, h, http.MethodGet, "/api/v1/agents/groups", tenantA, "tokV", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list groups: %d %s", w.Code, w.Body.String())
	}
	var groups []AgentGroup
	decodeAgentData(t, w.Body.Bytes(), &groups)
	want := []AgentGroup{{Name: "all-tills", Members: 2, Online: 1}, {Name: "store-oslo", Members: 1, Online: 1}}
	if len(groups) != 2 || groups[0] != want[0] || groups[1] != want[1] {
		t.Errorf("groups = %+v, want %+v (revoked and other-tenant agents excluded)", groups, want)
	}
}

func TestNodeConfig_RemoteAgentNeedsAnAgentOrAGroup(t *testing.T) {
	node := func(cfg string) *Node {
		return &Node{ID: "out", Type: "producer", Config: json.RawMessage(cfg)}
	}
	for cfg, wantErr := range map[string]string{
		`{"type":"remote_agent","remote_agent":{"agent_id":"a1","directory":"in"}}`:                      "",
		`{"type":"remote_agent","remote_agent":{"target":"group","group":"all-tills","directory":"in"}}`: "",
		`{"type":"remote_agent","remote_agent":{"directory":"in"}}`:                                      "agent_id is required",
		`{"type":"remote_agent","remote_agent":{"target":"group","directory":"in"}}`:                     "group is required",
		`{"type":"remote_agent","remote_agent":{"agent_id":"a1"}}`:                                       "directory is required",
	} {
		errs := validateEdgeNodeConfig(node(cfg))
		joined := strings.Join(errs, "; ")
		if wantErr == "" && len(errs) != 0 {
			t.Errorf("%s: unexpected %v", cfg, errs)
		}
		if wantErr != "" && !strings.Contains(joined, wantErr) {
			t.Errorf("%s: errors %q, want one containing %q", cfg, joined, wantErr)
		}
	}
}

func startWithGroupNode(t *testing.T, repo *agentRBACMock, tenantID, group, dir string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(repo, NewValidator())
	repo.connections["ra-conn"] = &Connection{
		ID: "ra-conn", TenantID: tenantID, Name: "group pipeline", Status: "stopped",
		Nodes: []*Node{
			{ID: "in", Type: "consumer", Config: json.RawMessage(`{"type":"http"}`)},
			{ID: "out", Type: "producer", Config: json.RawMessage(
				`{"type":"remote_agent","remote_agent":{"target":"group","group":"` + group + `","directory":"` + dir + `"}}`)},
		},
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/connections/ra-conn/start", nil)
	r = r.WithContext(ContextWithTenantID(context.Background(), tenantID))
	r.SetPathValue("id", "ra-conn")
	w := httptest.NewRecorder()
	h.StartConnection(w, r)
	return w
}

// A group pipeline starts when at least one member has the folder; members
// without it are the gateway's business (skipped and named there). No member
// at all, or none with the folder, is refused with a reason. Another tenant's
// members never count.
func TestStartConnection_GroupNeedsAUsableMember(t *testing.T) {
	repo := newAgentRBACMock()
	repo.agents["a1"] = &Agent{ID: "a1", TenantID: tenantA, Name: "till-1", Groups: []string{"all-tills"},
		Directories: []AgentDirectory{{Name: "catalogue-in", Mode: "write"}}}
	repo.agents["a2"] = &Agent{ID: "a2", TenantID: tenantA, Name: "till-2", Groups: []string{"all-tills"}}
	repo.agents["b1"] = &Agent{ID: "b1", TenantID: tenantB, Name: "other", Groups: []string{"store-oslo"},
		Directories: []AgentDirectory{{Name: "catalogue-in", Mode: "write"}}}
	repo.agents["a4"] = &Agent{ID: "a4", TenantID: tenantA, Name: "reader", Groups: []string{"readers"},
		Directories: []AgentDirectory{{Name: "catalogue-in", Mode: "read"}}}

	if w := startWithGroupNode(t, repo, tenantA, "all-tills", "catalogue-in"); strings.Contains(w.Body.String(), "NodeConfigError") {
		t.Errorf("a usable member was refused: %d %s", w.Code, w.Body.String())
	}
	// Bodies are JSON, so quotes inside messages arrive escaped.
	if w := startWithGroupNode(t, repo, tenantA, "all-tills", "nope"); !strings.Contains(w.Body.String(), `has a write folder named \"nope\"`) {
		t.Errorf("no member with the folder: %d %s", w.Code, w.Body.String())
	}
	if w := startWithGroupNode(t, repo, tenantA, "store-oslo", "catalogue-in"); !strings.Contains(w.Body.String(), `no agent in this workspace is in group \"store-oslo\"`) {
		t.Errorf("another tenant's members counted: %d %s", w.Code, w.Body.String())
	}
	// The folder exists on the only member, but read-only: not usable for an output.
	if w := startWithGroupNode(t, repo, tenantA, "readers", "catalogue-in"); !strings.Contains(w.Body.String(), `has a write folder named \"catalogue-in\"`) {
		t.Errorf("a read-only folder counted as a write folder: %d %s", w.Code, w.Body.String())
	}
}

// --- Resend ---

func resendReq(t *testing.T, h *Handler, tenantID, connID string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/connections/"+connID+"/resend", nil)
	r = r.WithContext(ContextWithTenantID(context.Background(), tenantID))
	r.SetPathValue("id", connID)
	w := httptest.NewRecorder()
	h.ResendConnection(w, r)
	return w
}

func TestResendConnection_ScopedRunningAndPublished(t *testing.T) {
	nc, _, stop := harness.StartEmbeddedJetStream(t)
	defer stop()
	repo := newAgentRBACMock()
	repo.connections["c-run"] = &Connection{ID: "c-run", TenantID: tenantA, Status: "running"}
	repo.connections["c-stop"] = &Connection{ID: "c-stop", TenantID: tenantA, Status: "stopped"}
	h := NewHandler(repo, NewValidator())

	if w := resendReq(t, h, tenantA, "c-run"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("no command bus: %d, want 503", w.Code)
	}
	h.SetPublisher(NewNATSPublisher(nc, nil))

	got := make(chan *nats.Msg, 1)
	sub, err := nc.Subscribe("vrsky.commands."+tenantA+".connection.resend", func(m *nats.Msg) { got <- m })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	if w := resendReq(t, h, tenantB, "c-run"); w.Code != http.StatusForbidden {
		t.Errorf("other tenant: %d, want 403", w.Code)
	}
	if w := resendReq(t, h, tenantA, "c-stop"); w.Code != http.StatusConflict {
		t.Errorf("stopped pipeline: %d, want 409", w.Code)
	}
	if w := resendReq(t, h, tenantA, "missing"); w.Code != http.StatusNotFound {
		t.Errorf("unknown: %d, want 404", w.Code)
	}
	if w := resendReq(t, h, tenantA, "c-run"); w.Code != http.StatusAccepted {
		t.Fatalf("resend: %d %s", w.Code, w.Body.String())
	}
	select {
	case m := <-got:
		var cmd ConnectionCommand
		_ = json.Unmarshal(m.Data, &cmd)
		if cmd.Type != "resend" || cmd.ConnectionID != "c-run" || cmd.TenantID != tenantA {
			t.Errorf("command = %+v", cmd)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no resend command reached the bus")
	}
}
