package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests in this file use aheadNow (2026-10-29 12:00 UTC) as the clock.
// For an Edm.Date cursor the watermark ceiling is yesterday (2026-10-28):
// see cursorTracker.

func dateRecs(pairs ...string) []json.RawMessage {
	var out []json.RawMessage
	for _, p := range pairs {
		id, d, _ := strings.Cut(p, "=")
		out = append(out, json.RawMessage(fmt.Sprintf(`{"id":%q,"postingDate":%q}`, id, d)))
	}
	return out
}

// TestCursorTracker_DateValues — an Edm.Date value (YYYY-MM-DD) is a
// watermark candidate like a DateTime, with its own ceiling (#602).
func TestCursorTracker_DateValues(t *testing.T) {
	cases := []struct {
		name      string
		ceiling   time.Time
		records   []string
		wantMark  string
		wantAhead []aheadRecord
	}{
		{"past dates set the watermark to the newest", aheadNow,
			[]string{"a=2026-10-01", "b=2026-10-20", "c=2026-10-10"}, "2026-10-20", nil},
		{"yesterday is a watermark", aheadNow,
			[]string{"a=2026-10-28"}, "2026-10-28", nil},
		{"today is held back", aheadNow,
			[]string{"a=2026-10-20", "t=2026-10-29"}, "2026-10-20",
			[]aheadRecord{{"t", "2026-10-29"}}},
		{"tomorrow is held back", aheadNow,
			[]string{"a=2026-10-20", "t=2026-10-30"}, "2026-10-20",
			[]aheadRecord{{"t", "2026-10-30"}}},
		{"far future is held back", aheadNow,
			[]string{"f=2099-01-01", "a=2026-10-20"}, "2026-10-20",
			[]aheadRecord{{"f", "2099-01-01"}}},
		{"only a future date leaves no watermark", aheadNow,
			[]string{"f=2099-01-01"}, "", []aheadRecord{{"f", "2099-01-01"}}},
		{"yesterday in UTC stays a candidate late in the day", time.Date(2026, 10, 29, 23, 59, 59, 0, time.UTC),
			[]string{"a=2026-10-28", "b=2026-10-29"}, "2026-10-28", []aheadRecord{{"b", "2026-10-29"}}},
		{"just after midnight UTC today is still held back", time.Date(2026, 10, 29, 0, 0, 1, 0, time.UTC),
			[]string{"a=2026-10-28", "b=2026-10-29"}, "2026-10-28", []aheadRecord{{"b", "2026-10-29"}}},
		{"BC's blank date (0001-01-01) is never a watermark", aheadNow,
			[]string{"a=0001-01-01"}, "", nil},
		{"BC's blank date does not hide a real one", aheadNow,
			[]string{"a=0001-01-01", "b=2026-10-20"}, "2026-10-20", nil},
		{"not a date is skipped", aheadNow,
			[]string{"a=2026-13-45", "b=29/10/2026", "c=2026-10-20"}, "2026-10-20", nil},
		{"no ceiling takes any date", time.Time{},
			[]string{"a=2026-10-20", "f=2099-01-01"}, "2099-01-01", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := cursorTracker{ceiling: tc.ceiling}
			got := tr.observe(dateRecs(tc.records...), "postingDate")
			if tr.raw != tc.wantMark {
				t.Errorf("watermark = %q, want %q", tr.raw, tc.wantMark)
			}
			if !reflect.DeepEqual(got, tc.wantAhead) {
				t.Errorf("ahead = %+v, want %+v", got, tc.wantAhead)
			}
		})
	}
}

// TestCursorTracker_DateTimeUnchanged — DateTime values keep the ceiling at
// now, a date-only ceiling does not leak into them.
func TestCursorTracker_DateTimeUnchanged(t *testing.T) {
	tr := cursorTracker{ceiling: aheadNow}
	// Both are on today's date; only the one before now is a candidate.
	got := tr.observe(recs(t, "a="+stamp(-time.Hour), "b="+stamp(time.Hour)), "lastModifiedDateTime")
	if tr.raw != stamp(-time.Hour) || len(got) != 1 || got[0].Key != "b" {
		t.Errorf("watermark = %q, ahead = %+v", tr.raw, got)
	}
	if len(tr.atWatermark) != 0 {
		t.Errorf("a DateTime watermark must not remember records, got %v", tr.atWatermark)
	}
}

