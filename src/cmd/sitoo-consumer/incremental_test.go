package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for incremental polling (#457), written against the acceptance
// criteria in plans/issue-457-sitoo-incremental.md. The fake Sitoo below
// serves a mutable collection, filtered on the cursor param as ">=", sorted
// by the cursor field (as sort-by-modified would), and paged by start/num.

const (
	incTenant = "tenant-inc"
	incConn   = "conn-inc"
	incNode   = "node-inc"
	incField  = "modified"
	incParam  = "modifiedfrom"
)

// fakeRecord is one record in the fake collection. Cursor nil means the
// record has no cursor field at all.
type fakeRecord struct {
	ID      string
	Cursor  *int64
	Version int
}

func cur(v int64) *int64 { return &v }

type fakeSitoo struct {
	mu    sync.Mutex
	field string // record field the cursor lives in
	param string // query param that filters on it (">=", or ">" when exclusive)
	// exclusive makes the filter ">" (like products' datelastmodified).
	exclusive bool
	// ignoreSort serves insertion order whatever sort is asked for.
	ignoreSort bool
	base       string // the server URL, for tests that build their own config
	recs       []*fakeRecord
	reqs       []url.Values // query of each request, in order
	paths      []string     // URL path of each request
	uris       []string     // raw request URI of each request
	served     [][]string   // ids served per request (nil when it failed)
	// cursor values served per request, in order (-1 for a record without one)
	servedCur [][]int64
	// before runs (with mu held) before request n (1-based) is answered. It
	// may change recs; a non-zero return is sent as the status instead.
	before func(n int, q url.Values) int
}

func newFakeSitoo(t *testing.T, field, param string, recs ...*fakeRecord) (*fakeSitoo, *httptest.Server) {
	t.Helper()
	f := &fakeSitoo{field: field, param: param, recs: recs}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeSitoo) encode(r *fakeRecord) json.RawMessage {
	m := map[string]any{"id": r.ID, "v": r.Version}
	if r.Cursor != nil {
		m[f.field] = *r.Cursor
	}
	b, _ := json.Marshal(m)
	return b
}

func (f *fakeSitoo) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	f.reqs = append(f.reqs, q)
	f.paths = append(f.paths, r.URL.Path)
	f.uris = append(f.uris, r.RequestURI)
	f.served = append(f.served, nil)
	f.servedCur = append(f.servedCur, nil)
	n := len(f.reqs)
	if f.before != nil {
		if st := f.before(n, q); st != 0 {
			w.WriteHeader(st)
			_, _ = w.Write([]byte(`{"error":"injected"}`))
			return
		}
	}

	var from *int64
	if v := q.Get(f.param); v != "" {
		p, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		from = &p
	}
	var match []*fakeRecord
	for _, rec := range f.recs {
		// A record without a cursor value cannot be filtered on; serve it.
		if from == nil || rec.Cursor == nil || *rec.Cursor > *from || (!f.exclusive && *rec.Cursor == *from) {
			match = append(match, rec)
		}
	}
	// Sorted on the cursor only when asked to, like a real listing; anything
	// else comes back in insertion order.
	if q.Get("sort") == f.field && !f.ignoreSort {
		sort.SliceStable(match, func(i, j int) bool {
			a, b := match[i].Cursor, match[j].Cursor
			if a == nil || b == nil {
				return a == nil && b != nil
			}
			return *a < *b
		})
	}
	start, _ := strconv.Atoi(q.Get("start"))
	num, _ := strconv.Atoi(q.Get("num"))
	if num <= 0 {
		num = 1000
	}
	items := []json.RawMessage{}
	var ids []string
	var curs []int64
	for i := start; i < len(match) && i < start+num; i++ {
		items = append(items, f.encode(match[i]))
		ids = append(ids, match[i].ID)
		cv := int64(-1)
		if match[i].Cursor != nil {
			cv = *match[i].Cursor
		}
		curs = append(curs, cv)
	}
	f.served[n-1] = ids
	f.servedCur[n-1] = curs
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sitooCollection{TotalCount: len(match), Items: items})
}

func (f *fakeSitoo) find(id string) *fakeRecord {
	for _, r := range f.recs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// update changes a record between polls: a new cursor value and new content.
func (f *fakeSitoo) update(id string, cursor int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.find(id)
	r.Cursor = cur(cursor)
	r.Version++
}

func (f *fakeSitoo) add(recs ...*fakeRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = append(f.recs, recs...)
}

func (f *fakeSitoo) setBefore(fn func(n int, q url.Values) int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.before = fn
}

// takeRequests returns the queries received so far and forgets them, so each
// assertion looks at one poll.
func (f *fakeSitoo) takeRequests() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.reqs
	f.reqs, f.paths, f.uris, f.served, f.servedCur = nil, nil, nil, nil, nil
	return out
}

// takePoll is takeRequests plus the cursor values each request served.
func (f *fakeSitoo) takePoll() ([]url.Values, [][]int64) {
	f.mu.Lock()
	curs := f.servedCur
	f.mu.Unlock()
	return f.takeRequests(), curs
}

// checkKeyset checks one poll pages by cursor value: the first request asks
// from first ("" = no filter), every later one from the previous page's last
// cursor value, and all of them from offset 0.
func checkKeyset(t *testing.T, label string, reqs []url.Values, curs [][]int64, first string) {
	t.Helper()
	if len(reqs) == 0 {
		t.Errorf("%s: no requests", label)
		return
	}
	for i, q := range reqs {
		if s := q.Get("start"); s != "0" {
			t.Errorf("%s request %d: start=%s, want 0", label, i+1, s)
		}
		if q.Get("sort") != incField {
			t.Errorf("%s request %d: sort=%q, want %q", label, i+1, q.Get("sort"), incField)
		}
		want := first
		if i > 0 {
			prev := curs[i-1]
			want = strconv.FormatInt(prev[len(prev)-1], 10)
		}
		if got := q.Get(incParam); got != want {
			t.Errorf("%s request %d: %s=%q, want %q", label, i+1, incParam, got, want)
		}
	}
}

func (f *fakeSitoo) snapshot() (reqs []url.Values, paths, uris []string, served [][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.reqs...), append([]string(nil), f.paths...),
		append([]string(nil), f.uris...), append([][]string(nil), f.served...)
}

func recs(cursors ...int64) []*fakeRecord {
	out := make([]*fakeRecord, len(cursors))
	for i, c := range cursors {
		out[i] = &fakeRecord{ID: string(rune('a' + i)), Cursor: cur(c), Version: 1}
	}
	return out
}

// pubRec is one record as it reached the pipeline.
type pubRec struct {
	ID      string
	Version int
}

func (p pubRec) String() string { return fmt.Sprintf("%s@v%d", p.ID, p.Version) }

