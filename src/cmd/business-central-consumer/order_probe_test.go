package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

// #769: the ordered resume of #733 relies on BC keeping $orderby, and the
// poll's own check sees only the pages it read. A probe of one record
// ascending and one descending confirms the order once per process before a
// stopped poll may store how far it got.

// A BC that ignores $orderby, and whose key order happens to put the first
// pages in ascending cursor order, with older records on a page the capped
// poll never reads. Without the probe the first poll stores a watermark past
// e and f, and they are never sent.
func TestOrderProbe_IgnoredOrderIsCaught(t *testing.T) {
	lowerPageCap(t, 2)
	recs := map[string]string{
		"a": mins(-60), "b": mins(-50), "c": mins(-40), "d": mins(-30), // pages 1 and 2: ascending
		"e": mins(-90), "f": mins(-80), // page 3: older
	}
	bc, c, got, mu, logs := orderedFixture(t, lmField, 2, recs)
	bc.ignoreOrder = true
	cfg := bc.cfg()

	var published []string
	for i := 0; i < 2; i++ {
		ids, err := pollOnce(t, c, got, mu, cfg)
		if !errors.Is(err, errPageCap) {
			t.Fatalf("poll %d: err = %v, want errPageCap", i+1, err)
		}
		published = append(published, ids...)
	}
	if cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src"); cp != nil && cp.LastProcessedMessageID != "" {
		t.Errorf("watermark %q stored although BC ignores $orderby", cp.LastProcessedMessageID)
	}
	qs := bc.firstPageQueries()
	if qs[0].Get("$orderby") != lmField+" asc" {
		t.Errorf("poll 1 asked $orderby=%q, want %q", qs[0].Get("$orderby"), lmField+" asc")
	}
	if ob, ok := qs[1]["$orderby"]; ok {
		t.Errorf("poll 2 still asked $orderby=%v after the probe found it ignored", ob)
	}
	if n := len(bc.probeQueries()); n != 2 {
		t.Errorf("%d probe requests, want 2 (one ascending, one descending, once)", n)
	}
	if out := logs.String(); !strings.Contains(out, "does not keep $orderby on cursor_field") || !strings.Contains(out, "level=WARN") {
		t.Errorf("no Warn saying the order did not hold: %s", out)
	}

	// With the cap out of the way the unordered poll reads everything: e and
	// f were not skipped.
	pageCap = maxPagesPerPoll
	ids, err := pollOnce(t, c, got, mu, cfg)
	if err != nil {
		t.Fatalf("uncapped poll: %v", err)
	}
	published = append(published, ids...)
	for _, id := range []string{"e", "f"} {
		if !strings.Contains(","+sorted(published)+",", ","+id+",") {
			t.Errorf("record %s never published; published %v", id, published)
		}
	}
	if n := len(bc.probeQueries()); n != 2 {
		t.Errorf("%d probe requests after three polls, want 2", n)
	}
}

// A BC that honours $orderby stays ordered: capped polls advance and deliver
// every record once, and the probe runs once per process for the tenant,
// company, entity and field, including after a restart of the node.
func TestOrderProbe_HonouredOrderStaysOrdered(t *testing.T) {
	lowerPageCap(t, 2)
	recs := map[string]string{
		"a": mins(-90), "b": mins(-80), "c": mins(-70), "d": mins(-60), "e": mins(-50),
		"f": mins(-40), "g": mins(-30), "h": mins(-20), "i": mins(-10),
	}
	bc, c, got, mu, logs := orderedFixture(t, lmField, 2, recs)
	cfg := bc.cfg()
	published, polls := drain(t, c, got, mu, cfg, 3)
	if polls < 2 {
		t.Fatalf("drained in %d polls; the case needs the cap to stop one", polls)
	}
	exactlyOnce(t, published, recs)
	for i, q := range bc.firstPageQueries() {
		if q.Get("$orderby") != lmField+" asc" {
			t.Errorf("poll %d asked $orderby=%q, want %q", i+1, q.Get("$orderby"), lmField+" asc")
		}
	}
	probes := bc.probeQueries()
	if len(probes) != 2 {
		t.Fatalf("%d probe requests, want 2", len(probes))
	}
	for i, want := range []string{lmField + " asc", lmField + " desc"} {
		if probes[i].Get("$orderby") != want || probes[i].Get("$top") != "1" {
			t.Errorf("probe %d asked $orderby=%q $top=%q, want %q and 1", i+1, probes[i].Get("$orderby"), probes[i].Get("$top"), want)
		}
	}
	if strings.Contains(logs.String(), "do not bracket") {
		t.Errorf("a BC that keeps the order was reported as not keeping it: %s", logs)
	}

	// A restart of the node (a fresh config) uses the cached answer.
	bc.add(map[string]string{"j": mins(-5), "k": mins(-4), "l": mins(-3), "m": mins(-2), "n": mins(-1)})
	if _, err := pollOnce(t, c, got, mu, bc.cfg()); !errors.Is(err, errPageCap) {
		t.Fatalf("poll after the restart: err = %v, want errPageCap", err)
	}
	if n := len(bc.probeQueries()); n != 2 {
		t.Errorf("%d probe requests after a restart of the node, want 2 (cached per process)", n)
	}

	// Another tenant pointing at the same company does not reuse the answer.
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(http.DefaultClient)
	if err := c.fetchAndPublish(context.Background(), "conn-2", "tenant-2", bc.cfg(), tok, c.logger); !errors.Is(err, errPageCap) {
		t.Fatalf("other tenant's poll: err = %v, want errPageCap", err)
	}
	if n := len(bc.probeQueries()); n != 4 {
		t.Errorf("%d probe requests after another tenant's poll, want 4", n)
	}
}

