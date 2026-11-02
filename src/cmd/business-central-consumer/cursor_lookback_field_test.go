package main

import (
	"reflect"
	"testing"
	"time"
)

// TestLookback_CursorFieldChangeWins (#610) — a changed cursor_field drops the
// watermark and the remembered set before lookback_days is looked at: the
// first poll after the change has no filter (no seed, no shift), and the set
// is rebuilt for the new field only.
func TestLookback_CursorFieldChangeWins(t *testing.T) {
	const lookback = 7
	t.Run("date to DateTime through a restart", func(t *testing.T) {
		bc := newTwoFieldBC(t, twoFieldRecs()...)
		c, got, mu := newTestConsumer()
		c.now = func() time.Time { return aheadNow }
		cfg := bc.cfg(dateField)
		cfg.LookbackDays = lookback
		poll(t, c, got, mu, cfg)
		if len(cfg.aheadSeen) != 1 {
			t.Fatalf("setup: the window (10-13 on) should hold b only, got %v", cfg.aheadSeen)
		}
		cfg2 := bc.cfg(lmField)
		cfg2.LookbackDays = lookback
		if ids := poll(t, c, got, mu, cfg2); sorted(ids) != "a,b" {
			t.Errorf("after the change published %q, want a,b once more", sorted(ids))
		}
		if bc.lastAsk() != "" {
			t.Errorf("after the change asked %q, want no filter", bc.lastAsk())
		}
		if len(cfg2.aheadSeen) != 0 {
			t.Errorf("remembered %v, want nothing: a DateTime keeps no window", cfg2.aheadSeen)
		}
	})
	t.Run("DateTime to date through a restart", func(t *testing.T) {
		bc := newTwoFieldBC(t, twoFieldRecs()...)
		c, got, mu := newTestConsumer()
		c.now = func() time.Time { return aheadNow }
		cfg := bc.cfg(lmField)
		cfg.LookbackDays = lookback
		poll(t, c, got, mu, cfg)
		cfg2 := bc.cfg(dateField)
		cfg2.LookbackDays = lookback
		if ids := poll(t, c, got, mu, cfg2); sorted(ids) != "a,b" {
			t.Errorf("after the change published %q, want a,b", sorted(ids))
		}
		if bc.lastAsk() != "" {
			t.Errorf("after the change asked %q: the DateTime watermark must not be shifted or sent", bc.lastAsk())
		}
		if want := map[string]string{"b": "2026-10-20"}; !reflect.DeepEqual(cfg2.aheadSeen, want) {
			t.Errorf("remembered = %v, want the new field's window %v", cfg2.aheadSeen, want)
		}
		// From here the window works on the new field.
		if ids := poll(t, c, got, mu, cfg2); len(ids) != 0 {
			t.Errorf("published %v, want nothing", ids)
		}
		if want := "postingDate ge 2026-10-13"; bc.lastAsk() != want {
			t.Errorf("asked %q, want %q", bc.lastAsk(), want)
		}
	})
	t.Run("the same config object", func(t *testing.T) {
		bc := newTwoFieldBC(t, twoFieldRecs()...)
		c, got, mu := newTestConsumer()
		c.now = func() time.Time { return aheadNow }
		cfg := bc.cfg(dateField)
		cfg.LookbackDays = lookback
		poll(t, c, got, mu, cfg)
		if len(cfg.aheadSeen) != 1 {
			t.Fatalf("setup: the window (10-13 on) should hold b only, got %v", cfg.aheadSeen)
		}
		cfg.CursorField = lmField
		if ids := poll(t, c, got, mu, cfg); sorted(ids) != "a,b" {
			t.Errorf("after the change published %q, want a,b", sorted(ids))
		}
		if bc.lastAsk() != "" || len(cfg.aheadSeen) != 0 {
			t.Errorf("asked %q, remembered %v: want no filter and an empty set", bc.lastAsk(), cfg.aheadSeen)
		}
	})
}
