package main

import (
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Registration is the one agent route without a credential, and a request
// carrying anything shaped like a token costs a database transaction. The
// limit below bounds how much of that one address can cause
// (plans/register-rate-limit.md).
//
// Only FAILED attempts count. A shop bringing 30 tills online from one office
// address sends 30 registrations in a few minutes, each with its own valid
// token; a limit on every request would stop that rollout, and this one never
// sees it. It is not a defence against guessing tokens — they are 256 random
// bits and live an hour — but against unbounded work and log noise.
const (
	// defaultRegisterMaxFailures is how many failed attempts an address gets
	// in a row. AGENT_REGISTER_MAX_FAILURES overrides it; 0 turns the limit off.
	defaultRegisterMaxFailures = 10
	// registerRefillEvery is how long it takes to earn one attempt back.
	registerRefillEvery = time.Minute
	// maxLimitedAddresses caps the memory a flood from many addresses can
	// take. Past it, addresses not yet tracked share overflowAddress.
	maxLimitedAddresses = 50_000
	overflowAddress     = "overflow"
)

var registerLimited = promauto.NewCounter(prometheus.CounterOpts{
	Name: "vrsky_agent_register_limited_total",
	Help: "Agent registrations refused with 429 because their address had too many failed attempts.",
})

// failureLimiter is a token bucket per client address. An attempt is spent
// before the work is done and handed back if it turned out to be genuine.
// Spending first is the point: checking first and counting the failure
// afterwards would let a thousand simultaneous requests all pass the check.
type failureLimiter struct {
	mu        sync.Mutex
	burst     float64 // 0 = disabled
	refill    time.Duration
	maxAddrs  int
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens  float64
	updated time.Time
	blocked bool // refused since it last had an attempt to spend
}

func newFailureLimiter(maxFailures int) *failureLimiter {
	return &failureLimiter{
		burst:    float64(maxFailures),
		refill:   registerRefillEvery,
		maxAddrs: maxLimitedAddresses,
		buckets:  map[string]*bucket{},
	}
}

// take spends one attempt for addr. ok is false when there is none left;
// retryAfter then says when the next one is earned, and first is true for the
// refusal that starts a block, so the caller can log a block once rather than
// once per refused request.
func (l *failureLimiter) take(addr string, now time.Time) (retryAfter time.Duration, ok, first bool) {
	if l == nil || l.burst <= 0 {
		return 0, true, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	b, known := l.buckets[addr]
	if !known {
		if len(l.buckets) >= l.maxAddrs {
			addr = overflowAddress
			b, known = l.buckets[addr]
		}
		if !known {
			b = &bucket{tokens: l.burst, updated: now}
			l.buckets[addr] = b
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

// refund hands back the attempt take spent, once the request proved genuine.
func (l *failureLimiter) refund(addr string, now time.Time) {
	if l == nil || l.burst <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, known := l.buckets[addr]
	if !known {
		// take put this address in the shared bucket; hand it back there.
		if b, known = l.buckets[overflowAddress]; !known {
			return
		}
	}
	l.refillLocked(b, now)
	b.tokens = math.Min(l.burst, b.tokens+1)
}

func (l *failureLimiter) refillLocked(b *bucket, now time.Time) {
	if elapsed := now.Sub(b.updated); elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+float64(elapsed)/float64(l.refill))
		b.updated = now
	}
}

// sweep forgets addresses whose allowance has refilled completely: such a
// bucket says nothing a fresh one would not. At most once per refill period,
// so a flood does not pay for a full scan on every request.
func (l *failureLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < l.refill {
		return
	}
	l.lastSweep = now
	for addr, b := range l.buckets {
		l.refillLocked(b, now)
		if b.tokens >= l.burst {
			delete(l.buckets, addr)
		}
	}
}

// clientAddress is the address a registration is counted against.
//
// X-Real-IP, because the agent Ingress is the only public way in and
// ingress-nginx sets that header to the address it accepted the connection
// from, replacing whatever the client sent. X-Forwarded-For is deliberately
// not read: its leftmost entry is the client's to choose whenever forwarded
// headers are passed through, and a limit keyed on it is dodged by changing a
// header. Without the header (local runs, tests) it is the peer address.
//
// An IPv6 address counts as its /64: one customer line is handed a whole
// prefix, and every address in it is the same machine room.
func clientAddress(r *http.Request) string {
	if addr, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return addressKey(addr)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addressKey(addr)
	}
	return "unknown"
}

func addressKey(addr netip.Addr) string {
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}

// registerMaxFailuresFromEnv reads AGENT_REGISTER_MAX_FAILURES. It exists so
// the limit can be changed, or switched off with 0, by `kubectl set env`
// rather than a build if it ever misfires in production.
func registerMaxFailuresFromEnv(logger *slog.Logger) int {
	raw := strings.TrimSpace(os.Getenv("AGENT_REGISTER_MAX_FAILURES"))
	if raw == "" {
		return defaultRegisterMaxFailures
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		logger.Warn("invalid AGENT_REGISTER_MAX_FAILURES; using the default",
			"value", raw, "default", defaultRegisterMaxFailures)
		return defaultRegisterMaxFailures
	}
	return n
}
