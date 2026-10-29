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

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Tests for #429: webhook-consumer on sdk.StartGuard. Boot restore and a start
// command (or two commands) call startConnection on different goroutines; a
// second start while one is in flight must not register the connection again,
// a stop that lands mid-start wins, and a start refused meanwhile is not lost
// when the first fails. Ordering is forced through the guard's AfterReserve and
// AfterActivate hooks, never with sleeps; every wait is bounded by wgWait.

const (
	wgConn   = "conn-webhook-guard"
	wgTenant = "tenant-webhook-guard"
	wgWait   = 5 * time.Second
	wgNodes  = `[{"id":"c1","type":"consumer","config":{"type":"http","http":{}}}]`
)

type wgFixture struct {
	c    *webhookConsumer
	mock sqlmock.Sqlmock
}

func newWGFixture(t *testing.T) *wgFixture {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &wgFixture{
		mock: mock,
		c: &webhookConsumer{
			db:                db,
			logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
			activeConnections: make(map[string]*ActiveConnection),
		},
	}
}

func (f *wgFixture) expectLookup(err error) {
	q := f.mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").WithArgs(wgConn, wgTenant)
	if err != nil {
		q.WillReturnError(err)
		return
	}
	q.WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
		AddRow(wgConn, wgTenant, "WH", []byte(wgNodes), []byte(`[]`)))
}

