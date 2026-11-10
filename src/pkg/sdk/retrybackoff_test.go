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

// #794: one connection's failing target does not stall another connection
// on the same durable. A message waiting out its retry delay keeps one of the
// durable's MaxAckPending slots (32); before the cap, 32 waiting messages from
// one connection held all of them and a message for any other connection
// waited for the whole schedule (measured 10.1 s in the review of #721). The
// subscriber holds at most MaxDelayed (8) waiting messages, at most 4 of them
// per connection, and NAKs the rest without a delay, so the other slots keep
// serving new messages.
//
// The bound: a message on a healthy connection waits only for the deliveries
// queued ahead of it, never for a retry timer. Here that is under
// stallBound, while the failing connection's schedule (4 × 2 s) is four times
// longer. Without the cap this test waits about 8 s.
func TestProducer_FailingConnectionDoesNotStallOthers(t *testing.T) {
	const (
		dead, alive = "conn-dead", "conn-alive"
		queued      = 40 // more than MaxAckPending
		stallBound  = 2500 * time.Millisecond
	)
	step := 2 * time.Second
	var aliveAt sync.Once
	delivered := make(chan time.Time, 1)
	p := &failingProducer{fail: func(conn string) error {
		if conn == dead {
			return sdk.RateLimited(errors.New("503 maintenance"), step)
		}
		aliveAt.Do(func() { delivered <- time.Now() })
		return nil
	}}
	h := harness.NewProducerHarness(t, p, harness.Options{
		Name:    "test-producer-stall",
		Backoff: []time.Duration{step, step, step, step},
	})
	var deadIDs []string
	for i := range queued {
		env := envelope.New()
		env.TenantID, env.IntegrationID = "tenant-a", dead
		env.Payload = []byte(fmt.Sprintf(`{"n":%d}`, i))
		h.Publish(t, env)
		deadIDs = append(deadIDs, env.ID)
	}
	env := envelope.New()
	env.TenantID, env.IntegrationID = "tenant-b", alive
	env.Payload = []byte(`{"n":"b"}`)
	published := time.Now()
	h.Publish(t, env)

	select {
	case at := <-delivered:
		took := at.Sub(published)
		t.Logf("tenant-b's message delivered %v after it was published", took.Round(time.Millisecond))
		if took > stallBound {
			t.Errorf("tenant-b's message took %v, want at most %v", took.Round(time.Millisecond), stallBound)
		}
	case <-time.After(4*step + 5*time.Second):
		t.Fatalf("tenant-b's message was not delivered within %v", 4*step+5*time.Second)
	}

	// The failing connection still got the backoff, for exactly as many
	// messages as the per-connection cap allows (4 with MaxAckPending 32):
	// those came back a whole step after their first attempt. The rest were
	// retried at once.
	time.Sleep(time.Until(published.Add(step + time.Second)))
	waited := 0
	for _, id := range deadIDs {
		at := p.envAttempts(id)
		if len(at) >= 2 && at[1].Sub(at[0]) >= step-30*time.Millisecond {
			waited++
		}
	}
	if waited != 4 {
		t.Errorf("%d of the failing connection's messages waited out the backoff, want 4 (the per-connection cap)", waited)
	}
}
