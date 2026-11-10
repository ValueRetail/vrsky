package sdk_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// failingProducer answers every delivery with fail(connection) and records
// when each attempt arrived, per connection. A nil error from fail delivers.
type failingProducer struct {
	sdk.BaseProducer
	fail func(conn string) error

	mu    sync.Mutex
	times map[string][]time.Time // per connection
	byEnv map[string][]time.Time // per envelope ID
}

func (p *failingProducer) Configure(context.Context, *sdk.Resources) error { return nil }
func (p *failingProducer) Deliver(_ context.Context, env *envelope.Envelope) error {
	p.mu.Lock()
	if p.times == nil {
		p.times, p.byEnv = map[string][]time.Time{}, map[string][]time.Time{}
	}
	now := time.Now()
	p.times[env.IntegrationID] = append(p.times[env.IntegrationID], now)
	p.byEnv[env.ID] = append(p.byEnv[env.ID], now)
	p.mu.Unlock()
	return p.fail(env.IntegrationID)
}

func (p *failingProducer) attempts(conn string) []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Time(nil), p.times[conn]...)
}

func (p *failingProducer) envAttempts(id string) []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Time(nil), p.byEnv[id]...)
}

// #794: a failed delivery comes back on the backoff schedule, not at once.
// Before the fix the subscriber NAK'd without a delay and JetStream
// redelivered on the next poll (a gap of 150 ms or less), so the five attempts
// took well under a second whatever the schedule said. The schedule here is
// short so the test runs in seconds; production uses messaging.DefaultBackoff.
//
// sdk.RateLimited waits the larger of its delay (kept between 1 s and the
// largest step) and the step.
func TestProducer_RetriesFollowTheBackoff(t *testing.T) {
	ms := time.Millisecond
	tests := []struct {
		name    string
		err     error
		backoff []time.Duration
		// wantGaps[i] is the expected wait between attempt i+1 and i+2.
		wantGaps []time.Duration
	}{
		{
			name:     "plain error waits each step",
			err:      errors.New("target refused the connection"),
			backoff:  []time.Duration{300 * ms, 600 * ms, 900 * ms, 1200 * ms},
			wantGaps: []time.Duration{300 * ms, 600 * ms, 900 * ms, 1200 * ms},
		},
		{
			name:     "retriable error waits each step",
			err:      sdk.Retriable(errors.New("503")),
			backoff:  []time.Duration{300 * ms, 600 * ms, 900 * ms, 1200 * ms},
			wantGaps: []time.Duration{300 * ms, 600 * ms, 900 * ms, 1200 * ms},
		},
		{
			// 1 s beats the 300 ms steps; the 1.2 s step beats 1 s.
			name:     "rate limited: the longer of delay and step",
			err:      sdk.RateLimited(errors.New("429"), time.Second),
			backoff:  []time.Duration{300 * ms, 300 * ms, 300 * ms, 1200 * ms},
			wantGaps: []time.Duration{time.Second, time.Second, time.Second, 1200 * ms},
		},
		{
			// An hour is cut to the largest step.
			name:     "rate limited: delay over the cap",
			err:      sdk.RateLimited(errors.New("429"), time.Hour),
			backoff:  []time.Duration{300 * ms, 300 * ms, 300 * ms, 1200 * ms},
			wantGaps: []time.Duration{1200 * ms, 1200 * ms, 1200 * ms, 1200 * ms},
		},
		{
			// Zero is raised to the 1 s floor, never a hot loop.
			name:     "rate limited: zero delay",
			err:      sdk.RateLimited(errors.New("429"), 0),
			backoff:  []time.Duration{300 * ms, 300 * ms, 300 * ms, 1200 * ms},
			wantGaps: []time.Duration{time.Second, time.Second, time.Second, 1200 * ms},
		},
	}
	// The gap is measured between Deliver calls. JetStream redelivers when
	// its timer fires and the next pull request (a quarter of the shortest
	// step at most, 75 ms here) picks it up; -race and a busy CI runner add
	// more. A gap shorter than the wait by more than the clock's slop is a
	// failure: that is the bug.
	const early, late = 30 * time.Millisecond, 450 * time.Millisecond
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &failingProducer{fail: func(string) error { return tt.err }}
			h := harness.NewProducerHarness(t, p, harness.Options{
				Name:    "test-producer-backoff",
				Backoff: tt.backoff,
			})
			env := envelope.New()
			env.TenantID, env.IntegrationID = "tenant-a", "conn-a"
			env.Payload = []byte(`{"n":1}`)
			h.Publish(t, env)

			var total time.Duration
			for _, g := range tt.wantGaps {
				total += g
			}
			got := h.ExpectDLQ(t, total+10*time.Second)
			dlqAt := time.Now()
			if got.ID != env.ID {
				t.Errorf("DLQ holds %s, want %s", got.ID, env.ID)
			}
			// Nothing comes back after the DLQ copy.
			time.Sleep(500 * ms)

			at := p.attempts("conn-a")
			if len(at) != messaging.MaxDeliveryAttempts {
				t.Fatalf("delivered %d times, want %d", len(at), messaging.MaxDeliveryAttempts)
			}
			for i := 1; i < len(at); i++ {
				gap, want := at[i].Sub(at[i-1]), tt.wantGaps[i-1]
				t.Logf("attempt %d: %v after attempt %d (want %v)", i+1, gap.Round(ms), i, want)
				if gap < want-early || gap > want+late {
					t.Errorf("attempt %d came %v after attempt %d, want %v (-%v/+%v)",
						i+1, gap.Round(ms), i, want, early, late)
				}
			}
			// The DLQ copy is made on the fifth failure, not before.
			if dlqAt.Before(at[len(at)-1]) {
				t.Errorf("dead-lettered at %v, before the last attempt at %v", dlqAt, at[len(at)-1])
			}
		})
	}
}

