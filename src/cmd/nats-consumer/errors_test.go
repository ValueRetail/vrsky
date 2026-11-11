package main

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

const tPassword = "pw-that-must-not-leak"

// statusRec records the arguments of every status write.
type statusRec struct {
	mu   sync.Mutex
	args [][]driver.Value
}

type argCapture struct {
	rec *statusRec
	i   int
}

func (a argCapture) Match(v driver.Value) bool {
	a.rec.mu.Lock()
	defer a.rec.mu.Unlock()
	for len(a.rec.args) <= a.i {
		a.rec.args = append(a.rec.args, nil)
	}
	a.rec.args[a.i] = append(a.rec.args[a.i], v)
	return true
}

// errorWrite returns the (status, reason) of the "error" write, if any.
func (r *statusRec) errorWrite() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.args) < 2 {
		return "", false
	}
	for i, st := range r.args[0] {
		if st == "error" && i < len(r.args[1]) {
			reason, _ := r.args[1][i].(string)
			return reason, true
		}
	}
	return "", false
}

// lockedBuf is a log sink the loop's goroutines can write to.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// withStatusDB gives c a mocked database that accepts any number of status
// writes of the "error" shape (four arguments) and records them.
func withStatusDB(t *testing.T, c *natsConsumer) *statusRec {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rec := &statusRec{}
	mock.MatchExpectationsInOrder(false)
	mock.ExpectExec(`UPDATE connections SET status = \$1, last_error = \$2`).
		WithArgs(argCapture{rec, 0}, argCapture{rec, 1}, argCapture{rec, 2}, argCapture{rec, 3}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	c.db = db
	return rec
}

// Errors retrying cannot fix end the connection with status error and a
// reason; nothing secret reaches the reason or the log.
func TestPermanentErrors_EndWithStatusError(t *testing.T) {
	userServer := func(o *natsserver.Options) { o.Username, o.Password = "rh", "right-password" }

	tests := []struct {
		name         string
		server       func(*natsserver.Options)
		setup        func(t *testing.T, js nats.JetStreamContext)
		cfg          func(cfg *NATSConfig, ext *external)
		allowPrivate bool
		want         string
	}{
		{
			name:   "bad password",
			server: userServer,
			cfg: func(cfg *NATSConfig, _ *external) {
				cfg.AuthType, cfg.Username, cfg.Password = authUserPassword, "rh", tPassword
			},
			allowPrivate: true,
			want:         "refused the credentials",
		},
		{
			name:         "unknown stream",
			cfg:          func(cfg *NATSConfig, _ *external) { cfg.Stream = "NOPE" },
			allowPrivate: true,
			want:         `stream "NOPE" does not exist`,
		},
		{
			name: "durable is a push consumer",
			setup: func(t *testing.T, js nats.JetStreamContext) {
				if _, err := js.AddConsumer(tStream, &nats.ConsumerConfig{Durable: tDur, DeliverSubject: "push.here", AckPolicy: nats.AckExplicitPolicy}); err != nil {
					t.Fatalf("add push durable: %v", err)
				}
			},
			allowPrivate: true,
			want:         "push consumer",
		},
		{
			name: "durable filters on something else",
			setup: func(t *testing.T, js nats.JetStreamContext) {
				if _, err := js.AddConsumer(tStream, &nats.ConsumerConfig{Durable: tDur, FilterSubject: "rh.evt.v1.tenant-b.>", AckPolicy: nats.AckExplicitPolicy}); err != nil {
					t.Fatalf("add durable: %v", err)
				}
			},
			allowPrivate: true,
			want:         "filters on",
		},
		{
			name: "durable without explicit ack",
			setup: func(t *testing.T, js nats.JetStreamContext) {
				if _, err := js.AddConsumer(tStream, &nats.ConsumerConfig{Durable: tDur, FilterSubject: tSubj, AckPolicy: nats.AckNonePolicy}); err != nil {
					t.Fatalf("add durable: %v", err)
				}
			},
			allowPrivate: true,
			want:         "explicit ack",
		},
		{
			name: "may not use JetStream",
			server: func(o *natsserver.Options) {
				o.Users = []*natsserver.User{{Username: "rh", Password: "right-password", Permissions: &natsserver.Permissions{
					Publish: &natsserver.SubjectPermission{Deny: []string{"$JS.API.>"}},
				}}}
			},
			cfg: func(cfg *NATSConfig, _ *external) {
				cfg.AuthType, cfg.Username, cfg.Password = authUserPassword, "rh", "right-password"
			},
			allowPrivate: true,
			want:         "may not",
		},
		{
			// A host name resolving to loopback: only the dial sees it.
			name: "server resolves to a private address",
			cfg: func(cfg *NATSConfig, ext *external) {
				cfg.Servers = []string{strings.Replace(ext.url(), "127.0.0.1", "localhost", 1)}
			},
			want: "private, loopback or link-local",
		},
		{
			name: "tls wanted, server has none",
			cfg: func(cfg *NATSConfig, ext *external) {
				cfg.Servers = []string{strings.Replace(ext.url(), "nats://", "tls://", 1)}
			},
			allowPrivate: true,
			want:         "does not offer TLS",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := startExternal(t, tt.server)
			var adminOpts []nats.Option
			if tt.server != nil {
				adminOpts = append(adminOpts, nats.UserInfo("rh", "right-password"))
			}
			if tt.name != "may not use JetStream" {
				_, js := ext.admin(adminOpts...)
				addStream(t, js)
				if tt.setup != nil {
					tt.setup(t, js)
				}
			}

			v := startVRSky(t)
			c := newTestConsumer(v.publish)
			logs := &lockedBuf{}
			c.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			c.allowPrivate = tt.allowPrivate
			rec := withStatusDB(t, c)

			cfg := baseConfig(ext.url())
			if tt.cfg != nil {
				tt.cfg(cfg, ext)
			}
			cancel, done := c.runLoop(cfg)
			select {
			case <-done:
			case <-time.After(wait):
				cancel()
				t.Fatal("the loop kept retrying a permanent error")
			}
			reason, ok := rec.errorWrite()
			if !ok {
				t.Fatal("no status error written")
			}
			if !strings.Contains(reason, tt.want) {
				t.Errorf("reason %q does not contain %q", reason, tt.want)
			}
			if strings.Contains(reason, tPassword) || strings.Contains(logs.String(), tPassword) {
				t.Error("the password reached the reason or the log")
			}
			c.mu.RLock()
			_, active := c.active[tConn]
			c.mu.RUnlock()
			if active {
				t.Error("the connection is still active after a permanent error")
			}
		})
	}
}

