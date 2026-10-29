package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
)

// A stop halts a session's subscribers, and since #521 a halted subscriber
// still finishes the poll it is in and dispatches what that poll brought. The
// gateway used to end its handlers' scope right after the halt, so such a
// message met an ended session: errSessionEnded, a NAK, and a delivery attempt
// spent (the last one dead-letters it). A stop now keeps the scope alive until
// the halted subscribers have handled their last poll, for at most stopGrace
// (#576).

// onlineAgent runs an agent that is up for the rest of the test: it long-polls
// as cred and acks every delivery as soon as it gets it, the way a till writes
// a small file. It stops before the gateway does.
//
// Its polls must not write last_seen_at: that goes to the sqlmock database,
// which can't take a query from this goroutine while the test scripts the
// next expectations on its own. So the agent counts as just seen.
func (e *testEnv) onlineAgent(t *testing.T, cred, agentID string) {
	t.Helper()
	e.g.mu.Lock()
	e.g.agentLocked(agentID).lastSeenWrite = time.Now().Add(time.Hour)
	e.g.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	call := func(method, path string, body []byte) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, e.srv.URL+path, strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+cred)
		req.Header.Set(agentproto.HeaderProto, "1")
		return http.DefaultClient.Do(req)
	}
	go func() {
		defer close(done)
		version := ""
		for ctx.Err() == nil {
			resp, err := call(http.MethodGet, "/agent/v1/work?wait=2&watches="+version, nil)
			if err != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			var w agentproto.WorkResponse
			_ = json.NewDecoder(resp.Body).Decode(&w)
			_ = resp.Body.Close()
			version = w.WatchesVersion
			for _, d := range w.Deliveries {
				if resp, err := call(http.MethodPost, "/agent/v1/deliveries/"+d.ID+"/ack",
					mustJSON(agentproto.AckRequest{Status: agentproto.AckOK})); err == nil {
					_ = resp.Body.Close()
				}
			}
		}
	}()
}

// outcome is what became of the message a test published right after the
// halt, for one durable.
type outcome int

const (
	handledOnce outcome = iota // the last poll brought it; acked on delivery 1
	untouched                  // never delivered; the next session gets it on delivery 1
)

func (o outcome) String() string {
	if o == handledOnce {
		return "handled once by the halted subscriber"
	}
	return "left for the next session"
}

// settleAfterStop reads what a stopped durable did with the one message
// published right after its halt. Either the last poll brought it and it was
// acked on its first delivery, or it was never delivered at all. A NAK or a
// term, or anything on a second delivery, fails the test.
func (e *testEnv) settleAfterStop(t *testing.T, durable string, acks *ackLog) outcome {
	t.Helper()
	var ci *nats.ConsumerInfo
	waitFor(t, durable+" to settle", func() bool {
		c, err := e.js.ConsumerInfo(messaging.MainStreamName, durable)
		ci = c
		if err != nil {
			return false
		}
		// A NAK'd message stays in NumAckPending until it is redelivered;
		// the ack log says so first.
		acks.mu.Lock()
		defer acks.mu.Unlock()
		for _, ev := range acks.events {
			if ev.kind != "+WPI" {
				return true
			}
		}
		return c.NumAckPending == 0
	})
	acks.mu.Lock()
	evs := append([]ackEvent(nil), acks.events...)
	acks.mu.Unlock()
	for _, ev := range evs {
		if ev.kind == "-NAK" || ev.kind == "+TERM" || ev.delivered != 1 {
			t.Fatalf("%s: %s on delivery %d; the ending session must not NAK the last poll's message (acks seen: %s)",
				durable, ev.kind, ev.delivered, describeAcks(evs))
		}
	}
	if ci.NumAckPending == 0 && ci.NumPending == 1 && len(evs) == 0 {
		return untouched
	}
	acks.settledOnFirstDelivery(t)
	if ci, err := e.js.ConsumerInfo(messaging.MainStreamName, durable); err != nil || ci.NumRedelivered != 0 {
		t.Fatalf("%s: after the ack: %+v, %v; want NumRedelivered 0", durable, ci, err)
	}
	return handledOnce
}

