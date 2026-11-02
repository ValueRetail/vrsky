package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

// The tests in this file use the date fixture of cursor_date_test.go: the clock
// is aheadNow (2026-10-29), so a date watermark can be at most 2026-10-28, and
// the cursor field is the Edm.Date postingDate (#629).

// TestLookback_BackdatedRecords is the bug (#629): a record dated before the
// watermark is never read. With lookback_days it is read once, and not again.
func TestLookback_BackdatedRecords(t *testing.T) {
	// Watermark after poll 1 is 2026-10-20 in every case.
	base := map[string]string{"a": "2026-10-01", "b": "2026-10-20"}
	cases := []struct {
		name     string
		days     int
		add      map[string]string // arrives after poll 1
		wantAsk  string            // filter of poll 2
		wantSent string            // published by poll 2
		wantThen string            // published by poll 3, nothing else changed
	}{
		{"backdated inside the window is sent once", 7,
			map[string]string{"late": "2026-10-15"}, "postingDate ge 2026-10-13", "late", ""},
		{"backdated on the first day of the window", 7,
			map[string]string{"late": "2026-10-13"}, "postingDate ge 2026-10-13", "late", ""},
		{"backdated one day before the window is not read", 7,
			map[string]string{"old": "2026-10-12"}, "postingDate ge 2026-10-13", "", ""},
		{"backdated a week before the window is not read", 7,
			map[string]string{"old": "2026-10-05"}, "postingDate ge 2026-10-13", "", ""},
		{"a wider window reaches further back", 30,
			map[string]string{"old": "2026-10-05"}, "postingDate ge 2026-09-20", "old", ""},
		{"a window across a month end", 25,
			map[string]string{"late": "2026-09-28"}, "postingDate ge 2026-09-25", "late", ""},
		{"backdated on the watermark day is sent once", 7,
			map[string]string{"same": "2026-10-20"}, "postingDate ge 2026-10-13", "same", ""},
		{"lookback 0 is today's behaviour: backdated is not read", 0,
			map[string]string{"late": "2026-10-15"}, "postingDate ge 2026-10-20", "", ""},
		{"lookback 1", 1,
			map[string]string{"late": "2026-10-19", "old": "2026-10-18"}, "postingDate ge 2026-10-19", "late", ""},
		{"a new record past the watermark still goes out", 7,
			map[string]string{"n": "2026-10-25"}, "postingDate ge 2026-10-13", "n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bc, c, got, mu, cfg, _, _ := dateFixture(t, copyRecs(base))
			cfg.LookbackDays = tc.days
			if ids := poll(t, c, got, mu, cfg); sorted(ids) != "a,b" {
				t.Fatalf("poll 1 published %v, want a,b", ids)
			}
			for id, d := range tc.add {
				bc.set(id, d)
			}
			ids := poll(t, c, got, mu, cfg)
			if bc.lastAsk() != tc.wantAsk {
				t.Errorf("poll 2 asked %q, want %q", bc.lastAsk(), tc.wantAsk)
			}
			if sorted(ids) != tc.wantSent {
				t.Errorf("poll 2 published %v, want %q", ids, tc.wantSent)
			}
			if ids := poll(t, c, got, mu, cfg); sorted(ids) != tc.wantThen {
				t.Errorf("poll 3 published %v, want %q: nothing changed, so nothing is sent again", ids, tc.wantThen)
			}
		})
	}
}

func copyRecs(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// TestLookback_WatermarkOnlyMovesForward — the window reads days below the
// watermark; what it finds there must never pull the watermark back.
func TestLookback_WatermarkOnlyMovesForward(t *testing.T) {
	bc, c, got, mu, cfg, _, _ := dateFixture(t, map[string]string{"a": "2026-10-01", "b": "2026-10-20"})
	cfg.LookbackDays = 7
	poll(t, c, got, mu, cfg)
	// The newest record is deleted and an older one shows up in the window.
	bc.mu.Lock()
	delete(bc.recs, "b")
	bc.mu.Unlock()
	bc.set("late", "2026-10-15")
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "late" {
		t.Fatalf("published %v, want late", ids)
	}
	poll(t, c, got, mu, cfg)
	if want := "postingDate ge 2026-10-13"; bc.lastAsk() != want {
		t.Errorf("asked %q, want %q: the watermark moved back", bc.lastAsk(), want)
	}
}

// TestLookback_EditedRecordIsSentAgain — a changed date is a different
// version of the record, as without lookback.
func TestLookback_EditedRecordIsSentAgain(t *testing.T) {
	bc, c, got, mu, cfg, _, _ := dateFixture(t, map[string]string{"a": "2026-10-01", "b": "2026-10-20"})
	cfg.LookbackDays = 7
	poll(t, c, got, mu, cfg)
	bc.set("b", "2026-10-18") // moved back, still inside the window
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "b" {
		t.Errorf("published %v, want b again", ids)
	}
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Errorf("published %v, want nothing", ids)
	}
}

