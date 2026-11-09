package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

// #733: an incremental poll asks for the feed in cursor order, so one that
// stops early (page cap, a next link off the API, a failed page) stores how
// far it got and the next poll continues there. These tests run polls until
// one completes and check that every record arrived exactly once.

// orderedBC is a Business Central that honours `<field> gt|ge <literal>`,
// `$orderby=<field> asc` and server-driven paging with a next link. Without
// $orderby it returns key (id) order, like BC, which is not cursor order.
// Like BC's, its $skiptoken is the sort key of the last record served, so a
// record edited while a poll pages moves rather than shifting the pages.
type orderedBC struct {
	srv   *httptest.Server
	tok   *httptest.Server
	field string
	size  int // records per page

	mu   sync.Mutex
	recs map[string]string // id → cursor value
	// firstPages is the query of every first-page request (no $skiptoken).
	firstPages []url.Values
	// fail, when set, is asked before a page is served, with the offset of
	// its first record: "500" fails it, "off-origin" gives it a next link
	// that leaves the API.
	fail func(offset int) string
	// ignoreOrder makes the fake ignore $orderby; rejectOrder makes it
	// answer 400 to it, as BC does for a field it cannot sort on.
	ignoreOrder, rejectOrder bool
	// beforePage, when set, runs (under the lock) before a page is built,
	// with the page's $skiptoken; a test edits recs in it.
	beforePage func(token string)
	// failFirst, when set, is the status the next first-page request gets
	// instead of a page; it is cleared once used.
	failFirst int
	// probes is the query of every order probe ($top, #769). They are not
	// pages: they do not count in firstPages, fail or beforePage.
	probes []url.Values
	// failProbe, when set, is the status the next probe gets; it is
	// cleared once used.
	failProbe int
	// hold, when set, runs before a page request takes the lock, with its
	// $skiptoken; a test blocks a page in it.
	hold func(token string, r *http.Request)
}

var orderedFilter = regexp.MustCompile(`^(\w+) (gt|ge) (\S+)$`)

func newOrderedBC(t *testing.T, field string, size int, recs map[string]string) *orderedBC {
	t.Helper()
	b := &orderedBC{field: field, size: size, recs: recs, tok: bcTestToken(t)}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(func() { b.srv.Close(); b.tok.Close() })
	return b
}

// less orders two cursor values by instant (a DateTime) or by text (a date,
// whose text order is its order).
func (b *orderedBC) less(x, y string) bool {
	tx, ex := time.Parse(time.RFC3339, x)
	ty, ey := time.Parse(time.RFC3339, y)
	if ex == nil && ey == nil {
		return tx.Before(ty)
	}
	return x < y
}

