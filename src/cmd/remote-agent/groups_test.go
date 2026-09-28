package main

import (
	"context"
	"database/sql/driver"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
	"github.com/ValueRetail/vrsky/pkg/messaging"
)

// Agent groups: one output node, many tills. See groups.go.

func groupOutputNode(id, group, dir, pattern string) map[string]any {
	return map[string]any{"id": id, "type": "producer", "config": map[string]any{
		"type": "remote_agent", "remote_agent": map[string]any{"target": "group", "group": group, "directory": dir, "filename_pattern": pattern}}}
}

func groupInputNode(id, group, dir string) map[string]any {
	return map[string]any{"id": id, "type": "consumer", "config": map[string]any{
		"type": "remote_agent", "remote_agent": map[string]any{"target": "group", "group": group, "directory": dir}}}
}

// memberRow is one agents row as liveMembers reads it.
func memberRow(id, name, groups, dirs string) []driver.Value {
	return []driver.Value{id, name, []byte(groups), []byte(dirs)}
}

const outWrite = `[{"name":"out","mode":"write"}]`

// expectMembers answers the next `times` membership reads for a tenant. The
// query's tenant condition is part of the expectation: a membership read that
// did not scope by tenant would not match, and the pipeline would refuse to
// start — which is how a group name is kept from meaning another workspace's
// agents.
func (e *testEnv) expectMembers(tenantID string, times int, rows ...[]driver.Value) {
	for i := 0; i < times; i++ {
		r := sqlmock.NewRows([]string{"id", "name", "groups", "directories"})
		for _, row := range rows {
			r.AddRow(row...)
		}
		e.mock.ExpectQuery(`SELECT id::text, name, groups, directories FROM agents\s+WHERE tenant_id::text = \$1 AND revoked_at IS NULL AND groups && \$2`).
			WithArgs(tenantID, sqlmock.AnyArg()).WillReturnRows(r)
	}
}

// startGroupPipeline starts tenant 1's pipeline: input → group all-tills, folder "out".
// Starting reads the members twice (the start check, then the reconcile).
func (e *testEnv) startGroupPipeline(t *testing.T, connID string, members ...[]driver.Value) {
	t.Helper()
	nodes := []any{groupOutputNode("out-1", "all-tills", "out", "")}
	e.expectStart(connID, tenant1, nodes, []any{}, nil, "running")
	e.expectMembers(tenant1, 2, members...)
	e.g.startConnection(context.Background(), connID, tenant1)
	if e.g.sessions[connID] == nil {
		t.Fatalf("group pipeline %s did not start", connID)
	}
}

func (e *testEnv) memberInfo(connID, agentID string) (*nats.ConsumerInfo, error) {
	return e.js.ConsumerInfo(messaging.MainStreamName, memberDurable(connID, agentID))
}

func (e *testEnv) ack(t *testing.T, cred, deliveryID string) {
	t.Helper()
	resp := e.do(t, http.MethodPost, "/agent/v1/deliveries/"+deliveryID+"/ack", cred, mustJSON(agentproto.AckRequest{Status: agentproto.AckOK}))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ack: %d", resp.StatusCode)
	}
}

func (e *testEnv) memberIDs(connID string) []string {
	e.g.mu.Lock()
	defer e.g.mu.Unlock()
	var ids []string
	for id := range e.g.sessions[connID].memberSubs {
		ids = append(ids, id)
	}
	return ids
}

