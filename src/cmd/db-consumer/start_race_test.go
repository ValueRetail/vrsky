package main

import (
	"context"
	"database/sql"
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
// start a second poller, a stop that lands mid-start wins (and the source
// database it opened is closed), and a failed start leaves nothing behind. The
// first start is held in its slow part through the SDK guard's AfterReserve
// hook.

const (
	srConn   = "conn-db-race"
	srTenant = "tenant-race"
	srWait   = 5 * time.Second
	srNodes  = `[{"id":"c1","type":"consumer","config":{"type":"database","database":{"host":"localhost","table":"widgets","poll_interval_seconds":3600}}}]`
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
	c         *dbConsumer
	mock      sqlmock.Sqlmock // management database
	src       sqlmock.Sqlmock // source database
	opens     atomic.Int32    // source databases opened: one per start that got past the lookup
	published atomic.Int32
}

func newSRFixture(t *testing.T) *srFixture {
	t.Helper()
	f := &srFixture{}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srcDB, src, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock src: %v", err)
	}
	t.Cleanup(func() { _ = srcDB.Close() })
	f.mock, f.src = mock, src

	f.c = &dbConsumer{
		db:                db,
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		activeConnections: make(map[string]*ActiveDBConnection),
		eventSubs:         make(map[string][]chan DBEvent),
		openSource: func(string) (*sql.DB, error) {
			f.opens.Add(1)
			return srcDB, nil
		},
		publish: func(context.Context, *envelope.Envelope) error {
			f.published.Add(1)
			return nil
		},
	}
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
		AddRow(srConn, srTenant, "DB Conn", []byte(srNodes), []byte(`[]`)))
}

func (f *srFixture) expectRunning() {
	f.mock.ExpectExec("UPDATE connections SET status").WithArgs("running", srConn, srTenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func (f *srFixture) activeCount() int {
	f.c.mu.RLock()
	defer f.c.mu.RUnlock()
	return len(f.c.activeConnections)
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
	f.src.ExpectQuery("SELECT \\* FROM widgets").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "alpha"))
	h := srHoldFirst(&f.c.starts)

	first := f.goStart()
	srAwait(t, h.reached, "first start to claim the connection")

	srAwait(t, f.goStart(), "second start to return while the first is in flight")
	if n := f.activeCount(); n != 0 {
		t.Errorf("active connections while the first start is held = %d, want 0", n)
	}

	close(h.release)
	srAwait(t, first, "first start")

	if n := h.claims.Load(); n != 1 {
		t.Errorf("starts that claimed the connection = %d, want 1", n)
	}
	if n := f.activeCount(); n != 1 {
		t.Errorf("active connections = %d, want 1", n)
	}
	if n := f.opens.Load(); n != 1 {
		t.Errorf("source databases opened = %d, want 1", n)
	}
	harness.Eventually(t, srWait, "the poller to query and publish", func() bool { return f.published.Load() >= 1 })
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("management db: %v", err)
	}
	if err := f.src.ExpectationsWereMet(); err != nil {
		t.Errorf("source db: %v", err)
	}
	if n := f.published.Load(); n != 1 {
		t.Errorf("published = %d, want 1 (one poller)", n)
	}
}

func TestStartRace_StopMidStartWins(t *testing.T) {
	f := newSRFixture(t)
	f.expectLookup(nil)
	f.expectRunning() // must stay unmet: a start a stop overtook does not write "running"
	f.src.ExpectClose()
	h := srHoldFirst(&f.c.starts)

	first := f.goStart()
	srAwait(t, h.reached, "start to claim the connection")
	f.c.handleStopCommand(srStopMsg())
	close(h.release)
	srAwait(t, first, "start")

	if n := f.activeCount(); n != 0 {
		t.Errorf("active connections after a stop mid-start = %d, want 0", n)
	}
	if f.mock.ExpectationsWereMet() == nil {
		t.Error(`status "running" was written for a start a stop overtook`)
	}
	if err := f.src.ExpectationsWereMet(); err != nil {
		t.Errorf("the abandoned start did not close its source database: %v", err)
	}
	if n := f.published.Load(); n != 0 {
		t.Errorf("published after a stop mid-start = %d, want 0", n)
	}
}

func TestStartRace_FailedStartLeavesNoClaim(t *testing.T) {
	f := newSRFixture(t)
	f.expectLookup(errors.New("db down"))
	f.expectLookup(nil)
	f.expectRunning()
	f.src.ExpectQuery("SELECT \\* FROM widgets").WillReturnRows(sqlmock.NewRows([]string{"id"}))

	f.c.startConnection(context.Background(), srConn, srTenant)
	if n := f.activeCount(); n != 0 {
		t.Fatalf("active connections after a failed start = %d, want 0", n)
	}
	f.c.startConnection(context.Background(), srConn, srTenant)
	if n := f.activeCount(); n != 1 {
		t.Fatalf("active connections after a retry = %d, want 1", n)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("management db: %v", err)
	}
}
