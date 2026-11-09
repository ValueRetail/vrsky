package sdk_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// rateLimitedProducer answers every delivery with sdk.RateLimited(after) and
// records when each attempt arrived.
type rateLimitedProducer struct {
	sdk.BaseProducer
	after time.Duration

	mu    sync.Mutex
	times []time.Time
}

func (p *rateLimitedProducer) Configure(context.Context, *sdk.Resources) error { return nil }
func (p *rateLimitedProducer) Deliver(context.Context, *envelope.Envelope) error {
	p.mu.Lock()
	p.times = append(p.times, time.Now())
	p.mu.Unlock()
	return sdk.RateLimited(errors.New("target answered 429"), p.after)
}

func (p *rateLimitedProducer) attempts() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Time(nil), p.times...)
}

// #721: the delay in sdk.RateLimited decides when the message comes back, the
// attempts still count toward MaxDeliver, and the fifth failure dead-letters
// the message. Before the fix the SDK NAK'd without a delay and JetStream
// redelivered at once (one poll, ~150 ms apart), whatever the delay said.
func TestProducer_RateLimitedWaitsForItsDelay(t *testing.T) {
	tests := []struct {
		name    string
		after   time.Duration
		backoff []time.Duration
		// wantGap is the shortest gap expected between two attempts; maxGap
		// the longest (it catches a delay that was not cut to the cap).
		wantGap, maxGap time.Duration
	}{
		{
			// The delay is honoured: 1.5 s apart, where the schedule's
			// steps are 1 s and a plain NAK comes back at once.
			name:    "delay in range",
			after:   1500 * time.Millisecond,
			backoff: []time.Duration{time.Second, time.Second, time.Second, 2 * time.Second},
			wantGap: 1400 * time.Millisecond,
			// JetStream measures the delay from the attempt's backoff
			// deadline, so the fourth wait is 1.5 s + (2 s - 1 s).
			maxGap: 3500 * time.Millisecond,
		},
		{
			// A minute is cut to the largest backoff step.
			name:    "delay above the largest step",
			after:   time.Minute,
			backoff: []time.Duration{time.Second, time.Second, time.Second, time.Second},
			wantGap: 900 * time.Millisecond,
			maxGap:  2500 * time.Millisecond,
		},
		{
			// Zero (or anything under a second) is raised to 1 s, never a
			// hot loop.
			name:    "zero delay",
			after:   0,
			backoff: []time.Duration{time.Second, time.Second, time.Second, time.Second},
			wantGap: 900 * time.Millisecond,
			maxGap:  2500 * time.Millisecond,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &rateLimitedProducer{after: tt.after}
			h := harness.NewProducerHarness(t, p, harness.Options{
				Name:    "test-producer-ratelimit",
				Backoff: tt.backoff,
			})
			h.Publish(t, &envelope.Envelope{Payload: []byte("x")})

			h.ExpectDLQ(t, 20*time.Second)
			// Nothing comes back after the DLQ copy.
			time.Sleep(500 * time.Millisecond)

			got := p.attempts()
			if len(got) != messaging.MaxDeliveryAttempts {
				t.Fatalf("delivered %d times, want %d", len(got), messaging.MaxDeliveryAttempts)
			}
			for i := 1; i < len(got); i++ {
				gap := got[i].Sub(got[i-1])
				t.Logf("attempt %d: %v after attempt %d", i+1, gap.Round(time.Millisecond), i)
				if gap < tt.wantGap || gap > tt.maxGap {
					t.Errorf("attempt %d came %v after attempt %d, want between %v and %v",
						i+1, gap.Round(time.Millisecond), i, tt.wantGap, tt.maxGap)
				}
			}
		})
	}
}
