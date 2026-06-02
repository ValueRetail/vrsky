package harness

import (
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Matcher reports whether an envelope satisfies a condition.
type Matcher func(*envelope.Envelope) bool

// MatchAny matches every envelope.
func MatchAny() Matcher { return func(*envelope.Envelope) bool { return true } }

// MatchTenant matches envelopes for a given tenant.
func MatchTenant(tenantID string) Matcher {
	return func(e *envelope.Envelope) bool { return e.TenantID == tenantID }
}

// MatchID matches an envelope by ID.
func MatchID(id string) Matcher {
	return func(e *envelope.Envelope) bool { return e.ID == id }
}

// Eventually polls fn until it returns true or the timeout elapses, failing
// the test otherwise. Useful for asserting a connector's side effect (e.g. a
// file appearing on disk) after Publish.
func Eventually(t *testing.T, timeout time.Duration, msg string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Eventually: condition not met within %s: %s", timeout, msg)
}