// #794 review of #721: Permanent wins over a delay, whichever wraps the
// other. The message is delivered once and dropped, not retried and not
// dead-lettered.
func TestProducer_PermanentWinsOverRateLimited(t *testing.T) {
	cause := errors.New("400 bad request")
	tests := []struct {
		name string
		err  error
	}{
		{"Permanent(RateLimited)", sdk.Permanent(sdk.RateLimited(cause, 5*time.Second))},
		{"RateLimited(Permanent)", sdk.RateLimited(sdk.Permanent(cause), 5*time.Second)},
		{"wrapped again", fmt.Errorf("deliver: %w", sdk.RateLimited(sdk.Permanent(cause), time.Second))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &failingProducer{fail: func(string) error { return tt.err }}
			d := 100 * time.Millisecond
			h := harness.NewProducerHarness(t, p, harness.Options{
				Name:    "test-producer-permanent",
				Backoff: []time.Duration{d, d, d, d},
			})
			env := envelope.New()
			env.TenantID, env.IntegrationID = "tenant-a", "conn-a"
			env.Payload = []byte(`{"n":1}`)
			h.Publish(t, env)

			// A retry would come back within 100 ms (the step) or 1 s (the
			// floor for the delay, cut to the 100 ms step here).
			h.ExpectNoDLQ(t, 1500*time.Millisecond)
			if n := len(p.attempts("conn-a")); n != 1 {
				t.Errorf("delivered %d times, want 1 (dropped as permanent)", n)
			}
		})
	}
}

// #794: failing connections do not stall a healthy one on the same durable.
// A message waiting out its retry delay keeps one of the durable's
// MaxAckPending slots (32); before the caps, 32 waiting messages held all of
// them and a message for any other connection waited for the failing ones'
// whole schedule (measured 10.1 s in the review of #721). The subscriber holds
// at most MaxDelayed (8) waiting messages, at most MaxDelayedPerConnection (4)
// of one connection, and NAKs the rest without a delay, so the other slots
// keep serving new messages.
//
// The bound: the healthy message waits for the deliveries queued ahead of it,
// not for a retry timer. stallBound is half of one step, so even a single
// retry timer in its way fails the test; without the caps it waits for the
// failing messages' whole schedule (4 steps).
const (
	stallStep  = 4 * time.Second
	stallBound = stallStep / 2
)

