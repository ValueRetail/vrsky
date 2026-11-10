package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A node with a setting or credential missing, or a filename_template that
// cannot be parsed or rendered, used to ack (drop) every message. Each is
// retried and then kept in the DLQ, so the message can be replayed once the
// node is fixed (#734). A complete node still uploads once, with no DLQ copy.
func TestSFTPProducer_IncompleteNodeEndsInDLQ(t *testing.T) {
	cases := []struct {
		name    string
		sftp    string // the node's sftp block
		wantDLQ bool
		want    string // in X-Vrsky-Last-Error
	}{
		{name: "no host", sftp: `{"username":"u","password":"p","remote_dir":"/up"}`, wantDLQ: true, want: "no host, username or remote_dir"},
		{name: "no username", sftp: `{"host":"sftp","password":"p","remote_dir":"/up"}`, wantDLQ: true, want: "no host, username or remote_dir"},
		{name: "no remote_dir", sftp: `{"host":"sftp","username":"u","password":"p"}`, wantDLQ: true, want: "no host, username or remote_dir"},
		{name: "no password or key", sftp: `{"host":"sftp","username":"u","remote_dir":"/up"}`, wantDLQ: true, want: "no password or private key"},
		{name: "template does not parse", sftp: `{"host":"sftp","username":"u","password":"p","remote_dir":"/up","filename_template":"{{.id"}`, wantDLQ: true, want: "filename_template: parse template"},
		{name: "template field missing from the payload", sftp: `{"host":"sftp","username":"u","password":"p","remote_dir":"/up","filename_template":"{{.sku}}.json"}`, wantDLQ: true, want: "filename_template: execute template"},
		{name: "complete", sftp: `{"host":"sftp","username":"u","password":"p","remote_dir":"/up","filename_template":"{{.id}}.json"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const connID = "sftp-replay"
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.MatchExpectationsInOrder(false)
			nodes := `[{"id":"p1","type":"producer","config":{"type":"sftp","sftp":` + tc.sftp + `}}]`
			for i := 0; i < 5; i++ {
				mock.ExpectQuery("FROM connections WHERE id").
					WithArgs(connID).
					WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
			}

			fake := &fakeSFTP{}
			var dialled atomic.Int32
			p := &sftpProducer{dial: func(*SFTPConfig) (sftpConn, error) {
				dialled.Add(1)
				return fake, nil
			}}
			h := harness.NewProducerHarness(t, p, harness.Options{Name: "sftp-producer", DB: db, Backoff: fastBackoff})
			lastErr := dlqLastError(t, h.NATS())

			env := envelope.New()
			env.IntegrationID = connID
			env.TenantID = "tenant-x"
			env.Payload = []byte(`{"id":"42"}`)
			h.Publish(t, env)

			if !tc.wantDLQ {
				harness.Eventually(t, 5*time.Second, "file uploaded", func() bool {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					_, ok := fake.written["/up/42.json"]
					return ok
				})
				h.ExpectNoDLQ(t, time.Second)
				return
			}
			got := h.ExpectDLQ(t, 15*time.Second)
			if got.ID != env.ID || string(got.Payload) != `{"id":"42"}` {
				t.Fatalf("DLQ holds %s %q, want %s with its payload", got.ID, got.Payload, env.ID)
			}
			wantErrContains(t, lastErr, tc.want)
			if n := dialled.Load(); n != 0 {
				t.Errorf("dialled %d times for an incomplete node, want 0", n)
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
