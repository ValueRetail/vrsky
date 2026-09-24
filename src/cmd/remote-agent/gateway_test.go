package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
	"github.com/ValueRetail/vrsky/pkg/auth"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

const (
	tenant1 = "11111111-1111-1111-1111-111111111111"
	tenant2 = "22222222-2222-2222-2222-222222222222"
	agentA  = "aaaaaaaa-0000-0000-0000-00000000000a" // tenant 1
	agentB  = "bbbbbbbb-0000-0000-0000-00000000000b" // tenant 2
	agentA2 = "aaaaaaaa-0000-0000-0000-0000000000a2" // tenant 1, a second machine
	credA   = agentproto.CredentialPrefix + "credential-of-agent-a"
	credA2  = agentproto.CredentialPrefix + "credential-of-agent-a2"
	credB   = agentproto.CredentialPrefix + "credential-of-agent-b"
	credRev = agentproto.CredentialPrefix + "credential-of-a-revoked-agent"
)

// testEnv is a gateway on an embedded JetStream, a sqlmock database for the
// pipeline and agent lookups, and an httptest server for the agent API.
type testEnv struct {
	g    *gateway
	mock sqlmock.Sqlmock
	nc   *nats.Conn
	js   nats.JetStreamContext
	srv  *httptest.Server

	mu        sync.Mutex
	published []*envelope.Envelope
	streamed  []*envelope.Envelope
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	nc, js, stop := harness.StartEmbeddedJetStream(t)
	t.Cleanup(stop)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.MatchExpectationsInOrder(false)
	t.Cleanup(func() { _ = db.Close() })

	e := &testEnv{mock: mock, nc: nc, js: js}
	g := newGateway()
	g.db = db
	g.nc = nc
	g.js = js
	g.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	g.inlineMax = 16
	g.outputAckWait = time.Second
	g.publish = func(_ context.Context, env *envelope.Envelope) error {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.published = append(e.published, env)
		return nil
	}
	g.publishStream = func(_ context.Context, env *envelope.Envelope, body io.Reader) error {
		b, _ := io.ReadAll(body)
		env.PayloadRef = "spill/test/" + env.ID
		env.PayloadSize = int64(len(b))
		e.mu.Lock()
		defer e.mu.Unlock()
		e.streamed = append(e.streamed, env)
		return nil
	}
	fixtures := map[string]struct {
		id      agentIdentity
		revoked bool
	}{
		auth.HashToken(credA):   {agentIdentity{ID: agentA, TenantID: tenant1, Name: "till-a"}, false},
		auth.HashToken(credB):   {agentIdentity{ID: agentB, TenantID: tenant2, Name: "till-b"}, false},
		auth.HashToken(credA2):  {agentIdentity{ID: agentA2, TenantID: tenant1, Name: "till-a2"}, false},
		auth.HashToken(credRev): {agentIdentity{ID: "revoked", TenantID: tenant1, Name: "gone"}, true},
	}
	g.lookupAgent = func(_ context.Context, hash string) (agentIdentity, bool, error) {
		f, ok := fixtures[hash]
		if !ok {
			return agentIdentity{}, false, sql.ErrNoRows
		}
		return f.id, f.revoked, nil
	}
	e.g = g

	mux := http.NewServeMux()
	mux.Handle("/agent/", g.agentRoutes())
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	t.Cleanup(func() { _ = g.Stop(context.Background()) })
	return e
}

// pipeline JSON builders.
func inputNode(id, agentID, dir string) map[string]any {
	return map[string]any{"id": id, "type": "consumer", "config": map[string]any{
		"type": "remote_agent", "remote_agent": map[string]any{"agent_id": agentID, "directory": dir}}}
}