func (b *orderedBC) serve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Has("$top") {
		b.probe(w, q)
		return
	}
	token := q.Get("$skiptoken")
	if b.hold != nil {
		b.hold(token, r)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if token == "" {
		b.firstPages = append(b.firstPages, q)
		if b.failFirst != 0 {
			code := b.failFirst
			b.failFirst = 0
			http.Error(w, "busy", code)
			return
		}
	}
	if b.beforePage != nil {
		b.beforePage(token)
	}
	ordered := false
	if ob := q.Get("$orderby"); ob != "" {
		if b.rejectOrder || ob != b.field+" asc" {
			http.Error(w, `{"error":{"code":"BadRequest","message":"cannot sort on this field"}}`, http.StatusBadRequest)
			return
		}
		ordered = !b.ignoreOrder
	}
	var op, lit string
	if f := q.Get("$filter"); f != "" {
		m := orderedFilter.FindStringSubmatch(f)
		if m == nil || m[1] != b.field {
			http.Error(w, `{"error":{"message":"bad filter"}}`, http.StatusBadRequest)
			return
		}
		op, lit = m[2], m[3]
	}
	ids := make([]string, 0, len(b.recs))
	for id, v := range b.recs {
		switch op {
		case "gt":
			if !b.less(lit, v) {
				continue
			}
		case "ge":
			if b.less(v, lit) {
				continue
			}
		}
		ids = append(ids, id)
	}
	before := func(vi, idi, vj, idj string) bool {
		if ordered && vi != vj {
			return b.less(vi, vj)
		}
		return idi < idj
	}
	sort.Slice(ids, func(i, j int) bool { return before(b.recs[ids[i]], ids[i], b.recs[ids[j]], ids[j]) })
	off := 0
	if token != "" {
		tv, tid, _ := strings.Cut(token, "|")
		off = sort.Search(len(ids), func(i int) bool { return before(tv, tid, b.recs[ids[i]], ids[i]) })
	}
	mode := ""
	if b.fail != nil {
		mode = b.fail(off)
	}
	if mode == "500" {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	end := min(off+b.size, len(ids))
	out := make([]string, 0, end-off)
	for _, id := range ids[off:end] {
		out = append(out, fmt.Sprintf(`{"id":%q,%q:%q}`, id, b.field, b.recs[id]))
	}
	next := ""
	switch {
	case mode == "off-origin":
		next = `,"@odata.nextLink":"https://collector.example/collect?page=2"`
	case end < len(ids):
		last := ids[end-1]
		q.Set("$skiptoken", b.recs[last]+"|"+last)
		next = fmt.Sprintf(`,"@odata.nextLink":%q`, b.srv.URL+r.URL.Path+"?"+q.Encode())
	}
	fmt.Fprintf(w, `{"value":[%s]%s}`, strings.Join(out, ","), next)
}

// probe answers an order probe: `$orderby=<field> asc|desc&$top=n`, sorted
// unless the fake ignores $orderby, in which case it is key order either way.
func (b *orderedBC) probe(w http.ResponseWriter, q url.Values) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probes = append(b.probes, q)
	if b.failProbe != 0 {
		code := b.failProbe
		b.failProbe = 0
		http.Error(w, "busy", code)
		return
	}
	field, dir, _ := strings.Cut(q.Get("$orderby"), " ")
	if b.rejectOrder || field != b.field || (dir != "asc" && dir != "desc") {
		http.Error(w, `{"error":{"code":"BadRequest","message":"cannot sort on this field"}}`, http.StatusBadRequest)
		return
	}
	if q.Get("$filter") != "" {
		http.Error(w, `{"error":{"message":"a probe carries no cursor clause"}}`, http.StatusBadRequest)
		return
	}
	top, err := strconv.Atoi(q.Get("$top"))
	if err != nil || top < 1 {
		http.Error(w, `{"error":{"message":"bad $top"}}`, http.StatusBadRequest)
		return
	}
	ids := make([]string, 0, len(b.recs))
	for id := range b.recs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		vi, vj := b.recs[ids[i]], b.recs[ids[j]]
		if b.ignoreOrder || vi == vj {
			return ids[i] < ids[j]
		}
		if dir == "desc" {
			return b.less(vj, vi)
		}
		return b.less(vi, vj)
	})
	out := []string{}
	for _, id := range ids[:min(top, len(ids))] {
		out = append(out, fmt.Sprintf(`{"id":%q,%q:%q}`, id, b.field, b.recs[id]))
	}
	fmt.Fprintf(w, `{"value":[%s]}`, strings.Join(out, ","))
}

func (b *orderedBC) probeQueries() []url.Values {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]url.Values(nil), b.probes...)
}

func (b *orderedBC) add(recs map[string]string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, v := range recs {
		b.recs[k] = v
	}
}

func (b *orderedBC) firstPageQueries() []url.Values {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]url.Values(nil), b.firstPages...)
}

func (b *orderedBC) cfg() *BCConfig {
	return &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		// Under a path, so a next link to /collect on another host is off
		// the API and not rebased onto it.
		Entity: "items", APIBaseURL: b.srv.URL + "/api/v2.0", TokenURL: b.tok.URL,
		Incremental: true, NodeID: "src", CursorField: b.field,
	}
}

// pollOnce runs one fetch and returns the ids it published and its error.
func pollOnce(t *testing.T, c *bcConsumer, got *[]*envelope.Envelope, mu *sync.Mutex, cfg *BCConfig) ([]string, error) {
	t.Helper()
	mu.Lock()
	before := len(*got)
	mu.Unlock()
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
		WithHTTPClient(http.DefaultClient)
	err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger)
	mu.Lock()
	defer mu.Unlock()
	var ids []string
	for _, env := range (*got)[before:] {
		var rs []map[string]any
		if jerr := json.Unmarshal(env.Payload, &rs); jerr != nil {
			t.Fatalf("payload: %v", jerr)
		}
		for _, r := range rs {
			ids = append(ids, r["id"].(string))
		}
	}
	return ids, err
}

// drain polls until a poll completes, at most limit polls, and returns the
// ids published and the number of polls.
func drain(t *testing.T, c *bcConsumer, got *[]*envelope.Envelope, mu *sync.Mutex, cfg *BCConfig, limit int) ([]string, int) {
	t.Helper()
	var all []string
	for n := 1; n <= limit; n++ {
		ids, err := pollOnce(t, c, got, mu, cfg)
		all = append(all, ids...)
		if err == nil {
			return all, n
		}
	}
	t.Fatalf("no poll completed in %d polls; published %v", limit, all)
	return nil, 0
}

