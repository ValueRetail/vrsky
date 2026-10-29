package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

var aheadNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func stamp(d time.Duration) string { return aheadNow.Add(d).Format(time.RFC3339Nano) }

func recs(t *testing.T, fields ...string) []json.RawMessage {
	t.Helper()
	// fields come as "id=stamp" pairs.
	var out []json.RawMessage
	for _, f := range fields {
		id, ts, _ := strings.Cut(f, "=")
		out = append(out, json.RawMessage(fmt.Sprintf(`{"id":%q,"lastModifiedDateTime":%q}`, id, ts)))
	}
	return out
}

// TestCursorTracker_BoundsTheWatermark — a cursor value past the ceiling must
// not become the watermark, however the page is ordered, and must be reported.
// The consumer sets the ceiling to now (review of #592: now + skew lost
// records BC stamped before a watermark that sat in the future).
func TestCursorTracker_BoundsTheWatermark(t *testing.T) {
	ceiling := aheadNow
	cases := []struct {
		name      string
		ceiling   time.Time
		records   []string
		wantMark  string
		wantAhead []aheadRecord
	}{
		{"sane records move the watermark", ceiling,
			[]string{"a=" + stamp(-2*time.Hour), "b=" + stamp(-time.Hour)},
			stamp(-time.Hour), nil},
		{"far-future record is ignored", ceiling,
			[]string{"a=" + stamp(-time.Hour), "f=2099-01-01T00:00:00Z"},
			stamp(-time.Hour), []aheadRecord{{"f", "2099-01-01T00:00:00Z"}}},
		{"far-future record first on the page", ceiling,
			[]string{"f=2099-01-01T00:00:00Z", "a=" + stamp(-time.Hour)},
			stamp(-time.Hour), []aheadRecord{{"f", "2099-01-01T00:00:00Z"}}},
		{"only a far-future record leaves no watermark", ceiling,
			[]string{"f=2099-01-01T00:00:00Z"},
			"", []aheadRecord{{"f", "2099-01-01T00:00:00Z"}}},
		{"exactly now is a watermark", ceiling,
			[]string{"a=" + stamp(0)},
			stamp(0), nil},
		{"one second ahead is held back", ceiling,
			[]string{"a=" + stamp(-time.Hour), "f=" + stamp(time.Second)},
			stamp(-time.Hour), []aheadRecord{{"f", stamp(time.Second)}}},
		{"at now + skew is held back", ceiling,
			[]string{"a=" + stamp(maxCursorSkew)},
			"", []aheadRecord{{"a", stamp(maxCursorSkew)}}},
		{"inside the skew is held back", ceiling,
			[]string{"a=" + stamp(time.Minute)},
			"", []aheadRecord{{"a", stamp(time.Minute)}}},
		{"past the skew is held back", ceiling,
			[]string{"a=" + stamp(-time.Hour), "f=" + stamp(maxCursorSkew+time.Second)},
			stamp(-time.Hour), []aheadRecord{{"f", stamp(maxCursorSkew + time.Second)}}},
		{"no ceiling behaves as before the bound existed", time.Time{},
			[]string{"a=" + stamp(-time.Hour), "f=2099-01-01T00:00:00Z"},
			"2099-01-01T00:00:00Z", nil},
		{"unparsable and missing values are skipped", ceiling,
			[]string{"a=not-a-time", "b="},
			"", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := cursorTracker{ceiling: tc.ceiling}
			got := tr.observe(recs(t, tc.records...), "lastModifiedDateTime")
			if tr.raw != tc.wantMark {
				t.Errorf("watermark = %q, want %q", tr.raw, tc.wantMark)
			}
			if !reflect.DeepEqual(got, tc.wantAhead) {
				t.Errorf("ahead = %+v, want %+v", got, tc.wantAhead)
			}
		})
	}
}