// TestStopGrace_MessageAfterHaltIsHandledOnce is the #521 reviewer's repro
// (TestReview521HaltThenEndScopeSpendsAttempt) on the gateway's own stop. A
// message is published from the afterHalt seam: the subscribers are halted
// but still in their last poll, so the poll brings it. Before #576 the stop
// then ended the session at once and the handler NAK'd it (delivery 2 next
// time, every round). Now the handler runs with the session still alive, the
// online agent writes the file, and the message is acked on its first
// delivery. A message the last poll did not bring is left for the next
// session, which must also get it on delivery 1.
func TestStopGrace_MessageAfterHaltIsHandledOnce(t *testing.T) {
	memberA := memberRow(agentA, "till-a", "{all-tills}", outWrite)
	memberA2 := memberRow(agentA2, "till-a2", "{all-tills}", outWrite)
	cases := []struct {
		name     string
		agents   map[string]string // credential → agent ID
		start    func(t *testing.T, e *testEnv, connID string)
		durables func(connID string) []string
	}{
		{
			name:     "agent",
			agents:   map[string]string{credA: agentA},
			start:    func(t *testing.T, e *testEnv, connID string) { e.startOutputPipeline(t, connID) },
			durables: func(connID string) []string { return []string{outputDurable(connID)} },
		},
		{
			name:   "group",
			agents: map[string]string{credA: agentA, credA2: agentA2},
			start:  func(t *testing.T, e *testEnv, connID string) { e.startGroupPipeline(t, connID, memberA, memberA2) },
			durables: func(connID string) []string {
				return []string{memberDurable(connID, agentA), memberDurable(connID, agentA2)}
			},
		},
	}
	const rounds = 5
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv(t)
			for cred, id := range c.agents {
				e.onlineAgent(t, cred, id)
			}
			tally := map[outcome]int{}
			for round := 0; round < rounds; round++ {
				connID := fmt.Sprintf("grace-%s-%d", c.name, round)
				c.start(t, e, connID)
				logs := map[string]*ackLog{}
				for _, d := range c.durables(connID) {
					logs[d] = e.watchAcks(t, d)
				}
				published := make(chan error, 1)
				var once sync.Once
				e.g.afterHalt = func() {
					once.Do(func() { published <- publishEnv(e, connID, fmt.Sprintf(`{"round":%d}`, round)) })
				}
				e.g.stopConnection(context.Background(), connID, tenant1)
				e.g.afterHalt = nil
				if err := <-published; err != nil {
					t.Fatalf("publish: %v", err)
				}

				var left []string
				for _, d := range c.durables(connID) {
					o := e.settleAfterStop(t, d, logs[d])
					tally[o]++
					if o == untouched {
						left = append(left, d)
					}
				}
				if len(left) > 0 {
					// The next session must get it on its first delivery.
					c.start(t, e, connID)
					for _, d := range left {
						logs[d].settledOnFirstDelivery(t)
					}
					e.g.stopConnection(context.Background(), connID, tenant1)
				}
			}
			if n := e.dlqMsgs(t); n != 0 {
				t.Fatalf("%d message(s) dead-lettered", n)
			}
			t.Logf("%d rounds: %d handled once by the halted subscriber, %d left for the next session",
				rounds, tally[handledOnce], tally[untouched])
		})
	}
}

