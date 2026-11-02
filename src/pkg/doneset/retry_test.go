package doneset

import (
	"testing"
	"time"
)

func TestRetryBackoff(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	s := New(nil, Key{}, "scope")

	if !s.RetryDue("a", now) {
		t.Fatal("a name that never failed must be due")
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour, time.Hour}
	for i, w := range want {
		failures, wait := s.RetryFailed("a", now)
		if failures != i+1 || wait != w {
			t.Fatalf("failure %d: got (%d, %s), want (%d, %s)", i+1, failures, wait, i+1, w)
		}
		if s.RetryDue("a", now.Add(w-time.Second)) {
			t.Fatalf("failure %d: due %s early", i+1, time.Second)
		}
		if !s.RetryDue("a", now.Add(w)) {
			t.Fatalf("failure %d: not due when the wait ran out", i+1)
		}
		now = now.Add(w)
	}
	// Other names have their own backoff.
	if !s.RetryDue("b", now) {
		t.Fatal("b never failed but is not due")
	}
	s.RetryDone("a")
	if s.Retrying() != 0 || !s.RetryDue("a", now) {
		t.Fatal("RetryDone must forget the name")
	}
	if failures, wait := s.RetryFailed("a", now); failures != 1 || wait != RetryBase {
		t.Fatalf("after RetryDone the backoff starts over: got (%d, %s)", failures, wait)
	}
}

// A name that leaves the listing is forgotten, whoever removed it.
func TestRetryForgetsNamesThatLeaveTheListing(t *testing.T) {
	now := time.Now()
	s := New(nil, Key{}, "scope")
	st := Stamp{Size: 1}
	s.Mark("a", st)
	s.Mark("b", st)
	s.RetryFailed("a", now)
	s.RetryFailed("b", now)

	s.Observe(map[string]Stamp{"a": st})
	if s.Retrying() != 1 || s.RetryDue("a", now) {
		t.Fatalf("a is still listed and must keep its backoff (retrying %d)", s.Retrying())
	}
	if !s.RetryDue("b", now) {
		t.Fatal("b left the listing and must be forgotten")
	}
}