// exactlyOnce reports every id of want not published exactly once, and any
// id published that is not in want.
func exactlyOnce(t *testing.T, published []string, want map[string]string) {
	t.Helper()
	count := map[string]int{}
	for _, id := range published {
		count[id]++
	}
	for id := range want {
		if count[id] != 1 {
			t.Errorf("record %s published %d times, want once", id, count[id])
		}
	}
	for id := range count {
		if _, ok := want[id]; !ok {
			t.Errorf("record %s published but not in the entity", id)
		}
	}
}

func orderedFixture(t *testing.T, field string, size int, recs map[string]string) (*orderedBC, *bcConsumer, *[]*envelope.Envelope, *sync.Mutex, *bytes.Buffer) {
	t.Helper()
	bc := newOrderedBC(t, field, size, recs)
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	var logs bytes.Buffer
	c.logger = slog.New(slog.NewTextHandler(&logs, nil))
	return bc, c, got, mu, &logs
}

func mins(m int) string { return stamp(time.Duration(m) * time.Minute) }

// TestOrderedResume_CappedPollsDeliverEverythingOnce: two records a page, a
// cap of two pages, so a poll reads four records. Each phase adds records
// and polls until a poll completes.
func TestOrderedResume_CappedPollsDeliverEverythingOnce(t *testing.T) {
	cases := []struct {
		name     string
		field    string
		lookback int
		phases   []map[string]string
		maxPolls int // per phase: a poll that does not advance never completes
	}{
		{"DateTime, distinct values", lmField, 0, []map[string]string{{
			"a": mins(-90), "b": mins(-80), "c": mins(-70), "d": mins(-60), "e": mins(-50),
			"f": mins(-40), "g": mins(-30), "h": mins(-20), "i": mins(-10),
		}}, 3},
		// The first poll stops on d, one of three records on -60: e and f
		// come back on the next poll, d does not go out again.
		{"DateTime, ties across the stop point", lmField, 0, []map[string]string{{
			"a": mins(-90), "b": mins(-80), "c": mins(-70),
			"d": mins(-60), "e": mins(-60), "f": mins(-60),
			"g": mins(-50), "h": mins(-40), "i": mins(-30),
		}}, 3},
		// The future-dated records sort last. Remembered after the first
		// phase, they are not reached again until the last poll of the
		// second, and must not go out a second time.
		{"DateTime, future-dated records remembered across stopped polls", lmField, 0, []map[string]string{
			{"a": mins(-200), "x": "2099-01-01T00:00:00Z", "y": mins(60)},
			{"b": mins(-90), "c": mins(-80), "d": mins(-70), "e": mins(-60), "f": mins(-50), "g": mins(-40)},
		}, 3},
		{"date, ties across the stop point", dateField, 0, []map[string]string{{
			"a": "2026-10-01", "b": "2026-10-02",
			"c": "2026-10-03", "d": "2026-10-03", "e": "2026-10-03",
			"f": "2026-10-04", "g": "2026-10-05", "h": "2026-10-06",
		}}, 3},
		{"date, future-dated records remembered across stopped polls", dateField, 0, []map[string]string{
			{"a": "2026-09-01", "x": "2099-01-01", "y": "2026-10-29"},
			{"b": "2026-10-01", "c": "2026-10-02", "d": "2026-10-03", "e": "2026-10-04", "f": "2026-10-05"},
		}, 3},
		// With lookback_days the window is read again on every poll and
		// remembered; a backdated record inside it is still read once.
		{"date with lookback_days", dateField, 1, []map[string]string{
			{
				"a": "2026-10-01", "b": "2026-10-02", "c": "2026-10-03", "d": "2026-10-04", "e": "2026-10-05",
				"f": "2026-10-06", "g": "2026-10-07", "h": "2026-10-08", "i": "2026-10-09", "j": "2026-10-10",
			},
			{"late": "2026-10-09", "k": "2026-10-11"},
		}, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lowerPageCap(t, 2)
			bc, c, got, mu, logs := orderedFixture(t, tc.field, 2, map[string]string{})
			cfg := bc.cfg()
			cfg.LookbackDays = tc.lookback
			want := map[string]string{}
			var published []string
			stopped := 0
			for i, phase := range tc.phases {
				bc.add(phase)
				for k, v := range phase {
					want[k] = v
				}
				ids, polls := drain(t, c, got, mu, cfg, tc.maxPolls)
				published = append(published, ids...)
				stopped += polls - 1
				t.Logf("phase %d: %d polls", i+1, polls)
			}
			if stopped == 0 {
				t.Fatal("no poll hit the cap: the case tests nothing")
			}
			exactlyOnce(t, published, want)
			if ids, err := pollOnce(t, c, got, mu, cfg); err != nil || len(ids) != 0 {
				t.Errorf("a poll after catching up published %v (err %v), want nothing", ids, err)
			}
			for i, q := range bc.firstPageQueries() {
				if q.Get("$orderby") != tc.field+" asc" {
					t.Errorf("poll %d asked $orderby=%q, want %q", i+1, q.Get("$orderby"), tc.field+" asc")
				}
			}
			if !strings.Contains(logs.String(), "watermark advanced to where the poll stopped") {
				t.Errorf("no page-cap Warn saying the poll advanced: %s", logs)
			}
		})
	}
}