// Every member receives its own copy, and each copy is acknowledged on its
// own: A's ack settles A's durable and leaves A2's message in flight.
func TestGroup_EachMemberGetsItsOwnCopy(t *testing.T) {
	e := newTestEnv(t)
	e.startGroupPipeline(t, "g-copy",
		memberRow(agentA, "till-a", "{all-tills}", outWrite),
		memberRow(agentA2, "till-a2", "{all-tills}", outWrite))
	e.publishTo(t, tenant1, "g-copy", `{"order":1}`)

	wA, wA2 := e.poll(t, credA, 5), e.poll(t, credA2, 5)
	if len(wA.Deliveries) != 1 || len(wA2.Deliveries) != 1 {
		t.Fatalf("deliveries: A %d, A2 %d — want one each", len(wA.Deliveries), len(wA2.Deliveries))
	}
	for _, d := range []agentproto.Delivery{wA.Deliveries[0], wA2.Deliveries[0]} {
		body, _ := base64.StdEncoding.DecodeString(d.InlineBase64)
		if string(body) != `{"order":1}` || d.Filename != "orders.json" || d.Directory != "out" {
			t.Errorf("delivery = %+v (body %q)", d, body)
		}
	}
	e.ack(t, credA, wA.Deliveries[0].ID)
	waitFor(t, "A's durable to settle", func() bool { ci, _ := e.memberInfo("g-copy", agentA); return ci != nil && ci.NumAckPending == 0 })
	if ci, _ := e.memberInfo("g-copy", agentA2); ci == nil || ci.NumAckPending != 1 {
		t.Fatalf("A2's message should still be in flight after A's ack: %+v", ci)
	}
	e.ack(t, credA2, wA2.Deliveries[0].ID)
	waitFor(t, "A2's durable to settle", func() bool { ci, _ := e.memberInfo("g-copy", agentA2); return ci != nil && ci.NumAckPending == 0 })
}

// The point of the feature: a till that is off does not hold up the others.
// A2 never polls; A receives and acknowledges message after message, while
// A2's first message waits with no delivery attempt spent.
func TestGroup_OfflineMemberDoesNotBlockOthers(t *testing.T) {
	e := newTestEnv(t)
	e.startGroupPipeline(t, "g-off",
		memberRow(agentA, "till-a", "{all-tills}", outWrite),
		memberRow(agentA2, "till-a2", "{all-tills}", outWrite))
	e.publishTo(t, tenant1, "g-off", `{"order":1}`)
	e.publishTo(t, tenant1, "g-off", `{"order":2}`)

	for i := 1; i <= 2; i++ {
		w := e.poll(t, credA, 5)
		if len(w.Deliveries) != 1 {
			t.Fatalf("A, message %d: got %d deliveries", i, len(w.Deliveries))
		}
		e.ack(t, credA, w.Deliveries[0].ID)
	}
	waitFor(t, "A to be fully acked", func() bool {
		ci, _ := e.memberInfo("g-off", agentA)
		return ci != nil && ci.NumAckPending == 0 && ci.NumPending == 0
	})

	time.Sleep(4 * e.g.outputAckWait) // A2 is offline all along
	ci, err := e.memberInfo("g-off", agentA2)
	if err != nil {
		t.Fatal(err)
	}
	if ci.NumRedelivered != 0 || ci.NumAckPending != 1 {
		t.Fatalf("A2 offline: NumRedelivered=%d NumAckPending=%d, want 0 and 1 (held, not retried)", ci.NumRedelivered, ci.NumAckPending)
	}
}

// A member added while the pipeline runs starts at "now": nothing from
// before it joined, everything from then on.
func TestGroup_LateJoinerStartsAtNow(t *testing.T) {
	e := newTestEnv(t)
	e.startGroupPipeline(t, "g-late", memberRow(agentA, "till-a", "{all-tills}", outWrite))
	e.publishTo(t, tenant1, "g-late", `{"order":1}`)
	w := e.poll(t, credA, 5)
	e.ack(t, credA, w.Deliveries[0].ID)

	e.expectMembers(tenant1, 1,
		memberRow(agentA, "till-a", "{all-tills}", outWrite),
		memberRow(agentA2, "till-a2", "{all-tills}", outWrite))
	e.g.reconcileAll(context.Background())
	if _, err := e.memberInfo("g-late", agentA2); err != nil {
		t.Fatalf("no durable for the new member: %v", err)
	}
	if w := e.poll(t, credA2, 1); len(w.Deliveries) != 0 {
		t.Fatalf("the late joiner received %d old message(s); it must start at now", len(w.Deliveries))
	}

	e.publishTo(t, tenant1, "g-late", `{"order":2}`)
	w = e.poll(t, credA2, 5)
	if len(w.Deliveries) != 1 {
		t.Fatalf("the late joiner got %d deliveries after joining, want 1", len(w.Deliveries))
	}
	body, _ := base64.StdEncoding.DecodeString(w.Deliveries[0].InlineBase64)
	if string(body) != `{"order":2}` {
		t.Errorf("late joiner got %q, want the message published after it joined", body)
	}
}