// stallCase publishes perConn always-failing messages on each of conns
// connections, then one message on a healthy connection, and checks that the
// healthy one is delivered within stallBound and that exactly wantWaited of
// the failing messages waited out the backoff (their second attempt a whole
// step after the first).
func stallCase(t *testing.T, name string, conns, perConn, wantWaited int) {
	t.Helper()
	const alive = "conn-alive"
	var aliveAt sync.Once
	delivered := make(chan time.Time, 1)
	p := &failingProducer{fail: func(conn string) error {
		if conn != alive {
			return sdk.RateLimited(errors.New("503 maintenance"), stallStep)
		}
		aliveAt.Do(func() { delivered <- time.Now() })
		return nil
	}}
	h := harness.NewProducerHarness(t, p, harness.Options{
		Name:    name,
		Backoff: []time.Duration{stallStep, stallStep, stallStep, stallStep},
	})
	var failingIDs []string
	for c := range conns {
		for i := range perConn {
			env := envelope.New()
			env.TenantID, env.IntegrationID = fmt.Sprintf("tenant-%d", c), fmt.Sprintf("conn-dead-%d", c)
			env.Payload = []byte(fmt.Sprintf(`{"n":%d}`, i))
			h.Publish(t, env)
			failingIDs = append(failingIDs, env.ID)
		}
	}
	env := envelope.New()
	env.TenantID, env.IntegrationID = "tenant-healthy", alive
	env.Payload = []byte(`{"n":"healthy"}`)
	published := time.Now()
	h.Publish(t, env)

	limit := 4*stallStep + 5*time.Second
	select {
	case at := <-delivered:
		took := at.Sub(published)
		t.Logf("the healthy message was delivered %v after it was published", took.Round(time.Millisecond))
		if took > stallBound {
			t.Errorf("the healthy message took %v, want at most %v", took.Round(time.Millisecond), stallBound)
		}
	case <-time.After(limit):
		t.Fatalf("the healthy message was not delivered within %v", limit)
	}

	// The rest were retried at once; their second attempt, if any, came
	// well inside a step.
	time.Sleep(time.Until(published.Add(stallStep + time.Second)))
	waited := 0
	for _, id := range failingIDs {
		at := p.envAttempts(id)
		if len(at) >= 2 && at[1].Sub(at[0]) >= stallStep-30*time.Millisecond {
			waited++
		}
	}
	if waited != wantWaited {
		t.Errorf("%d failing messages waited out the backoff, want %d", waited, wantWaited)
	}
}

// One connection with more failing messages than MaxAckPending: the
// per-connection cap (4) is what keeps slots free.
func TestProducer_FailingConnectionDoesNotStallOthers(t *testing.T) {
	t.Parallel()
	stallCase(t, "test-producer-stall", 1, 40, 4)
}

// Ten failing connections with four messages each: every one stays within
// the per-connection cap, so only the per-subscriber cap (8) keeps slots free.
// Without it 32 messages would wait and fill the durable.
func TestProducer_ManyFailingConnectionsDoNotStallOthers(t *testing.T) {
	t.Parallel()
	stallCase(t, "test-producer-stall-many", 10, 4, 8)
}

// #794: a handler that panics is retried on the backoff schedule too, not at
// once. The panic is recovered by the subscriber, not the SDK. The fifth
// attempt panics as well and is not dead-lettered (#811), so the test checks
// the gaps only.
func TestProducer_PanicRetriesFollowTheBackoff(t *testing.T) {
	ms := time.Millisecond
	backoff := []time.Duration{300 * ms, 600 * ms, 900 * ms, 1200 * ms}
	p := &failingProducer{fail: func(string) error { panic("connector bug") }}
	h := harness.NewProducerHarness(t, p, harness.Options{
		Name:    "test-producer-panic",
		Backoff: backoff,
	})
	env := envelope.New()
	env.TenantID, env.IntegrationID = "tenant-a", "conn-a"
	env.Payload = []byte(`{"n":1}`)
	h.Publish(t, env)

	deadline := time.Now().Add(3*time.Second + 10*time.Second)
	for len(p.attempts("conn-a")) < messaging.MaxDeliveryAttempts && time.Now().Before(deadline) {
		time.Sleep(50 * ms)
	}
	// Nothing comes back after the fifth attempt.
	time.Sleep(500 * ms)
	at := p.attempts("conn-a")
	if len(at) != messaging.MaxDeliveryAttempts {
		t.Fatalf("delivered %d times, want %d", len(at), messaging.MaxDeliveryAttempts)
	}
	const early, late = 30 * time.Millisecond, 450 * time.Millisecond
	for i := 1; i < len(at); i++ {
		gap, want := at[i].Sub(at[i-1]), backoff[i-1]
		t.Logf("attempt %d: %v after attempt %d (want %v)", i+1, gap.Round(ms), i, want)
		if gap < want-early || gap > want+late {
			t.Errorf("attempt %d came %v after attempt %d, want %v (-%v/+%v)",
				i+1, gap.Round(ms), i, want, early, late)
		}
	}
}