// TestOrderedResume_StopsOtherThanTheCap: a failed page, a next link off the
// API and a failed publish stop the first poll after its first page. The
// next poll continues after what the first one handled.
func TestOrderedResume_StopsOtherThanTheCap(t *testing.T) {
	recs := map[string]string{
		"a": mins(-90), "b": mins(-80), "c": mins(-80), "d": mins(-70), "e": mins(-60), "f": mins(-50),
	}
	cases := []struct {
		name      string
		page      string // what the fake does to the second page of the first poll
		publish   bool   // fail the second publish instead
		wantErr   error  // nil = any error
		wantAsk   string // the second poll's $filter
		wantFirst []string
	}{
		{"a page fails", "500", false, nil, lmField + " gt " + mins(-90), []string{"a", "b"}},
		{"a next link off the API", "off-origin", false, errNextOffOrigin, lmField + " gt " + mins(-90), []string{"a", "b"}},
		// The second page (c, d) was never published: the watermark must
		// not count its values, or c, which ties with b, would be skipped.
		{"a publish fails", "", true, nil, lmField + " gt " + mins(-90), []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			own := map[string]string{}
			for k, v := range recs {
				own[k] = v
			}
			bc, c, got, mu, _ := orderedFixture(t, lmField, 2, own)
			first := true
			bc.fail = func(off int) string {
				if first && off == 2 && tc.page == "500" {
					first = false
					return "500"
				}
				if first && off == 0 && tc.page == "off-origin" {
					first = false
					return "off-origin"
				}
				return ""
			}
			if tc.publish {
				inner, calls := c.publish, 0
				c.publish = func(ctx context.Context, env *envelope.Envelope) error {
					calls++
					if calls == 2 {
						return errors.New("nats down")
					}
					return inner(ctx, env)
				}
			}
			cfg := bc.cfg()
			ids, err := pollOnce(t, c, got, mu, cfg)
			if err == nil || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("first poll: err = %v, want %v", err, tc.wantErr)
			}
			if sorted(ids) != strings.Join(tc.wantFirst, ",") {
				t.Fatalf("first poll published %v, want %v", ids, tc.wantFirst)
			}
			rest, err := pollOnce(t, c, got, mu, cfg)
			if err != nil {
				t.Fatalf("second poll: %v", err)
			}
			qs := bc.firstPageQueries()
			if ask := qs[len(qs)-1].Get("$filter"); ask != tc.wantAsk {
				t.Errorf("second poll asked %q, want %q", ask, tc.wantAsk)
			}
			exactlyOnce(t, append(ids, rest...), recs)
		})
	}
}

// TestOrderedResume_ResendThatStopsFinishesOnTheNextPolls: a resend reads
// from the start; capped, it stores where it got to and the next polls send
// the rest, each record once.
func TestOrderedResume_ResendThatStopsFinishesOnTheNextPolls(t *testing.T) {
	recs := map[string]string{
		"a": mins(-90), "b": mins(-80), "c": mins(-70), "d": mins(-60), "e": mins(-50), "f": mins(-40), "g": mins(-30),
	}
	lowerPageCap(t, 2)
	bc, c, got, mu, _ := orderedFixture(t, lmField, 2, recs)
	cfg := bc.cfg()
	if _, polls := drain(t, c, got, mu, cfg, 3); polls < 2 {
		t.Fatalf("the first read took %d polls; the case needs the cap to stop it", polls)
	}
	cfg.resendAll = true
	ids, polls := drain(t, c, got, mu, cfg, 3)
	if polls < 2 {
		t.Fatalf("the resend took %d polls; the case needs the cap to stop it", polls)
	}
	exactlyOnce(t, ids, recs)
	if cfg.resendAll {
		t.Error("still resending after every record went out")
	}
}