func outputNode(id, agentID, dir, pattern string) map[string]any {
	return map[string]any{"id": id, "type": "producer", "config": map[string]any{
		"type": "remote_agent", "remote_agent": map[string]any{"agent_id": agentID, "directory": dir, "filename_pattern": pattern}}}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// expectStart scripts the database for one startConnection. agentDirs maps
// agent ID → directories JSON as the tenant-scoped lookup returns it; an agent
// missing from the map is not found (as for another tenant's agent).
func (e *testEnv) expectStart(connID, tenantID string, nodes, edges []any, agentDirs map[string]string, wantStatus string) {
	e.mock.ExpectQuery(`SELECT nodes, edges FROM connections\s+WHERE id::text = \$1 AND tenant_id = \$2`).
		WithArgs(connID, tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow(mustJSON(nodes), mustJSON(edges)))
	for _, n := range nodes {
		ra, ok := n.(map[string]any)["config"].(map[string]any)["remote_agent"].(map[string]any)
		if !ok {
			continue // not a remote_agent node; the gateway looks up no agent for it
		}
		agentID := ra["agent_id"].(string)
		q := e.mock.ExpectQuery(`SELECT directories FROM agents\s+WHERE id::text = \$1 AND tenant_id::text = \$2 AND revoked_at IS NULL`).
			WithArgs(agentID, tenantID)
		if dirs, ok := agentDirs[agentID]; ok {
			q.WillReturnRows(sqlmock.NewRows([]string{"directories"}).AddRow([]byte(dirs)))
		} else {
			q.WillReturnRows(sqlmock.NewRows([]string{"directories"}))
		}
	}
	if wantStatus == "running" {
		e.mock.ExpectExec(`UPDATE connections SET status = \$1`).WithArgs("running", connID, tenantID).
			WillReturnResult(sqlmock.NewResult(0, 1))
	} else {
		e.mock.ExpectExec(`UPDATE connections SET status = 'error'`).WithArgs(sqlmock.AnyArg(), connID, tenantID).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

func (e *testEnv) do(t *testing.T, method, path, cred string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(body))
	if cred != "" {
		req.Header.Set("Authorization", "Bearer "+cred)
	}
	req.Header.Set(agentproto.HeaderProto, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (e *testEnv) poll(t *testing.T, cred string, wait int) agentproto.WorkResponse {
	t.Helper()
	resp := e.do(t, http.MethodGet, "/agent/v1/work?wait="+itoa(wait), cred, nil)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("poll: %d %s", resp.StatusCode, b)
	}
	var w agentproto.WorkResponse
	_ = json.NewDecoder(resp.Body).Decode(&w)
	return w
}

func itoa(n int) string { return strconv.Itoa(n) }

func (e *testEnv) publishTo(t *testing.T, tenantID, connID string, payload string) *envelope.Envelope {
	t.Helper()
	return e.publishStep(t, tenantID, connID, payload, "")
}

// publishStep publishes as if a pipeline step had produced the message:
// lastProcessedBy "" is the input's own message, otherwise the node that
// last transformed it.
func (e *testEnv) publishStep(t *testing.T, tenantID, connID, payload, lastProcessedBy string) *envelope.Envelope {
	t.Helper()
	env := &envelope.Envelope{
		ID: uuid.NewString(), TenantID: tenantID, IntegrationID: connID,
		Payload: []byte(payload), PayloadSize: int64(len(payload)), ContentType: "application/json",
		Source: "test", CreatedAt: time.Now().UTC(), Metadata: map[string]any{"filename": "orders.json"},
	}
	if lastProcessedBy != "" {
		env.Metadata["_last_processed_by"] = lastProcessedBy
	}
	body, _ := envelope.Marshal(env)
	if err := messaging.NewPublisher(e.js).Publish(context.Background(), tenantID, connID, env.ID, body); err != nil {
		t.Fatalf("publish: %v", err)
	}
	return env
}

func (e *testEnv) consumerInfo(t *testing.T, connID string) *nats.ConsumerInfo {
	t.Helper()
	ci, err := e.js.ConsumerInfo(messaging.MainStreamName, outputDurable(connID))
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	return ci
}

// waitFor polls a condition; the gateway's work happens on other goroutines.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// startOutputPipeline starts tenant 1's pipeline: input → agent A's "out" folder.
func (e *testEnv) startOutputPipeline(t *testing.T, connID string) {
	t.Helper()
	nodes := []any{outputNode("out-1", agentA, "out", "")}
	e.expectStart(connID, tenant1, nodes, []any{}, map[string]string{agentA: `[{"name":"out","mode":"write"}]`}, "running")
	e.g.startConnection(context.Background(), connID, tenant1)
	if e.g.sessions[connID] == nil {
		t.Fatalf("pipeline %s did not start", connID)
	}
}

// --- Authentication ---

func TestAgentAuth_UnknownCredential401(t *testing.T) {
	e := newTestEnv(t)
	for _, cred := range []string{"", "not-a-credential", agentproto.CredentialPrefix + "nobody"} {
		if resp := e.do(t, http.MethodGet, "/agent/v1/work?wait=0", cred, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("credential %q: want 401, got %d", cred, resp.StatusCode)
		}
	}
}

func TestAgentAuth_RevokedCredential403(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do(t, http.MethodGet, "/agent/v1/work?wait=0", credRev, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("revoked agent: want 403, got %d", resp.StatusCode)
	}
	var er agentproto.ErrorResponse
	_ = json.NewDecoder(resp.Body).Decode(&er)
	if er.Error != agentproto.ErrAgentRevoked {
		t.Errorf("error code = %q, want %q", er.Error, agentproto.ErrAgentRevoked)
	}
}

func TestAgentAuth_OtherProtocolVersion426(t *testing.T) {
	e := newTestEnv(t)
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/agent/v1/work", nil)
	req.Header.Set("Authorization", "Bearer "+credA)
	req.Header.Set(agentproto.HeaderProto, "2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("want 426, got %d", resp.StatusCode)
	}
}

// The credential SQL: looked up by hash alone (the credential IS the
// capability), tenant taken from the row.
func TestAgentAuth_SQLLooksUpByHash(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	g := newGateway()
	g.db = db
	mock.ExpectQuery(`SELECT id::text, tenant_id::text, name, revoked_at FROM agents WHERE credential_hash = \$1`).
		WithArgs(auth.HashToken(credA)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "revoked_at"}).AddRow(agentA, tenant1, "till-a", nil))
	id, revoked, err := g.dbLookupAgent(context.Background(), auth.HashToken(credA))
	if err != nil || revoked || id.TenantID != tenant1 {
		t.Fatalf("lookup = %+v, %v, %v", id, revoked, err)
	}
}

// --- Starting pipelines: the tenant boundary for agents ---

// TestStart_RefusesAgentFromAnotherTenant: a pipeline in tenant 1 names tenant
// 2's agent. The tenant-scoped agent lookup finds nothing, so the pipeline is
// marked error and no session — no watch, no durable — is created. Were the
// lookup not scoped, tenant 1 could write files onto tenant 2's machine.
// (The SQL's tenant condition itself is proven against real Postgres in
// TestGatewayDB_* and by make lint-tenant.)
func TestStart_RefusesAgentFromAnotherTenant(t *testing.T) {
	e := newTestEnv(t)
	nodes := []any{outputNode("out-1", agentB, "out", "")}
	e.expectStart("conn-x", tenant1, nodes, []any{}, map[string]string{}, "error")
	e.g.startConnection(context.Background(), "conn-x", tenant1)

	if e.g.sessions["conn-x"] != nil {
		t.Fatal("a session was created for a pipeline naming another tenant's agent")
	}
	if w := e.poll(t, credB, 0); len(w.Watches) != 0 || len(w.Deliveries) != 0 {
		t.Errorf("agent B was given work from tenant 1's pipeline: %+v", w)
	}
	if err := e.mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestStart_RefusesUnknownDirectoryOrWrongMode(t *testing.T) {
	e := newTestEnv(t)
	// Output into a read-only folder.
	e.expectStart("conn-1", tenant1, []any{outputNode("o", agentA, "inbox", "")}, []any{},
		map[string]string{agentA: `[{"name":"inbox","mode":"read"}]`}, "error")
	e.g.startConnection(context.Background(), "conn-1", tenant1)
	// Input from a folder the agent does not have.
	e.expectStart("conn-2", tenant1, []any{inputNode("i", agentA, "missing")}, []any{},
		map[string]string{agentA: `[{"name":"inbox","mode":"read"}]`}, "error")
	e.g.startConnection(context.Background(), "conn-2", tenant1)

	if len(e.g.sessions) != 0 {
		t.Fatalf("sessions started despite bad folders: %v", e.g.sessions)
	}
	if err := e.mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// --- Output: the offline guarantee ---

func TestOutput_AcksOnlyAfterAgentConfirms(t *testing.T) {
	e := newTestEnv(t)
	e.startOutputPipeline(t, "conn-out")
	env := e.publishTo(t, tenant1, "conn-out", `{"order":1}`)

	w := e.poll(t, credA, 5)
	if len(w.Deliveries) != 1 {
		t.Fatalf("want 1 delivery, got %+v", w.Deliveries)
	}
	d := w.Deliveries[0]
	body, _ := base64.StdEncoding.DecodeString(d.InlineBase64)
	if string(body) != `{"order":1}` || d.Filename != "orders.json" || d.Directory != "out" {
		t.Fatalf("delivery = %+v (body %q)", d, body)
	}
	if ci := e.consumerInfo(t, "conn-out"); ci.NumAckPending != 1 {
		t.Fatalf("before the agent confirms: NumAckPending = %d, want 1 (in flight, not acked)", ci.NumAckPending)
	}

	resp := e.do(t, http.MethodPost, "/agent/v1/deliveries/"+d.ID+"/ack", credA, mustJSON(agentproto.AckRequest{Status: agentproto.AckOK}))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ack: %d", resp.StatusCode)
	}
	waitFor(t, "the message to be acked", func() bool { return e.consumerInfo(t, "conn-out").NumAckPending == 0 })
	_ = env
}

// TestOutput_HoldsWhileAgentOffline_NoRedeliveryCount is the offline
// guarantee. With nobody polling for several AckWait periods, the message must
// stay in flight — held by the handler, kept alive by heartbeats — with its
// delivery count unchanged, and then be delivered normally when the agent
// returns. A NAK-and-retry design would have spent its attempts and
// dead-lettered it within minutes.
func TestOutput_HoldsWhileAgentOffline_NoRedeliveryCount(t *testing.T) {
	e := newTestEnv(t)
	e.startOutputPipeline(t, "conn-off")
	e.publishTo(t, tenant1, "conn-off", `{"order":2}`)

	waitFor(t, "the message to reach the handler", func() bool { return e.consumerInfo(t, "conn-off").NumAckPending == 1 })
	time.Sleep(4 * e.g.outputAckWait) // the agent is offline

	ci := e.consumerInfo(t, "conn-off")
	if ci.NumRedelivered != 0 {
		t.Fatalf("NumRedelivered = %d while the agent was offline, want 0", ci.NumRedelivered)
	}
	if ci.NumAckPending != 1 {
		t.Fatalf("NumAckPending = %d, want the message still held", ci.NumAckPending)
	}

	w := e.poll(t, credA, 5)
	if len(w.Deliveries) != 1 {
		t.Fatalf("returning agent got %d deliveries, want 1", len(w.Deliveries))
	}
	e.do(t, http.MethodPost, "/agent/v1/deliveries/"+w.Deliveries[0].ID+"/ack", credA, mustJSON(agentproto.AckRequest{Status: agentproto.AckOK}))
	waitFor(t, "ack", func() bool { return e.consumerInfo(t, "conn-off").NumAckPending == 0 })
}

// A failed write is NAK'd and offered again — not acked and lost.
func TestOutput_FailedAckIsRetried(t *testing.T) {
	e := newTestEnv(t)
	e.startOutputPipeline(t, "conn-fail")
	e.publishTo(t, tenant1, "conn-fail", `{"order":3}`)

	first := e.poll(t, credA, 5).Deliveries
	if len(first) != 1 {
		t.Fatalf("want 1 delivery, got %d", len(first))
	}
	e.do(t, http.MethodPost, "/agent/v1/deliveries/"+first[0].ID+"/ack", credA,
		mustJSON(agentproto.AckRequest{Status: agentproto.AckFailed, Error: "disk full"}))

	var again []agentproto.Delivery
	waitFor(t, "the message to be offered again", func() bool {
		again = e.poll(t, credA, 1).Deliveries
		return len(again) == 1
	})
	if again[0].Filename != first[0].Filename {
		t.Errorf("retry names %q, first try named %q", again[0].Filename, first[0].Filename)
	}
	if ci := e.consumerInfo(t, "conn-fail"); ci.NumRedelivered < 1 {
		t.Errorf("NumRedelivered = %d, want the failure to count as a delivery attempt", ci.NumRedelivered)
	}
}

// An agent that took a delivery and vanished gets it again once the lease
// runs out — the crash-mid-write case.
func TestOutput_LeaseExpiryReoffers(t *testing.T) {
	e := newTestEnv(t)
	clock := time.Now()
	var clockMu sync.Mutex
	e.g.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	e.startOutputPipeline(t, "conn-lease")
	e.publishTo(t, tenant1, "conn-lease", `{"order":4}`)

	if n := len(e.poll(t, credA, 5).Deliveries); n != 1 {
		t.Fatalf("first poll: %d deliveries", n)
	}
	if n := len(e.poll(t, credA, 0).Deliveries); n != 0 {
		t.Fatalf("within the lease: re-offered %d deliveries, want 0", n)
	}
	clockMu.Lock()
	clock = clock.Add(agentproto.LeaseDuration + time.Second)
	clockMu.Unlock()
	if n := len(e.poll(t, credA, 0).Deliveries); n != 1 {
		t.Fatalf("after the lease: %d deliveries, want the same one again", n)
	}
}

// Stopping a pipeline releases the held message back to the stream so the
// next session delivers it; nothing is acked unconfirmed. Asserted on the
// outcome — the message reaches the agent after a restart — rather than on
// consumer counters, which lag the server.
func TestOutput_StopReleasesTheMessage(t *testing.T) {
	e := newTestEnv(t)
	e.startOutputPipeline(t, "conn-stop")
	e.publishTo(t, tenant1, "conn-stop", `{"order":5}`)
	waitFor(t, "in flight", func() bool { return e.consumerInfo(t, "conn-stop").NumAckPending == 1 })

	done := make(chan struct{})
	go func() { e.g.stopConnection(context.Background(), "conn-stop", tenant1); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return while a message was held")
	}
	if w := e.poll(t, credA, 0); len(w.Deliveries) != 0 {
		t.Errorf("a stopped pipeline still offers %d deliveries", len(w.Deliveries))
	}

	e.startOutputPipeline(t, "conn-stop")
	var got []agentproto.Delivery
	waitFor(t, "the released message to be delivered by the new session", func() bool {
		got = e.poll(t, credA, 1).Deliveries
		return len(got) == 1
	})
	body, _ := base64.StdEncoding.DecodeString(got[0].InlineBase64)
	if string(body) != `{"order":5}` {
		t.Fatalf("redelivered body = %q", body)
	}
}

// A pipeline's subject carries every step's copy of a message. An output after
// a filter must write only the filtered copy — not the raw input as well.
func TestOutput_TakesOnlyItsPredecessorsMessage(t *testing.T) {
	e := newTestEnv(t)
	nodes := []any{
		map[string]any{"id": "in", "type": "consumer", "config": map[string]any{"type": "http"}},
		map[string]any{"id": "f", "type": "filter", "config": map[string]any{}},
		outputNode("out", agentA, "out", ""),
	}
	edges := []any{map[string]any{"source": "in", "target": "f"}, map[string]any{"source": "f", "target": "out"}}
	e.expectStart("conn-pred", tenant1, nodes, edges, map[string]string{agentA: `[{"name":"out","mode":"write"}]`}, "running")
	e.g.startConnection(context.Background(), "conn-pred", tenant1)

	e.publishStep(t, tenant1, "conn-pred", `{"raw":true}`, "")
	e.publishStep(t, tenant1, "conn-pred", `{"filtered":true}`, "f")

	var bodies []string
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		for _, d := range e.poll(t, credA, 1).Deliveries {
			b, _ := base64.StdEncoding.DecodeString(d.InlineBase64)
			bodies = append(bodies, string(b))
			e.do(t, http.MethodPost, "/agent/v1/deliveries/"+d.ID+"/ack", credA, mustJSON(agentproto.AckRequest{Status: agentproto.AckOK}))
		}
	}
	if len(bodies) != 1 || bodies[0] != `{"filtered":true}` {
		t.Fatalf("agent was asked to write %q, want only the filtered message", bodies)
	}
}

// --- Isolation between agents ---

// TestIsolation_WorkNeverCrossesAgents: agent B (tenant 2) polls while tenant
// 1's pipeline has work waiting for agent A. B must see none of it — no
// deliveries, no watches.
func TestIsolation_WorkNeverCrossesAgents(t *testing.T) {
	e := newTestEnv(t)
	e.startOutputPipeline(t, "conn-iso")
	nodes := []any{inputNode("in-1", agentA, "inbox")}
	e.expectStart("conn-in", tenant1, nodes, []any{}, map[string]string{agentA: `[{"name":"inbox","mode":"read"}]`}, "running")
	e.g.startConnection(context.Background(), "conn-in", tenant1)
	e.publishTo(t, tenant1, "conn-iso", `{"secret":"tenant-1"}`)
	waitFor(t, "in flight", func() bool { return e.consumerInfo(t, "conn-iso").NumAckPending == 1 })

	w := e.poll(t, credB, 1)
	if len(w.Deliveries) != 0 || len(w.Watches) != 0 {
		t.Fatalf("agent B received tenant 1's work: %+v", w)
	}
	// A second machine in the SAME workspace is no more entitled to A's work:
	// the tenant check alone would let this one through.
	w = e.poll(t, credA2, 1)
	if len(w.Deliveries) != 0 || len(w.Watches) != 0 {
		t.Fatalf("agent A2 received agent A's work: %+v", w)
	}
	if a := e.poll(t, credA, 0); len(a.Deliveries) != 1 || len(a.Watches) != 1 {
		t.Fatalf("agent A should see its own delivery and watch, got %+v", a)
	}
}

// B cannot fetch the body of, or acknowledge, A's delivery — acking it would
// tell the pipeline a file was written that never was.
func TestIsolation_AckOrBodyForForeignDeliveryIs404(t *testing.T) {
	e := newTestEnv(t)
	e.startOutputPipeline(t, "conn-foreign")
	e.publishTo(t, tenant1, "conn-foreign", `{"x":1}`)
	d := e.poll(t, credA, 5).Deliveries
	if len(d) != 1 {
		t.Fatalf("setup: %d deliveries", len(d))
	}

	if resp := e.do(t, http.MethodGet, "/agent/v1/deliveries/"+d[0].ID+"/body", credB, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("B fetching A's body: want 404, got %d", resp.StatusCode)
	}
	if resp := e.do(t, http.MethodPost, "/agent/v1/deliveries/"+d[0].ID+"/ack", credB,
		mustJSON(agentproto.AckRequest{Status: agentproto.AckOK})); resp.StatusCode != http.StatusNotFound {
		t.Errorf("B acking A's delivery: want 404, got %d", resp.StatusCode)
	}
	if resp := e.do(t, http.MethodPost, "/agent/v1/deliveries/"+d[0].ID+"/ack", credA2,
		mustJSON(agentproto.AckRequest{Status: agentproto.AckOK})); resp.StatusCode != http.StatusNotFound {
		t.Errorf("A2 (same workspace) acking A's delivery: want 404, got %d", resp.StatusCode)
	}
	time.Sleep(200 * time.Millisecond)
	if ci := e.consumerInfo(t, "conn-foreign"); ci.NumAckPending != 1 {
		t.Fatalf("A's message is no longer pending after B's ack attempt: %+v", ci)
	}
}

// B cannot push a file into tenant 1's pipeline by naming its connection.
func TestIsolation_UploadToOtherTenantsConnection404(t *testing.T) {
	e := newTestEnv(t)
	nodes := []any{inputNode("in-1", agentA, "inbox")}
	e.expectStart("conn-up", tenant1, nodes, []any{}, map[string]string{agentA: `[{"name":"inbox","mode":"read"}]`}, "running")
	e.g.startConnection(context.Background(), "conn-up", tenant1)

	path := "/agent/v1/uploads?connection_id=conn-up&directory=inbox&filename=evil.json&upload_id=" + uuid.NewString()
	if resp := e.do(t, http.MethodPost, path, credB, []byte(`{"injected":true}`)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("B uploading into tenant 1's pipeline: want 404, got %d", resp.StatusCode)
	}
	// Same workspace, different machine: A2 is not the agent the pipeline
	// watches, so it cannot feed that pipeline either.
	path = "/agent/v1/uploads?connection_id=conn-up&directory=inbox&filename=a2.json&upload_id=" + uuid.NewString()
	if resp := e.do(t, http.MethodPost, path, credA2, []byte(`{"from":"a2"}`)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("A2 uploading into agent A's watch: want 404, got %d", resp.StatusCode)
	}
	if len(e.published)+len(e.streamed) != 0 {
		t.Fatalf("something was published: %+v %+v", e.published, e.streamed)
	}
}

// --- Input ---

// The envelope's tenant is the pipeline's, never the caller's, and the upload
// ID becomes the envelope ID (the de-duplication key). Small files go inline;
// large ones stream to the payload store.
func TestInput_UploadPublishesEnvelopeWithConnectionTenant(t *testing.T) {
	e := newTestEnv(t)
	nodes := []any{inputNode("in-1", agentA, "inbox")}
	e.expectStart("conn-in", tenant1, nodes, []any{}, map[string]string{agentA: `[{"name":"inbox","mode":"read"}]`}, "running")
	e.g.startConnection(context.Background(), "conn-in", tenant1)

	small := uuid.NewString()
	resp := e.do(t, http.MethodPost, "/agent/v1/uploads?connection_id=conn-in&directory=inbox&filename=a.json&upload_id="+small, credA, []byte(`{"a":1}`))
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("small upload: %d %s", resp.StatusCode, b)
	}
	large := uuid.NewString()
	e.do(t, http.MethodPost, "/agent/v1/uploads?connection_id=conn-in&directory=inbox&filename=b.csv&upload_id="+large, credA,
		[]byte(strings.Repeat("x,y\n", 100)))

	if len(e.published) != 1 || len(e.streamed) != 1 {
		t.Fatalf("published %d inline and %d streamed, want 1 and 1", len(e.published), len(e.streamed))
	}
	p := e.published[0]
	if p.ID != small || p.TenantID != tenant1 || p.IntegrationID != "conn-in" || string(p.Payload) != `{"a":1}` ||
		p.ContentType != "application/json" || p.Metadata["filename"] != "a.json" {
		t.Errorf("inline envelope = %+v", p)
	}
	if s := e.streamed[0]; s.ID != large || s.TenantID != tenant1 || s.ContentType != "text/csv" {
		t.Errorf("streamed envelope = %+v", s)
	}
}

