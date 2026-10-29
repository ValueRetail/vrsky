package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #457 around the poll rather than inside it: config that cannot be
// polled incrementally is refused before any request (AC 11), a changed
// query starts over (AC 12), and the checkpoint row survives a stop, an
// unreadable store and an undecodable row (AC 13).

// AC 11: what checkIncremental accepts and refuses.
func TestIncremental_CheckIncremental(t *testing.T) {
	cases := []struct {
		name     string
		resource string
		overlap  *int64
		ok       bool
	}{
		{"orders", "orders", nil, true},
		{"orders with a filter", "orders?orderstate=10", nil, true},
		{"orders with two filters", "orders?orderstate=10&customerref=7", nil, true},
		{"overlap 0", "orders", ov(0), true},
		{"large overlap", "orders", ov(500000), true},

		{"default resource (transactions)", "", nil, false},
		{"transactions", "transactions", nil, false},
		{"products", "products", nil, false},
		{"warehouseitems", "warehouseitems", nil, false},
		{"a sub-resource of orders", "orders/123", nil, false},
		{"orderitems", "orderitems", nil, false},
		{"transactions with a filter", "transactions?orderstate=10", nil, false},
		{"query sets orderidfrom", "orders?orderidfrom=5", nil, false},
		{"query sets sort", "orders?sort=orderid", nil, false},
		{"query sets sort descending", "orders?sort=-orderid", nil, false},
		{"query sets start", "orders?start=0", nil, false},
		{"query sets num", "orders?num=10", nil, false},
		{"query sets num after a filter", "orders?orderstate=10&num=10", nil, false},
		{"negative overlap", "orders", ov(-1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &SitooConfig{AccountID: 1, SiteID: 2, APIID: "x", APIPassword: "y",
				Resource: tc.resource, Incremental: true, CursorOverlap: tc.overlap, NodeID: incNode}
			err := cfg.checkIncremental()
			if tc.ok && err != nil {
				t.Errorf("checkIncremental refused a valid config: %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("checkIncremental accepted a config incremental polling must refuse")
			}
		})
	}
}

// AC 11: the refusal reaches the poll: fetchAndPublish fails with no request
// made, nothing published and nothing saved. A '#' in the resource is
// refused with incremental off too.
func TestIncremental_RefusedBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name     string
		resource string
		overlap  *int64
		inc      bool
	}{
		{"transactions", "transactions", nil, true},
		{"default resource", "", nil, true},
		{"products", "products", nil, true},
		{"query sets orderidfrom", "orders?orderidfrom=5", nil, true},
		{"query sets sort", "orders?sort=orderid", nil, true},
		{"query sets start", "orders?start=10", nil, true},
		{"query sets num", "orders?num=10", nil, true},
		{"negative overlap", "orders", ov(-5), true},
		{"fragment, incremental", "orders#x", nil, true},
		{"fragment after a filter, incremental", "orders?orderstate=10#x", nil, true},
		{"fragment, incremental off", "orders#x", nil, false},
		{"fragment on transactions, incremental off", "transactions#frag", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, false, ords(1, 2)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := incConfig(srv.URL, tc.overlap, 2)
			cfg.Resource = tc.resource
			cfg.Incremental = tc.inc

			if err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger); err == nil {
				t.Fatal("fetchAndPublish accepted the config, want an error")
			}
			if reqs := f.take(); len(reqs) != 0 {
				t.Errorf("made %d requests, want none (first: %s)", len(reqs), reqs[0].uri)
			}
			assertOnce(t, "refused poll", published(t, got, mu, incTenant, incConn))
			if w := watermark(t, store, incTenant, incConn, incNode); w != "" {
				t.Errorf("saved watermark %q", w)
			}
		})
	}
}

// AC 11 at start: a node with incremental on and a config it refuses is not
// started (no poller, no request); a valid one is.
func TestIncremental_RefusedAtStart(t *testing.T) {
	cases := []struct {
		name    string
		replace string // replaces `"resource":"transactions"` in the fixture's node
		starts  bool
	}{
		{"transactions", `"resource":"transactions","incremental":true`, false},
		{"query sets sort", `"resource":"orders?sort=orderid","incremental":true`, false},
		{"negative overlap", `"resource":"orders","incremental":true,"cursor_overlap":-1`, false},
		{"fragment", `"resource":"orders#x","incremental":true`, false},
		{"orders", `"resource":"orders?orderstate=10","incremental":true`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSRFixture(t)
			if !strings.Contains(f.nodes, `"resource":"transactions"`) {
				t.Fatalf("fixture node changed shape: %s", f.nodes)
			}
			f.nodes = strings.Replace(f.nodes, `"resource":"transactions"`, tc.replace, 1)
			f.c.checkpoints = &lockedStore{s: checkpoint.NewInMemoryStore()}
			f.expectLookup(nil)
			f.expectRunning()

			f.c.startConnection(context.Background(), srConn, srTenant)

			if tc.starts {
				if n := f.activeCount(); n != 1 {
					t.Fatalf("active pollers = %d, want 1", n)
				}
				harness.Eventually(t, srWait, "the first poll", func() bool { return f.polls.Load() >= 1 })
				return
			}
			if n := f.activeCount(); n != 0 {
				t.Errorf("active pollers = %d, want 0 (the config must be refused at start)", n)
			}
			// A refused start never launched a poller; give a stray one a
			// moment to show itself anyway.
			time.Sleep(50 * time.Millisecond)
			if n := f.polls.Load(); n != 0 {
				t.Errorf("Sitoo received %d requests from a refused start, want none", n)
			}
		})
	}
}

