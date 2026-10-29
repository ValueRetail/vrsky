package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// The first two tests came from the review of #592. With the watermark
// ceiling at now + skew, a record dated a few minutes ahead became the
// watermark, and the `gt` filter then hid what BC stamped before it.

// TestIncremental_CatchUpPollKeepsRecordsWrittenBeforeTheStamp — a record
// stamped an hour ahead is held back until the clock gets near it. It must
// not become the watermark while it is still ahead: BC keeps stamping real
// records with real time, below it.
func TestIncremental_CatchUpPollKeepsRecordsWrittenBeforeTheStamp(t *testing.T) {
	future := stamp(time.Hour)
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "f": future})
	c, got, mu := newTestConsumer()
	clock := aheadNow
	c.now = func() time.Time { return clock }
	cfg := bc.cfg()
	poll(t, c, got, mu, cfg) // a, f

	// The poll that first sees f inside the skew: now = f - 4m.
	clock = aheadNow.Add(56 * time.Minute)
	poll(t, c, got, mu, cfg)

	// One minute later BC modifies g; its stamp is BC's real time.
	bc.set("g", stamp(57*time.Minute))
	clock = aheadNow.Add(58 * time.Minute)
	ids := poll(t, c, got, mu, cfg)
	if sorted(ids) != "g" {
		t.Errorf("g (modified at now+57m, after the poll at now+56m) was not published: got %v; filter was %q",
			ids, bc.lastAsk())
	}
}

// TestIncremental_NearFutureStampInsideSkewDoesNotHideRealRecords — one
// record stamped 4 minutes ahead (a custom API page, an import) must not
// become the watermark and hide what BC writes in the next 4 minutes.
func TestIncremental_NearFutureStampInsideSkewDoesNotHideRealRecords(t *testing.T) {
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "x": stamp(4 * time.Minute)})
	c, got, mu := newTestConsumer()
	clock := aheadNow
	c.now = func() time.Time { return clock }
	cfg := bc.cfg()
	poll(t, c, got, mu, cfg)
	bc.set("g", stamp(time.Minute))
	clock = aheadNow.Add(2 * time.Minute)
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "g" {
		t.Errorf("g lost: got %v, filter %q", ids, bc.lastAsk())
	}
}

// TestIncremental_BCClockAheadOfThePod — BC's clock runs a few seconds ahead
// of the pod's, so a record written just before a poll is dated after the
// pod's now. It is held one poll and then taken as the watermark: every
// record goes out exactly once, nothing is lost, and nothing is logged at
// Warn (a few seconds is not a bad stamp).
func TestIncremental_BCClockAheadOfThePod(t *testing.T) {
	const skew = 3 * time.Second // BC time = pod time + skew
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "r": stamp(-time.Second + skew)})
	c, got, mu := newTestConsumer()
	clock := aheadNow
	c.now = func() time.Time { return clock }
	var logs bytes.Buffer
	c.logger = slog.New(slog.NewTextHandler(&logs, nil))
	cfg := bc.cfg()

	var all []string
	all = append(all, poll(t, c, got, mu, cfg)...) // a, r (r held: it is 2s ahead)

	bc.set("s", stamp(2*time.Second+skew)) // written at pod +2s
	clock = aheadNow.Add(10 * time.Second)
	all = append(all, poll(t, c, got, mu, cfg)...) // s; r is now current and deduped
	if want := "lastModifiedDateTime gt " + stamp(-time.Hour); bc.lastAsk() != want {
		t.Errorf("poll 2 asked %q, want %q (r must not have been the watermark while ahead)", bc.lastAsk(), want)
	}

	bc.set("u", stamp(19*time.Second+skew)) // written at pod +19s, dated +22s
	clock = aheadNow.Add(20 * time.Second)
	all = append(all, poll(t, c, got, mu, cfg)...) // u, held
	if want := "lastModifiedDateTime gt " + stamp(2*time.Second+skew); bc.lastAsk() != want {
		t.Errorf("poll 3 asked %q, want %q", bc.lastAsk(), want)
	}

	clock = aheadNow.Add(30 * time.Second)
	all = append(all, poll(t, c, got, mu, cfg)...) // nothing new; u becomes the watermark
	all = append(all, poll(t, c, got, mu, cfg)...)
	if want := "lastModifiedDateTime gt " + stamp(19*time.Second+skew); bc.lastAsk() != want {
		t.Errorf("poll 5 asked %q, want %q", bc.lastAsk(), want)
	}

	if s := sorted(all); s != "a,r,s,u" {
		t.Errorf("published %s over five polls, want each of a,r,s,u exactly once", s)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a few seconds of clock difference was logged at Warn:\n%s", logs.String())
	}
}

