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

// Tests for incremental polling of Sitoo orders (#457), written from the
// acceptance criteria in plans/issue-457-sitoo-incremental.md, not from the
// implementation. The cursor is fixed: field `orderid`, filter `orderidfrom`,
// `sort=orderid`. The fake Sitoo below serves a mutable collection of orders
// and can treat `orderidfrom` as ">=" or ">", ignore the sort or the filter,
// serve a repeated orderid or an order without one, and fail a request.

const (
	incTenant = "tenant-inc"
	incConn   = "conn-inc"
	incNode   = "node-inc"
)

// fakeOrder is one order in the fake collection. ID is what the fake filters
// and sorts on; Raw is how its orderid is served: "" the number itself, "-"
// left out, anything else that JSON verbatim (`"105"`, `105.5`, `null`).
// Every order also carries "ref" (= ID) and "v", which is how the tests
// recognise a published record whatever its orderid looks like.
type fakeOrder struct {
	ID  int64
	V   int
	Raw string
}

func (o *fakeOrder) encode() json.RawMessage {
	var b strings.Builder
	b.WriteString("{")
	switch o.Raw {
	case "":
		fmt.Fprintf(&b, `"orderid":%d,`, o.ID)
	case "-":
	default:
		fmt.Fprintf(&b, `"orderid":%s,`, o.Raw)
	}
	fmt.Fprintf(&b, `"ref":%d,"v":%d,"orderstate":10}`, o.ID, o.V)
	return json.RawMessage(b.String())
}

// fakeReq is one request the fake received.
type fakeReq struct {
	q      url.Values
	path   string
	uri    string
	served []int64 // ids served, in order; nil when the request failed
}

type fakeSitoo struct {
	mu sync.Mutex
	// exclusive makes orderidfrom ">" instead of the documented ">=".
	exclusive bool
	// ignoreSort serves Sitoo's default order (-orderid) whatever is asked.
	ignoreSort bool
	// ignoreFilter serves every order whatever orderidfrom says.
	ignoreFilter bool
	orders       []*fakeOrder
	reqs         []fakeReq
	// before runs (with mu held) before request n (1-based since the last
	// take) is answered. It may change orders; a non-zero return is sent as
	// the HTTP status instead of a page.
	before func(n int, q url.Values) int
}

func newFakeSitoo(t *testing.T, exclusive bool, orders ...*fakeOrder) (*fakeSitoo, *httptest.Server) {
	t.Helper()
	f := &fakeSitoo{exclusive: exclusive, orders: orders}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeSitoo) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	f.reqs = append(f.reqs, fakeReq{q: q, path: r.URL.Path, uri: r.RequestURI})
	n := len(f.reqs)
	if f.before != nil {
		if st := f.before(n, q); st != 0 {
			w.WriteHeader(st)
			_, _ = w.Write([]byte(`{"error":"injected"}`))
			return
		}
	}

	var from *int64
	if v := q.Get("orderidfrom"); v != "" && !f.ignoreFilter {
		p, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		from = &p
	}
	var match []*fakeOrder
	for _, o := range f.orders {
		if from == nil || o.ID > *from || (!f.exclusive && o.ID == *from) {
			match = append(match, o)
		}
	}
	asc := q.Get("sort") == "orderid" && !f.ignoreSort
	sort.SliceStable(match, func(i, j int) bool {
		if asc {
			return match[i].ID < match[j].ID
		}
		return match[i].ID > match[j].ID
	})
	start, _ := strconv.Atoi(q.Get("start"))
	num, _ := strconv.Atoi(q.Get("num"))
	if num <= 0 {
		num = 100
	}
	items := []json.RawMessage{}
	served := []int64{}
	for i := start; i < len(match) && i < start+num; i++ {
		items = append(items, match[i].encode())
		served = append(served, match[i].ID)
	}
	f.reqs[n-1].served = served
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sitooCollection{TotalCount: len(match), Items: items})
}

func (f *fakeSitoo) add(orders ...*fakeOrder) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orders = append(f.orders, orders...)
}

func (f *fakeSitoo) set(fn func(f *fakeSitoo)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeSitoo) setBefore(fn func(n int, q url.Values) int) {
	f.set(func(f *fakeSitoo) { f.before = fn })
}

// take returns the requests received since the last take and forgets them,
// so each assertion looks at one poll.
func (f *fakeSitoo) take() []fakeReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.reqs
	f.reqs = nil
	return out
}

func (f *fakeSitoo) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func ords(ids ...int64) []*fakeOrder {
	out := make([]*fakeOrder, len(ids))
	for i, id := range ids {
		out[i] = &fakeOrder{ID: id, V: 1}
	}
	return out
}

func idRange(from, to int64) []int64 {
	var out []int64
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func without(ids []int64, drop ...int64) []int64 {
	var out []int64
	for _, id := range ids {
		keep := true
		for _, d := range drop {
			if id == d {
				keep = false
			}
		}
		if keep {
			out = append(out, id)
		}
	}
	return out
}

func ov(n int64) *int64 { return &n }

var filterModes = []struct {
	name      string
	exclusive bool
}{
	{"orderidfrom inclusive", false},
	{"orderidfrom exclusive", true},
}

// pubRec is one record as it reached the pipeline.
type pubRec struct {
	ID int64 // the order's ref
	V  int
}

func (p pubRec) String() string { return fmt.Sprintf("%d@v%d", p.ID, p.V) }

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
		var items []struct {
			Ref int64 `json:"ref"`
			V   int   `json:"v"`
		}
		if err := json.Unmarshal(env.Payload, &items); err != nil {
			t.Fatalf("payload is not a JSON array of records: %v (%s)", err, env.Payload)
		}
		if len(items) == 0 {
			t.Errorf("published an envelope with no records: %s", env.Payload)
		}
		for _, it := range items {
			out = append(out, pubRec{ID: it.Ref, V: it.V})
		}
	}
	return out
}

