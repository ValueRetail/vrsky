// Package failurelimit bounds how many times something can FAIL from one
// source — a client address, an account name — before it is refused for a
// while. Genuine use is never slowed: an attempt is spent before the work is
// done and handed back once the request proved genuine.
//
// It is a token bucket per key, on the caller's clock. It began life in the
// remote-agent gateway for agent registration (plans/register-rate-limit.md)
// and moved here when the management API needed the same for logins
// (plans/login-rate-limit.md).
package failurelimit

import (
	"math"
	"net/netip"
	"sync"
	"time"
)

// Defaults for the bounds on memory. A flood from many sources must not grow
// the map without end: past MaxKeys, keys not yet tracked share one bucket.
const (
	DefaultMaxKeys = 50_000
	overflowKey    = "\x00overflow"
)

// Limiter is a token bucket per key. An attempt is spent before the work is
// done and handed back if it turned out to be genuine. Spending first is the
// point: checking first and counting the failure afterwards would let a
// thousand simultaneous requests all pass the check.
type Limiter struct {
	// MaxKeys caps the number of keys tracked; set before first use.
	MaxKeys int

	mu        sync.Mutex
	burst     float64 // 0 = disabled
	refill    time.Duration
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens  float64
	updated time.Time
	blocked bool // refused since it last had an attempt to spend
}

// New makes a limiter allowing maxFailures in a row per key, earning one back
// every refill. maxFailures 0 disables it: Take always allows.
func New(maxFailures int, refill time.Duration) *Limiter {
	return &Limiter{
		MaxKeys: DefaultMaxKeys,
		burst:   float64(maxFailures),
		refill:  refill,
		buckets: map[string]*bucket{},
	}
}

// Take spends one attempt for key. ok is false when there is none left;
// retryAfter then says when the next one is earned, and first is true for the
// refusal that starts a block, so the caller can log a block once rather than
// once per refused request.
func (l *Limiter) Take(key string, now time.Time) (retryAfter time.Duration, ok, first bool) {
	if l == nil || l.burst <= 0 {
		return 0, true, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	b, known := l.buckets[key]
	if !known {
		if len(l.buckets) >= l.MaxKeys {
			key = overflowKey
			b, known = l.buckets[key]
		}
		if !known {
			b = &bucket{tokens: l.burst, updated: now}
			l.buckets[key] = b
		}
	}
	l.refillLocked(b, now)
	if b.tokens >= 1 {
		b.tokens--
		b.blocked = false
		return 0, true, false
	}
	first = !b.blocked
	b.blocked = true
	return time.Duration((1 - b.tokens) * float64(l.refill)), false, first
}

// Refund hands back the attempt Take spent, once the request proved genuine.
// Never more than the allowance: a slow genuine request does not bank extra.
func (l *Limiter) Refund(key string, now time.Time) {
	if l == nil || l.burst <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, known := l.buckets[key]
	if !known {
		// Take put this key in the shared bucket; hand it back there.
		if b, known = l.buckets[overflowKey]; !known {
			return
		}
	}
	l.refillLocked(b, now)
	b.tokens = math.Min(l.burst, b.tokens+1)
}

// Tracked is how many keys the limiter currently remembers.
func (l *Limiter) Tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func (l *Limiter) refillLocked(b *bucket, now time.Time) {
	if elapsed := now.Sub(b.updated); elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+float64(elapsed)/float64(l.refill))
		b.updated = now
	}
}

// sweep forgets keys whose allowance has refilled completely: such a bucket
// says nothing a fresh one would not. At most once per refill period, so a
// flood does not pay for a full scan on every request.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < l.refill {
		return
	}
	l.lastSweep = now
	for key, b := range l.buckets {
		l.refillLocked(b, now)
		if b.tokens >= l.burst {
			delete(l.buckets, key)
		}
	}
}

// AddressKey is the key for a client address. An IPv6 address counts as its
// /64: one customer line is handed a whole prefix, and every address in it is
// the same machine room. An IPv4-mapped IPv6 address is its IPv4 address.
func AddressKey(addr netip.Addr) string {
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}