// seedFrozen stores a watermark the code before #591 could have left.
func seedFrozen(t *testing.T, c *bcConsumer, at time.Time, state string) {
	t.Helper()
	cp := &checkpoint.Checkpoint{
		TenantID: "tenant-1", ConnectionID: "conn-1", NodeID: "src",
		LastProcessedMessageID: "2099-01-01T00:00:00Z", LastProcessedAt: at,
	}
	if state != "" {
		cp.State = json.RawMessage(state)
	}
	if err := c.checkpoints.Save(context.Background(), cp); err != nil {
		t.Fatal(err)
	}
}

func storedWatermark(t *testing.T, c *bcConsumer) string {
	t.Helper()
	cp, err := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src")
	if err != nil || cp == nil {
		t.Fatalf("checkpoint: %v %v", cp, err)
	}
	return cp.LastProcessedMessageID
}

// TestIncremental_FrozenWatermarkResumesShortlyBeforeItWasStored — a node
// frozen at 2099 by the code before #591, without pictures, last saved when
// it stored that watermark. The recovery reads from an hour before that
// save: it re-sends that hour and sends what the frozen node missed, and
// nothing older.
func TestIncremental_FrozenWatermarkResumesShortlyBeforeItWasStored(t *testing.T) {
	frozeAt := aheadNow.Add(-2 * time.Hour)
	bc := newAheadBC(t, map[string]string{
		"o": stamp(-4 * time.Hour),                // read long before the freeze
		"w": stamp(-2*time.Hour - 30*time.Minute), // in the hour before the freeze: re-sent
		"m": stamp(-time.Hour),                    // modified after the freeze: missed until now
		"n": stamp(time.Minute),                   // BC's clock a little ahead: held, not evidence of anything
		"f": "2099-01-01T00:00:00Z",
	})
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	var logs bytes.Buffer
	c.logger = slog.New(slog.NewTextHandler(&logs, nil))
	seedFrozen(t, c, frozeAt, "")
	cfg := bc.cfg()

	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "f,m,n,w" {
		t.Errorf("recovery published %v, want f,m,n,w (o is older than the window)", ids)
	}
	resume := frozeAt.Add(-time.Hour).UTC().Format(time.RFC3339)
	if want := "lastModifiedDateTime gt " + resume; bc.asks[0] != want {
		t.Errorf("recovery asked %q, want %q", bc.asks[0], want)
	}
	if !strings.Contains(logs.String(), "resume_from="+resume) {
		t.Errorf("no Warn naming where the recovery resumed:\n%s", logs.String())
	}
	if w := storedWatermark(t, c); w != stamp(-time.Hour) {
		t.Errorf("stored watermark %q, want m's stamp %q", w, stamp(-time.Hour))
	}
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
		t.Errorf("poll after the recovery published %v, want nothing", ids)
	}
	if want := "lastModifiedDateTime gt " + stamp(-time.Hour); bc.lastAsk() != want {
		t.Errorf("poll after the recovery asked %q, want %q", bc.lastAsk(), want)
	}
}

