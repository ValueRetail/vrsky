package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #397: the boot restore and a start command (or two commands) call
// startConnection concurrently. A second start while one is in flight must not
// start a second poller, a stop that lands mid-start wins, and a failed start
// leaves nothing behind. The first start is held in its slow part through the
// SDK guard's AfterReserve hook.

const (
	srConn   = "conn-api-race"
	srTenant = "tenant-race"
	srWait   = 5 * time.Second
)

// srHold parks the first start that takes a claim until release is closed.
// Later claims pass through; claims counts them all.
type srHold struct {
	claims  atomic.Int32
	reached chan struct{}
	release chan struct{}
}

func srHoldFirst(g *sdk.StartGuard) *srHold {
	h := &srHold{reached: make(chan struct{}), release: make(chan struct{})}
	g.AfterReserve = func(string) {
		if h.claims.Add(1) == 1 {
			close(h.reached)
			<-h.release
		}
	}
	return h
}

func srAwait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(srWait):
		t.Fatalf("timed out waiting for %s", what)
	}
}

type srFixture struct {
	c         *apiConsumer
	mock      sqlmock.Sqlmock
	nodes     string
	hits      atomic.Int32 // requests the fake API received: one per poll
	published atomic.Int32
}

func newSRFixture(t *testing.T) *srFixture {
	t.Helper()
	f := &srFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f.mock = mock

	f.c = &apiConsumer{
		db:              db,
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		activePipelines: make(map[string]*activeConn),
		publish: func(context.Context, *envelope.Envelope) error {
			f.published.Add(1)
			return nil
		},
	}
	// Continuous polling with a long interval: a poller polls once at once,
	// then waits, so the number of requests is the number of pollers.
	nodes := fmt.Sprintf(`[{"id":"c1","type":"consumer","config":{"type":"api","api":{
		"base_url":%q,"poll_interval_seconds":3600,"endpoints":[{"path":"/items"}]}}}]`, srv.URL)
	f.nodes = nodes
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = f.c.Stop(ctx)
	})
	return f
}

func (f *srFixture) expectLookup(err error) {
	q := f.mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").WithArgs(srConn, srTenant)
	if err != nil {
		q.WillReturnError(err)
		return
	}
	q.WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
		AddRow(srConn, srTenant, "API Conn", []byte(f.nodes), []byte(`[]`)))
}

func (f *srFixture) expectRunning() {
	f.mock.ExpectExec("UPDATE connections SET status").WithArgs("running", srConn, srTenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func (f *srFixture) activeCount() int {
	f.c.mu.RLock()
	defer f.c.mu.RUnlock()
	return len(f.c.activePipelines)
}

func (f *srFixture) goStart() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.c.startConnection(context.Background(), srConn, srTenant)
	}()
	return done
}

func srStopMsg() *nats.Msg {
	data, _ := json.Marshal(map[string]string{"connection_id": srConn, "tenant_id": srTenant})
	return &nats.Msg{Subject: "vrsky.commands." + srTenant + ".connection.stop", Data: data}
}

func TestStartRace_SecondStartWhileInFlight(t *testing.T) {
	f := newSRFixture(t)
	f.expectLookup(nil)
	f.expectRunning()
	h := srHoldFirst(&f.c.starts)

	first := f.goStart()
	srAwait(t, h.reached, "first start to claim the connection")

	srAwait(t, f.goStart(), "second start to return while the first is in flight")
	if n := f.activeCount(); n != 0 {
		t.Errorf("active pipelines while the first start is held = %d, want 0", n)
	}

	close(h.release)
	srAwait(t, first, "first start")

	if n := h.claims.Load(); n != 1 {
		t.Errorf("starts that claimed the connection = %d, want 1", n)
	}
	if n := f.activeCount(); n != 1 {
		t.Errorf("active pipelines = %d, want 1", n)
	}
	harness.Eventually(t, srWait, "the poller to poll and publish", func() bool {
		return f.hits.Load() >= 1 && f.published.Load() >= 1
	})
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
	if n := f.hits.Load(); n != 1 {
		t.Errorf("polls = %d, want 1 (one poller)", n)
	}
}

func TestStartRace_StopMidStartWins(t *testing.T) {
	f := newSRFixture(t)
	f.expectLookup(nil)
	f.expectRunning() // must stay unmet: a start a stop overtook does not write "running"
	h := srHoldFirst(&f.c.starts)

	first := f.goStart()
	srAwait(t, h.reached, "start to claim the connection")
	f.c.handleStopCommand(srStopMsg())
	close(h.release)
	srAwait(t, first, "start")

	if n := f.activeCount(); n != 0 {
		t.Errorf("active pipelines after a stop mid-start = %d, want 0", n)
	}
	if f.mock.ExpectationsWereMet() == nil {
		t.Error(`status "running" was written for a start a stop overtook`)
	}
	if n := f.hits.Load(); n != 0 {
		t.Errorf("polls after a stop mid-start = %d, want 0", n)
	}
}

func TestStartRace_FailedStartLeavesNoClaim(t *testing.T) {
	f := newSRFixture(t)
	f.expectLookup(errors.New("db down"))
	f.expectLookup(nil)
	f.expectRunning()

	f.c.startConnection(context.Background(), srConn, srTenant)
	if n := f.activeCount(); n != 0 {
		t.Fatalf("active pipelines after a failed start = %d, want 0", n)
	}
	f.c.startConnection(context.Background(), srConn, srTenant)
	if n := f.activeCount(); n != 1 {
		t.Fatalf("active pipelines after a retry = %d, want 1", n)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}