// AC 12: changing the resource or its query string starts over with a full
// read instead of resuming a watermark that belongs to another query.
func TestIncremental_ChangedQueryStartsOver(t *testing.T) {
	cases := []struct {
		name     string
		from, to string
	}{
		{"filter value", "orders?orderstate=10", "orders?orderstate=20"},
		{"filter added", "orders", "orders?orderstate=10"},
		{"filter removed", "orders?orderstate=10", "orders"},
		{"another filter added", "orders?orderstate=10", "orders?orderstate=10&customerref=7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, false, ords(100, 101, 102)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := incConfig(srv.URL, ov(10), 1000)
			cfg.Resource = tc.from
			poll(t, c, incTenant, incConn, cfg)
			assertOnce(t, "first poll", published(t, got, mu, incTenant, incConn), v1(100, 101, 102)...)
			f.take()

			// Unchanged: resumes from the watermark, publishes nothing.
			poll(t, c, incTenant, incConn, cfg)
			checkKeyset(t, "unchanged query", f.take(), "92", 1000)
			assertOnce(t, "unchanged query", published(t, got, mu, incTenant, incConn))

			// Changed: a full read, every order once, and the row now belongs
			// to the new query.
			changed := *cfg
			changed.Resource = tc.to
			poll(t, c, incTenant, incConn, &changed)
			checkKeyset(t, "changed query", f.take(), "", 1000)
			assertOnce(t, "changed query", published(t, got, mu, incTenant, incConn), v1(100, 101, 102)...)

			poll(t, c, incTenant, incConn, &changed)
			checkKeyset(t, "changed query, next poll", f.take(), "92", 1000)
			assertOnce(t, "changed query, next poll", published(t, got, mu, incTenant, incConn))
		})
	}
}

// cancelAwareStore fails a Save whose context is already done, the way the
// Postgres store does.
type cancelAwareStore struct {
	mu sync.Mutex
	checkpoint.Store
}

func (s *cancelAwareStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Store.Save(ctx, cp)
}

func (s *cancelAwareStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Store.Get(ctx, tenantID, connectionID, nodeID)
}

// AC 13: a stop that lands right after a page is published must not lose
// that page's save: the orders are already out.
func TestIncremental_SaveSurvivesStopAfterPublish(t *testing.T) {
	cases := []struct {
		name     string
		pageSize int
		wantW    string
	}{
		{"single page", 1000, "109"},
		{"stop after page 1 of several", 3, "103"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := newFakeSitoo(t, false, ords(idRange(101, 109)...)...)
			store := &cancelAwareStore{Store: checkpoint.NewInMemoryStore()}
			c, got, mu := newIncConsumer(store)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			inner := c.publish
			c.publish = func(pctx context.Context, env *envelope.Envelope) error {
				err := inner(pctx, env)
				cancel() // the stop arrives right after the first page went out
				return err
			}
			// After a stop the poll may report the cancellation; runPoller
			// ignores errors once its context is done.
			_ = c.fetchAndPublish(ctx, incConn, incTenant, incConfig(srv.URL, ov(10), tc.pageSize), c.logger)

			p := published(t, got, mu, incTenant, incConn)
			if len(p) == 0 {
				t.Fatal("nothing published")
			}
			if w := watermark(t, store, incTenant, incConn, incNode); w != tc.wantW {
				t.Errorf("watermark = %q after a stop right after the publish, want %q", w, tc.wantW)
			}
		})
	}
}

// failingStore cannot be read. It counts what is asked of it.
type failingStore struct {
	checkpoint.Store
	gets, saves int
}

func (s *failingStore) Get(context.Context, string, string, string) (*checkpoint.Checkpoint, error) {
	s.gets++
	return nil, errors.New("database is down")
}

