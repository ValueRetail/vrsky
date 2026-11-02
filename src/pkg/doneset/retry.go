package doneset

import "time"

// A done name that is still listed while the consumer's after_action is
// delete or move is one whose after-action failed (#640). Before the done set
// was kept (#538) a restart published such a file again and so retried the
// after-action; now nothing does. The Set tracks the retries of its own
// poller, in memory: after a restart every such name is retried at once, and
// backs off again if that fails.

const (
	// RetryBase is how long after a failed after-action the first retry
	// waits. Each further failure doubles it.
	RetryBase = time.Minute
	// RetryMax caps the wait, so a name that never works costs one try an
	// hour, not one per poll.
	RetryMax = time.Hour
	// MaxRetriesPerPoll bounds the retries a single poll makes. A folder full
	// of leftovers (after_action switched from none to delete) is cleared over
	// several polls instead of holding one poll for minutes.
	MaxRetriesPerPoll = 20
)

// retryState is where one name stands: how many after-actions failed in a row
// and when the next try is due.
type retryState struct {
	failures int
	next     time.Time
}

// RetryDue reports whether the after-action of name may be tried at now: it
// has not failed in this Set's lifetime, or its backoff has run out.
func (s *Set) RetryDue(name string, now time.Time) bool {
	st, ok := s.retry[name]
	return !ok || !now.Before(st.next)
}

// RetryFailed records a failed after-action of name at now and returns how
// many have failed in a row and how long until the next try.
func (s *Set) RetryFailed(name string, now time.Time) (failures int, wait time.Duration) {
	if s.retry == nil {
		s.retry = make(map[string]retryState)
	}
	st := s.retry[name]
	st.failures++
	wait = RetryBase
	for i := 1; i < st.failures && wait < RetryMax; i++ {
		wait *= 2
	}
	wait = min(wait, RetryMax)
	st.next = now.Add(wait)
	s.retry[name] = st
	return st.failures, wait
}

// RetryDone forgets name: its after-action worked, or nobody needs it any
// more.
func (s *Set) RetryDone(name string) { delete(s.retry, name) }

// Retrying is the number of names with a failed after-action waiting for
// their next try.
func (s *Set) Retrying() int { return len(s.retry) }