// TestOrderedResume_NotIncrementalIsUnchanged: no $orderby, no watermark,
// and the capped poll reads the same pages again.
func TestOrderedResume_NotIncrementalIsUnchanged(t *testing.T) {
	lowerPageCap(t, 2)
	recs := map[string]string{"a": mins(-90), "b": mins(-80), "c": mins(-70), "d": mins(-60), "e": mins(-50)}
	bc, c, got, mu, _ := orderedFixture(t, lmField, 2, recs)
	cfg := bc.cfg()
	cfg.Incremental = false
	for i := 0; i < 2; i++ {
		ids, err := pollOnce(t, c, got, mu, cfg)
		if !errors.Is(err, errPageCap) {
			t.Fatalf("poll %d: err = %v, want errPageCap", i+1, err)
		}
		if sorted(ids) != "a,b,c,d" {
			t.Errorf("poll %d published %v, want a,b,c,d", i+1, ids)
		}
	}
	for i, q := range bc.firstPageQueries() {
		if _, ok := q["$orderby"]; ok {
			t.Errorf("poll %d asked $orderby %q without incremental", i+1, q.Get("$orderby"))
		}
		if q.Get("$filter") != "" {
			t.Errorf("poll %d asked $filter %q without incremental", i+1, q.Get("$filter"))
		}
	}
	if cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src"); cp != nil {
		t.Errorf("a checkpoint was stored without incremental: %+v", cp)
	}
}

// TestOrderedResume_UntrustedOrderDoesNotAdvance: when BC refuses $orderby,
// or returns the feed out of cursor order anyway, a stopped poll stores no
// watermark, as before #733, and says why.
func TestOrderedResume_UntrustedOrderDoesNotAdvance(t *testing.T) {
	// Key order a, b, c, d, e is not time order.
	recs := map[string]string{"a": mins(-50), "b": mins(-90), "c": mins(-60), "d": mins(-80), "e": mins(-70)}
	cases := []struct {
		name         string
		reject       bool
		wantLog      string
		wantOrderbys []bool // per first-page request: carried $orderby
	}{
		{"BC refuses $orderby", true, "refused $orderby on cursor_field", []bool{true, false, false}},
		// The first page is already out of order, so the order is not
		// trusted and the next poll asks without it (#769).
		{"BC ignores $orderby", false, "out of cursor_field order", []bool{true, false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lowerPageCap(t, 2)
			bc, c, got, mu, logs := orderedFixture(t, lmField, 2, recs)
			bc.rejectOrder, bc.ignoreOrder = tc.reject, !tc.reject
			cfg := bc.cfg()
			for i := 0; i < 2; i++ {
				ids, err := pollOnce(t, c, got, mu, cfg)
				if !errors.Is(err, errPageCap) {
					t.Fatalf("poll %d: err = %v, want errPageCap", i+1, err)
				}
				if sorted(ids) != "a,b,c,d" {
					t.Errorf("poll %d published %v, want a,b,c,d again", i+1, ids)
				}
			}
			if cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src"); cp != nil && cp.LastProcessedMessageID != "" {
				t.Errorf("watermark %q stored from a feed not in order", cp.LastProcessedMessageID)
			}
			qs := bc.firstPageQueries()
			if len(qs) != len(tc.wantOrderbys) {
				t.Fatalf("%d first-page requests, want %d", len(qs), len(tc.wantOrderbys))
			}
			for i, want := range tc.wantOrderbys {
				if _, ok := qs[i]["$orderby"]; ok != want {
					t.Errorf("request %d carried $orderby: %v, want %v", i+1, ok, want)
				}
			}
			out := logs.String()
			if !strings.Contains(out, tc.wantLog) {
				t.Errorf("log lacks %q: %s", tc.wantLog, out)
			}
			if !strings.Contains(out, "re-reads and re-sends") {
				t.Errorf("the page-cap Warn does not say the poll repeats: %s", out)
			}
		})
	}
}