func countRecs(recs ...[]pubRec) map[pubRec]int {
	m := map[pubRec]int{}
	for _, rs := range recs {
		for _, r := range rs {
			m[r]++
		}
	}
	return m
}

// assertOnce checks got holds exactly want, each exactly once.
func assertOnce(t *testing.T, label string, got []pubRec, want ...pubRec) {
	t.Helper()
	count := countRecs(got)
	for _, w := range want {
		if count[w] != 1 {
			t.Errorf("%s: order %s published %d times, want 1 (published: %v)", label, w, count[w], got)
		}
		delete(count, w)
	}
	for p, n := range count {
		t.Errorf("%s: order %s published %d times, want 0 (published: %v)", label, p, n, got)
	}
}

func v1(ids ...int64) []pubRec {
	out := make([]pubRec, len(ids))
	for i, id := range ids {
		out[i] = pubRec{ID: id, V: 1}
	}
	return out
}

func incConfig(srvURL string, overlap *int64, pageSize int) *SitooConfig {
	return &SitooConfig{
		AccountID: 1, SiteID: 2, APIID: "apiid", APIPassword: "pw",
		BaseURL: srvURL, Resource: "orders", PageSize: pageSize,
		Incremental: true, CursorOverlap: overlap, NodeID: incNode,
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

// fromFor is the orderidfrom a poll after watermark w should start at.
func fromFor(w int64, overlap *int64) string {
	o := int64(defaultCursorOverlap)
	if overlap != nil {
		o = *overlap
	}
	return strconv.FormatInt(max(0, w-o), 10)
}

func lastServed(t *testing.T, r fakeReq) int64 {
	t.Helper()
	if len(r.served) == 0 {
		t.Fatalf("request %s served nothing, so it has no last orderid", r.uri)
	}
	return r.served[len(r.served)-1]
}

// checkKeyset checks one poll's requests: every one is sort=orderid, start=0,
// num=num; the first asks from first ("" = no orderidfrom at all), every later
// one from the last orderid the previous page served.
func checkKeyset(t *testing.T, label string, reqs []fakeReq, first string, num int) {
	t.Helper()
	if len(reqs) == 0 {
		t.Errorf("%s: no requests", label)
		return
	}
	for i, r := range reqs {
		q := r.q
		if got := q["sort"]; len(got) != 1 || got[0] != "orderid" {
			t.Errorf("%s request %d: sort=%v, want [orderid] (%s)", label, i+1, got, r.uri)
		}
		if got := q["start"]; len(got) != 1 || got[0] != "0" {
			t.Errorf("%s request %d: start=%v, want [0] (%s)", label, i+1, got, r.uri)
		}
		if got := q["num"]; len(got) != 1 || got[0] != strconv.Itoa(num) {
			t.Errorf("%s request %d: num=%v, want [%d] (%s)", label, i+1, got, num, r.uri)
		}
		if i == 0 {
			if first == "" {
				if q.Has("orderidfrom") {
					t.Errorf("%s request 1: orderidfrom=%s, want none (a full read)", label, q.Get("orderidfrom"))
				}
			} else if got := q["orderidfrom"]; len(got) != 1 || got[0] != first {
				t.Errorf("%s request 1: orderidfrom=%v, want [%s]", label, got, first)
			}
			continue
		}
		prev := reqs[i-1].served
		if len(prev) == 0 {
			t.Errorf("%s request %d follows a page that served nothing", label, i+1)
			continue
		}
		want := strconv.FormatInt(prev[len(prev)-1], 10)
		if got := q["orderidfrom"]; len(got) != 1 || got[0] != want {
			t.Errorf("%s request %d: orderidfrom=%v, want [%s] (the previous page's last orderid)", label, i+1, got, want)
		}
	}
}

// AC 1: with incremental off nothing changes: every poll is an offset-paged
// full read that republishes everything, sends no orderidfrom and no sort,
// and never touches the checkpoint store, even with settings that incremental
// would refuse.
func TestIncremental_OffIsAFullReadEveryPoll(t *testing.T) {
	for _, resource := range []string{"transactions", "orders", "orders?orderstate=10"} {
		t.Run(resource, func(t *testing.T) {
			f, srv := newFakeSitoo(t, false, ords(idRange(1, 5)...)...)
			store := &failingStore{Store: checkpoint.NewInMemoryStore()}
			c, got, mu := newIncConsumer(store)
			cfg := incConfig(srv.URL, ov(-1), 2) // a negative overlap is only refused with incremental on
			cfg.Resource = resource
			cfg.Incremental = false

			for i := 1; i <= 2; i++ {
				poll(t, c, incTenant, incConn, cfg)
				assertOnce(t, fmt.Sprintf("poll %d", i), published(t, got, mu, incTenant, incConn), v1(idRange(1, 5)...)...)
				reqs := f.take()
				var starts []string
				for j, r := range reqs {
					starts = append(starts, r.q.Get("start"))
					if r.q.Has("orderidfrom") || r.q.Has("sort") {
						t.Errorf("poll %d request %d: %s, want no orderidfrom and no sort with incremental off", i, j+1, r.uri)
					}
					if r.q.Get("num") != "2" {
						t.Errorf("poll %d request %d: num=%s, want 2", i, j+1, r.q.Get("num"))
					}
				}
				if s := strings.Join(starts, ","); s != "0,2,4" {
					t.Errorf("poll %d: start values %s, want 0,2,4 (offset paging)", i, s)
				}
			}
			if store.saves != 0 || store.gets != 0 {
				t.Errorf("incremental off used the checkpoint store: %d gets, %d saves", store.gets, store.saves)
			}
		})
	}
}

// AC 2: no checkpoint means a full read: no orderidfrom on the first request,
// sort=orderid on every one, every order published once, and the checkpoint
// holds the highest orderid. A page size below 2 is raised to 2.
func TestIncremental_FirstPollReadsEverythingOnce(t *testing.T) {
	// Out of order on purpose: the watermark is the highest id, not the last.
	ids := []int64{105, 101, 109, 103, 107, 102, 108, 104, 106, 100}
	for _, fm := range filterModes {
		for _, ps := range []struct{ cfg, num int }{{0, defaultPageSize}, {1, 2}, {2, 2}, {3, 3}, {1000, 1000}} {
			t.Run(fmt.Sprintf("%s/page_size_%d", fm.name, ps.cfg), func(t *testing.T) {
				f, srv := newFakeSitoo(t, fm.exclusive, ords(ids...)...)
				store := checkpoint.NewInMemoryStore()
				c, got, mu := newIncConsumer(store)

				poll(t, c, incTenant, incConn, incConfig(srv.URL, nil, ps.cfg))

				assertOnce(t, "first poll", published(t, got, mu, incTenant, incConn), v1(ids...)...)
				checkKeyset(t, "first poll", f.take(), "", ps.num)
				if w := watermark(t, store, incTenant, incConn, incNode); w != "109" {
					t.Errorf("stored watermark = %q, want \"109\"", w)
				}
			})
		}
	}
}

// AC 3: the next poll asks for watermark − overlap (never below 0) and only
// orders not yet published go out; an unchanged collection publishes nothing.
func TestIncremental_NextPollAsksFromWatermarkMinusOverlap(t *testing.T) {
	cases := []struct {
		name    string
		base    int64 // orders base..base+9
		overlap *int64
	}{
		{"default overlap", 5000, nil},
		{"default overlap, clamped at 0", 100, nil},
		{"overlap 3", 5000, ov(3)},
		{"overlap 0", 5000, ov(0)},
		{"overlap larger than the ids", 5, ov(50)},
	}
	for _, fm := range filterModes {
		for _, tc := range cases {
			t.Run(fm.name+"/"+tc.name, func(t *testing.T) {
				w := tc.base + 9
				f, srv := newFakeSitoo(t, fm.exclusive, ords(idRange(tc.base, w)...)...)
				store := checkpoint.NewInMemoryStore()
				c, got, mu := newIncConsumer(store)
				cfg := incConfig(srv.URL, tc.overlap, 3)

				poll(t, c, incTenant, incConn, cfg)
				published(t, got, mu, incTenant, incConn)
				f.take()

				// Unchanged collection.
				poll(t, c, incTenant, incConn, cfg)
				checkKeyset(t, "unchanged poll", f.take(), fromFor(w, tc.overlap), 3)
				assertOnce(t, "unchanged poll", published(t, got, mu, incTenant, incConn))
				if got := watermark(t, store, incTenant, incConn, incNode); got != strconv.FormatInt(w, 10) {
					t.Errorf("watermark after an unchanged poll = %q, want %d", got, w)
				}

				// Three new orders.
				f.add(ords(w+1, w+2, w+5)...)
				poll(t, c, incTenant, incConn, cfg)
				checkKeyset(t, "poll with new orders", f.take(), fromFor(w, tc.overlap), 3)
				assertOnce(t, "poll with new orders", published(t, got, mu, incTenant, incConn), v1(w+1, w+2, w+5)...)
				if got := watermark(t, store, incTenant, incConn, incNode); got != strconv.FormatInt(w+5, 10) {
					t.Errorf("watermark = %q, want %d", got, w+5)
				}

				// The poll after that asks from the new watermark.
				poll(t, c, incTenant, incConn, cfg)
				checkKeyset(t, "poll after the advance", f.take(), fromFor(w+5, tc.overlap), 3)
				assertOnce(t, "poll after the advance", published(t, got, mu, incTenant, incConn))
			})
		}
	}
}

// AC 4: an order that becomes visible late (its id handed out before others
// it shows up after) is published once when its id is inside the overlap,
// and not at all when it is below it: a documented limit of the watermark.
func TestIncremental_LateOrders(t *testing.T) {
	cases := []struct {
		name     string
		initial  []int64
		overlap  int64
		pageSize int
		between  []int64 // added between poll 1 and poll 2
		during   []int64 // added before request duringAt of poll 1
		duringAt int
		want     []int64 // published exactly once over three polls
		never    []int64 // never published
	}{
		{name: "between polls, inside the overlap", initial: without(idRange(100, 110), 107), overlap: 5, pageSize: 3,
			between: []int64{107}, want: idRange(100, 110)},
		{name: "between polls, below the overlap", initial: without(idRange(100, 110), 103), overlap: 5, pageSize: 3,
			between: []int64{103}, want: without(idRange(100, 110), 103), never: []int64{103}},
		{name: "between polls, inside, with new orders past it", initial: without(idRange(100, 110), 108), overlap: 5,
			pageSize: 3, between: []int64{108, 111, 112}, want: idRange(100, 112)},
		{name: "during the poll, behind the read position", initial: without(idRange(100, 112), 104), overlap: 10,
			pageSize: 3, during: []int64{104}, duringAt: 3, want: idRange(100, 112)},
		{name: "during the poll, ahead of the read position", initial: idRange(100, 112), overlap: 10,
			pageSize: 3, during: []int64{150}, duringAt: 2, want: append(idRange(100, 112), 150)},
		{name: "during the poll, behind and below the overlap", initial: without(idRange(100, 130), 101), overlap: 5,
			pageSize: 4, during: []int64{101}, duringAt: 3, want: without(idRange(100, 130), 101), never: []int64{101}},
	}
	for _, fm := range filterModes {
		for _, tc := range cases {
			t.Run(fm.name+"/"+tc.name, func(t *testing.T) {
				f, srv := newFakeSitoo(t, fm.exclusive, ords(tc.initial...)...)
				store := checkpoint.NewInMemoryStore()
				c, got, mu := newIncConsumer(store)
				cfg := incConfig(srv.URL, ov(tc.overlap), tc.pageSize)
				if len(tc.during) > 0 {
					f.setBefore(func(n int, _ url.Values) int {
						if n == tc.duringAt {
							f.orders = append(f.orders, ords(tc.during...)...)
						}
						return 0
					})
				}
				poll(t, c, incTenant, incConn, cfg)
				if len(tc.during) > 0 && f.count() < tc.duringAt {
					t.Fatalf("poll 1 made %d requests; the late order was never added", f.count())
				}
				f.setBefore(nil)
				f.take()
				var all []pubRec
				all = append(all, published(t, got, mu, incTenant, incConn)...)

				f.add(ords(tc.between...)...)
				poll(t, c, incTenant, incConn, cfg)
				all = append(all, published(t, got, mu, incTenant, incConn)...)
				poll(t, c, incTenant, incConn, cfg)
				all = append(all, published(t, got, mu, incTenant, incConn)...)

				assertOnce(t, "three polls", all, v1(tc.want...)...)
				count := countRecs(all)
				for _, id := range tc.never {
					if count[pubRec{id, 1}] != 0 {
						t.Errorf("order %d, late and below the overlap, was published; the test's premise is wrong", id)
					}
				}
			})
		}
	}
}

// AC 5: a new consumer with the same checkpoint store resumes from the stored
// watermark and does not re-send what the overlap re-reads.
func TestIncremental_RestartResumesFromCheckpoint(t *testing.T) {
	for _, fm := range filterModes {
		for _, overlap := range []*int64{ov(0), ov(3), nil} {
			name := "default overlap"
			if overlap != nil {
				name = fmt.Sprintf("overlap %d", *overlap)
			}
			t.Run(fm.name+"/"+name, func(t *testing.T) {
				f, srv := newFakeSitoo(t, fm.exclusive, ords(idRange(100, 109)...)...)
				store := checkpoint.NewInMemoryStore()
				first, got1, mu1 := newIncConsumer(store)
				poll(t, first, incTenant, incConn, incConfig(srv.URL, overlap, 4))
				assertOnce(t, "before the restart", published(t, got1, mu1, incTenant, incConn), v1(idRange(100, 109)...)...)
				f.take()

				// A new consumer and a fresh config, as a restart loads it again.
				restarted, got2, mu2 := newIncConsumer(store)
				poll(t, restarted, incTenant, incConn, incConfig(srv.URL, overlap, 4))
				checkKeyset(t, "after the restart", f.take(), fromFor(109, overlap), 4)
				assertOnce(t, "after the restart", published(t, got2, mu2, incTenant, incConn))

				f.add(ords(110)...)
				poll(t, restarted, incTenant, incConn, incConfig(srv.URL, overlap, 4))
				assertOnce(t, "new order after the restart", published(t, got2, mu2, incTenant, incConn), v1(110)...)
				if w := watermark(t, store, incTenant, incConn, incNode); w != "110" {
					t.Errorf("watermark = %q, want \"110\"", w)
				}
			})
		}
	}
}

// AC 6: the checkpoint is keyed by (tenant, connection, node). The same node
// id under another tenant or connection, or another node in the same
// connection, keeps its own checkpoint, and its first poll is a full read.
func TestIncremental_CheckpointKeys(t *testing.T) {
	type key struct{ tenant, conn, node string }
	cases := []struct {
		name string
		a, b key
	}{
		{"other tenant, same connection and node", key{"tenant-a", "conn-1", incNode}, key{"tenant-b", "conn-1", incNode}},
		{"same tenant, other connection", key{"tenant-a", "conn-1", incNode}, key{"tenant-a", "conn-2", incNode}},
		{"same connection, other node", key{"tenant-a", "conn-1", "node-a"}, key{"tenant-a", "conn-1", "node-b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, false, ords(100, 101, 102)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfgA := incConfig(srv.URL, ov(10), 1000)
			cfgA.NodeID = tc.a.node
			cfgB := incConfig(srv.URL, ov(10), 1000)
			cfgB.NodeID = tc.b.node

			poll(t, c, tc.a.tenant, tc.a.conn, cfgA)
			assertOnce(t, "key a, first poll", published(t, got, mu, tc.a.tenant, tc.a.conn), v1(100, 101, 102)...)
			f.add(ords(103)...)
			f.take()

			poll(t, c, tc.b.tenant, tc.b.conn, cfgB)
			checkKeyset(t, "key b, first poll", f.take(), "", 1000)
			assertOnce(t, "key b, first poll", published(t, got, mu, tc.b.tenant, tc.b.conn), v1(100, 101, 102, 103)...)
			if w := watermark(t, store, tc.b.tenant, tc.b.conn, tc.b.node); w != "103" {
				t.Errorf("key b watermark = %q, want \"103\"", w)
			}
			if w := watermark(t, store, tc.a.tenant, tc.a.conn, tc.a.node); w != "102" {
				t.Errorf("key a watermark = %q, want \"102\" (untouched by key b's poll)", w)
			}

			// Key a still resumes from its own watermark.
			poll(t, c, tc.a.tenant, tc.a.conn, cfgA)
			checkKeyset(t, "key a, second poll", f.take(), "92", 1000)
			assertOnce(t, "key a, second poll", published(t, got, mu, tc.a.tenant, tc.a.conn), v1(103)...)
		})
	}
}

// An empty node id still checkpoints, under "sitoo-consumer", and resumes.
func TestIncremental_EmptyNodeIDUsesDefaultKey(t *testing.T) {
	f, srv := newFakeSitoo(t, false, ords(100, 101)...)
	store := checkpoint.NewInMemoryStore()
	c, got, mu := newIncConsumer(store)
	cfg := incConfig(srv.URL, ov(10), 1000)
	cfg.NodeID = ""

	poll(t, c, incTenant, incConn, cfg)
	assertOnce(t, "first poll", published(t, got, mu, incTenant, incConn), v1(100, 101)...)
	if w := watermark(t, store, incTenant, incConn, "sitoo-consumer"); w != "101" {
		t.Errorf("checkpoint under node id \"sitoo-consumer\" = %q, want \"101\"", w)
	}
	f.take()

	cfg2 := incConfig(srv.URL, ov(10), 1000)
	cfg2.NodeID = ""
	poll(t, c, incTenant, incConn, cfg2)
	checkKeyset(t, "second poll", f.take(), "91", 1000)
	assertOnce(t, "second poll", published(t, got, mu, incTenant, incConn))
}

// AC 7: a poll that fails on page k has saved pages 1 to k−1, and the next
// poll resumes after them: nothing is lost, and at most the failed page's
// orders go out twice (only when its publish failed after delivery). A first
// page that fails saves nothing.
func TestIncremental_FailureOnPageKKeepsEarlierPages(t *testing.T) {
	kinds := []string{"HTTP 500", "publish error"}
	phases := []string{"first poll", "later poll"}
	for _, fm := range filterModes {
		for _, kind := range kinds {
			for _, phase := range phases {
				for _, k := range []int{1, 2, 3} {
					t.Run(fmt.Sprintf("%s/%s/%s/page_%d", fm.name, kind, phase, k), func(t *testing.T) {
						f, srv := newFakeSitoo(t, fm.exclusive, ords(idRange(101, 110)...)...)
						store := checkpoint.NewInMemoryStore()
						c, got, mu := newIncConsumer(store)
						// Overlap 0, so every page of the later poll carries new orders.
						cfg := incConfig(srv.URL, ov(0), 3)
						var all []int64
						var prior []pubRec
						priorW := ""
						if phase == "later poll" {
							poll(t, c, incTenant, incConn, cfg)
							prior = published(t, got, mu, incTenant, incConn)
							priorW = watermark(t, store, incTenant, incConn, incNode)
							f.take()
							f.add(ords(idRange(111, 120)...)...)
							all = idRange(101, 120)
						} else {
							all = idRange(101, 110)
						}

						// Inject the failure on page k.
						failed := false
						switch kind {
						case "HTTP 500":
							f.setBefore(func(n int, _ url.Values) int {
								if n == k {
									failed = true
									return http.StatusInternalServerError
								}
								return 0
							})
						case "publish error":
							inner := c.publish
							c.publish = func(ctx context.Context, env *envelope.Envelope) error {
								if f.count() == k && !failed {
									failed = true
									_ = inner(ctx, env) // delivered, but reported as failed
									return errors.New("injected publish failure")
								}
								return inner(ctx, env)
							}
						}
						err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger)
						// take locks the fake, which orders the handler's write
						// of failed before the read below.
						reqs := f.take()
						if !failed {
							t.Fatalf("the failure was never injected on page %d", k)
						}
						if err == nil {
							t.Fatal("fetchAndPublish returned nil for a poll that failed")
						}
						if len(reqs) != k {
							t.Fatalf("failed poll made %d requests, want %d (it stops at the failed page)", len(reqs), k)
						}
						failedPoll := published(t, got, mu, incTenant, incConn)

						// Pages 1..k-1 went out and are saved.
						before := countRecs(prior)
						var earlier []pubRec
						seen := map[int64]bool{}
						for _, r := range reqs[:k-1] {
							for _, id := range r.served {
								if !seen[id] && before[pubRec{id, 1}] == 0 {
									earlier = append(earlier, pubRec{id, 1})
								}
								seen[id] = true
							}
						}
						var pageK []pubRec
						for _, id := range reqs[k-1].served {
							if !seen[id] && before[pubRec{id, 1}] == 0 {
								pageK = append(pageK, pubRec{id, 1})
							}
						}
						if kind == "HTTP 500" {
							assertOnce(t, "failed poll", failedPoll, earlier...)
						} else {
							assertOnce(t, "failed poll", failedPoll, append(append([]pubRec{}, earlier...), pageK...)...)
						}
						wantW := priorW
						if k > 1 {
							wantW = strconv.FormatInt(lastServed(t, reqs[k-2]), 10)
						}
						if w := watermark(t, store, incTenant, incConn, incNode); w != wantW {
							t.Errorf("watermark after failing on page %d = %q, want %q", k, w, wantW)
						}

						// The next poll resumes after the saved pages.
						f.setBefore(nil)
						poll(t, c, incTenant, incConn, cfg)
						first := ""
						if wantW != "" {
							first = wantW // overlap 0
						}
						checkKeyset(t, "recovery poll", f.take(), first, 3)
						recovery := published(t, got, mu, incTenant, incConn)

						total := countRecs(prior, failedPoll, recovery)
						dupOK := map[pubRec]bool{}
						if kind == "publish error" {
							for _, r := range pageK {
								dupOK[r] = true
							}
						}
						for _, id := range all {
							r := pubRec{id, 1}
							switch n := total[r]; {
							case n == 0:
								t.Errorf("order %d lost", id)
							case n == 2 && dupOK[r]:
							case n != 1:
								t.Errorf("order %d published %d times (only the failed page's orders may go out twice)", id, n)
							}
						}
						if len(total) != len(all) {
							t.Errorf("published %d distinct orders, want %d", len(total), len(all))
						}
						if w := watermark(t, store, incTenant, incConn, incNode); w != strconv.FormatInt(all[len(all)-1], 10) {
							t.Errorf("watermark after recovery = %q, want %d", w, all[len(all)-1])
						}
					})
				}
			}
		}
	}
}

// AC 8: a page that is not sorted ascending on orderid, repeats an orderid,
// shows Sitoo ignored orderidfrom, or holds an order without an integer
// orderid fails the poll before anything on that page is published. Earlier
// pages stay published and saved. A quoted integer orderid is fine.
func TestIncremental_PageChecks(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(f *fakeSitoo)
		wantErr  error // nil: the poll succeeds
		wantReqs int   // requests made, the last one being the bad page
	}{
		{name: "sort ignored (Sitoo's default -orderid)", setup: func(f *fakeSitoo) { f.ignoreSort = true },
			wantErr: errNotSorted, wantReqs: 1},
		{name: "repeated orderid", setup: func(f *fakeSitoo) { f.orders = append(f.orders, &fakeOrder{ID: 105, V: 2}) },
			// Pages: [101 102 103] [103 104 105] [105 105 106]
			wantErr: errNotSorted, wantReqs: 3},
		{name: "filter ignored", setup: func(f *fakeSitoo) { f.ignoreFilter = true },
			// Page 2 asks from 103 and gets 101 again.
			wantErr: errFilterIgnored, wantReqs: 2},
		{name: "order without orderid", setup: func(f *fakeSitoo) { f.orders[4].Raw = "-" },
			wantErr: errNoOrderID, wantReqs: 2},
		{name: "orderid null", setup: func(f *fakeSitoo) { f.orders[4].Raw = "null" },
			wantErr: errNoOrderID, wantReqs: 2},
		{name: "orderid a fraction", setup: func(f *fakeSitoo) { f.orders[4].Raw = "105.5" },
			wantErr: errNoOrderID, wantReqs: 2},
		{name: "orderid not a number", setup: func(f *fakeSitoo) { f.orders[4].Raw = `"abc"` },
			wantErr: errNoOrderID, wantReqs: 2},
		{name: "orderid an object", setup: func(f *fakeSitoo) { f.orders[4].Raw = `{"id":105}` },
			wantErr: errNoOrderID, wantReqs: 2},
		{name: "orderid a quoted integer is accepted", setup: func(f *fakeSitoo) { f.orders[4].Raw = `"105"` }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Inclusive filter, page size 3: pages [101 102 103] [103 104 105] ...
			f, srv := newFakeSitoo(t, false, ords(idRange(101, 109)...)...)
			f.set(tc.setup)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)

			err := c.fetchAndPublish(context.Background(), incConn, incTenant, incConfig(srv.URL, nil, 3), c.logger)
			reqs := f.take()
			p := published(t, got, mu, incTenant, incConn)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("fetchAndPublish: %v", err)
				}
				assertOnce(t, "poll", p, v1(idRange(101, 109)...)...)
				if w := watermark(t, store, incTenant, incConn, incNode); w != "109" {
					t.Errorf("watermark = %q, want \"109\"", w)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if len(reqs) != tc.wantReqs {
				t.Fatalf("made %d requests, want %d (the poll stops at the bad page)", len(reqs), tc.wantReqs)
			}
			// Exactly the pages before the bad one were published.
			var want []pubRec
			seen := map[int64]bool{}
			for _, r := range reqs[:len(reqs)-1] {
				for _, id := range r.served {
					if !seen[id] {
						want = append(want, pubRec{id, 1})
					}
					seen[id] = true
				}
			}
			assertOnce(t, "poll with a bad page", p, want...)
			wantW := ""
			if len(reqs) > 1 {
				wantW = strconv.FormatInt(lastServed(t, reqs[len(reqs)-2]), 10)
			}
			if w := watermark(t, store, incTenant, incConn, incNode); w != wantW {
				t.Errorf("watermark = %q, want %q (the last good page)", w, wantW)
			}
		})
	}
}

