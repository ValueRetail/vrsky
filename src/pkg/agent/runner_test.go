package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
)

func newTestRunner(t *testing.T, g *fakeGateway) (*Runner, *Config) {
	t.Helper()
	c := testConfig(t)
	c.PollIntervalSeconds = 1
	cred := &Credential{AgentID: "a1", Name: "till", ServerURL: g.srv.URL, Credential: g.credential}
	if err := SaveCredential(c.DataDir, cred); err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(c, cred, quiet)
	if err != nil {
		t.Fatal(err)
	}
	r.sleep = func(ctx context.Context, d time.Duration) bool { // no real backoff in tests
		return sleepCtx(ctx, min(d, 20*time.Millisecond))
	}
	return r, c
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The whole loop against a gateway: announce, pick up a watch and upload a
// file from it, write a delivery (inline and streamed) and acknowledge it.
func TestRunner_EndToEndAgainstGateway(t *testing.T) {
	g := newFakeGateway(t)
	r, c := newTestRunner(t, g)
	in, out := c.Directories["inbox"].Path, c.Directories["outbox"].Path
	f := filepath.Join(in, "sale-1.xml")
	_ = os.WriteFile(f, []byte("<sale/>"), 0o644)
	makeOld(t, f)

	g.set(func(g *fakeGateway) {
		g.watches = []agentproto.Watch{{Op: agentproto.OpWatchDir, ConnectionID: "conn-in", Directory: "inbox", After: agentproto.AfterMove}}
		g.deliveries = []agentproto.Delivery{
			{Op: agentproto.OpWriteFile, ID: "d-inline", Directory: "outbox", Filename: "small.json",
				Checksum: sum(`{"x":1}`), InlineBase64: "eyJ4IjoxfQ=="},
			{Op: agentproto.OpWriteFile, ID: "d-stream", Directory: "outbox", Filename: "big.csv",
				Checksum: sum("a,b\n1,2\n"), BodyURL: "/agent/v1/deliveries/d-stream/body"},
		}
		g.bodies["d-stream"] = "a,b\n1,2\n"
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	eventually(t, "both deliveries acknowledged", func() bool {
		_, acks, _ := g.snapshot()
		return len(acks) == 2
	})
	_, acks, announces := g.snapshot()
	for id, a := range acks {
		if a.Status != agentproto.AckOK {
			t.Errorf("delivery %s acked %+v", id, a)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(out, "small.json")); string(b) != `{"x":1}` {
		t.Errorf("small.json = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "big.csv")); string(b) != "a,b\n1,2\n" {
		t.Errorf("big.csv = %q", b)
	}
	if announces < 1 {
		t.Error("the agent never announced itself")
	}
	eventually(t, "the watched file uploaded and moved", func() bool {
		up, _, _ := g.snapshot()
		_, err := os.Stat(filepath.Join(in, "processed", "sale-1.xml"))
		return len(up) == 1 && err == nil
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

// A write that fails is reported as failed, so VRSky retries it — never
// acknowledged as done.
func TestRunner_FailedWriteIsReportedFailed(t *testing.T) {
	g := newFakeGateway(t)
	r, _ := newTestRunner(t, g)
	g.set(func(g *fakeGateway) {
		g.deliveries = []agentproto.Delivery{{Op: agentproto.OpWriteFile, ID: "d-bad", Directory: "outbox",
			Filename: "x.json", Checksum: sum("expected"), InlineBase64: "Y29ycnVwdA=="}} // "corrupt"
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Run(ctx) }()
	eventually(t, "the ack", func() bool { _, acks, _ := g.snapshot(); return len(acks) == 1 })
	_, acks, _ := g.snapshot()
	if acks["d-bad"].Status != agentproto.AckFailed || acks["d-bad"].Error == "" {
		t.Fatalf("ack = %+v, want failed with a reason", acks["d-bad"])
	}
}

// VRSky unreachable: the agent keeps trying and recovers on its own,
// announcing again when it gets back.
func TestRunner_SurvivesOutagesAndReannounces(t *testing.T) {
	g := newFakeGateway(t)
	r, _ := newTestRunner(t, g)
	g.set(func(g *fakeGateway) { g.failNext = 6 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	eventually(t, "the agent to get through after the outage", func() bool {
		_, _, announces := g.snapshot()
		return announces >= 1
	})
	select {
	case err := <-done:
		t.Fatalf("Run gave up during an outage: %v", err)
	default:
	}
}

// Revoked in VRSky: stop, remember it, and refuse to start again.
func TestRunner_RevokedStopsAndPersists(t *testing.T) {
	g := newFakeGateway(t)
	r, c := newTestRunner(t, g)
	g.set(func(g *fakeGateway) { g.revoked = true })

	err := r.Run(context.Background())
	if err != ErrRevoked {
		t.Fatalf("Run = %v, want ErrRevoked", err)
	}
	cred, lerr := LoadCredential(c.DataDir)
	if lerr != nil || !cred.Revoked {
		t.Fatalf("revocation not persisted: %+v, %v", cred, lerr)
	}
	if _, err := NewRunner(c, cred, quiet); err != ErrRevoked {
		t.Fatalf("a revoked agent started again: %v", err)
	}
}

// A watch or delivery of a kind this agent does not know is skipped, not
// misread — the protocol's forward-compatibility promise.
func TestRunner_IgnoresUnknownOperations(t *testing.T) {
	g := newFakeGateway(t)
	r, c := newTestRunner(t, g)
	g.set(func(g *fakeGateway) {
		g.deliveries = []agentproto.Delivery{{Op: "run_sql", ID: "d-x", Directory: "outbox", Filename: "x", InlineBase64: "eA=="}}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)
	if es, _ := os.ReadDir(c.Directories["outbox"].Path); len(es) != 0 {
		t.Fatalf("an unknown operation wrote %d files", len(es))
	}
	if _, acks, _ := g.snapshot(); len(acks) != 0 {
		t.Fatalf("an unknown operation was acknowledged: %+v", acks)
	}
}

// A delivery's BodyURL is followed with the agent's credential attached, so
// only the gateway's own delivery paths may be followed. Asserted on the
// outcome: the server must never receive the request. (Checking only that an
// error came back would pass on a DNS failure just as well.)
func TestClient_RefusesForeignBodyURL(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL, "cred")
	for _, u := range []string{"/api/v1/secrets", "/agent/v1/deliveries/../../api/v1/secrets", "/agent/v1/work", srv.URL + "/agent/v1/deliveries/x/body"} {
		if _, err := c.Body(context.Background(), u); err == nil {
			t.Errorf("followed body URL %q", u)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 0 {
		t.Fatalf("the credential was sent to %v", hits)
	}
}