// published drains what the consumer published since the last call and
// returns the records, checking every envelope on the way.
func published(t *testing.T, got *[]*envelope.Envelope, mu *sync.Mutex, tenant, conn string) []pubRec {
	t.Helper()
	mu.Lock()
	envs := *got
	*got = nil
	mu.Unlock()
	var out []pubRec
	for _, env := range envs {
		if env.TenantID != tenant || env.IntegrationID != conn {
			t.Errorf("envelope routed to %s/%s, want %s/%s", env.TenantID, env.IntegrationID, tenant, conn)
		}
		var items []map[string]any
		if err := json.Unmarshal(env.Payload, &items); err != nil {
			t.Fatalf("payload is not a JSON array of records: %v (%s)", err, env.Payload)
		}
		if len(items) == 0 {
			t.Errorf("published an envelope with no records: %s", env.Payload)
		}
		for _, it := range items {
			id, _ := it["id"].(string)
			v, _ := it["v"].(float64)
			out = append(out, pubRec{ID: id, Version: int(v)})
		}
	}
	return out
}

// assertPublishedExactly checks got holds exactly want, each exactly once.
func assertPublishedExactly(t *testing.T, got []pubRec, want ...pubRec) {
	t.Helper()
	count := map[pubRec]int{}
	for _, p := range got {
		count[p]++
	}
	for _, w := range want {
		if count[w] != 1 {
			t.Errorf("record %s published %d times, want 1 (published: %v)", w, count[w], got)
		}
		delete(count, w)
	}
	for p, n := range count {
		t.Errorf("record %s published %d times, want 0 (published: %v)", p, n, got)
	}
}

func v1(ids ...string) []pubRec {
	out := make([]pubRec, len(ids))
	for i, id := range ids {
		out[i] = pubRec{ID: id, Version: 1}
	}
	return out
}

func incConfig(srvURL string, overlap *int64, pageSize int) *SitooConfig {
	return &SitooConfig{
		AccountID: 1, SiteID: 2, APIID: "apiid", APIPassword: "pw",
		BaseURL: srvURL, Resource: "items", PageSize: pageSize,
		Incremental: true, CursorField: incField, CursorParam: incParam, CursorSort: incField,
		// The fake filters with >=, so it is inclusive and overlap 0 is allowed.
		CursorInclusive: true,
		CursorOverlap:   overlap, NodeID: incNode,
	}
}

// newIncConsumer is newTestConsumer with a checkpoint store.
func newIncConsumer(store checkpoint.Store) (*sitooConsumer, *[]*envelope.Envelope, *sync.Mutex) {
	c, got, mu := newTestConsumer()
	c.checkpoints = store
	return c, got, mu
}

func poll(t *testing.T, c *sitooConsumer, tenant, conn string, cfg *SitooConfig) {
	t.Helper()
	if err := c.fetchAndPublish(context.Background(), conn, tenant, cfg, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
}

// watermark returns the stored watermark for (tenant, conn, node), "" if none.
func watermark(t *testing.T, store checkpoint.Store, tenant, conn, node string) string {
	t.Helper()
	cp, err := store.Get(context.Background(), tenant, conn, node)
	if err != nil {
		t.Fatalf("checkpoint get: %v", err)
	}
	if cp == nil {
		return ""
	}
	return cp.LastProcessedMessageID
}

// AC 2: no checkpoint means a full read: the first request has no cursor
// param; every record goes out once and the checkpoint stores the highest
// cursor value. Later pages step forward by cursor value (keyset paging).
func TestIncremental_FirstPollPublishesAllAndStoresWatermark(t *testing.T) {
	for _, pageSize := range []int{1, 2, 3, 1000} {
		t.Run(fmt.Sprintf("page_size_%d", pageSize), func(t *testing.T) {
			// Out of insertion order on purpose: the watermark is the max, not the last.
			f, srv := newFakeSitoo(t, incField, incParam, recs(103, 100, 104, 101, 102)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)

			poll(t, c, incTenant, incConn, incConfig(srv.URL, nil, pageSize))

			assertPublishedExactly(t, published(t, got, mu, incTenant, incConn), v1("a", "b", "c", "d", "e")...)
			reqs, curs := f.takePoll()
			checkKeyset(t, "first poll", reqs, curs, "")
			wantNum := strconv.Itoa(max(pageSize, 2)) // a page size below 2 is raised to 2
			for i, q := range reqs {
				if q.Get("num") != wantNum {
					t.Errorf("request %d num = %s, want %s", i+1, q.Get("num"), wantNum)
				}
			}
			if w := watermark(t, store, incTenant, incConn, incNode); w != "104" {
				t.Errorf("stored watermark = %q, want \"104\"", w)
			}
		})
	}
}

// AC 3: the next poll asks for watermark - overlap and publishes only what
// is new or changed; an unchanged collection publishes nothing.
func TestIncremental_NextPollSendsCursorAndPublishesOnlyChanges(t *testing.T) {
	cases := []struct {
		name      string
		overlap   *int64
		wantParam string // watermark 104 minus the overlap
		wantNext  string // watermark 111 minus the overlap
	}{
		{"default overlap 60", nil, "44", "51"},
		{"overlap 10", cur(10), "94", "101"},
		{"overlap 0", cur(0), "104", "111"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102, 103, 104)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := incConfig(srv.URL, tc.overlap, 2)

			poll(t, c, incTenant, incConn, cfg)
			published(t, got, mu, incTenant, incConn)
			f.takeRequests()

			// Unchanged collection: cursor sent, nothing published.
			poll(t, c, incTenant, incConn, cfg)
			reqs, curs := f.takePoll()
			checkKeyset(t, "unchanged poll", reqs, curs, tc.wantParam)
			if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
				t.Errorf("unchanged collection published %v, want nothing", p)
			}
			if w := watermark(t, store, incTenant, incConn, incNode); w != "104" {
				t.Errorf("watermark after unchanged poll = %q, want \"104\"", w)
			}

			// One new record, one record changed (new cursor value, new content).
			f.add(&fakeRecord{ID: "f", Cursor: cur(110), Version: 1})
			f.update("c", 111)
			poll(t, c, incTenant, incConn, cfg)
			reqs, curs = f.takePoll()
			checkKeyset(t, "changed poll", reqs, curs, tc.wantParam)
			assertPublishedExactly(t, published(t, got, mu, incTenant, incConn),
				pubRec{"f", 1}, pubRec{"c", 2})
			if w := watermark(t, store, incTenant, incConn, incNode); w != "111" {
				t.Errorf("watermark after changed poll = %q, want \"111\"", w)
			}

			// And the poll after that asks from the new watermark.
			poll(t, c, incTenant, incConn, cfg)
			reqs, curs = f.takePoll()
			checkKeyset(t, "poll after advance", reqs, curs, tc.wantNext)
			if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
				t.Errorf("poll after advance published %v, want nothing", p)
			}
		})
	}
}