// The reconcile's own stop of a member subscriber it started for a session
// that ended meanwhile (groups.go): the same rule. The window is opened as in
// TestGroup_StopDuringReconcileReleasesTheMessage, and the message is
// published once that subscriber is halted.
func TestStopGrace_ReconcileRaceHandlesTheLastPoll(t *testing.T) {
	e := newTestEnv(t)
	e.onlineAgent(t, credA, agentA)
	e.onlineAgent(t, credA2, agentA2)
	memberA := memberRow(agentA, "till-a", "{all-tills}", outWrite)
	memberA2 := memberRow(agentA2, "till-a2", "{all-tills}", outWrite)
	e.startGroupPipeline(t, "g-grace-race", memberA)
	acksA2 := e.watchAcks(t, memberDurable("g-grace-race", agentA2))

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	free := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(free)
	e.g.afterMemberSubscribe = func(id string) {
		if id != agentA2 {
			return
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	// The first halt is the reconcile's, of A2's new subscriber: the stop
	// below waits for the reconcile before it halts anything.
	published := make(chan error, 1)
	var once sync.Once
	e.g.afterHalt = func() {
		once.Do(func() { published <- publishEnv(e, "g-grace-race", `{"order":"after-halt"}`) })
	}

	e.expectMembers(tenant1, 1, memberA, memberA2) // A2 joins
	reconciled := make(chan struct{})
	go func() { e.g.reconcileAll(context.Background()); close(reconciled) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the reconcile never started A2's subscriber")
	}
	stopped := make(chan struct{})
	go func() { e.g.stopConnection(context.Background(), "g-grace-race", tenant1); close(stopped) }()
	waitFor(t, "the stop to take the session out", func() bool {
		e.g.mu.Lock()
		defer e.g.mu.Unlock()
		return e.g.sessions["g-grace-race"] == nil
	})
	free()
	for _, w := range []struct {
		what string
		done chan struct{}
	}{{"the reconcile", reconciled}, {"the stop", stopped}} {
		select {
		case <-w.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not return", w.what)
		}
	}
	e.g.afterHalt = nil
	e.g.afterMemberSubscribe = nil
	if err := <-published; err != nil {
		t.Fatalf("publish: %v", err)
	}

	o := e.settleAfterStop(t, memberDurable("g-grace-race", agentA2), acksA2)
	t.Logf("A2's message: %v", o)
	if n := e.dlqMsgs(t); n != 0 {
		t.Fatalf("%d message(s) dead-lettered", n)
	}
}

// publishEnv is publishTo for a seam: it may run off the test goroutine, so
// it returns its error instead of failing the test.
func publishEnv(e *testEnv, connID, payload string) error {
	env := &envelope.Envelope{
		ID: uuid.NewString(), TenantID: tenant1, IntegrationID: connID,
		Payload: []byte(payload), PayloadSize: int64(len(payload)), ContentType: "application/json",
		Source: "test", CreatedAt: time.Now().UTC(), Metadata: map[string]any{"filename": "orders.json"},
	}
	body, err := envelope.Marshal(env)
	if err != nil {
		return err
	}
	return messaging.NewPublisher(e.js).Publish(context.Background(), tenant1, connID, env.ID, body)
}

// A message already in a handler when the stop comes, its file being written
// by an agent that is up: the agent finishes during the grace and the message
// is acked, not NAK'd and written a second time by the next session. Before
// #576 the stop ended the scope at once, the handler NAK'd, and the agent's
// ack came back 404. For the pipeline's own durable (a stop) and for a member
// the reconcile drops from the group.
func TestStopGrace_AgentFinishingDuringTheStopIsAcked(t *testing.T) {
	memberA := memberRow(agentA, "till-a", "{all-tills}", outWrite)
	memberA2 := memberRow(agentA2, "till-a2", "{all-tills}", outWrite)
	cases := []struct {
		name    string
		cred    string
		durable string
		start   func(t *testing.T, e *testEnv)
		stop    func(e *testEnv)
	}{
		{
			name: "pipeline stop", cred: credA, durable: outputDurable("grace-ack"),
			start: func(t *testing.T, e *testEnv) { e.startOutputPipeline(t, "grace-ack") },
			stop:  func(e *testEnv) { e.g.stopConnection(context.Background(), "grace-ack", tenant1) },
		},
		{
			name: "member leaves the group", cred: credA2, durable: memberDurable("grace-ack", agentA2),
			start: func(t *testing.T, e *testEnv) { e.startGroupPipeline(t, "grace-ack", memberA, memberA2) },
			stop: func(e *testEnv) {
				e.expectMembers(tenant1, 1, memberA,
					memberRow(agentA2, "till-a2", "{store-oslo}", outWrite)) // still live, out of the group
				e.g.reconcileAll(context.Background())
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv(t)
			c.start(t, e)
			acks := e.watchAcks(t, c.durable)
			e.publishTo(t, tenant1, "grace-ack", `{"order":1}`)
			w := e.poll(t, c.cred, 5)
			if len(w.Deliveries) != 1 {
				t.Fatalf("got %d deliveries, want 1", len(w.Deliveries))
			}

			stopped := make(chan struct{})
			go func() { c.stop(e); close(stopped) }()
			time.Sleep(e.g.stopGrace / 5) // writing the file
			resp := e.do(t, http.MethodPost, "/agent/v1/deliveries/"+w.Deliveries[0].ID+"/ack", c.cred,
				mustJSON(agentproto.AckRequest{Status: agentproto.AckOK}))
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("the stop did not return")
			}
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("the agent's ack during the stop: %d, want 204", resp.StatusCode)
			}
			acks.settledOnFirstDelivery(t)
		})
	}
}

