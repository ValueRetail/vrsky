package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// #769: a cancelled poll still stores its checkpoint (saveProgress retries on
// a context the stop does not cancel). A start right after the stop must not
// let that save land after the new poller's own. Built on the sqlmock fixture
// in start_race_test.go and the ordered fake in ordered_resume_test.go; the
// order of events is forced through a checkpoint store that holds the old
// poller's save, never with sleeps, and every wait is bounded by srWait.

// heldStore holds a Save of the watermark held until release is closed (a
// cancelled Save gives up at once, as a database driver does) and records
// every watermark stored.
type heldStore struct {
	checkpoint.Store
	held              string
	entered, returned chan struct{}
	release           chan struct{}
	enterOnce, retOnc sync.Once

	mu    sync.Mutex
	saved []string
}

func (s *heldStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	if cp.LastProcessedMessageID == s.held {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.enterOnce.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		defer s.retOnc.Do(func() { close(s.returned) })
	}
	err := s.Store.Save(ctx, cp)
	if err == nil {
		s.mu.Lock()
		s.saved = append(s.saved, cp.LastProcessedMessageID)
		s.mu.Unlock()
	}
	return err
}

func (s *heldStore) stored(v string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.saved {
		if x == v {
			return true
		}
	}
	return false
}

func TestStopStart_OldPollerCannotOverwriteTheNewCheckpoint(t *testing.T) {
	recs := map[string]string{"a": mins(-90), "b": mins(-80), "c": mins(-70), "d": mins(-60)}
	// The old poller reads a and b, is stopped on page 2, and stores a: the
	// value below the last one it read. The new poller reads on from there
	// and stores d.
	oldMark, newMark := mins(-90), mins(-60)

	f := newSRFixture(t)
	bc := newOrderedBC(t, lmField, 2, recs)
	testDone := make(chan struct{})
	t.Cleanup(func() { close(testDone) })
	atPage2 := make(chan struct{})
	var holdOnce sync.Once
	bc.hold = func(token string, r *http.Request) {
		if token == "" {
			return
		}
		first := false
		holdOnce.Do(func() { first = true })
		if !first {
			return
		}
		close(atPage2)
		select { // until the stop cancels the request
		case <-r.Context().Done():
		case <-testDone:
		}
	}
	f.nodes = fmt.Sprintf(`[{"id":"c1","type":"consumer","config":{"type":"business_central","business_central":{`+
		`"aad_tenant_id":"aad","company_id":"GUID","client_id":"cid","client_secret":"sec","entity":"items",`+
		`"api_base_url":%q,"token_url":%q,"incremental":true,"cursor_field":%q,"poll_interval_seconds":3600}}}]`,
		bc.srv.URL+"/api/v2.0", bc.tok.URL, lmField)
	store := &heldStore{
		Store: checkpoint.NewInMemoryStore(), held: oldMark,
		entered: make(chan struct{}), returned: make(chan struct{}), release: make(chan struct{}),
	}
	f.c.checkpoints = store
	f.c.saveTimeout = srWait
	f.c.httpClient = http.DefaultClient
	f.c.now = func() time.Time { return aheadNow }
	logs := &stLogBuf{}
	f.c.logger = slog.New(slog.NewTextHandler(logs, nil))
	var pubMu sync.Mutex
	var published []string
	f.c.publish = func(_ context.Context, env *envelope.Envelope) error {
		var rs []map[string]any
		if err := json.Unmarshal(env.Payload, &rs); err != nil {
			return err
		}
		pubMu.Lock()
		defer pubMu.Unlock()
		for _, r := range rs {
			published = append(published, r["id"].(string))
		}
		return nil
	}
	f.expectLookup(nil)
	f.expectLookup(nil)

	f.c.startConnection(context.Background(), srConn, srTenant)
	srAwait(t, atPage2, "the first poller to ask for page 2")
	f.c.handleStopCommand(srStopMsg())
	srAwait(t, store.entered, "the stopped poller to store its checkpoint")

	// The start must return at once: it runs on the NATS command goroutine.
	srAwait(t, f.goStart(), "the second start to return")
	// Either the new poller waits for the old one, or (the bug) it polls and
	// stores its checkpoint before the old one's save lands.
	harness.Eventually(t, srWait, "the new poller to wait, or to store its checkpoint", func() bool {
		return strings.Contains(logs.String(), "Waiting for the stopped poller") || store.stored(newMark)
	})
	close(store.release)
	srAwait(t, store.returned, "the stopped poller's save")
	harness.Eventually(t, srWait, "the new poller to store its checkpoint", func() bool { return store.stored(newMark) })

	cp, err := store.Get(context.Background(), srTenant, srConn, "c1")
	if err != nil || cp == nil || cp.LastProcessedMessageID != newMark {
		t.Fatalf("checkpoint = %+v (err %v), want %s: the stopped poller's save overwrote the new one's", cp, err, newMark)
	}
	pubMu.Lock()
	defer pubMu.Unlock()
	exactlyOnce(t, published, recs)
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}

// A new poller does not wait for a stopped one that has already exited, and
// waits for one that never exits only up to exitWait.
func TestStopStart_ExitWaitIsBounded(t *testing.T) {
	old := exitWaitMargin
	exitWaitMargin = 10 * time.Millisecond
	t.Cleanup(func() { exitWaitMargin = old })
	c, _, _ := newTestConsumer()
	c.saveTimeout = time.Millisecond
	if got, want := c.exitWait(), 12*time.Millisecond; got != want {
		t.Fatalf("exitWait = %v, want %v", got, want)
	}
	exited := &poller{done: make(chan struct{})}
	close(exited.done)
	stuck := &poller{done: make(chan struct{})}

	for _, tc := range []struct {
		name     string
		prev     *poller
		wantLogs []string
		noLogs   []string
	}{
		{"already exited", exited, nil, []string{"Waiting for the stopped poller", "has not exited"}},
		{"never exits", stuck, []string{"Waiting for the stopped poller", "level=WARN", "has not exited"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &stLogBuf{}
			logger := slog.New(slog.NewTextHandler(logs, nil))
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // so pollAfter returns instead of polling
			p := &poller{cancel: cancel, done: make(chan struct{})}
			returned := make(chan struct{})
			go func() {
				defer close(returned)
				c.pollAfter(ctx, tc.prev, p, "conn-1", "tenant-1", &BCConfig{}, logger)
			}()
			srAwait(t, returned, "pollAfter")
			srAwait(t, p.done, "done to close")
			out := logs.String()
			for _, want := range tc.wantLogs {
				if !strings.Contains(out, want) {
					t.Errorf("log lacks %q: %s", want, out)
				}
			}
			for _, not := range tc.noLogs {
				if strings.Contains(out, not) {
					t.Errorf("log has %q: %s", not, out)
				}
			}
		})
	}
}

func (l *stLogBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
