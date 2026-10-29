package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// Tests for #567: with after_action=move, a move_prefix whose moved objects
// the listing of prefix returns again makes the consumer publish them a second
// time and then copy them onto their own key and delete them (Azure and GCS
// lose the object). Poll mode refuses such a config at start; afterAction
// never copies an object onto its own key.

// mpLog is a log sink safe for the poller goroutine and the test at once.
type mpLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *mpLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *mpLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func mpNodes(prefix, movePrefix, afterAction, mode string) string {
	cfg := `"provider":"s3","bucket":"b","prefix":` + strconv.Quote(prefix) +
		`,"after_action":` + strconv.Quote(afterAction) +
		`,"move_prefix":` + strconv.Quote(movePrefix) +
		`,"poll_interval_seconds":3600`
	if mode != "" {
		cfg += `,"mode":` + strconv.Quote(mode) + `,"event_queue_url":"http://sqs.local/q"`
	}
	return `[{"id":"c1","type":"consumer","config":{"type":"cloud_storage","cloud_storage":{` + cfg + `}}}]`
}

func TestStart_RefusesMovePrefixTheListingReturns(t *testing.T) {
	tests := []struct {
		name        string
		prefix      string
		movePrefix  string
		afterAction string // default "move"
		mode        string // "" = poll
		refused     bool
	}{
		// Equal after normalising slashes.
		{name: "equal", prefix: "in/", movePrefix: "in/", refused: true},
		{name: "equal without slashes", prefix: "in", movePrefix: "in", refused: true},
		{name: "equal, move_prefix without trailing slash", prefix: "in/", movePrefix: "in", refused: true},
		{name: "equal, move_prefix with leading slash", prefix: "in/", movePrefix: "/in/", refused: true},

		// Under prefix.
		{name: "under", prefix: "in/", movePrefix: "in/done", refused: true},
		{name: "under, trailing slash", prefix: "in/", movePrefix: "in/done/", refused: true},
		{name: "under, leading slash on move_prefix", prefix: "in/", movePrefix: "/in/done", refused: true},
		{name: "under, leading slash on prefix", prefix: "/in/", movePrefix: "in/done", refused: true},
		{name: "under, deeper", prefix: "in/", movePrefix: "in/a/b/", refused: true},
		{name: "under, double slash", prefix: "in/", movePrefix: "in//done", refused: true},
		{name: "under, dot segments", prefix: "in/", movePrefix: "./in/x/../done", refused: true},

		// Empty prefix is the whole bucket.
		{name: "empty prefix", prefix: "", movePrefix: "processed", refused: true},
		{name: "empty prefix, move to root", prefix: "", movePrefix: "/", refused: true},

		// The listing matches prefix as text: "in" lists "in-done/a.csv".
		{name: "prefix without slash lists sibling", prefix: "in", movePrefix: "in-done/", refused: true},
		{name: "prefix without slash, move to root", prefix: "in", movePrefix: "/", refused: true},
		{name: "prefix without slash, move to root as dot", prefix: "in", movePrefix: ".", refused: true},
		// Base names starting with the rest of prefix land on their own key.
		{name: "prefix names files in move_prefix", prefix: "data/report_", movePrefix: "data", refused: true},

		// Outside the listing.
		{name: "sibling folder", prefix: "in/", movePrefix: "in-done/", refused: false},
		{name: "sibling folder without slash", prefix: "in/", movePrefix: "in-done", refused: false},
		{name: "sibling sharing a string prefix", prefix: "in/", movePrefix: "inx", refused: false},
		{name: "unrelated folder", prefix: "in/", movePrefix: "processed", refused: false},
		{name: "prefix without slash, unrelated folder", prefix: "in", movePrefix: "out", refused: false},
		{name: "one level up", prefix: "incoming/a/", movePrefix: "incoming", refused: false},
		{name: "bucket root", prefix: "in/", movePrefix: "/", refused: false},
		{name: "bucket root as dot", prefix: "in/", movePrefix: ".", refused: false},
		{name: "nested sibling", prefix: "data/2026/", movePrefix: "data/archive", refused: false},

		// Only move is checked, and only in poll mode.
		{name: "delete ignores move_prefix", prefix: "", movePrefix: "processed", afterAction: "delete", refused: false},
		{name: "event mode is not checked at start", prefix: "in/", movePrefix: "in/done", mode: "event", refused: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSRFixture(t)
			logs := &mpLog{}
			f.c.logger = slog.New(slog.NewTextHandler(logs, nil))
			f.c.newEvents = func(context.Context, *cloudConfig) (eventSource, error) { return &fakeEvents{}, nil }
			action := tt.afterAction
			if action == "" {
				action = "move"
			}
			f.nodes = mpNodes(tt.prefix, tt.movePrefix, action, tt.mode)
			f.expectLookup(nil)
			if !tt.refused {
				f.expectRunning()
			}

			f.c.startConnection(context.Background(), srConn, srTenant)

			if tt.refused {
				if n := f.activeCount(); n != 0 {
					t.Fatalf("prefix %q, move_prefix %q: start not refused (%d active)", tt.prefix, tt.movePrefix, n)
				}
				if n := f.polls.Load(); n != 0 {
					t.Fatalf("refused start opened the object store %d time(s)", n)
				}
				out := logs.String()
				for _, want := range []string{"level=ERROR", "move_prefix", strconv.Quote(tt.movePrefix), "prefix " + strconv.Quote(tt.prefix)} {
					// slog's text handler quotes the error value, escaping inner quotes.
					if !strings.Contains(out, want) && !strings.Contains(out, strings.ReplaceAll(want, `"`, `\"`)) {
						t.Errorf("log does not contain %s:\n%s", want, out)
					}
				}
				return
			}
			if n := f.activeCount(); n != 1 {
				t.Fatalf("prefix %q, move_prefix %q: start refused, want it running\n%s", tt.prefix, tt.movePrefix, logs.String())
			}
		})
	}
}