// AC 4: a record that changes mid-poll moves to the end of the sort order
// and shifts every record behind it by one. Plain offset paging would then
// skip the record that slid across the page boundary.
func TestIncremental_RecordChangedDuringPoll(t *testing.T) {
	cases := []struct {
		name     string
		pageSize int
		changeAt int    // request number the change lands before
		changed  string // record that changes
	}{
		// Page 1 is a,b,c; b moves to the end, so d slides back to index 2.
		{"first poll, change before page 2", 3, 2, "b"},
		// Page 1 is a,b; a moves; page 2 would have started at c's old slot.
		{"first poll, page size 2", 2, 2, "a"},
		// The change lands later: before page 3.
		{"first poll, change before page 3", 2, 3, "b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102, 103, 104, 105, 106)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			f.setBefore(func(n int, _ url.Values) int {
				if n == tc.changeAt {
					r := f.find(tc.changed)
					r.Cursor = cur(200)
					r.Version = 2
				}
				return 0
			})

			poll(t, c, incTenant, incConn, incConfig(srv.URL, cur(10), tc.pageSize))

			p := published(t, got, mu, incTenant, incConn)
			count := map[pubRec]int{}
			latest := map[string]bool{}
			for _, r := range p {
				count[r]++
			}
			for r, n := range count {
				if n != 1 {
					t.Errorf("record %s published %d times, want once", r, n)
				}
			}
			for _, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
				v := 1
				if id == tc.changed {
					v = 2
				}
				if count[pubRec{id, v}] == 0 {
					t.Errorf("latest version of %s (v%d) never published; published: %v", id, v, p)
				}
				latest[id] = true
			}
			for r := range count {
				if !latest[r.ID] {
					t.Errorf("published unknown record %s", r)
				}
			}
			if w := watermark(t, store, incTenant, incConn, incNode); w != "200" {
				t.Errorf("watermark = %q, want \"200\"", w)
			}
			// Paged by cursor value throughout, so the shift could not make it
			// skip anything.
			reqs, curs := f.takePoll()
			checkKeyset(t, "poll with a change", reqs, curs, "")

			// Nothing is left over for the next poll.
			f.setBefore(nil)
			poll(t, c, incTenant, incConn, incConfig(srv.URL, cur(10), tc.pageSize))
			if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
				t.Errorf("next poll published %v, want nothing", p)
			}
		})
	}
}

// The change-mid-poll case on an incremental (not first) poll, with the
// change landing inside the cursor-filtered window.
func TestIncremental_RecordChangedDuringIncrementalPoll(t *testing.T) {
	f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102, 103, 104)...)
	store := checkpoint.NewInMemoryStore()
	c, got, mu := newIncConsumer(store)
	cfg := incConfig(srv.URL, cur(10), 2)
	poll(t, c, incTenant, incConn, cfg)
	published(t, got, mu, incTenant, incConn)

	f.add(&fakeRecord{ID: "f", Cursor: cur(110), Version: 1}, &fakeRecord{ID: "g", Cursor: cur(111), Version: 1},
		&fakeRecord{ID: "h", Cursor: cur(112), Version: 1})
	// Window is >= 94: a,b,c,d,e,f,g,h. Page 1 is a,b; then a changes.
	f.setBefore(func(n int, _ url.Values) int {
		if n == 2 {
			r := f.find("a")
			r.Cursor = cur(150)
			r.Version = 2
		}
		return 0
	})
	f.takeRequests()
	poll(t, c, incTenant, incConn, cfg)
	assertPublishedExactly(t, published(t, got, mu, incTenant, incConn),
		pubRec{"f", 1}, pubRec{"g", 1}, pubRec{"h", 1}, pubRec{"a", 2})
	if w := watermark(t, store, incTenant, incConn, incNode); w != "150" {
		t.Errorf("watermark = %q, want \"150\"", w)
	}
}

// Constant churn (#472 review): the lowest record changes and moves to the
// end before every request after the first, so every page drifts. The poll
// still completes, publishes every record's latest version exactly once as
// of what it read, misses nothing that never changed, and advances the
// cursor. A churn budget stops the fake after a while so "latest" is fixed.
func TestIncremental_DriftEveryPageStillCompletes(t *testing.T) {
	cases := []struct {
		pageSize, records, changes int
	}{
		{3, 20, 15}, {5, 20, 15}, {8, 20, 15},
		// Long enough that a budget per poll (rather than per position) of
		// go-backs would run out.
		{5, 60, 80},
	}
	for _, tc := range cases {
		pageSize := tc.pageSize
		t.Run(fmt.Sprintf("page_size_%d_records_%d_changes_%d", tc.pageSize, tc.records, tc.changes), func(t *testing.T) {
			var cursors []int64
			for i := int64(0); i < int64(tc.records); i++ {
				cursors = append(cursors, 100+i)
			}
			f, srv := newFakeSitoo(t, incField, incParam, recs(cursors...)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			next := int64(1000)
			changes := 0
			f.setBefore(func(n int, _ url.Values) int {
				if n == 1 || changes >= tc.changes {
					return 0
				}
				var low *fakeRecord
				for _, r := range f.recs {
					if low == nil || *r.Cursor < *low.Cursor {
						low = r
					}
				}
				low.Cursor = cur(next)
				low.Version++
				next++
				changes++
				return 0
			})

			done := make(chan error, 1)
			go func() {
				done <- c.fetchAndPublish(context.Background(), incConn, incTenant, incConfig(srv.URL, cur(10), pageSize), c.logger)
			}()
			var err error
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("poll did not finish on a collection that drifts on every page")
			}
			if err != nil {
				t.Fatalf("fetchAndPublish: %v", err)
			}

			p := published(t, got, mu, incTenant, incConn)
			count := map[pubRec]int{}
			for _, r := range p {
				count[r]++
			}
			for r, n := range count {
				if n != 1 {
					t.Errorf("record %s published %d times, want once", r, n)
				}
			}
			// Every record that exists now must have gone out at its current
			// version: either read after its last change, or never changed.
			f.mu.Lock()
			final := map[string]int{}
			for _, r := range f.recs {
				final[r.ID] = r.Version
			}
			f.mu.Unlock()
			for id, v := range final {
				if count[pubRec{id, v}] == 0 {
					t.Errorf("record %s at its final version v%d never published (published: %v)", id, v, p)
				}
			}
			if w := watermark(t, store, incTenant, incConn, incNode); w == "" || w == "0" {
				t.Errorf("watermark = %q, want it advanced", w)
			} else if n, _ := strconv.ParseInt(w, 10, 64); n < int64(100+tc.records-1) {
				t.Errorf("watermark = %d, want at least %d (the highest cursor before any change)", n, 100+tc.records-1)
			}
		})
	}
}