// AC 8, on a later poll: Sitoo ignoring orderidfrom on the first request
// fails the poll with nothing published and the watermark where it was.
func TestIncremental_FilterIgnoredOnALaterPoll(t *testing.T) {
	f, srv := newFakeSitoo(t, false, ords(idRange(101, 109)...)...)
	store := checkpoint.NewInMemoryStore()
	c, got, mu := newIncConsumer(store)
	cfg := incConfig(srv.URL, ov(2), 3)
	poll(t, c, incTenant, incConn, cfg)
	published(t, got, mu, incTenant, incConn)
	f.take()

	f.set(func(f *fakeSitoo) { f.ignoreFilter = true })
	f.add(ords(110)...)
	err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger)
	if !errors.Is(err, errFilterIgnored) {
		t.Fatalf("error = %v, want errFilterIgnored", err)
	}
	if reqs := f.take(); len(reqs) != 1 {
		t.Errorf("made %d requests, want 1", len(reqs))
	}
	assertOnce(t, "poll with the filter ignored", published(t, got, mu, incTenant, incConn))
	if w := watermark(t, store, incTenant, incConn, incNode); w != "109" {
		t.Errorf("watermark = %q, want \"109\" (unchanged)", w)
	}
}

// AC 9: whether Sitoo treats orderidfrom as ">=" or ">", every order is
// published exactly once, over polls with orders added between and during
// them, at several page sizes and overlaps.
func TestIncremental_InclusiveOrExclusiveFilter(t *testing.T) {
	for _, fm := range filterModes {
		for _, pageSize := range []int{2, 3, 7, 1000} {
			for _, overlap := range []*int64{ov(0), ov(4), nil} {
				name := "default overlap"
				if overlap != nil {
					name = fmt.Sprintf("overlap %d", *overlap)
				}
				t.Run(fmt.Sprintf("%s/page_size_%d/%s", fm.name, pageSize, name), func(t *testing.T) {
					f, srv := newFakeSitoo(t, fm.exclusive, ords(idRange(1, 20)...)...)
					store := checkpoint.NewInMemoryStore()
					c, got, mu := newIncConsumer(store)
					cfg := incConfig(srv.URL, overlap, pageSize)
					var all []pubRec

					poll(t, c, incTenant, incConn, cfg)
					checkKeyset(t, "poll 1", f.take(), "", pageSize)
					all = append(all, published(t, got, mu, incTenant, incConn)...)

					f.add(ords(idRange(21, 25)...)...)
					poll(t, c, incTenant, incConn, cfg)
					checkKeyset(t, "poll 2", f.take(), fromFor(20, overlap), pageSize)
					all = append(all, published(t, got, mu, incTenant, incConn)...)

					// 28 lands between polls; 29 and 30 while poll 3 reads its
					// second page (or after it, when poll 3 has only one page).
					f.add(ords(28)...)
					addedDuring := false
					f.setBefore(func(n int, _ url.Values) int {
						if n == 2 {
							f.orders = append(f.orders, ords(29, 30)...)
							addedDuring = true
						}
						return 0
					})
					poll(t, c, incTenant, incConn, cfg)
					f.setBefore(nil)
					checkKeyset(t, "poll 3", f.take(), fromFor(25, overlap), pageSize)
					all = append(all, published(t, got, mu, incTenant, incConn)...)
					if !addedDuring {
						f.add(ords(29, 30)...)
					}

					poll(t, c, incTenant, incConn, cfg)
					f.take()
					all = append(all, published(t, got, mu, incTenant, incConn)...)
					poll(t, c, incTenant, incConn, cfg)
					f.take()
					all = append(all, published(t, got, mu, incTenant, incConn)...)

					assertOnce(t, "all polls", all, v1(append(idRange(1, 25), 28, 29, 30)...)...)
					if w := watermark(t, store, incTenant, incConn, incNode); w != "30" {
						t.Errorf("watermark = %q, want \"30\"", w)
					}
				})
			}
		}
	}
}

