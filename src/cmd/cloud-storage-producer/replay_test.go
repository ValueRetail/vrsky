package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A node with no bucket used to ack (drop) every message. It is retried and
// then kept in the DLQ, so the message can be replayed once the node is fixed
// (#734). A complete node still uploads once, with no DLQ copy.
func TestCloudProducer_IncompleteNodeEndsInDLQ(t *testing.T) {
	cases := []struct {
		name    string
		cloud   string // the node's cloud_storage block
		wantDLQ bool
	}{
		{name: "no bucket", cloud: `{"provider":"s3","prefix":"out"}`, wantDLQ: true},
		{name: "empty bucket", cloud: `{"provider":"s3","bucket":"","prefix":"out"}`, wantDLQ: true},
		{name: "complete", cloud: `{"provider":"s3","bucket":"b","prefix":"out","key_template":"{{.id}}.json"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const connID = "cloud-replay"
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.MatchExpectationsInOrder(false)
			nodes := `[{"id":"p1","type":"producer","config":{"type":"cloud_storage","cloud_storage":` + tc.cloud + `}}]`
			for i := 0; i < 5; i++ {
				mock.ExpectQuery("FROM connections WHERE id").
					WithArgs(connID).
					WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
			}

			fake := &fakeStore{}
			var opened atomic.Int32
			p := &cloudProducer{newStore: func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) {
				opened.Add(1)
				return fake, nil
			}}
			h := harness.NewProducerHarness(t, p, harness.Options{Name: "cloud-storage-producer", DB: db, Backoff: fastBackoff})
			lastErr := dlqLastError(t, h.NATS())

			env := envelope.New()
			env.IntegrationID = connID
			env.TenantID = "tenant-x"
			env.Payload = []byte(`{"id":"42"}`)
			h.Publish(t, env)

			if !tc.wantDLQ {
				harness.Eventually(t, 5*time.Second, "object written", func() bool {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					_, ok := fake.put["out/42.json"]
					return ok
				})
				h.ExpectNoDLQ(t, time.Second)
				return
			}
			got := h.ExpectDLQ(t, 15*time.Second)
			if got.ID != env.ID || string(got.Payload) != `{"id":"42"}` {
				t.Fatalf("DLQ holds %s %q, want %s with its payload", got.ID, got.Payload, env.ID)
			}
			wantErrContains(t, lastErr, "no bucket")
			if n := opened.Load(); n != 0 {
				t.Errorf("opened %d stores for an incomplete node, want 0", n)
			}
		})
	}
}

// fastBackoff gives the five attempts before the DLQ in well under a second.
var fastBackoff = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond}

// dlqLastError collects X-Vrsky-Last-Error of every dead-lettered message.
func dlqLastError(t *testing.T, nc *nats.Conn) chan string {
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

func wantErrContains(t *testing.T, ch chan string, want string) {
	t.Helper()
	select {
	case e := <-ch:
		if !strings.Contains(e, want) {
			t.Errorf("X-Vrsky-Last-Error = %q, want it to contain %q", e, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no X-Vrsky-Last-Error on the DLQ copy")
	}
}
