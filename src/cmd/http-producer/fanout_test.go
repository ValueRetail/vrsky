package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A message for several HTTP nodes, one of which keeps failing (#730). Each
// attempt used to send it to every node, so a healthy sibling got it five
// times and the DLQ copy named only the first failure. Now a node that took
// the message is skipped on redelivery and the DLQ copy names the failing
// node.

// fastBackoff gives the five attempts before the DLQ in well under a second.
var fastBackoff = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond}

// counter is an endpoint that counts its requests and answers each one with
// status.
type counter struct {
	srv    *httptest.Server
	hits   atomic.Int32
	status atomic.Int32
}

func newCounter(t *testing.T, status int) *counter {
	t.Helper()
	c := &counter{}
	c.status.Store(int32(status))
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits.Add(1)
		w.WriteHeader(int(c.status.Load()))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// mockFanOutRows serves a connection with one HTTP producer node per
// {node id, url} pair.
func mockFanOutRows(mock sqlmock.Sqlmock, connID string, nodes [][2]string) {
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		parts = append(parts, fmt.Sprintf(`{"id":%q,"type":"producer","config":{"type":"http","http":{"url":%q,"method":"POST"}}}`, n[0], n[1]))
	}
	rows := "[" + strings.Join(parts, ",") + "]"
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 5; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(rows), []byte(`[]`)))
	}
}