// A poll that reads one page never needs the order and does not probe.
func TestOrderProbe_SinglePagePollDoesNotProbe(t *testing.T) {
	bc, c, got, mu, _ := orderedFixture(t, lmField, 10, map[string]string{"a": mins(-90), "b": mins(-80)})
	cfg := bc.cfg()
	for i := 0; i < 2; i++ {
		if _, err := pollOnce(t, c, got, mu, cfg); err != nil {
			t.Fatalf("poll %d: %v", i+1, err)
		}
	}
	if n := len(bc.probeQueries()); n != 0 {
		t.Errorf("%d probe requests for single-page polls, want 0", n)
	}
}

// A probe that fails for another reason than a 400 decides nothing: that
// poll stores no resume point, it stays ordered, and the next poll probes
// again and then resumes.
func TestOrderProbe_FailedProbeDecidesNothing(t *testing.T) {
	lowerPageCap(t, 2)
	recs := map[string]string{"a": mins(-90), "b": mins(-80), "c": mins(-70), "d": mins(-60), "e": mins(-50)}
	bc, c, got, mu, _ := orderedFixture(t, lmField, 2, recs)
	bc.failProbe = http.StatusServiceUnavailable
	cfg := bc.cfg()
	ids, err := pollOnce(t, c, got, mu, cfg)
	if !errors.Is(err, errPageCap) {
		t.Fatalf("poll 1: err = %v, want errPageCap", err)
	}
	if cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src"); cp != nil && cp.LastProcessedMessageID != "" {
		t.Errorf("watermark %q stored by a poll whose order check failed", cp.LastProcessedMessageID)
	}
	rest, _ := drain(t, c, got, mu, cfg, 3)
	if n := len(bc.probeQueries()); n != 3 {
		t.Errorf("%d probe requests, want 3 (the failed one, then asked again)", n)
	}
	for i, q := range bc.firstPageQueries() {
		if q.Get("$orderby") != lmField+" asc" {
			t.Errorf("poll %d asked $orderby=%q, want it kept after a failed check", i+1, q.Get("$orderby"))
		}
	}
	// Poll 1 sent a to d and stored nothing, so a to d come again once.
	count := map[string]int{}
	for _, id := range append(ids, rest...) {
		count[id]++
	}
	for id, want := range map[string]int{"a": 2, "b": 2, "c": 2, "d": 2, "e": 1} {
		if count[id] != want {
			t.Errorf("record %s published %d times, want %d", id, count[id], want)
		}
	}
}

// TestProbeURL: the configured filter and no cursor clause, one record, in
// the direction asked.
func TestProbeURL(t *testing.T) {
	cfg := &BCConfig{CompanyID: "GUID", APIBaseURL: "https://bc.example/api/v2.0", Filter: "status eq 'Open'", CursorField: "postingDate"}
	got := cfg.probeURL("desc")
	want := "https://bc.example/api/v2.0/companies(GUID)/items?$filter=status+eq+%27Open%27&$orderby=postingDate+desc&$top=1"
	if got != want {
		t.Errorf("probeURL = %q, want %q", got, want)
	}
	cfg.Filter = ""
	if got := cfg.probeURL("asc"); got != "https://bc.example/api/v2.0/companies(GUID)/items?$orderby=postingDate+asc&$top=1" {
		t.Errorf("probeURL without a filter = %q", got)
	}
}