// startConnection refuses a stored config with a plaintext credential: status
// error, and the value appears nowhere. A key in another case is a credential
// too: encoding/json would read it into the config (#859 review).
func TestStart_PlaintextCredentialRefused(t *testing.T) {
	for _, tt := range []struct {
		name string
		cred map[string]any
	}{
		{"password", map[string]any{"auth_type": "user_password", "username": "rh", "password": tPassword}},
		{"Password", map[string]any{"auth_type": "user_password", "username": "rh", "password_secret_id": "0b1c", "Password": tPassword}},
		{"NKEY_SEED with Auth_Type", map[string]any{"Auth_Type": "nkey", "NKEY_SEED": "SU" + tPassword}},
		{"Nkey_Seed next to its secret", map[string]any{"auth_type": "nkey", "nkey_seed_secret_id": "0b1c", "Nkey_Seed": "SU" + tPassword}},
		{"Creds", map[string]any{"auth_type": "creds", "Creds": "-----BEGIN NATS USER JWT-----" + tPassword}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestConsumer(nil)
			logs := &lockedBuf{}
			c.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			c.db = db
			block := map[string]any{"servers": []string{"tls://nats.example.com:4222"}, "stream": tStream, "durable": tDur}
			for k, v := range tt.cred {
				block[k] = v
			}
			nodes, _ := json.Marshal([]map[string]any{{
				"id": "src", "type": "consumer",
				"config": map[string]any{"type": "nats", "nats": block},
			}})
			mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(tConn, tTenant).
				WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow(nodes))
			rec := &statusRec{}
			mock.ExpectExec(`UPDATE connections SET status = \$1, last_error = \$2`).
				WithArgs(argCapture{rec, 0}, argCapture{rec, 1}, argCapture{rec, 2}, argCapture{rec, 3}).
				WillReturnResult(sqlmock.NewResult(0, 1))

			c.startConnection(context.Background(), tConn, tTenant)

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("db: %v", err)
			}
			reason, ok := rec.errorWrite()
			if !ok || !strings.Contains(reason, "plaintext") {
				t.Fatalf("status error reason = %q (written %v), want one about plaintext", reason, ok)
			}
			if strings.Contains(reason, tPassword) || strings.Contains(logs.String(), tPassword) {
				t.Error("the plaintext credential reached the reason or the log")
			}
			if _, active := c.active[tConn]; active {
				t.Error("connection active after a refused start")
			}
		})
	}
}

// A durable deleted on the server after the loop bound it is not recreated:
// that would replay the stream from its first message. The connection ends
// with status error.
func TestDurableDeleted_EndsWithStatusError(t *testing.T) {
	ext := startExternal(t, nil)
	_, js := ext.admin()
	addStream(t, js)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-1", `{"n":1}`)

	v := startVRSky(t)
	c := newTestConsumer(v.publish)
	rec := withStatusDB(t, c)
	cancel, done := c.runLoop(baseConfig(ext.url()))
	defer stop(t, cancel, done)
	harness.Eventually(t, wait, "first event stored", func() bool { return v.stored(t) == 1 })

	if err := js.DeleteConsumer(tStream, tDur); err != nil {
		t.Fatalf("delete durable: %v", err)
	}
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("the loop went on after its durable was deleted")
	}
	reason, ok := rec.errorWrite()
	if !ok || !strings.Contains(reason, "no longer exists") {
		t.Fatalf("status error reason = %q (written %v)", reason, ok)
	}
	if _, err := js.ConsumerInfo(tStream, tDur); err == nil {
		t.Error("the durable was created again")
	}
}