// AC 10: a poll reads at most maxPagesPerPoll (100) pages, saves the
// watermark at the last of them, and the next poll carries on from there.
// Nothing is lost or published twice.
func TestIncremental_PageBudget(t *testing.T) {
	if maxPagesPerPoll != 100 {
		t.Errorf("maxPagesPerPoll = %d, want 100", maxPagesPerPoll)
	}
	const n = 250
	for _, fm := range filterModes {
		for _, overlap := range []int64{0, 10, 50} {
			t.Run(fmt.Sprintf("%s/overlap_%d", fm.name, overlap), func(t *testing.T) {
				f, srv := newFakeSitoo(t, fm.exclusive, ords(idRange(1, n)...)...)
				store := checkpoint.NewInMemoryStore()
				c, got, mu := newIncConsumer(store)
				cfg := incConfig(srv.URL, ov(overlap), 2)
				var all []pubRec

				pollOnce := func(label string) []fakeReq {
					t.Helper()
					err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger)
					// Running out of pages may or may not be reported as an
					// error; it must not be one of the page checks.
					if errors.Is(err, errNotSorted) || errors.Is(err, errFilterIgnored) || errors.Is(err, errNoOrderID) {
						t.Fatalf("%s: %v", label, err)
					}
					reqs := f.take()
					all = append(all, published(t, got, mu, incTenant, incConn)...)
					return reqs
				}

				reqs := pollOnce("poll 1")
				if len(reqs) != maxPagesPerPoll {
					t.Fatalf("poll 1 made %d requests, want exactly %d", len(reqs), maxPagesPerPoll)
				}
				checkKeyset(t, "poll 1", reqs, "", 2)
				w := lastServed(t, reqs[len(reqs)-1])
				if got := watermark(t, store, incTenant, incConn, incNode); got != strconv.FormatInt(w, 10) {
					t.Fatalf("watermark after poll 1 = %q, want %d (the 100th page's last orderid)", got, w)
				}
				assertOnce(t, "poll 1", all, v1(idRange(1, w)...)...)

				// Later polls carry on until the collection is read. Each of
				// these polls gets past its watermark, so the next one re-reads
				// the overlap from W − overlap rather than carrying on.
				for p := 2; ; p++ {
					if p > 10 {
						t.Fatal("still reading after 10 polls")
					}
					label := fmt.Sprintf("poll %d", p)
					reqs := pollOnce(label)
					if len(reqs) > maxPagesPerPoll {
						t.Errorf("%s made %d requests, more than %d", label, len(reqs), maxPagesPerPoll)
					}
					checkKeyset(t, label, reqs, fromFor(w, ov(overlap)), 2)
					stored, _ := strconv.ParseInt(watermark(t, store, incTenant, incConn, incNode), 10, 64)
					if stored <= w && len(reqs) == maxPagesPerPoll {
						t.Fatalf("%s used its whole budget without moving the watermark past %d", label, w)
					}
					w = stored
					if len(reqs) < maxPagesPerPoll {
						break
					}
				}
				if w != n {
					t.Errorf("final watermark = %d, want %d", w, n)
				}
				assertOnce(t, "all polls", all, v1(idRange(1, n)...)...)
			})
		}
	}
}

