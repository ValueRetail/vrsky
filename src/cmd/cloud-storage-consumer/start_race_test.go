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
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #397: the boot restore and a start command (or two commands) call
// startConnection concurrently. A second start while one is in flight must not
// start a second poller, a stop that lands mid-start wins, and a failed start
// leaves nothing behind. The first start is held in its slow part through the
// SDK guard's AfterReserve hook; object stores opened are counted, one per poller
// (the poll interval is an hour and the bucket is empty, so a poller opens its store once and then waits).

const (
	srConn   = "conn-cloud-race"
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
	c             *cloudConsumer
	mock          sqlmock.Sqlmock
	nodes         string
	writesRunning bool         // whether a start writes status "running"
	polls         atomic.Int32 // object stores opened
}

func newSRFixture(t *testing.T) *srFixture {
	t.Helper()
	f := &srFixture{writesRunning: true}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f.mock = mock
	f.nodes = `[{"id":"c1","type":"consumer","config":{"type":"cloud_storage","cloud_storage":{"provider":"s3","bucket":"b","prefix":"in/","after_action":"delete","poll_interval_seconds":3600}}}]`
	f.c = &cloudConsumer{
		db:      db,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		active:  make(map[string]*activeConn),
		publish: func(context.Context, *envelope.Envelope) error { return nil },
		newStore: func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) {
			f.polls.Add(1)
			return &fakeStore{}, nil
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
	q := f.mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(srConn, srTenant)
	if err != nil {
		q.WillReturnError(err)
		return
	}
	q.WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(f.nodes)))
}

func (f *srFixture) expectRunning() {
	if f.writesRunning {
		f.mock.ExpectExec("UPDATE connections SET status").WithArgs("running", srConn, srTenant).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

func (f *srFixture) activeCount() int {
	f.c.mu.RLock()
	defer f.c.mu.RUnlock()
	return len(f.c.active)
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
	harness.Eventually(t, srWait, "the poller to poll", func() bool { return f.polls.Load() >= 1 })
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
	if n := f.polls.Load(); n != 1 {
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
		t.Errorf("active connections after a stop mid-start = %d, want 0", n)
	}
	if f.writesRunning && f.mock.ExpectationsWereMet() == nil {
		t.Error(`status "running" was written for a start a stop overtook`)
	}
	if n := f.polls.Load(); n != 0 {
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
		t.Fatalf("active connections after a failed start = %d, want 0", n)
	}
	f.c.startConnection(context.Background(), srConn, srTenant)
	if n := f.activeCount(); n != 1 {
		t.Fatalf("active connections after a retry = %d, want 1", n)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}