// A record that appears mid-poll at or above where the poll is reading goes
// out in this poll. One that appears behind it (a late commit with an older
// cursor value) goes out in the next poll, as long as it is inside the
// overlap; that is what the overlap is for.
func TestIncremental_RecordInsertedDuringPoll(t *testing.T) {
	cases := []struct {
		name     string
		pageSize int
		cursor   int64 // cursor value of the record inserted before page 2
		thisPoll bool  // published by the poll it lands in
	}{
		{"ahead of the read position", 3, 150, true},
		{"ahead, page size 2", 2, 150, true},
		{"behind the read position, inside the overlap", 3, 101, false},
		{"behind, page size 2", 2, 101, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var all []*fakeRecord
			for i := 0; i < 10; i++ {
				all = append(all, &fakeRecord{ID: string(rune('a' + i)), Cursor: cur(int64(100 + 2*i)), Version: 1})
			}
			f, srv := newFakeSitoo(t, incField, incParam, all...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			f.setBefore(func(n int, _ url.Values) int {
				if n == 2 {
					f.recs = append(f.recs, &fakeRecord{ID: "z", Cursor: cur(tc.cursor), Version: 1})
				}
				return 0
			})
			cfg := incConfig(srv.URL, cur(60), tc.pageSize)
			poll(t, c, incTenant, incConn, cfg)
			first := published(t, got, mu, incTenant, incConn)
			f.setBefore(nil)
			poll(t, c, incTenant, incConn, cfg)
			second := published(t, got, mu, incTenant, incConn)

			want := append(v1("a", "b", "c", "d", "e", "f", "g", "h", "i", "j"), pubRec{"z", 1})
			assertPublishedExactly(t, append(first, second...), want...)
			inFirst := false
			for _, r := range first {
				if r.ID == "z" {
					inFirst = true
				}
			}
			if inFirst != tc.thisPoll {
				t.Errorf("z published in the poll it landed in: %v, want %v", inFirst, tc.thisPoll)
			}
		})
	}
}

// #472 review: at the default page size, the record at a page boundary
// changes in place (an order status change: same cursor value, new content),
// is deleted, or is the last record of all. Nothing fails, nothing is lost,
// nothing goes out twice, and the poll takes no more requests than the
// collection needs (the first version of this took about 50 times that).
func TestIncremental_BoundaryRecordChangesAtDefaultPageSize(t *testing.T) {
	cases := []struct {
		name     string
		records  int
		pageSize int
		at       int  // index of the record that changes before request 2
		del      bool // delete it instead of changing it in place
		maxReqs  int
	}{
		{"updated in place, page 1000", 2500, 1000, 999, false, 3},
		{"deleted, page 1000", 2500, 1000, 999, true, 3},
		{"updated in place, page 50", 2500, 50, 49, false, 52},
		{"last record updated, page 1000", 2000, 1000, 1999, false, 3},
		{"last record deleted, page 1000", 2000, 1000, 1999, true, 3},
		{"default page size, nothing changes", 2500, 0, -1, false, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			all := make([]*fakeRecord, tc.records)
			for i := range all {
				all[i] = &fakeRecord{ID: fmt.Sprintf("r%05d", i), Cursor: cur(int64(100000 + i)), Version: 1}
			}
			f, srv := newFakeSitoo(t, incField, incParam, all...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			var deleted string
			f.setBefore(func(n int, _ url.Values) int {
				if n == 2 && tc.at >= 0 {
					if tc.del {
						deleted = f.recs[tc.at].ID
						f.recs = append(f.recs[:tc.at], f.recs[tc.at+1:]...)
					} else {
						f.recs[tc.at].Version++
					}
				}
				return 0
			})
			poll(t, c, incTenant, incConn, incConfig(srv.URL, cur(10), tc.pageSize))

			reqs, curs := f.takePoll()
			checkKeyset(t, "poll", reqs, curs, "")
			if len(reqs) > tc.maxReqs {
				t.Errorf("poll took %d requests, want at most %d", len(reqs), tc.maxReqs)
			}
			count := map[pubRec]int{}
			for _, r := range published(t, got, mu, incTenant, incConn) {
				count[r]++
			}
			for r, n := range count {
				if n != 1 {
					t.Errorf("record %s published %d times, want once", r, n)
				}
			}
			for _, r := range f.recs {
				if count[pubRec{r.ID, r.Version}] != 1 {
					t.Errorf("record %s@v%d (its latest) published %d times, want once", r.ID, r.Version, count[pubRec{r.ID, r.Version}])
				}
			}
			if tc.del && deleted == "" {
				t.Fatal("nothing was deleted")
			}
			if w := watermark(t, store, incTenant, incConn, incNode); w == "" {
				t.Error("no checkpoint saved")
			}
		})
	}
}

// #472 review, finding 1: a listing that is not sorted on the cursor can
// lose a change for good (a record already read changes and keeps its place;
// a later record carries the watermark past the change). So incremental
// needs a sort; without one the config is refused, and if Sitoo ignores the
// sort the poll fails before publishing the unsorted page or saving.
func TestIncremental_UnsortedListingCannotLoseAnUpdate(t *testing.T) {
	newProducts := func(t *testing.T, ignoreSort bool) *fakeSitoo {
		var all []*fakeRecord
		for i := 0; i < 30; i++ {
			all = append(all, &fakeRecord{ID: fmt.Sprintf("p%02d", i), Cursor: cur(100 + int64(i)), Version: 1})
		}
		f, srv := newFakeSitoo(t, "datemodified", "datelastmodified", all...)
		f.mu.Lock()
		f.exclusive, f.ignoreSort = true, ignoreSort
		f.mu.Unlock()
		f.base = srv.URL
		return f
	}
	cfgFor := func(f *fakeSitoo, sortBy string) *SitooConfig {
		return &SitooConfig{AccountID: 1, SiteID: 2, APIID: "a", APIPassword: "p", BaseURL: f.base,
			Resource: "products", PageSize: 10, Incremental: true, NodeID: incNode,
			CursorField: "datemodified", CursorParam: "datelastmodified", CursorSort: sortBy}
	}
	// Mid-poll: p00 (read on page 1) changes, then p25 changes 10 minutes later.
	churn := func(f *fakeSitoo) {
		f.setBefore(func(n int, _ url.Values) int {
			if n == 2 {
				r := f.find("p00")
				r.Cursor, r.Version = cur(500), 2
			}
			if n == 3 {
				r := f.find("p25")
				r.Cursor, r.Version = cur(1100), 2
			}
			return 0
		})
	}

	t.Run("no cursor_sort is refused", func(t *testing.T) {
		f := newProducts(t, false)
		c, got, mu := newIncConsumer(checkpoint.NewInMemoryStore())
		if err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfgFor(f, ""), c.logger); err == nil {
			t.Fatal("incremental without cursor_sort accepted")
		}
		if reqs := f.takeRequests(); len(reqs) != 0 {
			t.Errorf("made %d requests, want none", len(reqs))
		}
		if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
			t.Errorf("published %v", p)
		}
	})

	t.Run("sort ignored by the server fails the poll", func(t *testing.T) {
		f := newProducts(t, true)
		churn(f)
		store := checkpoint.NewInMemoryStore()
		c, got, mu := newIncConsumer(store)
		// Insertion order is p00..p29, which happens to be sorted until p00
		// moves to 500 and stays first.
		err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfgFor(f, "datemodified"), c.logger)
		if !errors.Is(err, errNotSorted) {
			t.Fatalf("error = %v, want errNotSorted", err)
		}
		if w := watermark(t, store, incTenant, incConn, incNode); w != "" {
			t.Errorf("checkpoint %q saved by a poll over an unsorted listing", w)
		}
		for _, r := range published(t, got, mu, incTenant, incConn) {
			if r.Version != 1 {
				t.Errorf("published %s from an unsorted page", r)
			}
		}
	})

	t.Run("sorted listing publishes both changes", func(t *testing.T) {
		f := newProducts(t, false)
		churn(f)
		store := checkpoint.NewInMemoryStore()
		c, got, mu := newIncConsumer(store)
		cfg := cfgFor(f, "datemodified")
		poll(t, c, incTenant, incConn, cfg)
		f.setBefore(nil)
		poll(t, c, incTenant, incConn, cfg)
		count := map[pubRec]int{}
		for _, r := range published(t, got, mu, incTenant, incConn) {
			count[r]++
		}
		for _, want := range []pubRec{{"p00", 2}, {"p25", 2}} {
			if count[want] != 1 {
				t.Errorf("%s published %d times, want once", want, count[want])
			}
		}
		for r, n := range count {
			if n != 1 {
				t.Errorf("%s published %d times", r, n)
			}
		}
		if w := watermark(t, store, incTenant, incConn, incNode); w != "1100" {
			t.Errorf("watermark = %q, want \"1100\"", w)
		}
	})
}

