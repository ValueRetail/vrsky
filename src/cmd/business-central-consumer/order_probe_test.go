package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

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
	if out := logs.String(); !strings.Contains(out, "do not bracket what the poll read") || !strings.Contains(out, "level=WARN") {
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
