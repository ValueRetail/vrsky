package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/nats-io/nats.go"
)

// Tests for #498: a stop command ends a remote agent session only when it
// names the session's tenant in its payload and the same tenant on its
// subject. A stop whose subject and payload disagree is refused before it
// takes a turn on the connection's lane. They reuse the command-lanes fixture
// (command_lanes_test.go): "stopped" writes for either tenant are scripted and
// recorded, so a write the stop should not make shows in the log.
//
// The gateway's log is captured too: a refused stop logs exactly one warning
// naming the connection and the tenant IDs, and nothing else from the
// payload (stMarker rides along in it and must not show up).

const stMarker = "payload-marker-498"

func stStopMsg(subject, payloadTenant string) *nats.Msg {
	return &nats.Msg{
		Subject: subject,
		Data: mustJSON(map[string]string{
			"connection_id": clConn, "tenant_id": payloadTenant, "note": stMarker,
		}),
	}
}

// stLogBuf collects the gateway's log output; every slog record is one Write.
type stLogBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *stLogBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *stLogBuf) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func (b *stLogBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// warnings returns the WARN lines logged since the last reset.
func (b *stLogBuf) warnings() []string {
	var out []string
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.Contains(line, "level=WARN") {
			out = append(out, line)
		}
	}
	return out
}

func stSubject(tenant string) string { return "vrsky.commands." + tenant + ".connection.stop" }

func TestStopTenant_ForeignStopLeavesSession(t *testing.T) {
	cases := []struct {
		name     string
		msg      *nats.Msg
		mismatch bool     // subject and payload disagree: refused before a turn
		tenants  []string // tenant IDs the warning must name
	}{
		{"payload and subject name another tenant", stStopMsg(stSubject(tenant2), tenant2), false, []string{tenant1, tenant2}},
		{"payload names the tenant, subject another", stStopMsg(stSubject(tenant2), tenant1), true, []string{tenant1, tenant2}},
		{"payload names another tenant, subject the tenant", stStopMsg(stSubject(tenant1), tenant2), true, []string{tenant1, tenant2}},
		// The subject names no tenant; the payload's is the only one there is.
		{"payload names the tenant, malformed subject", stStopMsg("vrsky.commands.connection.stop", tenant1), true, []string{tenant1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLaneEnv(t)
			// Swapped before anything runs that reads the logger.
			logs := &stLogBuf{}
			l.g.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			l.expectStopped(clConn, tenant1, 1)
			l.expectStopped(clConn, tenant2, 1)
			l.expectLoad(clConn, tenant1, outNodes(""), nil)
			l.expectAgent(true)
			l.expectRunning(clConn, tenant1, 1, nil)
			l.g.startConnection(context.Background(), clConn, tenant1)
			l.wantLive(t, clConn, "")
			sess := l.sessions()[clConn]

			logs.reset()
			l.g.onCommand(false)(tc.msg)
			if tc.mismatch {
				if n := laneCount(&l.g.commands); n != 0 {
					t.Errorf("lanes after a stop whose subject and payload disagree = %d, want 0 (no turn taken)", n)
				}
				if n := goroutinesWith(frameCommand); n != 0 {
					t.Errorf("command goroutines after a refused stop = %d, want 0", n)
				}
			}
			l.awaitCommands(t)

			if got := l.sessions()[clConn]; got != sess {
				t.Fatalf("session after a foreign stop = %p, want the start's %p", got, sess)
			}
			l.wantLive(t, clConn, "")
			if n := l.log.count("stopped"); n != 0 {
				t.Errorf(`a foreign stop wrote "stopped" %d times, want 0`, n)
			}

			// One warning, naming the connection and the tenants, and nothing
			// else from the payload anywhere in the log.
			warns := logs.warnings()
			if len(warns) != 1 {
				t.Errorf("warnings after a foreign stop = %d, want 1:\n%s", len(warns), strings.Join(warns, "\n"))
			} else {
				for _, want := range append([]string{clConn}, tc.tenants...) {
					if !strings.Contains(warns[0], want) {
						t.Errorf("warning %q does not name %s", warns[0], want)
					}
				}
			}
			if out := logs.String(); strings.Contains(out, stMarker) {
				t.Errorf("the log carries the payload's other fields:\n%s", out)
			}

			// The tenant's own stop still ends the session and writes "stopped".
			l.command(false, clConn, tenant1)
			l.awaitCommands(t)
			if n := l.log.count("stopped"); n != 1 {
				t.Errorf(`"stopped" written %d times after the tenant's stop, want 1`, n)
			}
			l.wantNothingLeft(t, clConn, false)
		})
	}
}