// A server that ignores the cursor filter fails the poll before anything
// below the filter is published, and nothing is saved.
func TestIncremental_FilterIgnoredFailsThePoll(t *testing.T) {
	f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102, 103, 104)...)
	store := checkpoint.NewInMemoryStore()
	c, got, mu := newIncConsumer(store)
	cfg := incConfig(srv.URL, cur(0), 2)
	poll(t, c, incTenant, incConn, cfg)
	published(t, got, mu, incTenant, incConn)
	before := watermark(t, store, incTenant, incConn, incNode)

	f.mu.Lock()
	f.param = "somethingelse" // the fake no longer filters on what the poller sends
	f.mu.Unlock()
	f.add(&fakeRecord{ID: "x", Cursor: cur(105), Version: 1})
	err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger)
	if !errors.Is(err, errFilterIgnored) {
		t.Fatalf("error = %v, want errFilterIgnored", err)
	}
	if w := watermark(t, store, incTenant, incConn, incNode); w != before {
		t.Errorf("watermark moved from %q to %q on a failed poll", before, w)
	}
	if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
		t.Errorf("published %v", p)
	}
}

// More records than a page share one cursor value (a bulk import stamped in
// one second). The poll pages through that run by offset; a record in the
// run that changes mid-run moves out of it and shifts the rest, so the run is
// read again from its start. Everything goes out once.
func TestIncremental_RunOfEqualCursorValues(t *testing.T) {
	cases := []struct {
		name     string
		changeAt int // request number the change lands before (0 = none)
	}{
		{"no change", 0},
		{"change inside the run", 2},
		{"change later in the run", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var all []*fakeRecord
			for i := 0; i < 25; i++ {
				all = append(all, &fakeRecord{ID: fmt.Sprintf("b%02d", i), Cursor: cur(100), Version: 1})
			}
			all = append(all, &fakeRecord{ID: "after1", Cursor: cur(101), Version: 1},
				&fakeRecord{ID: "after2", Cursor: cur(102), Version: 1})
			f, srv := newFakeSitoo(t, incField, incParam, all...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			f.setBefore(func(n int, _ url.Values) int {
				if n == tc.changeAt {
					r := f.find("b03")
					r.Cursor, r.Version = cur(200), 2
				}
				return 0
			})
			cfg := incConfig(srv.URL, cur(10), 10)
			poll(t, c, incTenant, incConn, cfg)

			count := map[pubRec]int{}
			for _, r := range published(t, got, mu, incTenant, incConn) {
				count[r]++
			}
			for r, n := range count {
				if n != 1 {
					t.Errorf("record %s published %d times, want once", r, n)
				}
			}
			for _, r := range f.recs {
				if count[pubRec{r.ID, r.Version}] != 1 {
					t.Errorf("record %s@v%d (its latest) published %d times, want once", r.ID, r.Version, count[pubRec{r.ID, r.Version}])
				}
			}
			reqs := f.takeRequests()
			offset := false
			for _, q := range reqs {
				if q.Get("start") != "0" {
					offset = true
				}
			}
			if !offset {
				t.Errorf("never paged by offset through the run (requests: %v)", reqs)
			}
			// The next poll has nothing left to send.
			f.setBefore(nil)
			poll(t, c, incTenant, incConn, cfg)
			if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
				t.Errorf("next poll published %v", p)
			}
		})
	}
}

// A run that keeps changing under the poll would otherwise be read forever.
// The poll stops at its page cap (a few times what totalcount needs), fails
// without saving, and has sent each record at most once.
func TestIncremental_PageCapStopsAnEndlessPoll(t *testing.T) {
	var recs30 []json.RawMessage
	for i := 0; i < 30; i++ {
		recs30 = append(recs30, json.RawMessage(fmt.Sprintf(`{"id":"r%02d","v":1,%q:100}`, i, incField)))
	}
	var mu sync.Mutex
	requests := 0
	// Every request rotates the run by one, so the record a run page should
	// begin with is never there.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		num, _ := strconv.Atoi(r.URL.Query().Get("num"))
		var items []json.RawMessage
		for i := start; i < start+num && i < len(recs30); i++ {
			items = append(items, recs30[(i+n)%len(recs30)])
		}
		_ = json.NewEncoder(w).Encode(sitooCollection{TotalCount: len(recs30), Items: items})
	}))
	t.Cleanup(srv.Close)
	store := checkpoint.NewInMemoryStore()
	c, got, gmu := newIncConsumer(store)
	done := make(chan error, 1)
	go func() {
		done <- c.fetchAndPublish(context.Background(), incConn, incTenant, incConfig(srv.URL, cur(10), 10), c.logger)
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("poll never stopped")
	}
	if !errors.Is(err, errPageCap) {
		t.Fatalf("error = %v, want errPageCap", err)
	}
	wantCap := pageCapFactor*(30/10+1) + pageCapSlack
	mu.Lock()
	if requests != wantCap {
		t.Errorf("made %d requests, want the cap %d", requests, wantCap)
	}
	mu.Unlock()
	if w := watermark(t, store, incTenant, incConn, incNode); w != "" {
		t.Errorf("checkpoint %q saved by a capped poll", w)
	}
	count := map[pubRec]int{}
	for _, r := range published(t, got, gmu, incTenant, incConn) {
		count[r]++
		if count[r] > 1 {
			t.Errorf("record %s published more than once", r)
		}
	}
}