// TestLookback_FirstPollAndNoFilter — the first poll has no watermark and
// reads everything; the lookback only widens later filters.
func TestLookback_FirstPollAndNoFilter(t *testing.T) {
	bc, c, got, mu, cfg, _, _ := dateFixture(t, map[string]string{"a": "2026-10-01"})
	cfg.LookbackDays = 7
	poll(t, c, got, mu, cfg)
	if bc.asks[0] != "" {
		t.Errorf("poll 1 asked %q, want no filter", bc.asks[0])
	}
}

// TestLookback_SurvivesARestart — the window is in the checkpoint, so a new
// process neither sends the window again nor misses a record that arrived
// while it was down.
func TestLookback_SurvivesARestart(t *testing.T) {
	bc, c1, got1, mu1, cfg1, _, _ := dateFixture(t, map[string]string{
		"a": "2026-10-01", "b": "2026-10-20", "w": "2026-10-16"})
	cfg1.LookbackDays = 7
	poll(t, c1, got1, mu1, cfg1)
	bc.set("late", "2026-10-15")

	c2, got2, mu2 := newTestConsumer()
	c2.now = func() time.Time { return aheadNow }
	c2.checkpoints = c1.checkpoints
	cfg2 := bc.cfg()
	cfg2.LookbackDays = 7
	if ids := poll(t, c2, got2, mu2, cfg2); sorted(ids) != "late" {
		t.Errorf("after a restart published %v, want only late", ids)
	}
}

// TestLookback_PrunesTheWindow — the remembered set holds the records of the
// window and nothing older, however long the node runs.
func TestLookback_PrunesTheWindow(t *testing.T) {
	recs := map[string]string{}
	bc, c, got, mu, cfg, _, clock := dateFixture(t, recs)
	cfg.LookbackDays = 3
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 40; i++ {
		d := day.AddDate(0, 0, i)
		*clock = d.AddDate(0, 0, 2) // the date is always behind the ceiling
		bc.set(fmt.Sprintf("r%02d", i), d.Format(cursorDateLayout))
		bc.set(fmt.Sprintf("s%02d", i), d.Format(cursorDateLayout))
		ids := poll(t, c, got, mu, cfg)
		if sorted(ids) != fmt.Sprintf("r%02d,s%02d", i, i) {
			t.Fatalf("day %d published %v, want only that day's two records", i, ids)
		}
		// W-3 .. W is four days of two records, fewer at the start.
		want := 2 * min(i+1, 4)
		if n := len(cfg.aheadSeen); n != want {
			t.Fatalf("day %d remembered %d records, want %d", i, n, want)
		}
	}
	cp, _ := c.checkpoints.Get(t.Context(), "tenant-1", "conn-1", "src")
	if cp == nil {
		t.Fatal("no checkpoint saved")
	}
	if len(cp.State) > 2048 {
		t.Errorf("checkpoint state is %d bytes, want it bounded by the window", len(cp.State))
	}
	floor := day.AddDate(0, 0, 39-3).Format(cursorDateLayout)
	for k, v := range cfg.aheadSeen {
		if v < floor {
			t.Errorf("%s dated %s is below the window (%s) but still remembered", k, v, floor)
		}
	}
}

// TestLookback_DeletedRecordLeavesTheSet — the set is rebuilt from each
// poll's feed, so a record BC no longer returns is not kept.
func TestLookback_DeletedRecordLeavesTheSet(t *testing.T) {
	bc, c, got, mu, cfg, _, _ := dateFixture(t, map[string]string{"a": "2026-10-20", "b": "2026-10-18"})
	cfg.LookbackDays = 7
	poll(t, c, got, mu, cfg)
	bc.mu.Lock()
	delete(bc.recs, "b")
	bc.mu.Unlock()
	poll(t, c, got, mu, cfg)
	if want := map[string]string{"a": "2026-10-20"}; !reflect.DeepEqual(cfg.aheadSeen, want) {
		t.Errorf("remembered = %v, want %v", cfg.aheadSeen, want)
	}
}

