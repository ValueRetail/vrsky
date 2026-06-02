package sdk

import (
	"errors"
	"time"
)

// Error classification for connector Deliver/Evaluate/Convert results. The SDK
// runner maps these onto the underlying messaging (NAK / retry / DLQ)
// semantics so connector authors express intent, not transport mechanics:
//
//   - nil               → ack (success)
//   - Retriable(err)    → NAK; messaging retries with backoff, then DLQs
//   - Permanent(err)    → ack + log (poison message; retrying can't help)
//   - RateLimited(err)  → NAK; treated like Retriable today, with the delay
//                         recorded for future use
//   - any other error   → Retriable by default (safer than dropping)
//
// Use errors.Is/As — the wrappers preserve the cause.

type retriableError struct{ cause error }

func (e *retriableError) Error() string { return e.cause.Error() }
func (e *retriableError) Unwrap() error { return e.cause }

type permanentError struct{ cause error }

func (e *permanentError) Error() string { return e.cause.Error() }
func (e *permanentError) Unwrap() error { return e.cause }

type rateLimitedError struct {
	cause error
	after time.Duration
}

func (e *rateLimitedError) Error() string { return e.cause.Error() }
func (e *rateLimitedError) Unwrap() error { return e.cause }

// Retriable marks an error as transient — the SDK will NAK so the message is
// redelivered (and eventually DLQ'd if it keeps failing).
func Retriable(err error) error {
	if err == nil {
		return nil
	}
	return &retriableError{cause: err}
}

// Permanent marks an error as unrecoverable — retrying the same message will
// never succeed (bad config, validation failure, malformed payload). The SDK
// acks + logs rather than burning the retry budget.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{cause: err}
}

// RateLimited marks an error caused by downstream throttling; `after` is how
// long to wait before retrying. Currently treated as Retriable at the
// transport layer (the messaging backoff schedule applies); the delay is
// preserved for callers that inspect it.
func RateLimited(err error, after time.Duration) error {
	if err == nil {
		return nil
	}
	return &rateLimitedError{cause: err, after: after}
}

// IsPermanent reports whether err (or anything it wraps) was marked Permanent.
func IsPermanent(err error) bool {
	var pe *permanentError
	return errors.As(err, &pe)
}

// RetryAfter returns the delay from a RateLimited error, or (0, false).
func RetryAfter(err error) (time.Duration, bool) {
	var rl *rateLimitedError
	if errors.As(err, &rl) {
		return rl.after, true
	}
	return 0, false
}