// AC 5: a new consumer with the same checkpoint store resumes from the
// stored cursor and does not re-send.
func TestIncremental_RestartResumesFromCheckpoint(t *testing.T) {
	for _, overlap := range []int64{0, 10} {
		t.Run(fmt.Sprintf("overlap_%d", overlap), func(t *testing.T) {
			f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102, 103, 104)...)
			store := checkpoint.NewInMemoryStore()
			first, got1, mu1 := newIncConsumer(store)
			cfg := incConfig(srv.URL, cur(overlap), 2)
			poll(t, first, incTenant, incConn, cfg)
			assertPublishedExactly(t, published(t, got1, mu1, incTenant, incConn), v1("a", "b", "c", "d", "e")...)
			f.takeRequests()

			restarted, got2, mu2 := newIncConsumer(store)
			// A fresh config too, as a restart loads it again from the database.
			poll(t, restarted, incTenant, incConn, incConfig(srv.URL, cur(overlap), 2))
			reqs, curs := f.takePoll()
			checkKeyset(t, "after restart", reqs, curs, strconv.FormatInt(104-overlap, 10))
			if p := published(t, got2, mu2, incTenant, incConn); len(p) != 0 {
				t.Errorf("restarted consumer re-sent %v, want nothing", p)
			}

			f.add(&fakeRecord{ID: "f", Cursor: cur(105), Version: 1})
			poll(t, restarted, incTenant, incConn, incConfig(srv.URL, cur(overlap), 2))
			assertPublishedExactly(t, published(t, got2, mu2, incTenant, incConn), pubRec{"f", 1})
		})
	}
}

// AC 6: the same node id under another tenant or another connection keeps
// its own checkpoint. The second key's first poll is a full read.
func TestIncremental_CheckpointsAreScopedPerTenantAndConnection(t *testing.T) {
	cases := []struct {
		name           string
		tenant, conn   string
		otherT, otherC string
	}{
		{"other tenant, same connection id", "tenant-a", "conn-1", "tenant-b", "conn-1"},
		{"same tenant, other connection", "tenant-a", "conn-1", "tenant-a", "conn-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := incConfig(srv.URL, cur(10), 1000)

			poll(t, c, tc.tenant, tc.conn, cfg)
			assertPublishedExactly(t, published(t, got, mu, tc.tenant, tc.conn), v1("a", "b", "c")...)
			f.add(&fakeRecord{ID: "d", Cursor: cur(103), Version: 1})
			f.takeRequests()

			poll(t, c, tc.otherT, tc.otherC, cfg)
			for i, q := range f.takeRequests() {
				if q.Has(incParam) {
					t.Errorf("first poll of %s/%s, request %d sent %s=%s, want a full read (cursor leaked across keys)",
						tc.otherT, tc.otherC, i+1, incParam, q.Get(incParam))
				}
			}
			assertPublishedExactly(t, published(t, got, mu, tc.otherT, tc.otherC), v1("a", "b", "c", "d")...)
			if w := watermark(t, store, tc.otherT, tc.otherC, incNode); w != "103" {
				t.Errorf("%s/%s watermark = %q, want \"103\"", tc.otherT, tc.otherC, w)
			}
			if w := watermark(t, store, tc.tenant, tc.conn, incNode); w != "102" {
				t.Errorf("%s/%s watermark = %q, want \"102\" (untouched by the other key's poll)", tc.tenant, tc.conn, w)
			}

			// The first key still resumes from its own cursor.
			poll(t, c, tc.tenant, tc.conn, cfg)
			for i, q := range f.takeRequests() {
				if q.Get(incParam) != "92" {
					t.Errorf("%s/%s request %d: %s=%q, want \"92\"", tc.tenant, tc.conn, i+1, incParam, q.Get(incParam))
				}
			}
			assertPublishedExactly(t, published(t, got, mu, tc.tenant, tc.conn), pubRec{"d", 1})
		})
	}
}

// Two nodes in one connection keep separate checkpoints; an empty node id
// still checkpoints (under a fixed default) and resumes.
func TestIncremental_NodeIDKeysTheCheckpoint(t *testing.T) {
	f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101)...)
	store := checkpoint.NewInMemoryStore()
	c, got, mu := newIncConsumer(store)

	cfgA := incConfig(srv.URL, cur(10), 1000)
	cfgA.NodeID = "node-a"
	cfgB := incConfig(srv.URL, cur(10), 1000)
	cfgB.NodeID = "node-b"
	poll(t, c, incTenant, incConn, cfgA)
	published(t, got, mu, incTenant, incConn)
	f.takeRequests()
	poll(t, c, incTenant, incConn, cfgB)
	for _, q := range f.takeRequests() {
		if q.Has(incParam) {
			t.Errorf("node-b's first poll sent %s=%s, want a full read", incParam, q.Get(incParam))
		}
	}
	assertPublishedExactly(t, published(t, got, mu, incTenant, incConn), v1("a", "b")...)

	cfgEmpty := incConfig(srv.URL, cur(10), 1000)
	cfgEmpty.NodeID = ""
	poll(t, c, incTenant, incConn, cfgEmpty)
	published(t, got, mu, incTenant, incConn)
	f.takeRequests()
	cfgEmpty2 := incConfig(srv.URL, cur(10), 1000)
	cfgEmpty2.NodeID = ""
	poll(t, c, incTenant, incConn, cfgEmpty2)
	for _, q := range f.takeRequests() {
		if q.Get(incParam) != "91" {
			t.Errorf("empty node id, second poll: %s=%q, want \"91\" (checkpoint under the default node id)", incParam, q.Get(incParam))
		}
	}
	if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
		t.Errorf("empty node id, second poll published %v, want nothing", p)
	}
}

// AC 7: a poll that fails midway leaves the checkpoint where it was; the next
// poll reads from the old watermark again (at-least-once).
func TestIncremental_FailedPollDoesNotAdvance(t *testing.T) {
	f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102, 103, 104)...)
	store := checkpoint.NewInMemoryStore()
	c, got, mu := newIncConsumer(store)
	cfg := incConfig(srv.URL, cur(0), 2)
	poll(t, c, incTenant, incConn, cfg)
	published(t, got, mu, incTenant, incConn)

	f.add(&fakeRecord{ID: "f", Cursor: cur(110), Version: 1}, &fakeRecord{ID: "g", Cursor: cur(111), Version: 1},
		&fakeRecord{ID: "h", Cursor: cur(112), Version: 1})
	f.takeRequests()
	// Window >= 104 is e,f,g,h. Page 1 (e,f) lands, page 2 gets a 500.
	f.setBefore(func(n int, _ url.Values) int {
		if n == 2 {
			return http.StatusInternalServerError
		}
		return 0
	})
	if err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger); err == nil {
		t.Fatal("fetchAndPublish returned nil when page 2 failed")
	}
	if w := watermark(t, store, incTenant, incConn, incNode); w != "104" {
		t.Errorf("watermark after a failed poll = %q, want \"104\" (unchanged)", w)
	}
	published(t, got, mu, incTenant, incConn) // f may have gone out before the failure

	f.setBefore(nil)
	f.takeRequests()
	poll(t, c, incTenant, incConn, cfg)
	reqs, curs := f.takePoll()
	checkKeyset(t, "poll after failure (from the old watermark)", reqs, curs, "104")
	p := published(t, got, mu, incTenant, incConn)
	for _, want := range []pubRec{{"g", 1}, {"h", 1}} {
		n := 0
		for _, r := range p {
			if r == want {
				n++
			}
		}
		if n != 1 {
			t.Errorf("after a failed poll, %s published %d times, want 1 (published: %v)", want, n, p)
		}
	}
	for _, r := range p {
		if r.ID != "f" && r.ID != "g" && r.ID != "h" {
			t.Errorf("after a failed poll, re-sent %s which the last complete poll already published", r)
		}
	}
	if w := watermark(t, store, incTenant, incConn, incNode); w != "112" {
		t.Errorf("watermark after recovery = %q, want \"112\"", w)
	}
}

