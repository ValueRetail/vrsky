package main

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/ValueRetail/vrsky/pkg/failurelimit"
)

// Registration is the one agent route without a credential, and a request
// carrying anything shaped like a token costs a database transaction. The
// limit below bounds how much of that one address can cause
// (plans/register-rate-limit.md). The bucket itself is pkg/failurelimit.
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
)

func newRegisterLimiter(maxFailures int) *failurelimit.Limiter {
	return failurelimit.New(maxFailures, registerRefillEvery)
}

var registerLimited = promauto.NewCounter(prometheus.CounterOpts{
	Name: "vrsky_agent_register_limited_total",
	Help: "Agent registrations refused with 429 because their address had too many failed attempts.",
})

// clientAddress is the address a registration is counted against.
//
// X-Real-IP, because the agent Ingress is the only public way in and
// ingress-nginx sets that header to the address it accepted the connection
// from, replacing whatever the client sent. X-Forwarded-For is deliberately
// not read: its leftmost entry is the client's to choose whenever forwarded
// headers are passed through, and a limit keyed on it is dodged by changing a
// header. Without the header (local runs, tests) it is the peer address.
//
// An IPv6 address counts as its /64 (failurelimit.AddressKey).
func clientAddress(r *http.Request) string {
	if addr, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return failurelimit.AddressKey(addr)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return failurelimit.AddressKey(addr)
	}
	return "unknown"
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