// orderedOff reports, after polls on bc, that no watermark was stored, that
// the first poll asked for the order and the later ones did not, and that the
// verdict "no" is cached.
func orderedOff(t *testing.T, c *bcConsumer, bc *orderedBC, cfg *BCConfig) {
	t.Helper()
	if cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src"); cp != nil && cp.LastProcessedMessageID != "" {
		t.Errorf("watermark %q stored although BC ignores $orderby", cp.LastProcessedMessageID)
	}
	qs := bc.firstPageQueries()
	if qs[0].Get("$orderby") != cfg.effectiveCursorField()+" asc" {
		t.Errorf("poll 1 asked $orderby=%q", qs[0].Get("$orderby"))
	}
	for i, q := range qs[1:] {
		if ob, ok := q["$orderby"]; ok {
			t.Errorf("poll %d still asked $orderby=%v", i+2, ob)
		}
	}
	if honoured, known := c.orderVerdict("tenant-1", cfg); !known || honoured {
		t.Errorf("cached verdict = (%v, known %v), want a cached no", honoured, known)
	}
}

// A date cursor, with BC ignoring $orderby and page 1 all on one date: both
// probes answer with the same record on that date, and the last value read
// is that date too (review of #789). One value at both ends decides nothing,
// so no capped poll stores a resume point, and e and f, older than page 1,
// are still sent. A check that took this for a yes would store 2026-01-06 on
// the first capped poll and never send e and f.
func TestOrderProbe_OneDateOnPageOneStoresNothing(t *testing.T) {
	lowerPageCap(t, 2)
	recs := map[string]string{
		"a": "2026-01-05", "b": "2026-01-05", "c": "2026-01-05", "d": "2026-01-06",
		"e": "2026-01-01", "f": "2026-01-02",
	}
	bc, c, got, mu, _ := orderedFixture(t, dateField, 2, recs)
	bc.ignoreOrder = true
	cfg := bc.cfg()
	var published []string
	for i := 0; i < 2; i++ {
		ids, err := pollOnce(t, c, got, mu, cfg)
		if !errors.Is(err, errPageCap) {
			t.Fatalf("poll %d: err = %v, want errPageCap", i+1, err)
		}
		published = append(published, ids...)
	}
	if cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src"); cp != nil && cp.LastProcessedMessageID != "" {
		t.Errorf("watermark %q stored by a capped poll whose order check decided nothing", cp.LastProcessedMessageID)
	}
	if _, known := c.orderVerdict("tenant-1", cfg); known {
		t.Error("a verdict was cached from probes that found one value")
	}
	if n := len(bc.probeQueries()); n != 4 {
		t.Errorf("%d probe requests over two polls, want 4 (asked again)", n)
	}
	pageCap = maxPagesPerPoll
	ids, err := pollOnce(t, c, got, mu, cfg)
	if err != nil {
		t.Fatalf("uncapped poll: %v", err)
	}
	published = append(published, ids...)
	for _, id := range []string{"e", "f"} {
		if !strings.Contains(","+sorted(published)+",", ","+id+",") {
			t.Errorf("record %s never published; published %v", id, published)
		}
	}
}

