package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Tests for the #472 review findings: config that cannot be polled safely is
// refused, a changed query starts over, the checkpoint survives a stop at the
// end of a poll, and an unreadable checkpoint fails the poll.

// cursor_overlap 0 is only safe when the filter returns the watermark's own
// value; anything else is refused before a request goes out.
func TestIncremental_ZeroOverlapNeedsInclusiveFilter(t *testing.T) {
	cases := []struct {
		name      string
		resource  string
		field     string
		param     string
		sort      string
		inclusive bool
		wantErr   bool
	}{
		{name: "products datelastmodified is exclusive", resource: "products", field: "datemodified",
			param: "datelastmodified", sort: "datemodified", wantErr: true},
		{name: "custom param, not declared", resource: "items", field: incField, param: incParam, sort: incField, wantErr: true},
		{name: "custom param on orders, not declared", resource: "orders", field: "datemodified", param: "datemodifiedfrom", wantErr: true},
		{name: "orders default is inclusive", resource: "orders"},
		{name: "custom param declared inclusive", resource: "items", field: incField, param: incParam, sort: incField, inclusive: true},
		{name: "products declared inclusive", resource: "products", field: "datemodified",
			param: "datelastmodified", sort: "datemodified", inclusive: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &SitooConfig{Resource: tc.resource, Incremental: true, CursorField: tc.field, CursorSort: tc.sort,
				CursorParam: tc.param, CursorInclusive: tc.inclusive, CursorOverlap: cur(0)}
			_, err := cfg.cursorSpec()
			if (err != nil) != tc.wantErr {
				t.Fatalf("cursorSpec error = %v, want error %v", err, tc.wantErr)
			}
			// Overlap 1 is fine either way.
			cfg.CursorOverlap = cur(1)
			if _, err := cfg.cursorSpec(); err != nil {
				t.Errorf("cursorSpec with overlap 1: %v", err)
			}
		})
	}

	// And the refusal reaches the poll: no request, nothing saved.
	f, srv := newFakeSitoo(t, "datemodified", "datelastmodified", recs(1, 2)...)
	store := checkpoint.NewInMemoryStore()
	c, got, mu := newIncConsumer(store)
	cfg := &SitooConfig{AccountID: 1, SiteID: 2, APIID: "x", APIPassword: "y", BaseURL: srv.URL,
		Resource: "products", Incremental: true, CursorOverlap: cur(0), NodeID: incNode,
		CursorField: "datemodified", CursorParam: "datelastmodified", CursorSort: "datemodified"}
	if err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger); err == nil {
		t.Fatal("fetchAndPublish accepted overlap 0 on an exclusive filter")
	}
	if reqs, _, _, _ := f.snapshot(); len(reqs) != 0 {
		t.Errorf("made %d requests, want none", len(reqs))
	}
	if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
		t.Errorf("published %v", p)
	}
}

// A cursor_param the poller sets itself, or a resource with '#', is refused.
func TestIncremental_RejectsReservedParamAndFragment(t *testing.T) {
	cases := []struct {
		name     string
		resource string
		param    string
		inc      bool
	}{
		{"param start", "items", "start", true},
		{"param num", "items", "num", true},
		{"param sort", "items", "sort", true},
		{"param upper case", "items", "Start", true},
		{"fragment in resource, incremental", "items#x", incParam, true},
		{"fragment in resource query, incremental", "orders?orderstate=10#x", "", true},
		{"fragment in resource, full read", "items#x", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, incField, incParam, recs(1, 2)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			field, sortBy := "", ""
			if tc.param != "" {
				field, sortBy = incField, incField
			}
			cfg := &SitooConfig{AccountID: 1, SiteID: 2, APIID: "x", APIPassword: "y", BaseURL: srv.URL,
				Resource: tc.resource, Incremental: tc.inc, CursorField: field, CursorParam: tc.param,
				CursorSort: sortBy, NodeID: incNode}
			err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger)
			if err == nil {
				t.Fatal("fetchAndPublish accepted the config, want an error")
			}
			// Refused for the reason under test, not some other gap in the config.
			want := "set by the poller"
			if strings.Contains(tc.resource, "#") {
				want = "'#'"
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to mention %s", err, want)
			}
			if reqs, _, _, _ := f.snapshot(); len(reqs) != 0 {
				t.Errorf("made %d requests, want none", len(reqs))
			}
			if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
				t.Errorf("published %v", p)
			}
		})
	}
}

