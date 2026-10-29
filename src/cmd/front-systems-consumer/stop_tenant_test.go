package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"
)

// Tests for #498: a stop command acts only when it names, in its payload and
// on its subject, the tenant the connection was started for. Front Systems
// writes no status and keeps no per-connection resources: its active entry is
// the whole of "started". The fixture is self-contained (no callback URL, so
// a start registers nothing); a held start is parked in AfterReserve.

const (
	t498Conn   = "conn-front-systems-498"
	t498Tenant = "tenant-front-systems-498"
	t498Other  = "tenant-other"
	t498Wait   = 5 * time.Second
)

func t498Consumer(t *testing.T) *frontSystemsConsumer {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	nodes, _ := json.Marshal([]any{map[string]any{"id": "c1", "type": "consumer", "config": map[string]any{
		"type": "front_systems", "front_systems": map[string]any{"base_url": "http://front-systems.invalid"}}}})
	mock.ExpectQuery(configLookupSQL).WithArgs(t498Conn, t498Tenant).
		WillReturnRows(configRows(nodes))
	return &frontSystemsConsumer{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func t498Active(c *frontSystemsConsumer) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.active[t498Conn]
	return ok
}

func t498Stop(subjectTenant, payloadTenant string) *nats.Msg {
	data, _ := json.Marshal(map[string]string{"connection_id": t498Conn, "tenant_id": payloadTenant})
	return &nats.Msg{Subject: "vrsky.commands." + subjectTenant + ".connection.stop", Data: data}
}

var t498ForeignStops = []struct {
	name string
	msg  func() *nats.Msg
}{
	{"payload and subject name another tenant", func() *nats.Msg { return t498Stop(t498Other, t498Other) }},
	{"payload names the tenant, subject another", func() *nats.Msg { return t498Stop(t498Other, t498Tenant) }},
	{"payload names another tenant, subject the tenant", func() *nats.Msg { return t498Stop(t498Tenant, t498Other) }},
}

func TestStopTenant_ForeignStopKeepsConnectionStarted(t *testing.T) {
	for _, tc := range t498ForeignStops {
		t.Run(tc.name, func(t *testing.T) {
			c := t498Consumer(t)
			c.startConnection(context.Background(), t498Conn, t498Tenant)
			if !t498Active(c) {
				t.Fatal("connection not started")
			}

			c.handleStopCommand(tc.msg())
			if !t498Active(c) {
				t.Fatal("a foreign stop forgot the connection")
			}

			c.handleStopCommand(t498Stop(t498Tenant, t498Tenant))
			if t498Active(c) {
				t.Error("connection still started after its tenant's stop")
			}
		})
	}
}

// A foreign stop while the start is held does not abandon the start.
func TestStopTenant_ForeignStopMidStartDoesNotAbandon(t *testing.T) {
	for _, tc := range t498ForeignStops {
		t.Run(tc.name, func(t *testing.T) {
			c := t498Consumer(t)
			reached, release := make(chan struct{}), make(chan struct{})
			c.starts.AfterReserve = func(string) {
				close(reached)
				select {
				case <-release:
				case <-time.After(t498Wait):
				}
			}
			done := make(chan struct{})
			go func() { defer close(done); c.startConnection(context.Background(), t498Conn, t498Tenant) }()
			t498Await(t, reached, "start to claim the connection")
			c.handleStopCommand(tc.msg())
			close(release)
			t498Await(t, done, "start")

			if !t498Active(c) {
				t.Fatal("start abandoned by a foreign stop")
			}
		})
	}
}

func t498Await(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(t498Wait):
		t.Fatalf("timed out waiting for %s", what)
	}
}
