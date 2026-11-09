package messaging

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

type delayErr struct{ d time.Duration }

func (e delayErr) Error() string           { return "rate limited" }
func (e delayErr) NakDelay() time.Duration { return e.d }

// #721: the delay a handler error asks for, clamped to [1 s, largest step].
func TestNakDelay(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		backoff []time.Duration
		want    time.Duration
		wantOK  bool
	}{
		{"plain error asks for nothing", errors.New("boom"), DefaultBackoff, 0, false},
		{"in range", delayErr{5 * time.Second}, DefaultBackoff, 5 * time.Second, true},
		{"wrapped", fmt.Errorf("deliver: %w", delayErr{7 * time.Second}), DefaultBackoff, 7 * time.Second, true},
		{"zero raised to the floor", delayErr{0}, DefaultBackoff, time.Second, true},
		{"negative raised to the floor", delayErr{-time.Second}, DefaultBackoff, time.Second, true},
		{"under the floor", delayErr{100 * time.Millisecond}, DefaultBackoff, time.Second, true},
		{"over the cap", delayErr{time.Hour}, DefaultBackoff, 2 * time.Minute, true},
		{"cap is the largest step, not the last", delayErr{time.Hour},
			[]time.Duration{time.Second, 3 * time.Minute, time.Minute}, 3 * time.Minute, true},
		{"a schedule shorter than the floor wins", delayErr{5 * time.Second},
			[]time.Duration{50 * time.Millisecond, 50 * time.Millisecond}, 50 * time.Millisecond, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := nakDelay(tt.err, tt.backoff)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("nakDelay = (%v, %v), want (%v, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