// A member in the group but without the folder is skipped — no durable, no
// deliveries — and named once in the panel, so a misconfigured till is seen.
func TestGroup_MemberWithoutTheFolderIsSkippedAndNamed(t *testing.T) {
	e := newTestEnv(t)
	ch, unsub := e.g.events.subscribe("g-skip")
	defer unsub()
	e.startGroupPipeline(t, "g-skip",
		memberRow(agentA, "till-a", "{all-tills}", outWrite),
		memberRow(agentA2, "till-a2", "{all-tills}", `[{"name":"elsewhere","mode":"write"}]`))

	if ids := e.memberIDs("g-skip"); len(ids) != 1 || ids[0] != agentA {
		t.Fatalf("member subscriptions = %v, want only A", ids)
	}
	if _, err := e.memberInfo("g-skip", agentA2); err == nil {
		t.Error("a durable was created for a member that cannot take the folder")
	}
	select {
	case ev := <-ch:
		if ev.Type != "warning" || !strings.Contains(ev.Message, "till-a2") || !strings.Contains(ev.Message, "out") {
			t.Errorf("event = %+v, want a warning naming till-a2 and the folder", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no warning event about the skipped member")
	}
	e.publishTo(t, tenant1, "g-skip", `{"order":1}`)
	if w := e.poll(t, credA, 5); len(w.Deliveries) != 1 {
		t.Fatalf("A got %d deliveries, want 1", len(w.Deliveries))
	}
	if w := e.poll(t, credA2, 1); len(w.Deliveries) != 0 {
		t.Fatalf("the skipped member got %d deliveries", len(w.Deliveries))
	}
}

// Removed from the group: its subscription stops but its durable stays, so
// putting it back resumes where it was. Gone or revoked: the durable goes
// too, so nothing keeps waiting for it.
func TestGroup_RemovedMemberKeepsItsDurable_RevokedLosesIt(t *testing.T) {
	e := newTestEnv(t)
	e.startGroupPipeline(t, "g-rm",
		memberRow(agentA, "till-a", "{all-tills}", outWrite),
		memberRow(agentA2, "till-a2", "{all-tills}", outWrite))

	e.expectMembers(tenant1, 1,
		memberRow(agentA, "till-a", "{all-tills}", outWrite),
		memberRow(agentA2, "till-a2", "{store-oslo}", outWrite)) // still live, out of the group
	e.g.reconcileAll(context.Background())
	if ids := e.memberIDs("g-rm"); len(ids) != 1 || ids[0] != agentA {
		t.Fatalf("after removal from the group, subscriptions = %v, want only A", ids)
	}
	if _, err := e.memberInfo("g-rm", agentA2); err != nil {
		t.Fatalf("removed member's durable was deleted: %v — it must be kept", err)
	}

	e.expectMembers(tenant1, 1, memberRow(agentA, "till-a", "{all-tills}", outWrite)) // A2 revoked
	e.g.reconcileAll(context.Background())
	if _, err := e.memberInfo("g-rm", agentA2); err == nil {
		t.Fatal("revoked member's durable still exists; it would hold messages forever")
	}
}

func TestGroup_NoUsableMemberIsRefused(t *testing.T) {
	e := newTestEnv(t)
	nodes := []any{groupOutputNode("out-1", "all-tills", "out", "")}
	e.expectStart("g-none", tenant1, nodes, []any{}, nil, "error")
	e.expectMembers(tenant1, 1, memberRow(agentA2, "till-a2", "{all-tills}", `[{"name":"elsewhere","mode":"write"}]`))
	e.g.startConnection(context.Background(), "g-none", tenant1)
	if e.g.sessions["g-none"] != nil {
		t.Fatal("a group pipeline with no usable member started")
	}
	if err := e.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expected the error status to be recorded: %v", err)
	}
}

// A group source: the watch goes to every member, an upload is accepted from
// any current member and from nobody else, and a member that leaves loses
// both at once.
func TestGroup_UploadAcceptedFromAnyCurrentMemberOnly(t *testing.T) {
	e := newTestEnv(t)
	inboxRead := `[{"name":"inbox","mode":"read"}]`
	nodes := []any{groupInputNode("in-1", "all-tills", "inbox")}
	e.expectStart("g-in", tenant1, nodes, []any{}, nil, "running")
	e.expectMembers(tenant1, 2,
		memberRow(agentA, "till-a", "{all-tills}", inboxRead),
		memberRow(agentA2, "till-a2", "{all-tills}", inboxRead))
	e.g.startConnection(context.Background(), "g-in", tenant1)
	if e.g.sessions["g-in"] == nil {
		t.Fatal("group input pipeline did not start")
	}

	for _, cred := range []string{credA, credA2} {
		w := e.poll(t, cred, 2)
		if len(w.Watches) != 1 || w.Watches[0].Directory != "inbox" || w.Watches[0].ConnectionID != "g-in" {
			t.Fatalf("%s watches = %+v, want inbox on g-in", cred, w.Watches)
		}
	}
	upload := func(cred string) int {
		resp := e.do(t, http.MethodPost, "/agent/v1/uploads?connection_id=g-in&directory=inbox&filename=sales.csv&upload_id="+uuid.NewString(),
			cred, []byte("a,b\n1,2\n"))
		return resp.StatusCode
	}
	if code := upload(credA2); code != http.StatusCreated {
		t.Fatalf("upload from member A2: %d, want 201", code)
	}
	if code := upload(credB); code != http.StatusNotFound {
		t.Fatalf("upload from another tenant's agent: %d, want 404", code)
	}
	e.mu.Lock()
	n := len(e.published)
	e.mu.Unlock()
	if n != 1 {
		t.Fatalf("published %d envelopes, want 1", n)
	}

	e.expectMembers(tenant1, 1, memberRow(agentA, "till-a", "{all-tills}", inboxRead)) // A2 left
	e.g.reconcileAll(context.Background())
	if w := e.poll(t, credA2, 2); len(w.Watches) != 0 {
		t.Fatalf("A2 still has watches after leaving the group: %+v", w.Watches)
	}
	if code := upload(credA2); code != http.StatusNotFound {
		t.Fatalf("upload from a former member: %d, want 404", code)
	}
}

// The membership read carries the pipeline's tenant, so tenant 2's group
// pipeline is served by tenant 2's agents only — asserted through the query
// expectation, which a query without the tenant condition would not meet.
func TestIsolation_GroupMembershipIsPerTenant(t *testing.T) {
	e := newTestEnv(t)
	nodes := []any{groupOutputNode("out-1", "all-tills", "out", "")}
	e.expectStart("g-b", tenant2, nodes, []any{}, nil, "running")
	e.expectMembers(tenant2, 2, memberRow(agentB, "till-b", "{all-tills}", outWrite))
	e.g.startConnection(context.Background(), "g-b", tenant2)
	if e.g.sessions["g-b"] == nil {
		t.Fatal("tenant 2's group pipeline did not start")
	}
	if ids := e.memberIDs("g-b"); len(ids) != 1 || ids[0] != agentB {
		t.Fatalf("members = %v, want tenant 2's agent only", ids)
	}
	e.publishTo(t, tenant2, "g-b", `{"order":1}`)
	if w := e.poll(t, credB, 5); len(w.Deliveries) != 1 {
		t.Fatalf("B got %d deliveries, want 1", len(w.Deliveries))
	}
	if w := e.poll(t, credA, 1); len(w.Deliveries) != 0 {
		t.Fatalf("tenant 1's agent, same group name, got %d deliveries from tenant 2's pipeline", len(w.Deliveries))
	}
	if err := e.mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A pipeline with both a single-agent output and a group output: the
// pipeline's durable serves the first, the members' durables the second, and
// nobody receives a message twice.
func TestGroup_MixedPipelineDeliversOnceEach(t *testing.T) {
	e := newTestEnv(t)
	nodes := []any{outputNode("single", agentA, "out", ""), groupOutputNode("grp", "all-tills", "out", "")}
	e.expectStart("g-mix", tenant1, nodes, []any{}, map[string]string{agentA: outWrite}, "running")
	e.expectMembers(tenant1, 2, memberRow(agentA2, "till-a2", "{all-tills}", outWrite))
	e.g.startConnection(context.Background(), "g-mix", tenant1)
	if e.g.sessions["g-mix"] == nil {
		t.Fatal("mixed pipeline did not start")
	}
	e.publishTo(t, tenant1, "g-mix", `{"order":1}`)

	wA, wA2 := e.poll(t, credA, 5), e.poll(t, credA2, 5)
	if len(wA.Deliveries) != 1 || len(wA2.Deliveries) != 1 {
		t.Fatalf("deliveries: A %d, A2 %d — want exactly one each", len(wA.Deliveries), len(wA2.Deliveries))
	}
	e.ack(t, credA, wA.Deliveries[0].ID)
	e.ack(t, credA2, wA2.Deliveries[0].ID)
	// Nothing else queued for either: a second poll comes back empty.
	if w := e.poll(t, credA, 1); len(w.Deliveries) != 0 {
		t.Fatalf("A received a second copy: %+v", w.Deliveries)
	}
	if w := e.poll(t, credA2, 1); len(w.Deliveries) != 0 {
		t.Fatalf("A2 received a second copy: %+v", w.Deliveries)
	}
}

func TestParseRemoteNodes_GroupTarget(t *testing.T) {
	nodes := mustJSON([]any{
		groupInputNode("in", "all-tills", "inbox"),
		groupOutputNode("out", "store-oslo", "out", "{id}.{extension}"),
		outputNode("single", agentA, "out2", ""),
		map[string]any{"id": "bad", "type": "producer", "config": map[string]any{
			"type": "remote_agent", "remote_agent": map[string]any{"target": "group", "directory": "x"}}}, // no group
	})
	inputs, outputs, err := parseRemoteNodes(nodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].Target != targetGroup || inputs[0].Group != "all-tills" || inputs[0].AgentID != "" || inputs[0].After != agentproto.AfterMove {
		t.Errorf("inputs = %+v", inputs)
	}
	if len(outputs) != 2 || outputs[0].Target != targetGroup || outputs[0].Group != "store-oslo" || outputs[1].Target != targetAgent || outputs[1].AgentID != agentA {
		t.Errorf("outputs = %+v (a group node without a group must be dropped)", outputs)
	}
	single := []remoteNode{{NodeID: "o", Target: targetAgent, AgentID: agentA, Directory: "out"}}
	group := []remoteNode{{NodeID: "o", Target: targetGroup, Group: "all-tills", Directory: "out"}}
	if fingerprint(nil, single) == fingerprint(nil, group) {
		t.Error("switching a node from one agent to a group must change the fingerprint, or a redeploy would be a no-op")
	}
}