// TestIncremental_FrozenWatermarkFallsBackToAFullRead — when LastProcessedAt
// does not mark the freeze (pictures saved every poll, or it is not set),
// only a read from the start is sure not to miss anything.
func TestIncremental_FrozenWatermarkFallsBackToAFullRead(t *testing.T) {
	cases := []struct {
		name  string
		at    time.Time
		state string
	}{
		{"last processed at not set", time.Time{}, ""},
		{"pictures saved every poll", aheadNow.Add(-time.Minute), `{"pictures":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bc := newAheadBC(t, map[string]string{
				"o": stamp(-4 * time.Hour), "m": stamp(-time.Hour), "f": "2099-01-01T00:00:00Z",
				// below the stored watermark: on a full read that is no reason
				// to store none and read everything again
				"f0": "2098-01-01T00:00:00Z",
			})
			c, got, mu := newTestConsumer()
			c.now = func() time.Time { return aheadNow }
			seedFrozen(t, c, tc.at, tc.state)
			cfg := bc.cfg()
			if ids := poll(t, c, got, mu, cfg); sorted(ids) != "f,f0,m,o" {
				t.Errorf("published %v, want a full re-read (f,f0,m,o)", ids)
			}
			if bc.asks[0] != "" {
				t.Errorf("first request carried $filter=%q, want none", bc.asks[0])
			}
			if w := storedWatermark(t, c); w != stamp(-time.Hour) {
				t.Errorf("stored watermark %q, want %q", w, stamp(-time.Hour))
			}
		})
	}
}

// TestIncremental_RecoveryFindingOnlyFutureRecordsDropsTheFrozenWatermark —
// review finding 3: a recovery read with nothing to take a watermark from
// used to write the frozen one back. It now stores where it started.
func TestIncremental_RecoveryFindingOnlyFutureRecordsDropsTheFrozenWatermark(t *testing.T) {
	frozeAt := aheadNow.Add(-2 * time.Hour)
	cases := []struct {
		name    string
		at      time.Time
		wantMk  string // stored after the recovery
		wantAsk string // the next poll's filter
	}{
		{"bounded recovery", frozeAt,
			frozeAt.Add(-time.Hour).Format(time.RFC3339),
			"lastModifiedDateTime gt " + frozeAt.Add(-time.Hour).Format(time.RFC3339)},
		{"full read", time.Time{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bc := newAheadBC(t, map[string]string{"f": "2099-01-01T00:00:00Z"})
			c, got, mu := newTestConsumer()
			c.now = func() time.Time { return aheadNow }
			seedFrozen(t, c, tc.at, "")
			cfg := bc.cfg()
			if ids := poll(t, c, got, mu, cfg); sorted(ids) != "f" {
				t.Fatalf("recovery published %v, want f", ids)
			}
			if w := storedWatermark(t, c); w != tc.wantMk {
				t.Errorf("stored watermark %q, want %q (the frozen one must not come back)", w, tc.wantMk)
			}
			if ids := poll(t, c, got, mu, cfg); len(ids) != 0 {
				t.Errorf("next poll published %v, want nothing", ids)
			}
			if bc.lastAsk() != tc.wantAsk {
				t.Errorf("next poll asked %q, want %q", bc.lastAsk(), tc.wantAsk)
			}
		})
	}
}

// TestIncremental_WatermarkSlightlyAheadIsNotFrozen — a stored watermark a
// few seconds past now (BC's clock ahead when the code before #591 stored
// it) is a real stamp, not a frozen watermark: no recovery read.
func TestIncremental_WatermarkSlightlyAheadIsNotFrozen(t *testing.T) {
	bc := newAheadBC(t, map[string]string{"a": stamp(-time.Hour), "b": stamp(3 * time.Second)})
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	if err := c.checkpoints.Save(context.Background(), &checkpoint.Checkpoint{
		TenantID: "tenant-1", ConnectionID: "conn-1", NodeID: "src",
		LastProcessedMessageID: stamp(3 * time.Second), LastProcessedAt: aheadNow.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if ids := poll(t, c, got, mu, bc.cfg()); len(ids) != 0 {
		t.Errorf("published %v, want nothing", ids)
	}
	if want := "lastModifiedDateTime gt " + stamp(3*time.Second); bc.asks[0] != want {
		t.Errorf("asked %q, want %q", bc.asks[0], want)
	}
}

func TestStateHasPictures(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{"", false},
		{"{}", false},
		{"not json", false},
		{`{"pictures":null,"ahead":{"f":"2099-01-01T00:00:00Z"}}`, false},
		{`{"pictures":{}}`, true},
		{`{"pictures":{"r1":{"pictureId":"p1"}}}`, true},
	}
	for _, tc := range cases {
		if got := stateHasPictures(json.RawMessage(tc.state)); got != tc.want {
			t.Errorf("stateHasPictures(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

// TestWarnAheadSize — a remembered set that large means cursor_field is a
// business date; say so once, not every poll.
func TestWarnAheadSize(t *testing.T) {
	c, _, _ := newTestConsumer()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	many := func() map[string]string {
		m := map[string]string{}
		for i := 0; i <= aheadWarnSize; i++ {
			m[strconv.Itoa(i)] = "2099-01-01T00:00:00Z"
		}
		return m
	}
	cfg := &BCConfig{aheadSeen: many()}
	c.warnAheadSize(cfg, logger)
	c.warnAheadSize(cfg, logger)
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Fatalf("want one Warn for %d remembered records, got %d:\n%s", len(cfg.aheadSeen), n, logs.String())
	}
	cfg.aheadSeen = map[string]string{"f": "2099-01-01T00:00:00Z"}
	c.warnAheadSize(cfg, logger)
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Errorf("a small set logged a Warn")
	}
	cfg.aheadSeen = many()
	c.warnAheadSize(cfg, logger)
	if n := strings.Count(logs.String(), "level=WARN"); n != 2 {
		t.Errorf("growing past the size again should Warn again, got %d Warns", n)
	}
}

// TestIncremental_FrozenRecoveryNeverStartsInTheFuture — a LastProcessedAt
// later than the clock (the pod's clock went back) must not put the recovery
// read's start in the future, where it would hide records all over again.
func TestIncremental_FrozenRecoveryNeverStartsInTheFuture(t *testing.T) {
	bc := newAheadBC(t, map[string]string{"m": stamp(-30 * time.Minute), "f": "2099-01-01T00:00:00Z"})
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	seedFrozen(t, c, aheadNow.Add(3*time.Hour), "")
	if ids := poll(t, c, got, mu, bc.cfg()); sorted(ids) != "f,m" {
		t.Errorf("published %v, want f,m", ids)
	}
	if want := "lastModifiedDateTime gt " + stamp(-time.Hour); bc.asks[0] != want {
		t.Errorf("recovery asked %q, want %q", bc.asks[0], want)
	}
}

// TestIncremental_FrozenThenResavedFallsBackToAFullRead — from the second
// review of #592. The old code froze on f1 (2099) at now-10h, missed m
// (modified at now-5h), then read f2 (2100) and saved again at now-2h. The
// bounded window (from now-3h) cannot see m. The recovery read returns f1, a
// far-future value below the stored watermark, which shows a second save may
// have happened: the next poll reads from the start, m goes out, and f1 and
// f2 are not sent again.
func TestIncremental_FrozenThenResavedFallsBackToAFullRead(t *testing.T) {
	bc := newAheadBC(t, map[string]string{
		"a": stamp(-12 * time.Hour), "f1": "2099-01-01T00:00:00Z",
		"m": stamp(-5 * time.Hour), "f2": "2100-01-01T00:00:00Z",
	})
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	var logs bytes.Buffer
	c.logger = slog.New(slog.NewTextHandler(&logs, nil))
	if err := c.checkpoints.Save(context.Background(), &checkpoint.Checkpoint{
		TenantID: "tenant-1", ConnectionID: "conn-1", NodeID: "src",
		LastProcessedMessageID: "2100-01-01T00:00:00Z", LastProcessedAt: aheadNow.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// A fresh config each poll, as after a restart: the remembered set must
	// come back from the checkpoint.
	if ids := poll(t, c, got, mu, bc.cfg()); sorted(ids) != "f1,f2" {
		t.Fatalf("bounded recovery published %v, want f1,f2", ids)
	}
	if w := storedWatermark(t, c); w != "" {
		t.Errorf("stored watermark %q after the evidence of a second save, want none (full read next)", w)
	}
	if !strings.Contains(logs.String(), "may have saved again") {
		t.Errorf("no Warn about the fallback:\n%s", logs.String())
	}
	if ids := poll(t, c, got, mu, bc.cfg()); sorted(ids) != "a,m" {
		t.Errorf("full read published %v, want a,m (f1 and f2 already sent)", ids)
	}
	if bc.lastAsk() != "" {
		t.Errorf("second poll asked %q, want no filter", bc.lastAsk())
	}
	if ids := poll(t, c, got, mu, bc.cfg()); len(ids) != 0 {
		t.Errorf("third poll published %v, want nothing", ids)
	}
	if want := "lastModifiedDateTime gt " + stamp(-5*time.Hour); bc.lastAsk() != want {
		t.Errorf("third poll asked %q, want %q", bc.lastAsk(), want)
	}
}