func TestRecordKey(t *testing.T) {
	cases := []struct{ name, rec, want string }{
		{"id wins", `{"id":"g1","number":"N1"}`, "g1"},
		{"number when no id", `{"number":"N1"}`, "N1"},
	}
	for _, tc := range cases {
		var f map[string]json.RawMessage
		_ = json.Unmarshal([]byte(tc.rec), &f)
		if got := recordKey(json.RawMessage(tc.rec), f); got != tc.want {
			t.Errorf("%s: key = %q, want %q", tc.name, got, tc.want)
		}
	}
	a, b := `{"x":1}`, `{"x":2}`
	var fa, fb map[string]json.RawMessage
	_ = json.Unmarshal([]byte(a), &fa)
	_ = json.Unmarshal([]byte(b), &fb)
	ka, kb := recordKey(json.RawMessage(a), fa), recordKey(json.RawMessage(b), fb)
	if ka == kb || !strings.HasPrefix(ka, "sha256:") {
		t.Errorf("records without id or number need distinct hashed keys, got %q and %q", ka, kb)
	}
}

// aheadBC is a Business Central that honours `lastModifiedDateTime gt X`, so
// a test sees what a real watermark would hide.
type aheadBC struct {
	srv  *httptest.Server
	tok  *httptest.Server
	mu   sync.Mutex
	recs map[string]string // id → lastModifiedDateTime
	asks []string          // $filter of every request
}

func newAheadBC(t *testing.T, recs map[string]string) *aheadBC {
	t.Helper()
	b := &aheadBC{recs: recs, tok: bcTestToken(t)}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		f := r.URL.Query().Get("$filter")
		b.asks = append(b.asks, f)
		var after time.Time
		if _, v, ok := strings.Cut(f, " gt "); ok {
			after, _ = time.Parse(time.RFC3339, v)
		}
		var out []string
		for id, ts := range b.recs {
			if pt, _ := time.Parse(time.RFC3339, ts); pt.After(after) {
				out = append(out, fmt.Sprintf(`{"id":%q,"lastModifiedDateTime":%q}`, id, ts))
			}
		}
		fmt.Fprintf(w, `{"value":[%s]}`, strings.Join(out, ","))
	}))
	t.Cleanup(func() { b.srv.Close(); b.tok.Close() })
	return b
}

func (b *aheadBC) set(id, ts string) { b.mu.Lock(); b.recs[id] = ts; b.mu.Unlock() }
func (b *aheadBC) lastAsk() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.asks[len(b.asks)-1]
}

func (b *aheadBC) cfg() *BCConfig {
	return &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: b.srv.URL, TokenURL: b.tok.URL,
		Incremental: true, NodeID: "src",
	}
}

// poll runs one fetch and returns the record ids it published.
func poll(t *testing.T, c *bcConsumer, got *[]*envelope.Envelope, mu *sync.Mutex, cfg *BCConfig) []string {
	t.Helper()
	mu.Lock()
	before := len(*got)
	mu.Unlock()
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
		WithHTTPClient(http.DefaultClient)
	if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	var ids []string
	for _, env := range (*got)[before:] {
		var rs []map[string]any
		if err := json.Unmarshal(env.Payload, &rs); err != nil {
			t.Fatalf("payload: %v", err)
		}
		for _, r := range rs {
			ids = append(ids, r["id"].(string))
		}
	}
	return ids
}

func sorted(ids []string) string {
	c := append([]string(nil), ids...)
	for i := range c {
		for j := i + 1; j < len(c); j++ {
			if c[j] < c[i] {
				c[i], c[j] = c[j], c[i]
			}
		}
	}
	return strings.Join(c, ",")
}