func TestInput_RejectsBadFilenameAndUnwatchedFolder(t *testing.T) {
	e := newTestEnv(t)
	nodes := []any{inputNode("in-1", agentA, "inbox")}
	e.expectStart("conn-in", tenant1, nodes, []any{}, map[string]string{agentA: `[{"name":"inbox","mode":"read"}]`}, "running")
	e.g.startConnection(context.Background(), "conn-in", tenant1)

	id := uuid.NewString()
	for _, c := range []struct {
		query string
		want  int
	}{
		{"connection_id=conn-in&directory=inbox&filename=..%2Fx&upload_id=" + id, http.StatusBadRequest},
		{"connection_id=conn-in&directory=inbox&filename=a.json&upload_id=not-a-uuid", http.StatusBadRequest},
		{"connection_id=conn-in&directory=other&filename=a.json&upload_id=" + id, http.StatusNotFound},
		{"connection_id=nope&directory=inbox&filename=a.json&upload_id=" + id, http.StatusNotFound},
	} {
		if resp := e.do(t, http.MethodPost, "/agent/v1/uploads?"+c.query, credA, []byte("x")); resp.StatusCode != c.want {
			t.Errorf("%s: want %d, got %d", c.query, c.want, resp.StatusCode)
		}
	}
	if len(e.published)+len(e.streamed) != 0 {
		t.Fatal("a rejected upload was published")
	}
}