// TestCursorTracker_LookbackWindowStaysBounded — a first poll over a large
// entity, oldest to newest, must not keep every record it reads.
func TestCursorTracker_LookbackWindowStaysBounded(t *testing.T) {
	tr := cursorTracker{ceiling: aheadNow, lookbackDays: 3}
	day := time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)
	peak := 0
	for i := 0; i < 20000; i++ {
		d := day.AddDate(0, 0, i/5) // five records a day, 4000 days
		tr.observe(dateRecs(fmt.Sprintf("r%05d=%s", i, d.Format(cursorDateLayout))), "postingDate")
		peak = max(peak, len(tr.window))
	}
	if peak > 2*windowPruneMin+1 {
		t.Errorf("window peaked at %d records, want at most %d", peak, 2*windowPruneMin)
	}
	if got := tr.remembered(); len(got) != 20 { // four days of five
		t.Errorf("remembered %d records, want 20", len(got))
	}
}

// TestCursorTracker_LookbackWindow — what the tracker keeps, by case.
func TestCursorTracker_LookbackWindow(t *testing.T) {
	cases := []struct {
		name    string
		days    int
		seed    string
		records []string
		want    map[string]string
	}{
		{"off keeps nothing", 0, "", []string{"a=2026-10-19", "b=2026-10-20"}, nil},
		{"records inside the window", 2, "", []string{"a=2026-10-17", "b=2026-10-18", "c=2026-10-20"},
			map[string]string{"b": "2026-10-18", "c": "2026-10-20"}},
		{"order does not matter", 2, "", []string{"c=2026-10-20", "b=2026-10-18", "a=2026-10-17"},
			map[string]string{"b": "2026-10-18", "c": "2026-10-20"}},
		{"the seeded watermark is the floor", 2, "2026-10-20", []string{"a=2026-10-17", "b=2026-10-19"},
			map[string]string{"b": "2026-10-19"}},
		{"future dates are not the window's", 2, "", []string{"a=2026-10-20", "f=2099-01-01"},
			map[string]string{"a": "2026-10-20"}},
		{"BC's blank date is never kept", 400, "", []string{"a=2026-10-20", "z=0001-01-01"},
			map[string]string{"a": "2026-10-20"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := cursorTracker{ceiling: aheadNow, lookbackDays: tc.days}
			if tc.seed != "" {
				tr.seed(tc.seed)
			}
			tr.observe(dateRecs(tc.records...), "postingDate")
			got := tr.remembered()
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("remembered = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLookback_Validation — lookback_days is 0..365.
func TestLookback_Validation(t *testing.T) {
	cases := []struct {
		days    int
		wantErr bool
	}{
		{-1, true}, {-365, true}, {0, false}, {1, false}, {7, false},
		{maxLookbackDays, false}, {maxLookbackDays + 1, true}, {100000, true},
	}
	for _, tc := range cases {
		cfg := &BCConfig{LookbackDays: tc.days}
		err := cfg.validateLookback()
		if (err != nil) != tc.wantErr {
			t.Errorf("lookback_days %d: error = %v, want error: %v", tc.days, err, tc.wantErr)
		}
	}
}

// TestLookback_InvalidValueFailsTheFetch — a bad value stops the poll before
// it asks BC for anything.
func TestLookback_InvalidValueFailsTheFetch(t *testing.T) {
	for _, days := range []int{-1, maxLookbackDays + 1} {
		bc, c, _, _, cfg, _, _ := dateFixture(t, map[string]string{"a": "2026-10-01"})
		cfg.LookbackDays = days
		tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
			WithHTTPClient(http.DefaultClient)
		if err := c.fetchAndPublish(t.Context(), "conn-1", "tenant-1", cfg, tok, c.logger); err == nil {
			t.Errorf("lookback_days %d: fetch succeeded, want an error", days)
		}
		if len(bc.asks) != 0 {
			t.Errorf("lookback_days %d: BC was asked %v, want no request", days, bc.asks)
		}
	}
}

// TestLookback_ConfigParses — the JSON key.
func TestLookback_ConfigParses(t *testing.T) {
	var nc nodeConfig
	if err := json.Unmarshal([]byte(`{"type":"business_central","business_central":{"incremental":true,"cursor_field":"postingDate","lookback_days":14}}`), &nc); err != nil {
		t.Fatal(err)
	}
	if nc.BusinessCentral.LookbackDays != 14 {
		t.Errorf("lookback_days = %d, want 14", nc.BusinessCentral.LookbackDays)
	}
}

// TestLookback_ChangingTheSetting — a change needs no reset.
func TestLookback_ChangingTheSetting(t *testing.T) {
	recs := map[string]string{"a": "2026-10-01", "w1": "2026-10-18", "w2": "2026-10-14", "b": "2026-10-20"}

	t.Run("widening sends the newly covered days once", func(t *testing.T) {
		bc, c, got, mu, cfg, _, _ := dateFixture(t, copyRecs(recs))
		cfg.LookbackDays = 3 // remembers 17..20: w1, b
		poll(t, c, got, mu, cfg)
		cfg.LookbackDays = 7 // reads 13..20; w2 (10-14) was not remembered
		if ids := poll(t, c, got, mu, cfg); sorted(ids) != "w2" {
			t.Errorf("published %v, want w2 once: it is new to the window", ids)
		}
		if want := "postingDate ge 2026-10-13"; bc.lastAsk() != want {
			t.Errorf("asked %q, want %q", bc.lastAsk(), want)
		}
		if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
			t.Errorf("published %v, want nothing", ids)
		}
	})
	t.Run("narrowing sends nothing and drops the surplus", func(t *testing.T) {
		bc, c, got, mu, cfg, _, _ := dateFixture(t, copyRecs(recs))
		cfg.LookbackDays = 7
		poll(t, c, got, mu, cfg)
		cfg.LookbackDays = 3
		if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
			t.Errorf("published %v, want nothing", ids)
		}
		if want := "postingDate ge 2026-10-17"; bc.lastAsk() != want {
			t.Errorf("asked %q, want %q", bc.lastAsk(), want)
		}
		if want := map[string]string{"w1": "2026-10-18", "b": "2026-10-20"}; !reflect.DeepEqual(cfg.aheadSeen, want) {
			t.Errorf("remembered = %v, want %v", cfg.aheadSeen, want)
		}
	})
	t.Run("turning it off is the old behaviour", func(t *testing.T) {
		bc, c, got, mu, cfg, _, _ := dateFixture(t, copyRecs(recs))
		cfg.LookbackDays = 7
		poll(t, c, got, mu, cfg)
		cfg.LookbackDays = 0
		if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
			t.Errorf("published %v, want nothing", ids)
		}
		if want := "postingDate ge 2026-10-20"; bc.lastAsk() != want {
			t.Errorf("asked %q, want %q", bc.lastAsk(), want)
		}
		if want := map[string]string{"b": "2026-10-20"}; !reflect.DeepEqual(cfg.aheadSeen, want) {
			t.Errorf("remembered = %v, want %v", cfg.aheadSeen, want)
		}
	})
}

// TestLookback_IgnoredForADateTimeCursor — a last-modified stamp cannot be
// backdated, so the window would only re-read days of changes: the filter
// stays `gt`, and one Warn says why nothing happened.
func TestLookback_IgnoredForADateTimeCursor(t *testing.T) {
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "b": stamp(-time.Minute)})
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	var logs strings.Builder
	c.logger = slog.New(slog.NewTextHandler(&logs, nil))
	cfg := bc.cfg()
	cfg.LookbackDays = 7
	poll(t, c, got, mu, cfg)
	bc.set("c", stamp(-time.Second))
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "c" {
		t.Errorf("published %v, want c", ids)
	}
	if want := "lastModifiedDateTime gt " + stamp(-time.Minute); bc.lastAsk() != want {
		t.Errorf("asked %q, want %q", bc.lastAsk(), want)
	}
	if len(cfg.aheadSeen) != 0 {
		t.Errorf("remembered %v, want nothing for a DateTime", cfg.aheadSeen)
	}
	if n := strings.Count(logs.String(), "lookback_days is set but cursor_field is a DateTime"); n != 1 {
		t.Errorf("want one Warn, got %d:\n%s", n, logs.String())
	}
}

// TestLookback_NeedsIncremental — without incremental there is no watermark.
func TestLookback_NeedsIncremental(t *testing.T) {
	bc, c, got, mu, cfg, _, _ := dateFixture(t, map[string]string{"a": "2026-10-01", "b": "2026-10-20"})
	cfg.LookbackDays = 7
	cfg.Incremental = false
	poll(t, c, got, mu, cfg)
	poll(t, c, got, mu, cfg)
	if bc.lastAsk() != "" {
		t.Errorf("asked %q, want no filter", bc.lastAsk())
	}
}
