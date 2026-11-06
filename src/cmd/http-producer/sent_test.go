package main

import (
	"fmt"
	"testing"
	"time"
)

func TestSentSet(t *testing.T) {
	k := func(i int) sentKey {
		return sentKey{connID: "c", envelopeID: fmt.Sprintf("e%d", i), nodeID: "n", method: "POST", url: "https://x"}
	}
	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		run  func(t *testing.T, s *sentSet)
	}{
		{"kept within the TTL", func(t *testing.T, s *sentSet) {
			s.add(t0, k(1))
			if !s.has(k(1), t0.Add(time.Hour-time.Nanosecond)) {
				t.Error("entry gone before the TTL")
			}
		}},
		{"expires at the TTL and is removed", func(t *testing.T, s *sentSet) {
			s.add(t0, k(1))
			if s.has(k(1), t0.Add(time.Hour)) {
				t.Error("entry still there at the TTL")
			}
			if s.size() != 0 {
				t.Errorf("size = %d after expiry, want 0", s.size())
			}
		}},
		{"forget removes", func(t *testing.T, s *sentSet) {
			s.add(t0, k(1), k(2))
			s.forget(k(1))
			if s.has(k(1), t0) || !s.has(k(2), t0) {
				t.Error("forget removed the wrong entry")
			}
		}},
		{"full set drops expired entries first", func(t *testing.T, s *sentSet) {
			s.add(t0, k(1))
			s.add(t0.Add(30*time.Minute), k(2), k(3))
			s.add(t0.Add(time.Hour), k(4)) // k(1) expired
			if s.size() != 3 || s.has(k(1), t0.Add(time.Hour)) {
				t.Errorf("size = %d, k1 kept = %v; want 3 and false", s.size(), s.has(k(1), t0.Add(time.Hour)))
			}
			for _, i := range []int{2, 3, 4} {
				if !s.has(k(i), t0.Add(time.Hour)) {
					t.Errorf("k%d dropped", i)
				}
			}
		}},
		{"full set drops the oldest", func(t *testing.T, s *sentSet) {
			for i := 1; i <= 3; i++ {
				s.add(t0.Add(time.Duration(i)*time.Minute), k(i))
			}
			s.add(t0.Add(4*time.Minute), k(4))
			if s.size() != 3 {
				t.Errorf("size = %d, want the cap 3", s.size())
			}
			if s.has(k(1), t0.Add(4*time.Minute)) {
				t.Error("oldest entry kept")
			}
		}},
		{"re-adding a key does not evict", func(t *testing.T, s *sentSet) {
			s.add(t0, k(1), k(2), k(3))
			s.add(t0.Add(time.Minute), k(3))
			if s.size() != 3 || !s.has(k(1), t0.Add(time.Minute)) {
				t.Error("re-adding an existing key evicted another")
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, newSentSet(time.Hour, 3))
		})
	}
}

// The TTL must cover a retry from the DLQ, not only the redeliveries.
func TestSentTTLCoversTheDLQ(t *testing.T) {
	if sentTTL < 7*24*time.Hour {
		t.Errorf("sentTTL = %s, shorter than the DLQ's 7 days", sentTTL)
	}
	if sentMax <= 0 {
		t.Errorf("sentMax = %d, want a positive cap", sentMax)
	}
}