// A handler that cannot finish (its agent is offline) holds a stop for at
// most the grace: the scope then ends, the message is NAK'd once and waits
// for the next session. All of a session's subscribers share one grace, and a
// shutdown ends its sessions side by side, so neither multiplies it. Nothing
// is left running afterwards.
func TestStopGrace_StuckHandlersBoundTheStop(t *testing.T) {
	memberA := memberRow(agentA, "till-a", "{all-tills}", outWrite)
	memberA2 := memberRow(agentA2, "till-a2", "{all-tills}", outWrite)
	type held struct{ connID, durable string }
	cases := []struct {
		name  string
		start func(t *testing.T, e *testEnv) []held
		stop  func(e *testEnv)
	}{
		{
			name: "agent",
			start: func(t *testing.T, e *testEnv) []held {
				e.startOutputPipeline(t, "stuck-1")
				return []held{{"stuck-1", outputDurable("stuck-1")}}
			},
			stop: func(e *testEnv) { e.g.stopConnection(context.Background(), "stuck-1", tenant1) },
		},
		{
			name: "mixed pipeline",
			start: func(t *testing.T, e *testEnv) []held {
				nodes := []any{outputNode("single", agentA, "out", ""), groupOutputNode("grp", "all-tills", "out", "")}
				e.expectStart("stuck-mix", tenant1, nodes, []any{}, map[string]string{agentA: outWrite}, "running")
				e.expectMembers(tenant1, 2, memberA, memberA2)
				e.g.startConnection(context.Background(), "stuck-mix", tenant1)
				if e.g.sessions["stuck-mix"] == nil {
					t.Fatal("mixed pipeline did not start")
				}
				return []held{
					{"stuck-mix", outputDurable("stuck-mix")},
					{"stuck-mix", memberDurable("stuck-mix", agentA)},
					{"stuck-mix", memberDurable("stuck-mix", agentA2)},
				}
			},
			stop: func(e *testEnv) { e.g.stopConnection(context.Background(), "stuck-mix", tenant1) },
		},
		{
			name: "shutdown",
			start: func(t *testing.T, e *testEnv) []held {
				var hs []held
				for i := 0; i < 3; i++ {
					id := fmt.Sprintf("stuck-shut-%d", i)
					e.startOutputPipeline(t, id)
					hs = append(hs, held{id, outputDurable(id)})
				}
				return hs
			},
			stop: func(e *testEnv) { _ = e.g.Stop(context.Background()) },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := dispatchLoops()
			e := newTestEnv(t)
			hs := c.start(t, e)
			logs := map[string]*ackLog{}
			for _, h := range hs {
				logs[h.durable] = e.watchAcks(t, h.durable)
			}
			seen := map[string]bool{}
			for _, h := range hs {
				if !seen[h.connID] {
					seen[h.connID] = true
					e.publishTo(t, tenant1, h.connID, `{"order":1}`)
				}
			}
			for _, h := range hs {
				waitFor(t, h.durable+" to hold its message", func() bool {
					ci, err := e.js.ConsumerInfo(messaging.MainStreamName, h.durable)
					return err == nil && ci.NumAckPending == 1
				})
			}

			began := time.Now()
			stopped := make(chan struct{})
			go func() { c.stop(e); close(stopped) }()
			limit := e.g.stopGrace + 800*time.Millisecond
			select {
			case <-stopped:
			case <-time.After(10 * time.Second):
				t.Fatal("the stop did not return: a stuck handler hangs it")
			}
			took := time.Since(began)
			t.Logf("stop took %v (grace %v)", took.Round(time.Millisecond), e.g.stopGrace)
			if took > limit {
				t.Fatalf("the stop took %v; a stuck handler may hold it for the grace (%v) and a poll, not more", took, e.g.stopGrace)
			}

			// Released once each, never dead-lettered.
			for _, h := range hs {
				logs[h.durable].waitUntil(t, "the release", func(evs []ackEvent) bool {
					naks := 0
					for _, ev := range evs {
						switch ev.kind {
						case "-NAK":
							naks++
						case "+WPI":
						default:
							t.Fatalf("%s: %s on delivery %d; want the held message released with one NAK", h.durable, ev.kind, ev.delivered)
						}
					}
					if naks > 1 {
						t.Fatalf("%s: NAK'd %d times; want once", h.durable, naks)
					}
					return naks == 1
				})
			}
			if n := e.dlqMsgs(t); n != 0 {
				t.Fatalf("%d message(s) dead-lettered", n)
			}
			if !pollUntil(func() bool {
				return goroutinesWith(".(*gateway).stopSubscribers") == 0 && dispatchLoops() == base
			}) {
				t.Fatalf("left running after the stop: %d stop goroutines, %d dispatch loops (baseline %d)",
					goroutinesWith(".(*gateway).stopSubscribers"), dispatchLoops(), base)
			}
		})
	}
}