// TestIncremental_FarFutureRecordDoesNotFreezeTheWatermark is the bug: before
// the fix the record stamped 2099 became the watermark, and "after 2099"
// returns nothing, so b, c and d below were never read.
func TestIncremental_FarFutureRecordDoesNotFreezeTheWatermark(t *testing.T) {
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "f": "2099-01-01T00:00:00Z"})
	c, got, mu := newTestConsumer()
	clock := aheadNow
	c.now = func() time.Time { return clock }
	var logs bytes.Buffer
	c.logger = slog.New(slog.NewTextHandler(&logs, nil))
	cfg := bc.cfg()

	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "a,f" {
		t.Fatalf("poll 1 published %v, want a and f (the far-future record is still delivered)", ids)
	}
	bc.set("b", stamp(-time.Minute))
	ids := poll(t, c, got, mu, cfg)
	if want := "lastModifiedDateTime gt " + stamp(-time.Hour); bc.lastAsk() != want {
		t.Errorf("poll 2 asked %q, want %q — the 2099 record moved the watermark", bc.lastAsk(), want)
	}
	if sorted(ids) != "b" {
		t.Errorf("poll 2 published %v, want only b (f was already sent)", ids)
	}

	bc.set("c", stamp(-time.Second))
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "c" {
		t.Errorf("poll 3 published %v, want only c", ids)
	}
	if want := "lastModifiedDateTime gt " + stamp(-time.Minute); bc.lastAsk() != want {
		t.Errorf("poll 3 asked %q, want %q", bc.lastAsk(), want)
	}
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Errorf("poll 4 on an unchanged feed published %v, want nothing", ids)
	}
	if n := strings.Count(logs.String(), "record_id=f"); n != 1 || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("want exactly one Warn naming record f, got %d:\n%s", n, logs.String())
	}
}

// TestIncremental_AheadRecordIsEditedAndResent — an edit changes the cursor
// value, which makes it a different version of the record: it goes out again.
func TestIncremental_AheadRecordIsEditedAndResent(t *testing.T) {
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "f": "2099-01-01T00:00:00Z"})
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	cfg := bc.cfg()
	poll(t, c, got, mu, cfg)
	bc.set("f", "2099-06-01T00:00:00Z")
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "f" {
		t.Errorf("edited far-future record: published %v, want f once more", ids)
	}
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Errorf("unchanged again: published %v, want nothing", ids)
	}
}

// TestIncremental_AheadRecordBecomesCurrent — when the clock reaches the
// record's stamp it is no longer ahead: it must not be sent a second time,
// and from then on it is an ordinary watermark.
func TestIncremental_AheadRecordBecomesCurrent(t *testing.T) {
	future := stamp(time.Hour)
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "f": future})
	c, got, mu := newTestConsumer()
	clock := aheadNow
	c.now = func() time.Time { return clock }
	cfg := bc.cfg()
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "a,f" {
		t.Fatalf("poll 1 published %v", ids)
	}
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Fatalf("poll 2 published %v while f was still ahead", ids)
	}
	if want := "lastModifiedDateTime gt " + stamp(-time.Hour); bc.lastAsk() != want {
		t.Fatalf("an hour-ahead record moved the watermark: asked %q, want %q", bc.lastAsk(), want)
	}
	clock = aheadNow.Add(2 * time.Hour)
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Errorf("once current, f was sent again: %v", ids)
	}
	poll(t, c, got, mu, cfg)
	if got := bc.lastAsk(); got != "lastModifiedDateTime gt "+future {
		t.Errorf("watermark = %q, want it to have caught up to f's stamp", got)
	}
}

// TestIncremental_AheadStateSurvivesRestart — the record of what was sent
// ahead lives in the checkpoint, not in the process.
func TestIncremental_AheadStateSurvivesRestart(t *testing.T) {
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "f": "2099-01-01T00:00:00Z"})
	c1, got1, mu1 := newTestConsumer()
	c1.now = func() time.Time { return aheadNow }
	poll(t, c1, got1, mu1, bc.cfg())

	c2, got2, mu2 := newTestConsumer()
	c2.now = func() time.Time { return aheadNow }
	c2.checkpoints = c1.checkpoints
	if ids := poll(t, c2, got2, mu2, bc.cfg()); len(ids) != 0 {
		t.Errorf("after a restart published %v, want nothing", ids)
	}
}