// TestCursorTracker_RemembersTheWatermarkDay — a date watermark is read back
// with `ge`, so the records on that day come back and must be recognised.
func TestCursorTracker_RemembersTheWatermarkDay(t *testing.T) {
	cases := []struct {
		name    string
		batches [][]string
		want    map[string]string
	}{
		{"records on the newest day", [][]string{{"a=2026-10-19", "b=2026-10-20", "c=2026-10-20"}},
			map[string]string{"b": "2026-10-20", "c": "2026-10-20"}},
		{"a newer day on a later page drops the earlier set",
			[][]string{{"a=2026-10-19", "b=2026-10-19"}, {"c=2026-10-20"}},
			map[string]string{"c": "2026-10-20"}},
		{"an older day on a later page is not added",
			[][]string{{"c=2026-10-20"}, {"a=2026-10-19"}},
			map[string]string{"c": "2026-10-20"}},
		{"the same day on both pages", [][]string{{"a=2026-10-20"}, {"b=2026-10-20"}},
			map[string]string{"a": "2026-10-20", "b": "2026-10-20"}},
		{"future dates are the ahead set's business", [][]string{{"a=2026-10-20", "f=2099-01-01"}},
			map[string]string{"a": "2026-10-20"}},
		{"nothing observed", [][]string{{}}, map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := cursorTracker{ceiling: aheadNow}
			for _, b := range tc.batches {
				tr.observe(dateRecs(b...), "postingDate")
			}
			got := tr.atWatermark
			if got == nil {
				got = map[string]string{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("remembered = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFilterWithCursor_Date — a date watermark is written as the OData v4
// Edm.Date literal (no quotes) and read back with `ge`; DateTime keeps `gt`.
func TestFilterWithCursor_Date(t *testing.T) {
	cases := []struct {
		name, filter, field, cursor, want string
	}{
		{"date", "", "postingDate", "2026-10-20", "postingDate ge 2026-10-20"},
		{"date with a configured filter", "status eq 'Open' or status eq 'Draft'", "postingDate", "2026-10-20",
			"(status eq 'Open' or status eq 'Draft') and postingDate ge 2026-10-20"},
		{"datetime keeps gt", "", "lastModifiedDateTime", "2026-10-20T10:00:00Z", "lastModifiedDateTime gt 2026-10-20T10:00:00Z"},
		{"datetime with fraction keeps gt", "", "lastModifiedDateTime", "2026-10-20T10:00:00.500Z", "lastModifiedDateTime gt 2026-10-20T10:00:00.500Z"},
		{"no cursor, no clause", "x eq 1", "postingDate", "", "x eq 1"},
	}
	for _, tc := range cases {
		cfg := &BCConfig{Filter: tc.filter, CursorField: tc.field}
		if got := cfg.filterWithCursor(tc.cursor); got != tc.want {
			t.Errorf("%s: filter = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// dateBC is a Business Central whose cursor field is an Edm.Date. It honours
// `postingDate ge|gt YYYY-MM-DD` and, like the real thing, rejects a literal
// that is not an unquoted date.
type dateBC struct {
	*aheadBC
}

var dateFilter = regexp.MustCompile(`^postingDate (ge|gt) (\d{4}-\d{2}-\d{2})$`)

func newDateBC(t *testing.T, recs map[string]string) *dateBC {
	t.Helper()
	b := &dateBC{aheadBC: &aheadBC{recs: recs, tok: bcTestToken(t)}}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		f := r.URL.Query().Get("$filter")
		b.asks = append(b.asks, f)
		op, from := "", ""
		if f != "" {
			m := dateFilter.FindStringSubmatch(f)
			if m == nil {
				http.Error(w, `{"error":{"message":"bad filter"}}`, http.StatusBadRequest)
				return
			}
			op, from = m[1], m[2]
		}
		ids := make([]string, 0, len(b.recs))
		for id := range b.recs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		var out []string
		for _, id := range ids {
			d := b.recs[id]
			if (op == "ge" && d >= from) || (op == "gt" && d > from) || op == "" {
				out = append(out, fmt.Sprintf(`{"id":%q,"postingDate":%q}`, id, d))
			}
		}
		fmt.Fprintf(w, `{"value":[%s]}`, strings.Join(out, ","))
	}))
	t.Cleanup(func() { b.srv.Close(); b.tok.Close() })
	return b
}

func (b *dateBC) cfg() *BCConfig {
	cfg := b.aheadBC.cfg()
	cfg.CursorField = "postingDate"
	return cfg
}

// dateFixture is the usual setup: the clock is aheadNow (2026-10-29), the
// ceiling for a date is therefore 2026-10-28.
func dateFixture(t *testing.T, recs map[string]string) (*dateBC, *bcConsumer, *[]*envelope.Envelope, *sync.Mutex, *BCConfig, *bytes.Buffer, *time.Time) {
	t.Helper()
	bc := newDateBC(t, recs)
	c, got, mu := newTestConsumer()
	clock := aheadNow
	c.now = func() time.Time { return clock }
	var logs bytes.Buffer
	c.logger = slog.New(slog.NewTextHandler(&logs, nil))
	return bc, c, got, mu, bc.cfg(), &logs, &clock
}

// TestDateCursor_FirstPollSetsTheWatermark is the bug: before the fix a date
// never became a watermark, so poll 2 asked for no filter and read and sent
// the whole entity again.
func TestDateCursor_FirstPollSetsTheWatermark(t *testing.T) {
	bc, c, got, mu, cfg, _, _ := dateFixture(t, map[string]string{
		"a": "2026-10-01", "b": "2026-10-20", "c": "2026-10-20", "d": "2026-10-10"})
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "a,b,c,d" {
		t.Fatalf("poll 1 published %v", ids)
	}
	ids := poll(t, c, got, mu, cfg)
	if want := "postingDate ge 2026-10-20"; bc.lastAsk() != want {
		t.Errorf("poll 2 asked %q, want %q", bc.lastAsk(), want)
	}
	if len(ids) != 0 {
		t.Errorf("poll 2 published %v, want nothing: nothing is new", ids)
	}
}

// TestDateCursor_OnlyNewDatesAreSent — the watermark advances with the dates.
func TestDateCursor_OnlyNewDatesAreSent(t *testing.T) {
	bc, c, got, mu, cfg, _, _ := dateFixture(t, map[string]string{"a": "2026-10-01", "b": "2026-10-20"})
	poll(t, c, got, mu, cfg)
	bc.set("n1", "2026-10-22")
	bc.set("n2", "2026-10-25")
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "n1,n2" {
		t.Fatalf("poll 2 published %v, want n1,n2", ids)
	}
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Errorf("poll 3 published %v, want nothing", ids)
	}
	if want := "postingDate ge 2026-10-25"; bc.lastAsk() != want {
		t.Errorf("poll 3 asked %q, want %q", bc.lastAsk(), want)
	}
}

// TestDateCursor_RecordAddedLaterOnTheWatermarkDayIsNotLost — a date has no
// time of day, so a record posted after the poll with the same date as the
// watermark is not "after" it. `gt` would hide it for good; `ge` returns the
// day again and the node leaves out what it already sent.
func TestDateCursor_RecordAddedLaterOnTheWatermarkDayIsNotLost(t *testing.T) {
	bc, c, got, mu, cfg, _, _ := dateFixture(t, map[string]string{"a": "2026-10-01", "b": "2026-10-20"})
	poll(t, c, got, mu, cfg)
	bc.set("late", "2026-10-20")
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "late" {
		t.Fatalf("published %v, want only the late record (b was already sent)", ids)
	}
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Errorf("published %v, want nothing: late was sent once", ids)
	}
	// And once the watermark has moved on, the old day's keys are dropped.
	bc.set("n", "2026-10-22")
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "n" {
		t.Fatalf("published %v, want n", ids)
	}
	if want := map[string]string{"n": "2026-10-22"}; !reflect.DeepEqual(cfg.aheadSeen, want) {
		t.Errorf("remembered = %v, want only the new watermark day %v", cfg.aheadSeen, want)
	}
}

// TestDateCursor_SurvivesARestart — what was sent on the watermark day is in
// the checkpoint, so a new process does not send it again, and still sends
// the record that arrived while it was down.
func TestDateCursor_SurvivesARestart(t *testing.T) {
	bc, c1, got1, mu1, cfg1, _, _ := dateFixture(t, map[string]string{"a": "2026-10-01", "b": "2026-10-20"})
	poll(t, c1, got1, mu1, cfg1)
	bc.set("late", "2026-10-20")

	c2, got2, mu2 := newTestConsumer()
	c2.now = func() time.Time { return aheadNow }
	c2.checkpoints = c1.checkpoints
	if ids := poll(t, c2, got2, mu2, bc.cfg()); sorted(ids) != "late" {
		t.Errorf("after a restart published %v, want only late", ids)
	}
}

// TestDateCursor_FutureDatesFollowTheAheadRules (#591) — a date after the
// ceiling is sent once, stays out of the watermark, is not sent again, and
// is not sent a second time once the clock reaches it.
func TestDateCursor_FutureDatesFollowTheAheadRules(t *testing.T) {
	bc, c, got, mu, cfg, logs, clock := dateFixture(t, map[string]string{
		"a": "2026-10-20", "today": "2026-10-29", "f": "2099-01-01"})
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "a,f,today" {
		t.Fatalf("poll 1 published %v, want all three", ids)
	}
	bc.set("b", "2026-10-25")
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "b" {
		t.Fatalf("poll 2 published %v, want only b (the future records were sent)", ids)
	}
	if want := "postingDate ge 2026-10-20"; bc.lastAsk() != want {
		t.Errorf("poll 2 asked %q, want %q: the 2099 record moved the watermark", bc.lastAsk(), want)
	}
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Errorf("poll 3 published %v, want nothing", ids)
	}
	if want := "postingDate ge 2026-10-25"; bc.lastAsk() != want {
		t.Errorf("poll 3 asked %q, want %q", bc.lastAsk(), want)
	}
	if n := strings.Count(logs.String(), "record_id=f"); n != 1 || strings.Contains(logs.String(), "record_id=today") {
		t.Errorf("want one Warn for f and none for the date of today, got:\n%s", logs.String())
	}

	// Two days on: today's record is now a watermark candidate. It goes out
	// no second time, and the watermark catches up to it.
	*clock = aheadNow.Add(48 * time.Hour)
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Errorf("once current, the record was sent again: %v", ids)
	}
	poll(t, c, got, mu, cfg)
	if want := "postingDate ge 2026-10-29"; bc.lastAsk() != want {
		t.Errorf("asked %q, want the watermark caught up to %q", bc.lastAsk(), want)
	}
}

// TestDateCursor_TodaysRecordsAreNotLostToTheTimeZone — BC's local date can
// be a day behind or ahead of the pod's UTC date. A watermark on today's UTC
// date would hide a record BC stamps with its own, earlier, today. The
// ceiling of yesterday keeps the watermark behind every time zone.
func TestDateCursor_TodaysRecordsAreNotLostToTheTimeZone(t *testing.T) {
	bc, c, got, mu, cfg, _, clock := dateFixture(t, map[string]string{"a": "2026-10-20", "x": "2026-10-30"})
	// Already the 30th in UTC, still the 29th in New York, where a record is
	// dated the 30th (a posting date set ahead).
	*clock = time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	poll(t, c, got, mu, cfg)
	bc.set("ny", "2026-10-29") // posted in New York, dated its today
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "ny" {
		t.Errorf("published %v, want ny", ids)
	}
}

// TestDateCursor_CorrectedDateIsSentAgain — an edit that moves the date is a
// different version of the record, like an edit of a DateTime.
func TestDateCursor_CorrectedDateIsSentAgain(t *testing.T) {
	bc, c, got, mu, cfg, _, _ := dateFixture(t, map[string]string{"a": "2026-10-01", "b": "2026-10-20"})
	poll(t, c, got, mu, cfg)
	bc.set("b", "2026-10-21")
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "b" {
		t.Errorf("published %v, want b again", ids)
	}
}

// TestDateCursor_WatermarkDaySizeWarn — a business date puts a whole day in
// the checkpoint; past aheadWarnSize that is logged once.
func TestDateCursor_WatermarkDaySizeWarn(t *testing.T) {
	recs := map[string]string{}
	for i := 0; i <= aheadWarnSize; i++ {
		recs[fmt.Sprintf("r%05d", i)] = "2026-10-20"
	}
	_, c, got, mu, cfg, logs, _ := dateFixture(t, recs)
	poll(t, c, got, mu, cfg)
	poll(t, c, got, mu, cfg)
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 || !strings.Contains(logs.String(), "date") {
		t.Errorf("want one Warn about the date cursor, got %d:\n%s", n, logs.String())
	}
}

// TestDateTimeCursor_StillUsesGtAndKeepsNoState — DateTime behaviour is
// untouched: `gt`, nothing remembered for the watermark record.
func TestDateTimeCursor_StillUsesGtAndKeepsNoState(t *testing.T) {
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "b": stamp(-time.Minute)})
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	cfg := bc.cfg()
	poll(t, c, got, mu, cfg)
	bc.set("c", stamp(-time.Second))
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "c" {
		t.Errorf("published %v, want c", ids)
	}
	if want := "lastModifiedDateTime gt " + stamp(-time.Minute); bc.asks[len(bc.asks)-1] != want {
		t.Errorf("asked %q, want %q", bc.asks[len(bc.asks)-1], want)
	}
	if len(cfg.aheadSeen) != 0 || cfg.nodeState() != nil {
		t.Errorf("DateTime kept state: %v", cfg.aheadSeen)
	}
}
