package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
// start a second bridge, a stop that lands mid-start wins, and a failed start
// leaves nothing behind. The first start is held in its slow part through the
// SDK guard's AfterReserve hook.
//
// A bridge runs against an embedded JetStream. With no cached source payload it
// sends one start command to the source pipeline, so the number of those
// commands is the number of bridges.

const (
	srConn         = "conn-tenant-race"
	srTenant       = "tenant-race"
	srSourceTenant = "tenant-source"
	srSourceConn   = "conn-source"
	srWait         = 5 * time.Second
	srNodes        = `[{"id":"c1","type":"consumer","config":{"type":"tenant","tenant":{"source_tenant_id":"tenant-source","source_connection_id":"conn-source"}}}]`
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
	c        *tenantConsumer
	mock     sqlmock.Sqlmock
	triggers atomic.Int32 // start commands the bridges sent to the source pipeline
}

func newSRFixture(t *testing.T) *srFixture {
	t.Helper()
	f := &srFixture{}
	nc, _, cleanup := harness.StartEmbeddedJetStream(t)
	t.Cleanup(cleanup)
	sub, err := nc.Subscribe("vrsky.commands."+srSourceTenant+".connection.start", func(*nats.Msg) { f.triggers.Add(1) })
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f.mock = mock
	f.c = &tenantConsumer{
		db:            db,
		nc:            nc,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		activeBridges: make(map[string]context.CancelFunc),
		publish:       func(context.Context, *envelope.Envelope) error { return nil },
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = f.c.Stop(ctx)
	})
	return f
}

func (f *srFixture) expectLookup(err error) {
	q := f.mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges").WithArgs(srConn, srTenant)
	if err != nil {
		q.WillReturnError(err)
		return
	}
	q.WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
		AddRow(srConn, srTenant, "Tenant Conn", []byte(srNodes), []byte(`[]`)))
}

func (f *srFixture) expectRunning() {
	f.mock.ExpectExec("UPDATE connections SET status").WithArgs("running", srConn, srTenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectBridge is what a bridge reads first: the approved data connection.
func (f *srFixture) expectBridge() {
	f.mock.ExpectQuery("FROM tenant_data_connections").WithArgs(srTenant, srSourceTenant).
		WillReturnRows(sqlmock.NewRows([]string{"allowed_fields", "denied_fields", "shared_connection_ids"}).
			AddRow(nil, nil, nil))
}

func (f *srFixture) activeCount() int {
	f.c.mu.RLock()
	defer f.c.mu.RUnlock()
	return len(f.c.activeBridges)
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
	f.expectBridge()
	h := srHoldFirst(&f.c.starts)

	first := f.goStart()
	srAwait(t, h.reached, "first start to claim the connection")

	srAwait(t, f.goStart(), "second start to return while the first is in flight")
	if n := f.activeCount(); n != 0 {
		t.Errorf("active bridges while the first start is held = %d, want 0", n)
	}

	close(h.release)
	srAwait(t, first, "first start")

	if n := h.claims.Load(); n != 1 {
		t.Errorf("starts that claimed the connection = %d, want 1", n)
	}
	harness.Eventually(t, srWait, "the bridge to trigger its source", func() bool { return f.triggers.Load() >= 1 })
	if n := f.activeCount(); n != 1 {
		t.Errorf("active bridges = %d, want 1", n)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
	if n := f.triggers.Load(); n != 1 {
		t.Errorf("source triggers = %d, want 1 (one bridge)", n)
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
		t.Errorf("active bridges after a stop mid-start = %d, want 0", n)
	}
	if f.mock.ExpectationsWereMet() == nil {
		t.Error(`status "running" was written for a start a stop overtook`)
	}
}

func TestStartRace_FailedStartLeavesNoClaim(t *testing.T) {
	f := newSRFixture(t)
	f.expectLookup(errors.New("db down"))
	f.expectLookup(nil)
	f.expectRunning()
	f.expectBridge()

	f.c.startConnection(context.Background(), srConn, srTenant)
	if n := f.activeCount(); n != 0 {
		t.Fatalf("active bridges after a failed start = %d, want 0", n)
	}
	f.c.startConnection(context.Background(), srConn, srTenant)
	harness.Eventually(t, srWait, "the bridge to trigger its source", func() bool { return f.triggers.Load() >= 1 })
	if n := f.activeCount(); n != 1 {
		t.Fatalf("active bridges after a retry = %d, want 1", n)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}