// A failed first poll (no checkpoint yet) saves nothing.
func TestIncremental_FailedFirstPollSavesNothing(t *testing.T) {
	f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102, 103)...)
	store := checkpoint.NewInMemoryStore()
	c, _, _ := newIncConsumer(store)
	f.setBefore(func(n int, _ url.Values) int {
		if n == 2 {
			return http.StatusInternalServerError
		}
		return 0
	})
	if err := c.fetchAndPublish(context.Background(), incConn, incTenant, incConfig(srv.URL, nil, 2), c.logger); err == nil {
		t.Fatal("fetchAndPublish returned nil when page 2 failed")
	}
	if w := watermark(t, store, incTenant, incConn, incNode); w != "" {
		t.Errorf("failed first poll saved watermark %q", w)
	}
}

// Records without a cursor value are published, never dropped; they do not
// move the watermark and are not remembered.
func TestIncremental_RecordWithoutCursorIsPublishedEveryPoll(t *testing.T) {
	all := append(recs(100, 101), &fakeRecord{ID: "x", Cursor: nil, Version: 1})
	_, srv := newFakeSitoo(t, incField, incParam, all...)
	store := checkpoint.NewInMemoryStore()
	c, got, mu := newIncConsumer(store)
	cfg := incConfig(srv.URL, cur(10), 1000)

	poll(t, c, incTenant, incConn, cfg)
	assertPublishedExactly(t, published(t, got, mu, incTenant, incConn), v1("a", "b", "x")...)
	if w := watermark(t, store, incTenant, incConn, incNode); w != "101" {
		t.Errorf("watermark = %q, want \"101\"", w)
	}
	poll(t, c, incTenant, incConn, cfg)
	assertPublishedExactly(t, published(t, got, mu, incTenant, incConn), pubRec{"x", 1})
}

// AC 8 and AC 9: URL shape for a resource with its own query string, and the
// per-resource defaults.
func TestIncremental_URLAndResourceDefaults(t *testing.T) {
	cases := []struct {
		name      string
		resource  string
		field     string // record field the fake keeps the cursor in
		param     string // param the poller must send
		cfgField  string
		cfgParam  string
		cfgSort   string
		wantPath  string
		wantQuery map[string]string // must be present on every request
		wantSort  string            // "" means no sort param
		wantFirst string            // param on the second poll's first request
	}{
		{name: "orders defaults", resource: "orders", field: "orderid", param: "orderidfrom",
			wantPath: "/accounts/1/sites/2/orders", wantSort: "orderid", wantFirst: "944"},
		{name: "orders with a query string", resource: "orders?orderstate=10", field: "orderid", param: "orderidfrom",
			wantPath: "/accounts/1/sites/2/orders", wantQuery: map[string]string{"orderstate": "10"}, wantSort: "orderid",
			wantFirst: "944"},
		{name: "products with its cursor configured", resource: "products", field: "datemodified", param: "datelastmodified",
			cfgField: "datemodified", cfgParam: "datelastmodified", cfgSort: "datemodified",
			wantPath: "/accounts/1/sites/2/products", wantSort: "datemodified", wantFirst: "943"},
		{name: "custom resource with sort and query", resource: "/warehouseitems?warehouseid=7&x=a%20b",
			field: "moddate", param: "datelastmodifiedfrom", cfgField: "moddate", cfgParam: "datelastmodifiedfrom",
			cfgSort: "moddate", wantPath: "/accounts/1/sites/2/warehouseitems",
			wantQuery: map[string]string{"warehouseid": "7", "x": "a b"}, wantSort: "moddate", wantFirst: "943"},
		{name: "orders with explicit overrides", resource: "orders", field: "datemodified", param: "datemodifiedfrom",
			cfgField: "datemodified", cfgParam: "datemodifiedfrom", cfgSort: "datemodified",
			wantPath: "/accounts/1/sites/2/orders", wantSort: "datemodified", wantFirst: "943"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, tc.field, tc.param, recs(1000, 1001, 1002, 1003, 1004)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := &SitooConfig{
				AccountID: 1, SiteID: 2, APIID: "apiid", APIPassword: "pw",
				BaseURL: srv.URL, Resource: tc.resource, PageSize: 2, Incremental: true,
				CursorField: tc.cfgField, CursorParam: tc.cfgParam, CursorSort: tc.cfgSort, NodeID: incNode,
			}
			poll(t, c, incTenant, incConn, cfg)
			assertPublishedExactly(t, published(t, got, mu, incTenant, incConn), v1("a", "b", "c", "d", "e")...)
			if w := watermark(t, store, incTenant, incConn, incNode); w != "1004" {
				t.Errorf("watermark = %q, want \"1004\" (cursor read from %q)", w, tc.field)
			}
			firstPollReqs, _, _, _ := f.snapshot()
			poll(t, c, incTenant, incConn, cfg)
			if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
				t.Errorf("second poll published %v, want nothing", p)
			}

			reqs, paths, uris, _ := f.snapshot()
			n1 := len(firstPollReqs)
			if firstPollReqs[0].Has(tc.param) {
				t.Errorf("first poll's first request sent %s=%s, want a full read", tc.param, firstPollReqs[0].Get(tc.param))
			}
			if n1 >= len(reqs) {
				t.Fatalf("second poll made no requests")
			}
			if v := reqs[n1].Get(tc.param); v != tc.wantFirst {
				t.Errorf("second poll's first request %s = %q, want %q (1004 - default overlap 60)", tc.param, v, tc.wantFirst)
			}
			for i, q := range reqs {
				if paths[i] != tc.wantPath {
					t.Errorf("request %d path = %q, want %q", i+1, paths[i], tc.wantPath)
				}
				if n := strings.Count(uris[i], "?"); n != 1 {
					t.Errorf("request %d URI %q has %d '?', want 1", i+1, uris[i], n)
				}
				for k, v := range tc.wantQuery {
					if got := q[k]; len(got) != 1 || got[0] != v {
						t.Errorf("request %d %s = %v, want [%q]", i+1, k, got, v)
					}
				}
				if !q.Has("start") || !q.Has("num") {
					t.Errorf("request %d lacks start/num: %s", i+1, uris[i])
				}
				if s := q.Get("sort"); s != tc.wantSort {
					t.Errorf("request %d sort = %q, want %q", i+1, s, tc.wantSort)
				}
				if i >= n1 && !q.Has(tc.param) {
					t.Errorf("request %d of the second poll has no %s", i+1, tc.param)
				}
			}
		})
	}
}