// TestIncremental_SaneRecordsBehaveAsBefore — nothing dated after now: no
// extra state and no records withheld.
func TestIncremental_SaneRecordsBehaveAsBefore(t *testing.T) {
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "b": stamp(-time.Minute)})
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	cfg := bc.cfg()
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "a,b" {
		t.Fatalf("published %v", ids)
	}
	if st := cfg.nodeState(); st != nil {
		t.Errorf("state = %s, want none when nothing was ahead", st)
	}
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Errorf("second poll published %v, want nothing", ids)
	}
	if want := "lastModifiedDateTime gt " + stamp(-time.Minute); bc.lastAsk() != want {
		t.Errorf("asked %q, want %q", bc.lastAsk(), want)
	}
}

// TestIncremental_RecordInsideTheSkewIsHeldBack inverts what the first
// version of #592 asserted: a record a minute ahead used to become the
// watermark at once. It is published, but held out of the watermark until
// the clock passes it, then taken without a second send.
func TestIncremental_RecordInsideTheSkewIsHeldBack(t *testing.T) {
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "b": stamp(time.Minute)})
	c, got, mu := newTestConsumer()
	clock := aheadNow
	c.now = func() time.Time { return clock }
	cfg := bc.cfg()
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "a,b" {
		t.Fatalf("published %v", ids)
	}
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Errorf("second poll published %v, want nothing", ids)
	}
	if want := "lastModifiedDateTime gt " + stamp(-time.Hour); bc.lastAsk() != want {
		t.Errorf("b (a minute ahead) moved the watermark: asked %q, want %q", bc.lastAsk(), want)
	}
	clock = aheadNow.Add(2 * time.Minute)
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Errorf("once current, b was sent again: %v", ids)
	}
	poll(t, c, got, mu, cfg)
	if want := "lastModifiedDateTime gt " + stamp(time.Minute); bc.lastAsk() != want {
		t.Errorf("asked %q, want the watermark caught up to b: %q", bc.lastAsk(), want)
	}
	if st := cfg.nodeState(); st != nil {
		t.Errorf("state = %s, want it empty once b is current", st)
	}
}

// TestNodeState_CarriesPicturesAndAhead — both live in one checkpoint column.
func TestNodeState_CarriesPicturesAndAhead(t *testing.T) {
	cfg := &BCConfig{
		pictureSeen: map[string]seenPicture{"r1": {PictureID: "p1", Number: "N1"}},
		aheadSeen:   map[string]string{"f": "2099-01-01T00:00:00Z"},
	}
	var st pictureState
	if err := json.Unmarshal(cfg.nodeState(), &st); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.Pictures, cfg.pictureSeen) || !reflect.DeepEqual(st.Ahead, cfg.aheadSeen) {
		t.Errorf("round trip lost state: %+v", st)
	}
}

// TestIncremental_AheadStateIsPruned — a record that stops being ahead (its
// date was corrected, or it is gone) must not stay in the checkpoint forever.
func TestIncremental_AheadStateIsPruned(t *testing.T) {
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "f": "2099-01-01T00:00:00Z"})
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	cfg := bc.cfg()
	poll(t, c, got, mu, cfg)
	if len(cfg.aheadSeen) != 1 || cfg.nodeState() == nil {
		t.Fatalf("setup: aheadSeen = %v", cfg.aheadSeen)
	}
	bc.set("f", stamp(-time.Second)) // the date was corrected
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "f" {
		t.Fatalf("corrected record: published %v, want f", ids)
	}
	if len(cfg.aheadSeen) != 0 || cfg.nodeState() != nil {
		t.Errorf("aheadSeen = %v, state = %s; want both empty", cfg.aheadSeen, cfg.nodeState())
	}
	cp, err := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src")
	if err != nil || cp == nil {
		t.Fatalf("checkpoint: %v %v", cp, err)
	}
	if strings.Contains(string(cp.State), "2099") {
		t.Errorf("stored state still names the corrected record: %s", cp.State)
	}
}