// TestPollURL_OrderBy: $orderby only for an ordered poll, beside the
// configured filter and the cursor clause, which it does not change.
func TestPollURL_OrderBy(t *testing.T) {
	cases := []struct {
		name, filter, field, cursor string
		ordered                     bool
		wantFilter, wantOrder       string
	}{
		{"ordered, no cursor yet", "", "", "", true, "", "lastModifiedDateTime asc"},
		{"ordered with a configured filter", "status eq 'Open' or status eq 'Draft'", "", "2026-10-20T10:00:00Z", true,
			"(status eq 'Open' or status eq 'Draft') and lastModifiedDateTime gt 2026-10-20T10:00:00Z", "lastModifiedDateTime asc"},
		{"ordered on a date field", "", "postingDate", "2026-10-20", true, "postingDate ge 2026-10-20", "postingDate asc"},
		{"not ordered", "x eq 1", "", "", false, "x eq 1", ""},
	}
	for _, tc := range cases {
		cfg := &BCConfig{CompanyID: "GUID", APIBaseURL: "https://bc.example/api/v2.0", Filter: tc.filter, CursorField: tc.field}
		u, err := url.Parse(cfg.pollURL(tc.cursor, tc.ordered))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		q := u.Query()
		if q.Get("$filter") != tc.wantFilter {
			t.Errorf("%s: $filter = %q, want %q", tc.name, q.Get("$filter"), tc.wantFilter)
		}
		if q.Get("$orderby") != tc.wantOrder {
			t.Errorf("%s: $orderby = %q, want %q", tc.name, q.Get("$orderby"), tc.wantOrder)
		}
		if u.Path != "/api/v2.0/companies(GUID)/items" {
			t.Errorf("%s: path = %q", tc.name, u.Path)
		}
	}
	if got := (&BCConfig{CompanyID: "GUID", APIBaseURL: "https://bc.example/api/v2.0"}).entityURL(""); strings.Contains(got, "orderby") {
		t.Errorf("entityURL (the preview) asks for an order: %q", got)
	}
}

// TestOrderedResume_StuckOnADateSaysSo: more records on one date than a poll
// reads. A date is read back with `ge`, so every poll starts on that day and
// stops inside it: no progress, and the page-cap Warn must say "not
// advanced" rather than promise the rest will arrive (review of #733).
func TestOrderedResume_StuckOnADateSaysSo(t *testing.T) {
	lowerPageCap(t, 2)
	recs := map[string]string{
		"a": "2026-10-01",
		"b": "2026-10-03", "c": "2026-10-03", "d": "2026-10-03", "e": "2026-10-03", "f": "2026-10-03",
		"g": "2026-10-04",
	}
	bc, c, got, mu, logs := orderedFixture(t, dateField, 2, recs)
	cfg := bc.cfg()
	for i := 0; i < 3; i++ {
		if _, err := pollOnce(t, c, got, mu, cfg); !errors.Is(err, errPageCap) {
			t.Fatalf("poll %d: err = %v, want errPageCap", i+1, err)
		}
	}
	logs.Reset()
	ids, err := pollOnce(t, c, got, mu, cfg)
	if !errors.Is(err, errPageCap) {
		t.Fatalf("poll 4: err = %v, want errPageCap", err)
	}
	if len(ids) != 0 {
		t.Errorf("poll 4 published %v, want nothing (it reads the same day again)", ids)
	}
	out := logs.String()
	if !strings.Contains(out, "was not advanced") || strings.Contains(out, "advanced to where the poll stopped") {
		t.Errorf("a poll stuck on one date logged: %s", out)
	}
	if cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src"); cp == nil || cp.LastProcessedMessageID != "2026-10-03" {
		t.Errorf("watermark = %+v, want 2026-10-03", cp)
	}
}

