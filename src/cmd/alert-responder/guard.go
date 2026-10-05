package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/notify"
)

// Limits the responder enforces in code. The model is told about them, but
// nothing here depends on it listening.
const (
	// runCooldown: one diagnosis per alert (same name and labels) per hour.
	// Alertmanager repeats a firing alert every few hours; the responder must
	// not re-run, and re-act, on every repeat.
	runCooldown = time.Hour
	// actionCooldown: each kind of action at most once per pipeline per hour,
	// whichever alert asked for it.
	actionCooldown = time.Hour
	// maxDLQRetriesPerHour bounds retry_dlq_message per pipeline.
	maxDLQRetriesPerHour = 20
)

// fingerprint identifies an alert across its repeats: name plus labels.
func fingerprint(a *notify.Alert) string {
	keys := make([]string, 0, len(a.Labels))
	for k := range a.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(a.Name)
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%s", k, a.Labels[k])
	}
	return b.String()
}

// guard holds what must survive between runs: when each alert was last
// handled, which alerts the responder acted on, and when each action last ran
// on each pipeline.
type guard struct {
	mu             sync.Mutex
	now            func() time.Time
	maxRunsPerHour int
	lastRun        map[string]time.Time   // fingerprint → last run
	acted          map[string]bool        // fingerprint → an action was taken
	runs           []time.Time            // all runs, for the hourly cap
	lastAction     map[string][]time.Time // tenant|connection|action → times
}

func newGuard(maxRunsPerHour int) *guard {
	return &guard{
		now: time.Now, maxRunsPerHour: maxRunsPerHour,
		lastRun: map[string]time.Time{}, acted: map[string]bool{}, lastAction: map[string][]time.Time{},
	}
}

// admitRun reports whether a firing alert may be diagnosed now, and records
// the run if so.
func (g *guard) admitRun(fp string) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if last, ok := g.lastRun[fp]; ok && now.Sub(last) < runCooldown {
		return false, "already handled within the last hour"
	}
	recent := g.runs[:0]
	for _, t := range g.runs {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	g.runs = recent
	if len(g.runs) >= g.maxRunsPerHour {
		return false, fmt.Sprintf("hourly run cap reached (%d)", g.maxRunsPerHour)
	}
	g.runs = append(g.runs, now)
	g.lastRun[fp] = now
	return true, ""
}

// admitAction reports whether an action may run on a pipeline now, and
// records it if so. limit is how many times per hour (1 for a redeploy or a
// resend, maxDLQRetriesPerHour for DLQ retries).
func (g *guard) admitAction(tenantID, connID, action string, limit int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	key := tenantID + "|" + connID + "|" + action
	recent := g.lastAction[key][:0]
	for _, t := range g.lastAction[key] {
		if now.Sub(t) < actionCooldown {
			recent = append(recent, t)
		}
	}
	if len(recent) >= limit {
		g.lastAction[key] = recent
		return fmt.Errorf("refused: %s already ran %d time(s) on this pipeline in the last hour (limit %d); report this instead of retrying", action, len(recent), limit)
	}
	g.lastAction[key] = append(recent, now)
	return nil
}

func (g *guard) markActed(fp string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.acted[fp] = true
}

// takeActed reports whether the responder acted on this alert, and forgets
// it — called when the alert resolves.
func (g *guard) takeActed(fp string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	acted := g.acted[fp]
	delete(g.acted, fp)
	return acted
}