// --- Watches ---

// A new watch reaches an agent that is mid-poll at once, not after the hold.
func TestWork_NewWatchWakesAPollingAgent(t *testing.T) {
	e := newTestEnv(t)
	first := e.poll(t, credA, 0)

	got := make(chan agentproto.WorkResponse, 1)
	go func() {
		resp := e.do(t, http.MethodGet, "/agent/v1/work?wait=20&watches="+first.WatchesVersion, credA, nil)
		var w agentproto.WorkResponse
		_ = json.NewDecoder(resp.Body).Decode(&w)
		got <- w
	}()
	time.Sleep(200 * time.Millisecond)
	nodes := []any{inputNode("in-1", agentA, "inbox")}
	e.expectStart("conn-w", tenant1, nodes, []any{}, map[string]string{agentA: `[{"name":"inbox","mode":"read"}]`}, "running")
	e.g.startConnection(context.Background(), "conn-w", tenant1)

	select {
	case w := <-got:
		if len(w.Watches) != 1 || w.Watches[0].Directory != "inbox" || w.Watches[0].After != agentproto.AfterMove {
			t.Fatalf("watches = %+v", w.Watches)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the poll was not woken by the new watch")
	}
}

// --- Registration (SQL shape; the race itself is proven against Postgres) ---

func TestRegister_ConsumesTokenAtomicallyAndCreatesAgentInTokensTenant(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	g := newGateway()
	g.db = db
	g.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	token := agentproto.RegTokenPrefix + "abc"

	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE agent_registration_tokens SET used_at = NOW\(\)\s+WHERE token_hash = \$1 AND used_at IS NULL AND expires_at > NOW\(\)`).
		WithArgs(auth.HashToken(token)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "suggested", "created_by"}).AddRow("tok-1", tenant1, "LAGER-01", ""))
	mock.ExpectQuery(`INSERT INTO agents`).
		WithArgs(tenant1, "LAGER-01", "host", "windows", "amd64", "0.1.0", sqlmock.AnyArg(), sqlmock.AnyArg(), nil).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(agentA))
	mock.ExpectExec(`UPDATE agent_registration_tokens SET used_by_agent`).WithArgs(agentA, "tok-1", tenant1).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	body := mustJSON(agentproto.RegisterRequest{RegistrationToken: token, Hostname: "host", OS: "windows", Arch: "amd64",
		Version: "0.1.0", Directories: []agentproto.Directory{{Name: "inbox", Mode: "read"}}})
	rec := httptest.NewRecorder()
	g.agentRoutes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent/v1/register", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	var out agentproto.RegisterResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.AgentID != agentA || !strings.HasPrefix(out.Credential, agentproto.CredentialPrefix) || out.Name != "LAGER-01" {
		t.Errorf("response = %+v", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestRegister_UsedOrExpiredToken401(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	g := newGateway()
	g.db = db
	g.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE agent_registration_tokens`).WillReturnRows(sqlmock.NewRows([]string{"id", "t", "s", "c"}))
	mock.ExpectRollback()

	rec := httptest.NewRecorder()
	g.agentRoutes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent/v1/register",
		bytes.NewReader(mustJSON(agentproto.RegisterRequest{RegistrationToken: agentproto.RegTokenPrefix + "used"}))))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), agentproto.ErrTokenExpiredOrUsed) {
		t.Fatalf("used token: %d %s", rec.Code, rec.Body.String())
	}
}

// --- Pipeline parsing ---

func TestParseRemoteNodes_ResolvesPredecessorsAndDefaults(t *testing.T) {
	nodes := mustJSON([]any{
		inputNode("in", agentA, "inbox"),
		map[string]any{"id": "f", "type": "filter", "config": map[string]any{}},
		outputNode("out", agentA, "out", "{id}.{extension}"),
		map[string]any{"id": "file", "type": "producer", "config": map[string]any{"type": "file"}},
	})
	edges := mustJSON([]any{map[string]any{"source": "in", "target": "f"}, map[string]any{"source": "f", "target": "out"}})
	ins, outs, err := parseRemoteNodes(nodes, edges)
	if err != nil || len(ins) != 1 || len(outs) != 1 {
		t.Fatalf("parse = %+v %+v %v", ins, outs, err)
	}
	if ins[0].After != agentproto.AfterMove {
		t.Errorf("after defaults to %q, want move", ins[0].After)
	}
	if outs[0].PredecessorID != "f" || outs[0].PredIsConsumer {
		t.Errorf("output predecessor = %+v", outs[0])
	}
	if !eligible(outs[0], "f") || eligible(outs[0], "") {
		t.Error("output after a filter must take the filter's message and not the raw input")
	}
}