// TestOrderedResume_StoppedRecoveryKeepsTheFrozenWatermark: a recovery read
// past a frozen pre-#591 watermark that stops early stores nothing (review of
// #755). Its check for a node that saved again after freezing
// (recovery.resaved) needs the end of the feed, so the next poll must run the
// recovery again rather than resume as a normal poll.
func TestOrderedResume_StoppedRecoveryKeepsTheFrozenWatermark(t *testing.T) {
	const frozen = "2099-01-01T00:00:00Z"
	lowerPageCap(t, 2)
	bc, c, got, mu, _ := orderedFixture(t, lmField, 1, map[string]string{
		"a": mins(-170), "b": mins(-160), "c": mins(-150), "d": mins(-140),
	})
	if err := c.checkpoints.Save(context.Background(), &checkpoint.Checkpoint{
		TenantID: "tenant-1", ConnectionID: "conn-1", NodeID: "src",
		LastProcessedMessageID: frozen, LastProcessedAt: aheadNow.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := bc.cfg()
	if _, err := pollOnce(t, c, got, mu, cfg); !errors.Is(err, errPageCap) {
		t.Fatalf("err = %v, want errPageCap", err)
	}
	if want := lmField + " gt " + aheadNow.Add(-3*time.Hour).Format(time.RFC3339); bc.firstPageQueries()[0].Get("$filter") != want {
		t.Fatalf("the poll asked %q, want the bounded recovery read %q", bc.firstPageQueries()[0].Get("$filter"), want)
	}
	cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src")
	if cp == nil || cp.LastProcessedMessageID != frozen {
		t.Errorf("checkpoint = %+v, want the frozen watermark %s kept", cp, frozen)
	}
}

// TestOrderedResume_ResendWithNothingToResumeFromStaysAResend: every record
// a resend read is on one timestamp, so it has no point to resume from. It
// must stay a resend: ending it would leave the next poll on the old
// watermark, and the records the capped resend never reached would not go
// out again (review of #755).
func TestOrderedResume_ResendWithNothingToResumeFromStaysAResend(t *testing.T) {
	ts := mins(-60)
	bc, c, got, mu, _ := orderedFixture(t, lmField, 1, map[string]string{"a": ts, "b": ts, "c": ts})
	cfg := bc.cfg()
	if ids, err := pollOnce(t, c, got, mu, cfg); err != nil || sorted(ids) != "a,b,c" {
		t.Fatalf("first poll: %v, %v", ids, err)
	}
	lowerPageCap(t, 2)
	cfg.resendAll = true
	ids, err := pollOnce(t, c, got, mu, cfg)
	if !errors.Is(err, errPageCap) || sorted(ids) != "a,b" {
		t.Fatalf("capped resend: published %v, err %v; want a,b and errPageCap", ids, err)
	}
	if !cfg.resendAll {
		t.Fatal("the capped resend ended with nothing to resume from")
	}
	pageCap = maxPagesPerPoll
	ids, err = pollOnce(t, c, got, mu, cfg)
	if err != nil {
		t.Fatalf("resend after the cap was raised: %v", err)
	}
	if !strings.Contains(","+sorted(ids)+",", ",c,") {
		t.Errorf("published %v after the cap was raised, want c resent", ids)
	}
}

// TestOrderedResume_LatestValueWinsInTheRememberedSet: k is read on the
// watermark day, then edited to a future date while the poll pages, and read
// again further on. The remembered set must keep the newer value, which is
// what the next poll gets back; keeping the old one sends k a third time
// (review of #755). Both a full poll and one the cap stops after k's second
// read.
func TestOrderedResume_LatestValueWinsInTheRememberedSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  int
	}{
		{"full poll", 0},
		// a, j, k, k again, z; zz is past the cap, so k is not on the last
		// value read and only the ahead set has its newer value.
		{"stopped poll", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cap > 0 {
				lowerPageCap(t, tc.cap)
			}
			bc, c, got, mu, _ := orderedFixture(t, dateField, 1, map[string]string{
				"a": "2026-10-01", "j": "2026-10-03", "k": "2026-10-03", "z": "2099-06-01", "zz": "2099-07-01",
			})
			edited := false
			bc.beforePage = func(token string) {
				if !edited && strings.HasSuffix(token, "|k") {
					edited = true
					bc.recs["k"] = "2099-01-01"
				}
			}
			cfg := bc.cfg()
			var published []string
			for i := 0; i < 3; i++ {
				ids, err := pollOnce(t, c, got, mu, cfg)
				if err != nil && !errors.Is(err, errPageCap) {
					t.Fatalf("poll %d: %v", i+1, err)
				}
				published = append(published, ids...)
			}
			if !edited {
				t.Fatal("k was never edited: the case tests nothing")
			}
			if tc.cap > 0 && !strings.Contains(sorted(published), "zz") {
				t.Fatal("zz never published: the later polls did not finish the feed")
			}
			n := 0
			for _, id := range published {
				if id == "k" {
					n++
				}
			}
			if n != 2 {
				t.Errorf("k published %d times, want 2 (before and after the edit); all: %v", n, published)
			}
			if got := cfg.aheadSeen["k"]; got != "2099-01-01" {
				t.Errorf("remembered k = %q, want its newer value 2099-01-01", got)
			}
		})
	}
}

// TestOrderedResume_TransientErrorKeepsTheOrder: only a 400 to the ordered
// first page turns $orderby off. A 503 fails that poll and the next one asks
// in order again (review of #755).
func TestOrderedResume_TransientErrorKeepsTheOrder(t *testing.T) {
	for _, code := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			bc, c, got, mu, _ := orderedFixture(t, lmField, 2, map[string]string{"a": mins(-90), "b": mins(-80)})
			bc.failFirst = code
			cfg := bc.cfg()
			if _, err := pollOnce(t, c, got, mu, cfg); err == nil {
				t.Fatalf("poll 1: want the %d to surface", code)
			}
			if _, err := pollOnce(t, c, got, mu, cfg); err != nil {
				t.Fatalf("poll 2: %v", err)
			}
			for i, q := range bc.firstPageQueries() {
				if q.Get("$orderby") != lmField+" asc" {
					t.Errorf("request %d asked $orderby=%q, want %q", i+1, q.Get("$orderby"), lmField+" asc")
				}
			}
		})
	}
}

