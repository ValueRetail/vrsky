package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// TestMeasurePollStats measures what an idle poll of a large folder costs in
// stats, and how long a replaced file takes to be published, with every done
// file stat'ed each poll (as since #420) and with them looked at in turns
// (#537). It is a measurement, not a check, so it only runs when
// VRSKY_MEASURE_STATS is set (to the number of files; 1 means 10 000).
// VRSKY_MEASURE_STAT_DELAY (a duration, say 1ms) adds that much to each stat,
// to stand in for an SMB round trip:
//
//	VRSKY_MEASURE_STATS=1 VRSKY_MEASURE_STAT_DELAY=1ms go test -run TestMeasurePollStats -v ./cmd/file-consumer/
func TestMeasurePollStats(t *testing.T) {
	v := os.Getenv("VRSKY_MEASURE_STATS")
	if v == "" {
		t.Skip("set VRSKY_MEASURE_STATS to measure")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		t.Fatalf("VRSKY_MEASURE_STATS=%q: want a number of files", v)
	}
	if n == 1 {
		n = 10000
	}
	var delay time.Duration
	if d := os.Getenv("VRSKY_MEASURE_STAT_DELAY"); d != "" {
		if delay, err = time.ParseDuration(d); err != nil {
			t.Fatalf("VRSKY_MEASURE_STAT_DELAY=%q: %v", d, err)
		}
	}
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		name := "order-2026-10-29-" + strconv.Itoa(1000000+i) + ".csv"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for m, mode := range []struct {
		name   string
		budget int
	}{
		{"every file every poll (before)", n + 1},
		{"in turns (after)", 0},
	} {
		var stats atomic.Int64
		clock := newFakeClock()
		probe := &recordingProbe{}
		c := &fileConsumer{
			logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
			publish:           probe.publish,
			inlineMax:         1 << 20,
			pollInterval:      watchInterval,
			now:               clock.now,
			knownStatsPerPoll: mode.budget,
			stat: func(path string) (os.FileInfo, error) {
				stats.Add(1)
				if delay > 0 {
					time.Sleep(delay)
				}
				return os.Stat(path)
			},
		}
		ac := &ActiveConnection{ConnectionID: "measure", TenantID: "measure", WatchDir: dir, NodeID: knownNode, Cancel: func() {}}
		if err := c.loadKnown(context.Background(), ac, false); err != nil {
			t.Fatal(err)
		}
		poll := func() {
			clock.advance(watchInterval)
			c.pollOnce(context.Background(), ac)
		}

		const idle = 20
		var took time.Duration
		var most int64
		stats.Store(0)
		for i := 0; i < idle; i++ {
			before := stats.Load()
			began := time.Now()
			poll()
			took += time.Since(began)
			most = max(most, stats.Load()-before)
		}
		mean := stats.Load() / idle

		// Replace one file and count the polls until it is published.
		replaced := "order-2026-10-29-" + strconv.Itoa(1000000+n/2) + ".csv"
		// A different version per mode: each mode's start takes the folder
		// as it is, the previous mode's replacement included.
		writeAt(t, dir, replaced, []byte("replaced in "+mode.name), time.Date(2026, 10, 29, 12, m, 0, 0, time.UTC))
		polls := 0
		for len(probe.forName(replaced)) == 0 {
			poll()
			polls++
			if polls > 10*(n/defaultKnownStatsPerPoll+2) {
				t.Fatalf("%s: replacement not published after %d polls", mode.name, polls)
			}
		}
		t.Logf("%s, %d files, %s per stat added: %d stats per idle poll (mean of %d, most %d), %s per idle poll; replacement published on poll %d",
			mode.name, n, delay, mean, idle, most, took/idle, polls)
	}
}