// afterAction refuses a move whose destination is the object's own key: event
// mode can hand it an object that was moved already.
func TestAfterAction_NeverCopiesOntoOwnKey(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		movePrefix string
		wantDst    string // "" = not moved
	}{
		{name: "already in move_prefix", key: "processed/a.csv", movePrefix: "processed"},
		{name: "already in move_prefix, trailing slash", key: "processed/a.csv", movePrefix: "processed/"},
		{name: "already in move_prefix, leading slash", key: "processed/a.csv", movePrefix: "/processed"},
		{name: "root object, root move_prefix", key: "a.csv", movePrefix: "."},
		{name: "root object, slash move_prefix", key: "a.csv", movePrefix: "/"},
		{name: "moved", key: "in/a.csv", movePrefix: "processed", wantDst: "processed/a.csv"},
		{name: "moved up a level", key: "in/sub/a.csv", movePrefix: "in", wantDst: "in/a.csv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := &fakeStore{objects: map[string][]byte{tt.key: []byte("x")}}
			c := &cloudConsumer{}
			cfg := &cloudConfig{AfterAction: "move", MovePrefix: tt.movePrefix}
			err := c.afterAction(context.Background(), fs, cfg, tt.key)

			if tt.wantDst == "" {
				if err == nil {
					t.Fatalf("move of %q to %q: want an error", tt.key, tt.movePrefix)
				}
				if len(fs.copied) != 0 || len(fs.deleted) != 0 {
					t.Fatalf("copied %v, deleted %v; want neither", fs.copied, fs.deleted)
				}
				if _, ok := fs.objects[tt.key]; !ok {
					t.Fatalf("object %q is gone", tt.key)
				}
				return
			}
			if err != nil {
				t.Fatalf("move: %v", err)
			}
			if got := fs.copied[tt.key]; got != tt.wantDst {
				t.Fatalf("copied to %q, want %q", got, tt.wantDst)
			}
			if _, ok := fs.objects[tt.key]; ok {
				t.Fatalf("source %q still there after the move", tt.key)
			}
		})
	}
}

// A move_prefix next to prefix keeps working: two polls publish the object
// once and leave it in move_prefix.
func TestPoll_SiblingMovePrefixPublishesOnce(t *testing.T) {
	fs := &fakeStore{objects: map[string][]byte{"in/a.csv": []byte("a,b\n")}}
	var published atomic.Int32
	c := &cloudConsumer{
		publish: func(context.Context, *envelope.Envelope) error { published.Add(1); return nil },
	}
	cfg := &cloudConfig{
		Config:      objectstore.Config{Bucket: "b", Prefix: "in/"},
		AfterAction: "move",
		MovePrefix:  "in-done",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	done := c.doneSet("conn", "tenant", cfg)
	for i := 0; i < 2; i++ {
		c.pollOnce(context.Background(), "conn", "tenant", fs, cfg, done, logger)
	}
	if n := published.Load(); n != 1 {
		t.Fatalf("published %d times, want 1", n)
	}
	if _, ok := fs.objects["in-done/a.csv"]; !ok {
		t.Fatalf("object not in move_prefix: %v", fs.objects)
	}
	if _, ok := fs.objects["in/a.csv"]; ok {
		t.Fatal("source still there after the move")
	}
}