// The two probes answer with different records and the value ascending is
// above the value descending: the order is not kept, whatever the records.
func TestOrderProbe_InvertedEndsTurnOrderOff(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	var mu sync.Mutex
	var firsts []url.Values
	var apiURL string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// lo and hi swapped: ascending answers the later value.
		if answerOrderProbe(w, r, "2026-10-03T00:00:00Z", "2026-10-01T00:00:00Z") {
			return
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"value":[{"number":"C","lastModifiedDateTime":"2026-10-02T12:00:00Z"}]}`)
			return
		}
		mu.Lock()
		firsts = append(firsts, r.URL.Query())
		mu.Unlock()
		fmt.Fprintf(w, `{"value":[{"number":"A","lastModifiedDateTime":"2026-10-02T08:00:00Z"},`+
			`{"number":"B","lastModifiedDateTime":"2026-10-02T10:00:00Z"}],"@odata.nextLink":"%s?page=2"}`, apiURL)
	}))
	defer api.Close()
	apiURL = api.URL + "/companies(GUID)/items"

	c, _, _ := newTestConsumer()
	logs := &stLogBuf{}
	c.logger = slog.New(slog.NewTextHandler(logs, nil))
	cfg := &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: api.URL, TokenURL: tokenSrv.URL, Incremental: true, NodeID: "src",
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(http.DefaultClient)
	for i := 0; i < 2; i++ {
		if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
			t.Fatalf("poll %d: %v", i+1, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(firsts) != 2 || firsts[0].Get("$orderby") == "" {
		t.Fatalf("first-page requests %v, want two, the first ordered", firsts)
	}
	if ob, ok := firsts[1]["$orderby"]; ok {
		t.Errorf("poll 2 still asked $orderby=%v after inverted probe answers", ob)
	}
	if honoured, known := c.orderVerdict("tenant-1", cfg); !known || honoured {
		t.Errorf("cached verdict = (%v, known %v), want a cached no", honoured, known)
	}
	if !strings.Contains(logs.String(), "do not bracket what the poll read") {
		t.Errorf("no Warn about the bracket: %s", logs)
	}
}

func TestOrderBrackets(t *testing.T) {
	at := func(m int) time.Time { return aheadNow.Add(time.Duration(m) * time.Minute) }
	lo, hi := at(-60), at(-10)
	for _, tc := range []struct {
		name     string
		lo, hi   time.Time
		last     time.Time
		haveLast bool
		want     bool
	}{
		{"lo == hi", lo, lo, lo, true, false},
		{"lo == hi, nothing read", lo, lo, time.Time{}, false, false},
		{"lo > hi", hi, lo, at(-30), true, false},
		{"last on lo", lo, hi, lo, true, true},
		{"last on hi", lo, hi, hi, true, true},
		{"last between", lo, hi, at(-30), true, true},
		{"last above hi", lo, hi, at(-5), true, false},
		{"last below lo", lo, hi, at(-70), true, false},
		{"nothing read, lo < hi", lo, hi, time.Time{}, false, true},
	} {
		if got := orderBrackets(tc.lo, tc.hi, tc.last, tc.haveLast); got != tc.want {
			t.Errorf("%s: orderBrackets = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// BC ignores $orderby and the record it answers both probes with is edited
// between them, so the two answers bracket what the poll read. It is still
// the same record: the order is not kept.
func TestOrderProbe_EditBetweenProbesIsCaught(t *testing.T) {
	lowerPageCap(t, 2)
	recs := map[string]string{
		"a": mins(-60), "b": mins(-50), "c": mins(-40), "d": mins(-30),
		"e": mins(-90), "f": mins(-80),
	}
	bc, c, got, mu, logs := orderedFixture(t, lmField, 2, recs)
	bc.ignoreOrder = true
	bc.afterProbe = func(dir string) {
		if dir == "asc" {
			bc.recs["a"] = mins(-10)
		}
	}
	cfg := bc.cfg()
	for i := 0; i < 2; i++ {
		if _, err := pollOnce(t, c, got, mu, cfg); !errors.Is(err, errPageCap) {
			t.Fatalf("poll %d: err = %v, want errPageCap", i+1, err)
		}
	}
	orderedOff(t, c, bc, cfg)
	if !strings.Contains(logs.String(), "with the same record") {
		t.Errorf("no Warn naming the same record: %s", logs)
	}
}

// Records out of order on page 2, after the probe said yes: the node stops
// asking for the order and caches the "no".
func TestOrderProbe_DisorderOnALaterPageTurnsOrderOff(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	var mu sync.Mutex
	var firsts []url.Values
	var apiURL string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if answerOrderProbe(w, r, "2026-10-01T00:00:00Z", "2026-10-03T00:00:00Z") {
			return
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"value":[{"number":"C","lastModifiedDateTime":"2026-10-01T05:00:00Z"}]}`)
			return
		}
		mu.Lock()
		firsts = append(firsts, r.URL.Query())
		mu.Unlock()
		fmt.Fprintf(w, `{"value":[{"number":"A","lastModifiedDateTime":"2026-10-01T10:00:00Z"},`+
			`{"number":"B","lastModifiedDateTime":"2026-10-02T10:00:00Z"}],"@odata.nextLink":"%s?page=2"}`, apiURL)
	}))
	defer api.Close()
	apiURL = api.URL + "/companies(GUID)/items"

	c, _, _ := newTestConsumer()
	logs := &stLogBuf{}
	c.logger = slog.New(slog.NewTextHandler(logs, nil))
	cfg := &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: api.URL, TokenURL: tokenSrv.URL, Incremental: true, NodeID: "src",
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(http.DefaultClient)
	for i := 0; i < 2; i++ {
		if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
			t.Fatalf("poll %d: %v", i+1, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(firsts) != 2 || firsts[0].Get("$orderby") == "" {
		t.Fatalf("first-page requests %v, want two, the first ordered", firsts)
	}
	if ob, ok := firsts[1]["$orderby"]; ok {
		t.Errorf("poll 2 still asked $orderby=%v after records came back out of order", ob)
	}
	if honoured, known := c.orderVerdict("tenant-1", cfg); !known || honoured {
		t.Errorf("cached verdict = (%v, known %v), want a cached no", honoured, known)
	}
	if !strings.Contains(logs.String(), "out of cursor_field order") {
		t.Errorf("no Warn about the order: %s", logs)
	}
}

// A cached "no" stays: a later "yes" for the same key does not undo it.
func TestSetOrderVerdict_NoIsFinal(t *testing.T) {
	c, _, _ := newTestConsumer()
	cfg := &BCConfig{CompanyID: "GUID", APIBaseURL: "https://bc.example/api/v2.0"}
	c.setOrderVerdict("tenant-1", cfg, true)
	if honoured, known := c.orderVerdict("tenant-1", cfg); !known || !honoured {
		t.Fatalf("verdict = (%v, %v), want a cached yes", honoured, known)
	}
	c.setOrderVerdict("tenant-1", cfg, false)
	c.setOrderVerdict("tenant-1", cfg, true)
	if honoured, known := c.orderVerdict("tenant-1", cfg); !known || honoured {
		t.Errorf("verdict = (%v, %v) after no then yes, want the no kept", honoured, known)
	}
}

// Every record on one value, BC honouring the order: nothing is decided,
// whether the two probes answer with different records or, when BC breaks
// ties by key ascending both ways, with the same one. The node keeps asking
// for the order, stores no resume point, and probes again on the next poll.
// On a date field a stopped poll's resume point would be that date, so the
// watermark check bites there.
func TestOrderProbe_OneValueDecidesNothing(t *testing.T) {
	for _, tc := range []struct {
		name, field, value string
		tiesByKeyAsc       bool
	}{
		{"DateTime, ties reversed descending", lmField, mins(-60), false},
		{"DateTime, ties by key ascending both ways", lmField, mins(-60), true},
		{"date, ties reversed descending", dateField, "2026-01-05", false},
		{"date, ties by key ascending both ways", dateField, "2026-01-05", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lowerPageCap(t, 2)
			v := tc.value
			bc, c, got, mu, _ := orderedFixture(t, tc.field, 2, map[string]string{"a": v, "b": v, "c": v, "d": v, "e": v})
			bc.tiesByKeyAsc = tc.tiesByKeyAsc
			cfg := bc.cfg()
			for i := 0; i < 2; i++ {
				if _, err := pollOnce(t, c, got, mu, cfg); !errors.Is(err, errPageCap) {
					t.Fatalf("poll %d: err = %v, want errPageCap", i+1, err)
				}
			}
			if _, known := c.orderVerdict("tenant-1", cfg); known {
				t.Error("a verdict was cached from probes that found one value")
			}
			if n := len(bc.probeQueries()); n != 4 {
				t.Errorf("%d probe requests over two polls, want 4 (asked again)", n)
			}
			for i, q := range bc.firstPageQueries() {
				if q.Get("$orderby") != tc.field+" asc" {
					t.Errorf("poll %d asked $orderby=%q, want it kept", i+1, q.Get("$orderby"))
				}
			}
			if cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src"); cp != nil && cp.LastProcessedMessageID != "" {
				t.Errorf("watermark %q stored", cp.LastProcessedMessageID)
			}
		})
	}
}

// A cursor_field whose values are not dates (a code) never gets a resume
// point, so it is never probed.
func TestOrderProbe_UnparseableCursorDoesNotProbe(t *testing.T) {
	lowerPageCap(t, 2)
	bc, c, got, mu, logs := orderedFixture(t, "code", 2, map[string]string{"a": "A1", "b": "B1", "c": "C1", "d": "D1", "e": "E1"})
	cfg := bc.cfg()
	for i := 0; i < 2; i++ {
		if _, err := pollOnce(t, c, got, mu, cfg); !errors.Is(err, errPageCap) {
			t.Fatalf("poll %d: err = %v, want errPageCap", i+1, err)
		}
	}
	if n := len(bc.probeQueries()); n != 0 {
		t.Errorf("%d probe requests for a field that never parses, want 0", n)
	}
	if strings.Contains(logs.String(), "order check") {
		t.Errorf("an order-check line was logged: %s", logs)
	}
}