// AC 9: other resources need both cursor_field and cursor_param.
func TestIncremental_UnknownResourceWithoutCursorSettingsErrors(t *testing.T) {
	cases := []struct {
		name, resource, field, param string
	}{
		{"neither", "warehouseitems", "", ""},
		{"field only", "warehouseitems", "moddate", ""},
		{"param only", "warehouseitems", "", "datelastmodifiedfrom"},
		{"default resource", "", "", ""},
		{"transactions", "transactions", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, "moddate", "datelastmodifiedfrom", recs(1, 2)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := &SitooConfig{
				AccountID: 1, SiteID: 2, APIID: "apiid", APIPassword: "pw", BaseURL: srv.URL,
				Resource: tc.resource, Incremental: true, CursorField: tc.field, CursorParam: tc.param, NodeID: incNode,
			}
			if err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger); err == nil {
				t.Fatal("fetchAndPublish returned nil for an incremental resource with no cursor settings")
			}
			if p := published(t, got, mu, incTenant, incConn); len(p) != 0 {
				t.Errorf("published %v, want nothing", p)
			}
			if reqs, _, _, _ := f.snapshot(); len(reqs) != 0 {
				t.Errorf("made %d requests, want none", len(reqs))
			}
			if w := watermark(t, store, incTenant, incConn, incNode); w != "" {
				t.Errorf("saved watermark %q", w)
			}
		})
	}
}

// AC 1: with incremental off nothing changes: every poll is a full read,
// republishes everything, sends no cursor and saves no checkpoint, even with
// cursor settings present.
func TestIncremental_OffRepublishesEverything(t *testing.T) {
	for _, resource := range []string{"items", "orders?orderstate=10"} {
		t.Run(resource, func(t *testing.T) {
			f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := incConfig(srv.URL, cur(10), 2)
			cfg.Resource = resource
			cfg.Incremental = false

			for i := 0; i < 2; i++ {
				poll(t, c, incTenant, incConn, cfg)
				p := published(t, got, mu, incTenant, incConn)
				if len(p) < 3 {
					t.Errorf("poll %d published %v, want every record", i+1, p)
				}
				seen := map[string]bool{}
				for _, r := range p {
					seen[r.ID] = true
				}
				for _, id := range []string{"a", "b", "c"} {
					if !seen[id] {
						t.Errorf("poll %d did not publish %s", i+1, id)
					}
				}
			}
			reqs, _, uris, _ := f.snapshot()
			for i, q := range reqs {
				if q.Has(incParam) {
					t.Errorf("request %d sent %s with incremental off", i+1, incParam)
				}
				if n := strings.Count(uris[i], "?"); n != 1 {
					t.Errorf("request %d URI %q has %d '?', want 1", i+1, uris[i], n)
				}
				if strings.HasPrefix(resource, "orders?") && q.Get("orderstate") != "10" {
					t.Errorf("request %d lost orderstate=10: %s", i+1, uris[i])
				}
			}
			if w := watermark(t, store, incTenant, incConn, incNode); w != "" {
				t.Errorf("incremental off saved watermark %q", w)
			}
		})
	}
}

// lockedStore makes the in-memory store safe for the poller goroutine and
// the test goroutine at once.
type lockedStore struct {
	mu sync.Mutex
	s  *checkpoint.InMemoryStore
}

func (l *lockedStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.Save(ctx, cp)
}

func (l *lockedStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp, err := l.s.Get(ctx, tenantID, connectionID, nodeID)
	if cp != nil {
		c := *cp
		cp = &c
	}
	return cp, err
}

func (l *lockedStore) Delete(ctx context.Context, tenantID, connectionID, nodeID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.Delete(ctx, tenantID, connectionID, nodeID)
}

func (l *lockedStore) DeleteForConnection(ctx context.Context, tenantID, connectionID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.DeleteForConnection(ctx, tenantID, connectionID)
}

// End to end through the SDK runner and an embedded JetStream: the boot
// restore starts the poller, the first poll publishes everything, later
// polls of an unchanged collection publish nothing, and a new record goes
// out alone. The database is sqlmock (connection lookup only).
func TestIncremental_ThroughHarness(t *testing.T) {
	const (
		connID = "conn-sitoo-inc"
		tenant = "tenant-sitoo-inc"
	)
	f, srv := newFakeSitoo(t, incField, incParam, recs(100, 101, 102)...)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery("SELECT id::text, tenant_id FROM connections").WithArgs("sitoo").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id"}).AddRow(connID, tenant))
	nodes := fmt.Sprintf(`[{"id":"in","type":"consumer","config":{"type":"sitoo","sitoo":{
		"account_id":1,"site_id":2,"api_id":"apiid","api_password":"pw","base_url":%q,
		"resource":"items","poll_interval_seconds":1,"incremental":true,
		"cursor_field":%q,"cursor_param":%q,"cursor_sort":%q,"cursor_inclusive":true,
		"cursor_overlap":10}}}]`, srv.URL, incField, incParam, incField)
	mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))

	store := &lockedStore{s: checkpoint.NewInMemoryStore()}
	c := &sitooConsumer{checkpoints: store}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "sitoo-consumer", DB: db})

	decode := func(env *envelope.Envelope) []string {
		var items []map[string]any
		if err := json.Unmarshal(env.Payload, &items); err != nil {
			t.Fatalf("payload: %v", err)
		}
		var ids []string
		for _, it := range items {
			ids = append(ids, fmt.Sprint(it["id"]))
		}
		sort.Strings(ids)
		return ids
	}

	first := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 10*time.Second)
	if first.IntegrationID != connID {
		t.Fatalf("published for %s, want %s", first.IntegrationID, connID)
	}
	if ids := decode(first); strings.Join(ids, ",") != "a,b,c" {
		t.Fatalf("first envelope carries %v, want [a b c]", ids)
	}
	harness.Eventually(t, 5*time.Second, "the first poll's checkpoint", func() bool {
		cp, _ := store.Get(context.Background(), tenant, connID, "in")
		return cp != nil && cp.LastProcessedMessageID == "102"
	})
	// Let at least one more poll of the unchanged collection run.
	harness.Eventually(t, 5*time.Second, "a poll with the cursor", func() bool {
		reqs, _, _, _ := f.snapshot()
		for _, q := range reqs {
			if q.Get(incParam) == "92" {
				return true
			}
		}
		return false
	})

	f.add(&fakeRecord{ID: "d", Cursor: cur(103), Version: 1})
	// The next envelope must be d alone: the polls in between published nothing.
	next := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 10*time.Second)
	if ids := decode(next); strings.Join(ids, ",") != "d" {
		t.Fatalf("envelope after adding d carries %v, want [d]", ids)
	}
	harness.Eventually(t, 5*time.Second, "the checkpoint to advance to 103", func() bool {
		cp, _ := store.Get(context.Background(), tenant, connID, "in")
		return cp != nil && cp.LastProcessedMessageID == "103"
	})
}