// Changing what the node asks for (the resource, its own filter, the cursor
// param or field) starts over with a full read instead of resuming a
// watermark that belongs to another query.
func TestIncremental_ChangedQueryStartsOver(t *testing.T) {
	cases := []struct {
		name   string
		change func(*SitooConfig)
	}{
		{"resource query", func(c *SitooConfig) { c.Resource = "items?warehouseid=8" }},
		{"resource path", func(c *SitooConfig) { c.Resource = "warehouses/1/items?warehouseid=7" }},
		{"cursor param", func(c *SitooConfig) { c.CursorParam = "modifiedafter" }},
		{"cursor field", func(c *SitooConfig) { c.CursorField = "modified2" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The fake filters on whichever param the poller sends; it only
			// matters that the second poll sends none.
			f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := incConfig(srv.URL, cur(10), 1000)
			cfg.Resource = "items?warehouseid=7"
			poll(t, c, incTenant, incConn, cfg)
			assertPublishedExactly(t, published(t, got, mu, incTenant, incConn), v1("a", "b", "c")...)

			// Unchanged: resumes, sends the cursor, publishes nothing.
			f.takeRequests()
			poll(t, c, incTenant, incConn, cfg)
			if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
				t.Fatalf("unchanged query re-published %v", p)
			}
			if reqs := f.takeRequests(); len(reqs) == 0 || !reqs[0].Has(incParam) {
				t.Fatalf("unchanged query did not resume from the cursor: %v", reqs)
			}

			changed := *cfg
			tc.change(&changed)
			poll(t, c, incTenant, incConn, &changed)
			for i, q := range f.takeRequests() {
				for _, p := range []string{incParam, "modifiedafter"} {
					if q.Has(p) {
						t.Errorf("request %d after the change sent %s=%s, want a full read", i+1, p, q.Get(p))
					}
				}
			}
			// A full read is a first poll: the records go out again, once.
			assertPublishedExactly(t, published(t, got, mu, incTenant, incConn), v1("a", "b", "c")...)
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

// A stop that lands between the last publish and the save must not lose the
// save: the records are already out.
func TestIncremental_SaveSurvivesStopAfterLastPublish(t *testing.T) {
	_, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102)...)
	store := &cancelAwareStore{Store: checkpoint.NewInMemoryStore()}
	c, got, mu := newIncConsumer(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := c.publish
	c.publish = func(pctx context.Context, env *envelope.Envelope) error {
		err := inner(pctx, env)
		cancel() // the stop arrives right after the (only) page went out
		return err
	}
	if err := c.fetchAndPublish(ctx, incConn, incTenant, incConfig(srv.URL, cur(10), 1000), c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	assertPublishedExactly(t, published(t, got, mu, incTenant, incConn), v1("a", "b", "c")...)
	if w := watermark(t, store, incTenant, incConn, incNode); w != "102" {
		t.Errorf("watermark = %q after a stop at the end of the poll, want \"102\"", w)
	}
}

// failingStore cannot be read.
type failingStore struct {
	checkpoint.Store
	saves int
}

func (s *failingStore) Get(context.Context, string, string, string) (*checkpoint.Checkpoint, error) {
	return nil, errors.New("database is down")
}

func (s *failingStore) Save(context.Context, *checkpoint.Checkpoint) error {
	s.saves++
	return nil
}

// An unreadable checkpoint fails the poll before anything is fetched or
// published, instead of falling back to a full re-send.
func TestIncremental_UnreadableCheckpointFailsThePoll(t *testing.T) {
	f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102)...)
	store := &failingStore{Store: checkpoint.NewInMemoryStore()}
	c, got, mu := newIncConsumer(store)
	err := c.fetchAndPublish(context.Background(), incConn, incTenant, incConfig(srv.URL, cur(10), 1000), c.logger)
	if err == nil || !strings.Contains(err.Error(), "database is down") {
		t.Fatalf("fetchAndPublish error = %v, want the store's error", err)
	}
	if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
		t.Errorf("published %v with an unreadable checkpoint, want nothing", p)
	}
	if reqs, _, _, _ := f.snapshot(); len(reqs) != 0 {
		t.Errorf("made %d requests, want none", len(reqs))
	}
	if store.saves != 0 {
		t.Errorf("saved %d checkpoints, want none", store.saves)
	}
}