// AC 14: a resource with its own query string makes one well-formed URL per
// request, with the resource's parameters kept, incremental on or off.
func TestIncremental_ResourceQueryStringURL(t *testing.T) {
	cases := []struct {
		resource  string
		wantQuery map[string]string
	}{
		{"orders?orderstate=10", map[string]string{"orderstate": "10"}},
		{"orders?orderstate=10&customerref=a%20b", map[string]string{"orderstate": "10", "customerref": "a b"}},
	}
	for _, tc := range cases {
		for _, inc := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/incremental_%v", tc.resource, inc), func(t *testing.T) {
				f, srv := newFakeSitoo(t, false, ords(idRange(100, 104)...)...)
				store := checkpoint.NewInMemoryStore()
				c, got, mu := newIncConsumer(store)
				cfg := incConfig(srv.URL, ov(2), 2)
				cfg.Resource = tc.resource
				cfg.Incremental = inc
				poll(t, c, incTenant, incConn, cfg)
				poll(t, c, incTenant, incConn, cfg)
				published(t, got, mu, incTenant, incConn)
				reqs := f.take()
				if len(reqs) == 0 {
					t.Fatal("no requests")
				}
				for i, r := range reqs {
					if r.path != "/accounts/1/sites/2/orders" {
						t.Errorf("request %d path = %q, want /accounts/1/sites/2/orders", i+1, r.path)
					}
					if n := strings.Count(r.uri, "?"); n != 1 {
						t.Errorf("request %d URI %q has %d '?', want 1", i+1, r.uri, n)
					}
					for k, v := range tc.wantQuery {
						if got := r.q[k]; len(got) != 1 || got[0] != v {
							t.Errorf("request %d %s = %v, want [%q] (%s)", i+1, k, got, v, r.uri)
						}
					}
					for _, k := range []string{"start", "num"} {
						if got := r.q[k]; len(got) != 1 {
							t.Errorf("request %d %s = %v, want exactly one (%s)", i+1, k, got, r.uri)
						}
					}
					if inc {
						if got := r.q["sort"]; len(got) != 1 || got[0] != "orderid" {
							t.Errorf("request %d sort = %v, want [orderid] (%s)", i+1, got, r.uri)
						}
					}
				}
			})
		}
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
	c := *cp
	return l.s.Save(ctx, &c)
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
// restore starts the poller from the node config, the first poll publishes
// every order, later polls of an unchanged collection publish nothing, and a
// new order goes out alone. The database is sqlmock (connection lookup only).
func TestIncremental_ThroughHarness(t *testing.T) {
	const (
		connID = "conn-sitoo-inc"
		tenant = "tenant-sitoo-inc"
	)
	f, srv := newFakeSitoo(t, false, ords(100, 101, 102)...)

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
		"resource":"orders","poll_interval_seconds":1,"incremental":true,"cursor_overlap":10}}}]`, srv.URL)
	mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))

	store := &lockedStore{s: checkpoint.NewInMemoryStore()}
	c := &sitooConsumer{checkpoints: store}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "sitoo-consumer", DB: db})

	decode := func(env *envelope.Envelope) string {
		var items []struct {
			OrderID int64 `json:"orderid"`
		}
		if err := json.Unmarshal(env.Payload, &items); err != nil {
			t.Fatalf("payload: %v", err)
		}
		var ids []string
		for _, it := range items {
			ids = append(ids, strconv.FormatInt(it.OrderID, 10))
		}
		sort.Strings(ids)
		return strings.Join(ids, ",")
	}

	first := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 10*time.Second)
	if first.IntegrationID != connID {
		t.Fatalf("published for %s, want %s", first.IntegrationID, connID)
	}
	if ids := decode(first); ids != "100,101,102" {
		t.Fatalf("first envelope carries %s, want 100,101,102", ids)
	}
	harness.Eventually(t, 5*time.Second, "the first poll's checkpoint", func() bool {
		cp, _ := store.Get(context.Background(), tenant, connID, "in")
		return cp != nil && cp.LastProcessedMessageID == "102"
	})
	// Let at least one poll of the unchanged collection run.
	harness.Eventually(t, 5*time.Second, "a poll from the watermark", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, r := range f.reqs {
			if r.q.Get("orderidfrom") == "92" && r.served != nil {
				return true
			}
		}
		return false
	})

	f.add(ords(103)...)
	// The next envelope must be 103 alone: the polls in between published nothing.
	next := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 10*time.Second)
	if ids := decode(next); ids != "103" {
		t.Fatalf("envelope after adding 103 carries %s, want 103", ids)
	}
	harness.Eventually(t, 5*time.Second, "the checkpoint to advance to 103", func() bool {
		cp, _ := store.Get(context.Background(), tenant, connID, "in")
		return cp != nil && cp.LastProcessedMessageID == "103"
	})
}

// Found by the test author: with an overlap wider than a poll's page budget
// (page size 2, default overlap 1000), every poll after the first used to
// start at W − overlap, re-read the same 100 pages and never get past W.
// A poll cut short by the budget before getting past W is now carried on by
// the next one.
func TestIncremental_OverlapWiderThanThePageBudgetStillProgresses(t *testing.T) {
	// Page size 2 reads at most 200 ids a poll, so an overlap of 300 takes
	// more than one poll to re-read.
	const n, overlap = 600, 300
	for _, fm := range filterModes {
		t.Run(fm.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, fm.exclusive, ords(idRange(1, n)...)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := incConfig(srv.URL, ov(overlap), 2)
			var all []pubRec
			// A poll cut short before getting past the watermark saves where
			// it got to, and the next one must start further on. So every
			// poll either moves the watermark or reads further than the one
			// before, and the poller can't go round in circles.
			last, prevFrom, prevStuck := int64(0), int64(-1), false
			for p := 1; ; p++ {
				if p > 60 {
					t.Fatalf("still reading after 60 polls; watermark %d", last)
				}
				if err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger); err != nil {
					t.Fatalf("poll %d: %v", p, err)
				}
				reqs := f.take()
				all = append(all, published(t, got, mu, incTenant, incConn)...)
				from := int64(0)
				if v := reqs[0].q.Get("orderidfrom"); v != "" {
					from, _ = strconv.ParseInt(v, 10, 64)
				}
				if prevStuck && from <= prevFrom {
					t.Fatalf("poll %d starts at orderid %d, no further than the stuck poll before it (%d)", p, from, prevFrom)
				}
				w, _ := strconv.ParseInt(watermark(t, store, incTenant, incConn, incNode), 10, 64)
				cutShort := len(reqs) == maxPagesPerPoll && len(reqs[len(reqs)-1].served) == 2
				prevFrom, prevStuck = from, cutShort && w == last
				last = w
				if last == n && !cutShort {
					break
				}
			}
			if last != n {
				t.Errorf("final watermark = %d, want %d", last, n)
			}
			assertOnce(t, "all polls", all, v1(idRange(1, n)...)...)

			// Read to the end, the next poll goes back to re-reading the
			// overlap from W − overlap.
			if err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger); err != nil {
				t.Fatal(err)
			}
			reqs := f.take()
			checkKeyset(t, "after the end", reqs, fromFor(n, ov(overlap)), 2)
			if extra := published(t, got, mu, incTenant, incConn); len(extra) != 0 {
				t.Errorf("re-reading the overlap published %v", extra)
			}
		})
	}
}