func (f *wgFixture) expectStatus(status string) {
	f.mock.ExpectExec("UPDATE connections SET status").WithArgs(status, wgConn, wgTenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func (f *wgFixture) activeCount() int {
	f.c.mu.RLock()
	defer f.c.mu.RUnlock()
	return len(f.c.activeConnections)
}

// noFurtherRunning fails the test if the code under test has written "running"
// more often than the test scripted: the test scripts one extra "running" as a
// sentinel and takes it itself here. A write the code made beyond what was
// scripted would have taken the sentinel first.
func (f *wgFixture) noFurtherRunning(t *testing.T, what string) {
	t.Helper()
	if err := f.c.updateConnectionStatus(wgConn, wgTenant, "running"); err != nil {
		t.Errorf("%s: %v", what, err)
	}
}

func wgCmd(kind string) *nats.Msg {
	data, _ := json.Marshal(map[string]string{"connection_id": wgConn, "tenant_id": wgTenant})
	return &nats.Msg{Subject: "vrsky.commands." + wgTenant + ".connection." + kind, Data: data}
}

// wgHold parks the first start that takes a claim until release is closed.
// Later claims pass through; claims counts them all.
type wgHold struct {
	claims  atomic.Int32
	reached chan struct{}
	release chan struct{}
}

func wgHoldFirst(g *sdk.StartGuard) *wgHold {
	h := &wgHold{reached: make(chan struct{}), release: make(chan struct{})}
	g.AfterReserve = func(id string) {
		if id != wgConn {
			return
		}
		if h.claims.Add(1) == 1 {
			close(h.reached)
			select {
			case <-h.release:
			case <-time.After(wgWait):
			}
		}
	}
	return h
}

func wgAwait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(wgWait):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func goDone(fn func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	return done
}

// The second start of each pair, as boot restore and the start-command
// subscription make it.
var wgSecondStarts = []struct {
	name  string
	start func(c *webhookConsumer)
}{
	{"start command", func(c *webhookConsumer) { c.handleStartCommand(wgCmd("start")) }},
	{"restore", func(c *webhookConsumer) { c.startConnection(context.Background(), wgConn, wgTenant) }},
}

// AC1: two starts of one connection register it once: one map entry, one
// lookup, one "running" write.
func TestWebhookStartGuard_ConcurrentStartsRegisterOnce(t *testing.T) {
	for _, tc := range wgSecondStarts {
		t.Run(tc.name, func(t *testing.T) {
			f := newWGFixture(t)
			f.expectLookup(nil)
			f.expectStatus("running")
			f.expectStatus("running") // sentinel, see noFurtherRunning
			h := wgHoldFirst(&f.c.starts)

			first := goDone(func() { f.c.startConnection(context.Background(), wgConn, wgTenant) })
			wgAwait(t, h.reached, "first start to claim the connection")
			wgAwait(t, goDone(func() { tc.start(f.c) }), "second start to return while the first is in flight")
			if n := f.activeCount(); n != 0 {
				t.Errorf("registered while the first start is held = %d, want 0", n)
			}
			close(h.release)
			wgAwait(t, first, "first start")

			if n := h.claims.Load(); n != 1 {
				t.Errorf("starts that claimed the connection = %d, want 1", n)
			}
			if n := f.activeCount(); n != 1 {
				t.Errorf("registered connections = %d, want 1", n)
			}
			f.noFurtherRunning(t, `"running" was written more than once`)
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db (one lookup, one running write): %v", err)
			}
		})
	}
}

// AC2: a stop that lands while a start is in progress ends stopped. Landing in
// the lookup it leaves nothing registered and no "running"; landing between
// Activate and the "running" write it leaves "stopped" as the last write.
func TestWebhookStartGuard_StopMidStartEndsStopped(t *testing.T) {
	cases := []struct {
		name   string
		script func(f *wgFixture)
		hook   func(f *wgFixture, stop func())
	}{
		{
			name: "during the lookup",
			script: func(f *wgFixture) {
				f.expectLookup(nil)
				f.expectStatus("running") // sentinel: must stay for the test to take
			},
			hook: func(f *wgFixture, stop func()) {
				f.c.starts.AfterReserve = func(id string) {
					if id == wgConn {
						stop()
					}
				}
			},
		},
		{
			name: "after Activate, before the running write",
			script: func(f *wgFixture) {
				f.expectLookup(nil)
				f.expectStatus("stopped")
				f.expectStatus("running") // sentinel: a "running" after "stopped" takes it
			},
			hook: func(f *wgFixture, stop func()) {
				f.c.starts.AfterActivate = func(id string) {
					if id == wgConn {
						stop()
					}
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWGFixture(t)
			tc.script(f)
			var stops atomic.Int32
			tc.hook(f, func() {
				if stops.Add(1) == 1 {
					f.c.handleStopCommand(wgCmd("stop"))
				}
			})

			wgAwait(t, goDone(func() { f.c.startConnection(context.Background(), wgConn, wgTenant) }), "start")

			if n := stops.Load(); n != 1 {
				t.Fatalf("stops landed mid-start = %d, want 1", n)
			}
			if n := f.activeCount(); n != 0 {
				t.Errorf("registered connections after the stop = %d, want 0", n)
			}
			if f.c.getActiveConnection(wgConn) != nil {
				t.Error("the webhook still answers after the stop")
			}
			f.noFurtherRunning(t, `"running" was written for a start a stop overtook, or after "stopped"`)
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db: %v", err)
			}
		})
	}
}

// AC3: a start refused while another is in flight runs once that one fails
// (here on its lookup) and registers the connection.
func TestWebhookStartGuard_RefusedStartRunsWhenFirstFails(t *testing.T) {
	for _, tc := range wgSecondStarts {
		t.Run(tc.name, func(t *testing.T) {
			f := newWGFixture(t)
			f.expectLookup(errors.New("read tcp: connection reset by peer"))
			f.expectLookup(nil)
			f.expectStatus("running")
			f.expectStatus("running") // sentinel
			h := wgHoldFirst(&f.c.starts)

			first := goDone(func() { f.c.startConnection(context.Background(), wgConn, wgTenant) })
			wgAwait(t, h.reached, "first start to claim the connection")
			wgAwait(t, goDone(func() { tc.start(f.c) }), "second start, refused while the first is in flight")
			if n := h.claims.Load(); n != 1 {
				t.Fatalf("starts that claimed the connection while the first is held = %d, want 1", n)
			}
			close(h.release)
			wgAwait(t, first, "first start, and the refused start it runs again")

			if n := h.claims.Load(); n != 2 {
				t.Errorf("starts that claimed the connection = %d, want 2 (the failed one, then the refused one)", n)
			}
			if n := f.activeCount(); n != 1 {
				t.Fatalf("registered connections = %d, want 1", n)
			}
			f.noFurtherRunning(t, `"running" was written more than once`)
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf("db (failed lookup, lookup, running): %v", err)
			}
		})
	}
}