func (s *failingStore) Save(context.Context, *checkpoint.Checkpoint) error {
	s.saves++
	return nil
}

// AC 13: a checkpoint store that cannot be read fails the poll before
// anything is fetched or published, instead of falling back to a full
// re-send.
func TestIncremental_UnreadableStoreFailsThePoll(t *testing.T) {
	f, srv := newFakeSitoo(t, false, ords(100, 101, 102)...)
	store := &failingStore{Store: checkpoint.NewInMemoryStore()}
	c, got, mu := newIncConsumer(store)
	err := c.fetchAndPublish(context.Background(), incConn, incTenant, incConfig(srv.URL, ov(10), 1000), c.logger)
	if err == nil || !strings.Contains(err.Error(), "database is down") {
		t.Fatalf("fetchAndPublish error = %v, want the store's error", err)
	}
	assertOnce(t, "poll with an unreadable store", published(t, got, mu, incTenant, incConn))
	if reqs := f.take(); len(reqs) != 0 {
		t.Errorf("made %d requests, want none (the checkpoint is read first)", len(reqs))
	}
	if store.saves != 0 {
		t.Errorf("saved %d checkpoints, want none", store.saves)
	}
}

// saveFailingStore reads fine but cannot write.
type saveFailingStore struct {
	checkpoint.Store
}

func (s *saveFailingStore) Save(context.Context, *checkpoint.Checkpoint) error {
	return errors.New("disk full")
}

// A save that fails fails the poll: it does not read further pages it could
// not record.
func TestIncremental_FailedSaveFailsThePoll(t *testing.T) {
	f, srv := newFakeSitoo(t, false, ords(idRange(101, 109)...)...)
	c, got, mu := newIncConsumer(&saveFailingStore{Store: checkpoint.NewInMemoryStore()})
	err := c.fetchAndPublish(context.Background(), incConn, incTenant, incConfig(srv.URL, ov(10), 3), c.logger)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("fetchAndPublish error = %v, want the save's error", err)
	}
	if reqs := f.take(); len(reqs) != 1 {
		t.Errorf("made %d requests, want 1 (the poll stops at the first failed save)", len(reqs))
	}
	assertOnce(t, "poll with a failing save", published(t, got, mu, incTenant, incConn), v1(101, 102, 103)...)
}

// AC 13: a row that cannot be used (State that is not JSON, a watermark that
// is not a number, State from another query) means a first poll: everything
// is read and published once, and the row is replaced by one the next poll
// resumes from.
func TestIncremental_UndecodableRowReadsEverythingOnce(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(cp *checkpoint.Checkpoint)
	}{
		{"state not JSON", func(cp *checkpoint.Checkpoint) { cp.State = json.RawMessage(`{not json`) }},
		{"watermark not a number", func(cp *checkpoint.Checkpoint) { cp.LastProcessedMessageID = "abc" }},
		{"watermark empty", func(cp *checkpoint.Checkpoint) { cp.LastProcessedMessageID = "" }},
		{"state from another query", func(cp *checkpoint.Checkpoint) { cp.State = json.RawMessage(`{}`) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, false, ords(idRange(100, 109)...)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := incConfig(srv.URL, ov(5), 4)

			// A real row first, then damage one part of it.
			poll(t, c, incTenant, incConn, cfg)
			published(t, got, mu, incTenant, incConn)
			f.take()
			cp, err := store.Get(context.Background(), incTenant, incConn, incNode)
			if err != nil || cp == nil {
				t.Fatalf("no checkpoint after the first poll (err %v)", err)
			}
			damaged := *cp
			tc.corrupt(&damaged)
			if err := store.Save(context.Background(), &damaged); err != nil {
				t.Fatal(err)
			}

			poll(t, c, incTenant, incConn, cfg)
			checkKeyset(t, "poll over the damaged row", f.take(), "", 4)
			assertOnce(t, "poll over the damaged row", published(t, got, mu, incTenant, incConn), v1(idRange(100, 109)...)...)

			cp, err = store.Get(context.Background(), incTenant, incConn, incNode)
			if err != nil || cp == nil {
				t.Fatalf("no checkpoint after the full read (err %v)", err)
			}
			if cp.LastProcessedMessageID != "109" {
				t.Errorf("replaced row's watermark = %q, want \"109\"", cp.LastProcessedMessageID)
			}
			if !json.Valid(cp.State) {
				t.Errorf("replaced row's state is not JSON: %s", cp.State)
			}

			poll(t, c, incTenant, incConn, cfg)
			checkKeyset(t, "poll after the row was replaced", f.take(), "104", 4)
			assertOnce(t, "poll after the row was replaced", published(t, got, mu, incTenant, incConn))
		})
	}
}