// dlqErrors collects X-Vrsky-Last-Error of every dead-lettered message.
func dlqErrors(t *testing.T, nc *nats.Conn) chan string {
	t.Helper()
	ch := make(chan string, 4)
	sub, err := nc.Subscribe("vrsky.dlq.>", func(m *nats.Msg) {
		select {
		case ch <- m.Header.Get("X-Vrsky-Last-Error"):
		default:
		}
	})
	if err != nil {
		t.Fatalf("subscribe dlq: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return ch
}

func fanOutEnv(connID string) *envelope.Envelope {
	env := envelope.New()
	env.ID = uuid.NewString()
	env.IntegrationID = connID
	env.TenantID = "tenant-1"
	env.ContentType = "application/json"
	env.Payload = []byte(`{"order":1}`)
	return env
}

func TestFanOut_FailingNodeDoesNotResendToHealthySibling(t *testing.T) {
	cases := []struct {
		name string
		// failing returns the failing node's URL and a check of how often it
		// was tried (nil when nothing is sent, as for a refused URL).
		failing func(t *testing.T) (url string, tried func() int32)
		want    string // in X-Vrsky-Last-Error
	}{
		{
			name: "refused address",
			failing: func(*testing.T) (string, func() int32) {
				// Refused before anything is sent, with or without the dev switch.
				return "ftp://partner.example/in", nil
			},
			want: "node bad: destination refused",
		},
		{
			name: "5xx",
			failing: func(t *testing.T) (string, func() int32) {
				c := newCounter(t, http.StatusServiceUnavailable)
				return c.srv.URL, c.hits.Load
			},
			want: "node bad: upstream 503",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowLocalTarget(t)
			good := newCounter(t, http.StatusOK)
			badURL, badTried := tc.failing(t)

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			const connID = "conn-fanout"
			mockFanOutRows(mock, connID, [][2]string{{"good", good.srv.URL}, {"bad", badURL}})

			p := &httpProducer{}
			h := harness.NewProducerHarness(t, p, harness.Options{Name: "http-producer", DB: db, Backoff: fastBackoff})
			lastErr := dlqErrors(t, h.NATS())

			env := fanOutEnv(connID)
			h.Publish(t, env)

			got := h.ExpectDLQ(t, 15*time.Second)
			if got.ID != env.ID {
				t.Fatalf("DLQ holds %s, want %s", got.ID, env.ID)
			}
			select {
			case e := <-lastErr:
				if !strings.Contains(e, tc.want) {
					t.Errorf("X-Vrsky-Last-Error = %q, want it to contain %q", e, tc.want)
				}
				if strings.Contains(e, "node good") {
					t.Errorf("X-Vrsky-Last-Error = %q names the node that took the message", e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no X-Vrsky-Last-Error on the DLQ copy")
			}
			if n := good.hits.Load(); n != 1 {
				t.Errorf("healthy node received %d requests over five attempts, want 1", n)
			}
			if badTried != nil {
				if n := badTried(); n != 5 {
					t.Errorf("failing node tried %d times, want 5", n)
				}
			}
			// The DLQ keeps the healthy node's entry so a retry from the DLQ
			// skips it too.
			if n := p.sentSet().size(); n != 1 {
				t.Errorf("skip set holds %d entries after the DLQ, want 1", n)
			}
		})
	}
}

func TestFanOut_BothHealthyGetOneEach(t *testing.T) {
	allowLocalTarget(t)
	a := newCounter(t, http.StatusOK)
	b := newCounter(t, http.StatusOK)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const connID = "conn-fanout-ok"
	mockFanOutRows(mock, connID, [][2]string{{"a", a.srv.URL}, {"b", b.srv.URL}})

	p := &httpProducer{}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "http-producer", DB: db, Backoff: fastBackoff})
	h.Publish(t, fanOutEnv(connID))

	harness.Eventually(t, 5*time.Second, "both nodes received the message", func() bool {
		return a.hits.Load() >= 1 && b.hits.Load() >= 1
	})
	h.ExpectNoDLQ(t, time.Second)
	if na, nb := a.hits.Load(), b.hits.Load(); na != 1 || nb != 1 {
		t.Errorf("requests: a=%d b=%d, want 1 each", na, nb)
	}
	if n := p.sentSet().size(); n != 0 {
		t.Errorf("skip set holds %d entries after a clean delivery, want 0", n)
	}
}

// A retry from the DLQ republishes the same envelope. Handled by the same
// process, it reaches only the node that failed, and once that node takes it
// the skip set is empty again.
func TestFanOut_RetryAfterFixSendsOnlyToTheFailedNode(t *testing.T) {
	good := newCounter(t, http.StatusOK)
	bad := newCounter(t, http.StatusBadGateway)
	p := newStreamingProducer(t, []*HTTPConfig{
		{NodeID: "good", URL: good.srv.URL, Method: "POST"},
		{NodeID: "bad", URL: bad.srv.URL, Method: "POST"},
	})
	env := testEnv()
	for i := 0; i < 5; i++ {
		err := p.Deliver(context.Background(), env)
		if err == nil || !strings.Contains(err.Error(), "node bad: upstream 502") {
			t.Fatalf("attempt %d: err = %v, want the bad node's 502", i+1, err)
		}
	}
	bad.status.Store(http.StatusOK)
	if err := p.Deliver(context.Background(), env); err != nil {
		t.Fatalf("retry after the fix: %v", err)
	}
	if n := good.hits.Load(); n != 1 {
		t.Errorf("healthy node received %d requests, want 1", n)
	}
	if n := bad.hits.Load(); n != 6 {
		t.Errorf("fixed node received %d requests, want 6", n)
	}
	if n := p.sentSet().size(); n != 0 {
		t.Errorf("skip set holds %d entries after the message is done, want 0", n)
	}
	// Done means done: a later, unrelated delivery of the same envelope id
	// (nothing in the set) goes to both nodes again.
	if err := p.Deliver(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if n := good.hits.Load(); n != 2 {
		t.Errorf("healthy node received %d requests after the set was cleared, want 2", n)
	}
}

// The skip is per message, per node and per target: another message, another
// node, or the same node pointed somewhere else between attempts all get sent.
func TestFanOut_SkipKey(t *testing.T) {
	good := newCounter(t, http.StatusOK)
	moved := newCounter(t, http.StatusOK)
	bad := newCounter(t, http.StatusInternalServerError)
	cfgGood := &HTTPConfig{NodeID: "good", URL: good.srv.URL, Method: "POST"}
	p := newStreamingProducer(t, []*HTTPConfig{cfgGood, {NodeID: "bad", URL: bad.srv.URL, Method: "POST"}})

	first := testEnv()
	if err := p.Deliver(context.Background(), first); err == nil {
		t.Fatal("want the bad node's error")
	}

	// Another message: sent to the healthy node.
	second := testEnv()
	second.ID = "env-other"
	if err := p.Deliver(context.Background(), second); err == nil {
		t.Fatal("want the bad node's error")
	}
	if n := good.hits.Load(); n != 2 {
		t.Fatalf("healthy node received %d requests for two messages, want 2", n)
	}

	// Another connection with the same node id and envelope id.
	p.configCache["conn-2"] = []*HTTPConfig{{NodeID: "good", URL: good.srv.URL, Method: "POST"}, {NodeID: "bad", URL: bad.srv.URL, Method: "POST"}}
	p.configCacheTime["conn-2"] = time.Now()
	other := testEnv()
	other.IntegrationID = "conn-2"
	if err := p.Deliver(context.Background(), other); err == nil {
		t.Fatal("want the bad node's error")
	}
	if n := good.hits.Load(); n != 3 {
		t.Fatalf("healthy node received %d requests across connections, want 3", n)
	}

	// The healthy node edited to point at a new URL: it is a new target.
	cfgGood.URL = moved.srv.URL
	if err := p.Deliver(context.Background(), first); err == nil {
		t.Fatal("want the bad node's error")
	}
	if n := moved.hits.Load(); n != 1 {
		t.Errorf("moved node received %d requests, want 1", n)
	}

	// A second node with the same URL is its own target.
	p.configCache["conn-1"] = append(p.configCache["conn-1"], &HTTPConfig{NodeID: "twin", URL: moved.srv.URL, Method: "POST"})
	if err := p.Deliver(context.Background(), first); err == nil {
		t.Fatal("want the bad node's error")
	}
	if n := moved.hits.Load(); n != 2 {
		t.Errorf("same URL on another node: %d requests, want 2", n)
	}

	// Same node, method changed: a new target.
	cfgGood.Method = "PUT"
	if err := p.Deliver(context.Background(), first); err == nil {
		t.Fatal("want the bad node's error")
	}
	if n := moved.hits.Load(); n != 3 {
		t.Errorf("method changed: %d requests, want 3", n)
	}
}

// A 4xx is final for that node (acked by design), so it is not sent again
// when the message comes back for a failed sibling.
func TestFanOut_4xxNodeIsNotRetried(t *testing.T) {
	rejecting := newCounter(t, http.StatusBadRequest)
	bad := newCounter(t, http.StatusServiceUnavailable)
	p := newStreamingProducer(t, []*HTTPConfig{
		{NodeID: "rejecting", URL: rejecting.srv.URL, Method: "POST"},
		{NodeID: "bad", URL: bad.srv.URL, Method: "POST"},
	})
	env := testEnv()
	for i := 0; i < 3; i++ {
		if err := p.Deliver(context.Background(), env); err == nil {
			t.Fatal("want the bad node's error")
		}
	}
	if n := rejecting.hits.Load(); n != 1 {
		t.Errorf("4xx node received %d requests, want 1", n)
	}
}

// Every failing node is named, not only the first.
func TestFanOut_ErrorNamesEveryFailingNode(t *testing.T) {
	a := newCounter(t, http.StatusInternalServerError)
	b := newCounter(t, http.StatusTooManyRequests)
	p := newStreamingProducer(t, []*HTTPConfig{
		{NodeID: "a", URL: a.srv.URL, Method: "POST"},
		{NodeID: "b", URL: b.srv.URL, Method: "POST"},
	})
	err := p.Deliver(context.Background(), testEnv())
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"node a: upstream 500", "node b: upstream 429"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if n := p.sentSet().size(); n != 0 {
		t.Errorf("no node took the message, but the skip set holds %d entries", n)
	}
}

// dropping is an endpoint that counts each request, then closes the
// connection without answering: the request went out, the node never said it
// took it.
func dropping(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer cannot be hijacked")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// assertOnlyTaken checks that the skip set holds exactly the given nodes'
// keys for env.
func assertOnlyTaken(t *testing.T, p *httpProducer, env *envelope.Envelope, taken ...*HTTPConfig) {
	t.Helper()
	s := p.sentSet()
	if n := s.size(); n != len(taken) {
		t.Errorf("skip set holds %d entries, want %d", n, len(taken))
	}
	for _, c := range taken {
		k := sentKey{connID: env.IntegrationID, envelopeID: env.ID, nodeID: c.NodeID, target: targetHash(c.Method, c.URL)}
		if !s.has(k, time.Now()) {
			t.Errorf("skip set does not hold node %s", c.NodeID)
		}
	}
}

// A node whose request went out but which never answered (the connection was
// closed, a transport error) has not taken the message: it is tried again on
// every attempt and never lands in the skip set.
func TestFanOut_NodeThatFailedMidRequestIsRetried(t *testing.T) {
	good := newCounter(t, http.StatusOK)
	drop, dropHits := dropping(t)
	cfgGood := &HTTPConfig{NodeID: "good", URL: good.srv.URL, Method: "POST"}
	cfgDrop := &HTTPConfig{NodeID: "drop", URL: drop.URL, Method: "POST"}
	p := newStreamingProducer(t, []*HTTPConfig{cfgGood, cfgDrop})
	env := testEnv()
	for i := 0; i < 3; i++ {
		err := p.Deliver(context.Background(), env)
		if err == nil || !strings.Contains(err.Error(), "node drop: transport:") {
			t.Fatalf("attempt %d: err = %v, want the drop node's transport error", i+1, err)
		}
	}
	if n := good.hits.Load(); n != 1 {
		t.Errorf("healthy node received %d requests, want 1", n)
	}
	if n := dropHits.Load(); n != 3 {
		t.Errorf("dropping node received %d requests, want 3", n)
	}
	assertOnlyTaken(t, p, env, cfgGood)
}

// The same when the delivery is cancelled while the failing node's request is
// in flight (shutdown, a deadline): the node that answered 2xx is remembered,
// the one that was cut off is not.
func TestFanOut_CancelledMidRequestIsRetried(t *testing.T) {
	good := newCounter(t, http.StatusOK)
	var slowHits atomic.Int32
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowHits.Add(1)
		// Never answers; the client gives up. The server only notices the
		// client leaving once the body is read, so read it first, and let
		// go at the end of the test so Close does not wait on this handler.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) }) // runs before slow.Close
	cfgGood := &HTTPConfig{NodeID: "good", URL: good.srv.URL, Method: "POST"}
	cfgSlow := &HTTPConfig{NodeID: "slow", URL: slow.URL, Method: "POST"}
	p := newStreamingProducer(t, []*HTTPConfig{cfgGood, cfgSlow})
	env := testEnv()
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := p.Deliver(ctx, env)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "node slow: transport:") {
			t.Fatalf("attempt %d: err = %v, want the slow node's transport error", i+1, err)
		}
	}
	if n := good.hits.Load(); n != 1 {
		t.Errorf("healthy node received %d requests, want 1", n)
	}
	if n := slowHits.Load(); n != 2 {
		t.Errorf("cut-off node received %d requests, want 2", n)
	}
	assertOnlyTaken(t, p, env, cfgGood)
}