// TestOrderedResume_Randomized drives many small random entities through
// capped polls with records edited while a poll pages, restarts (a fresh
// node config, state only from the checkpoint) and failed pages. Every
// record's final value must be published, and no record is published twice
// with the same value. Sizes keep within what can be passed: fewer records
// on one value, and fewer future-dated records, than a poll reads.
// RSEED=<n> runs one seed alone.
func TestOrderedResume_Randomized(t *testing.T) {
	const runs = 300
	for run := 0; run < runs; run++ {
		if os.Getenv("RSEED") != "" && fmt.Sprint(run) != os.Getenv("RSEED") {
			continue
		}
		seed := uint64(run)
		rng := rand.New(rand.NewPCG(seed, 733))
		size, capPages := 1+rng.IntN(3), 1+rng.IntN(3)
		perPoll := size * capPages
		if perPoll < 3 {
			capPages = 3
			perPoll = size * capPages
		}
		tick := 0
		next := func() string {
			tick++
			return aheadNow.Add(-10*time.Hour + time.Duration(tick)*time.Second).Format(time.RFC3339)
		}
		recs := map[string]string{}
		for i, n := 0, 3+rng.IntN(15); i < n; {
			v := next()
			for j := 0; j < 1+rng.IntN(perPoll-1) && i < n; j++ { // a tie group smaller than a poll
				recs[fmt.Sprintf("r%02d", i)] = v
				i++
			}
		}
		for i, n := 0, rng.IntN(perPoll); i < n; i++ { // future-dated, fewer than a poll reads
			recs[fmt.Sprintf("f%02d", i)] = aheadNow.Add(time.Duration(1+rng.IntN(48)) * time.Hour).Format(time.RFC3339)
		}

		ok := t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			lowerPageCap(t, capPages)
			bc, c, got, mu, _ := orderedFixture(t, lmField, size, recs)
			edits, failures := 1+rng.IntN(4), rng.IntN(3)
			bc.beforePage = func(string) {
				if edits > 0 && rng.IntN(4) == 0 { // an edit stamps BC's now
					edits--
					ids := make([]string, 0, len(bc.recs))
					for id := range bc.recs {
						ids = append(ids, id)
					}
					sort.Strings(ids)
					bc.recs[ids[rng.IntN(len(ids))]] = next()
				}
			}
			bc.fail = func(int) string {
				if failures > 0 && rng.IntN(5) == 0 {
					failures--
					return "500"
				}
				return ""
			}
			type version struct{ id, value string }
			sent := map[version]int{}
			cfg := bc.cfg()
			done := false
			for p := 0; p < 60 && !done; p++ {
				if rng.IntN(4) == 0 {
					cfg = bc.cfg() // restart
				}
				mu.Lock()
				before := len(*got)
				mu.Unlock()
				// Done is a poll that completes with no edit or failure left
				// to happen while it pages; an edit during a poll is the
				// next poll's to read.
				bc.mu.Lock()
				quiet := edits == 0 && failures == 0
				bc.mu.Unlock()
				_, err := pollOnce(t, c, got, mu, cfg)
				mu.Lock()
				for _, env := range (*got)[before:] {
					var rs []map[string]string
					if jerr := json.Unmarshal(env.Payload, &rs); jerr != nil {
						t.Fatalf("payload: %v", jerr)
					}
					for _, r := range rs {
						sent[version{r["id"], r[lmField]}]++
					}
				}
				mu.Unlock()
				done = err == nil && quiet
			}
			if !done {
				t.Fatalf("size %d, cap %d: the feed never completed", size, capPages)
			}
			for v, n := range sent {
				if n > 1 {
					t.Errorf("%s at %s published %d times", v.id, v.value, n)
				}
			}
			bc.mu.Lock()
			defer bc.mu.Unlock()
			for id, v := range bc.recs {
				if sent[version{id, v}] == 0 {
					t.Errorf("%s at its final value %s never published", id, v)
				}
			}
		})
		if !ok {
			t.Logf("size %d, cap %d, records %v", size, capPages, recs)
			return
		}
	}
}
