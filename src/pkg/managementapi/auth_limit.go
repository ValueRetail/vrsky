package managementapi

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/ValueRetail/vrsky/pkg/failurelimit"
)

// Limits on the unauthenticated auth routes (plans/login-rate-limit.md).
//
// Login is limited per client address and per account, and only FAILURES
// count: an attempt is spent before anything is looked up or hashed and handed
// back when the password was right. So a correct login is never slowed, an
// office full of people is not blocked by its own successes, and a guess
// costs the guesser something whichever way it comes in. Sign-up is limited
// per address on every request, because every request hashes a password
// (bcrypt cost 12, about a quarter of a second of CPU).
const (
	// defaultLoginMaxFailures is how many failed logins an address, or an
	// account, gets in a row. AUTH_LOGIN_MAX_FAILURES overrides it; 0 = off.
	defaultLoginMaxFailures = 10
	loginAddressRefill      = time.Minute
	loginAccountRefill      = 5 * time.Minute
	// defaultSignupMaxAttempts is how many sign-up requests an address gets
	// in a row. AUTH_SIGNUP_MAX_ATTEMPTS overrides it; 0 = off.
	defaultSignupMaxAttempts = 5
	signupRefill             = 10 * time.Minute
	// knownAddressWindow is how far back a successful login from an address
	// lets that address keep trying an account others have blocked.
	knownAddressWindow = 30 * 24 * time.Hour

	limitedMessage = "too many failed sign-in attempts — wait a few minutes and try again"
)

var authLimited = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "vrsky_auth_limited_total",
	Help: "Requests to the auth routes refused with 429: endpoint is login or signup, scope is address or account.",
}, []string{"endpoint", "scope"})

// authLimits holds the three buckets. A Handler always has one (NewHandler
// gives it the defaults), so no caller needs a nil check.
type authLimits struct {
	address *failurelimit.Limiter // failed logins per client address
	account *failurelimit.Limiter // failed logins per submitted email
	signup  *failurelimit.Limiter // sign-up requests per client address
	now     func() time.Time
}

func newAuthLimits(loginMaxFailures, signupMaxAttempts int) *authLimits {
	return &authLimits{
		address: failurelimit.New(loginMaxFailures, loginAddressRefill),
		account: failurelimit.New(loginMaxFailures, loginAccountRefill),
		signup:  failurelimit.New(signupMaxAttempts, signupRefill),
		now:     time.Now,
	}
}

// AuthLimitsFromEnv reads AUTH_LOGIN_MAX_FAILURES and AUTH_SIGNUP_MAX_ATTEMPTS.
// They exist so a limit can be changed, or switched off with 0, by
// `kubectl set env` rather than a build if it ever misfires in production.
func AuthLimitsFromEnv(logger *slog.Logger) *authLimits {
	read := func(name string, def int) int {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return def
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			logger.Warn("invalid "+name+"; using the default", "value", raw, "default", def)
			return def
		}
		return n
	}
	return newAuthLimits(read("AUTH_LOGIN_MAX_FAILURES", defaultLoginMaxFailures),
		read("AUTH_SIGNUP_MAX_ATTEMPTS", defaultSignupMaxAttempts))
}

// SetAuthLimits replaces the default limits (used by main with the env knobs).
func (h *Handler) SetAuthLimits(l *authLimits) { h.authLimits = l }

// refuseLimited answers a request the limiter refused: 429, Retry-After, one
// message whatever the scope — the answer must not say whether the account
// exists. A block is logged and audited once, when it starts, not once per
// refused request: the log is one of the things the limit protects.
func (h *Handler) refuseLimited(w http.ResponseWriter, r *http.Request, endpoint, scope, key, email string, wait time.Duration, first bool) {
	authLimited.WithLabelValues(endpoint, scope).Inc()
	if first {
		slog.Default().Warn("Sign-in attempts blocked: too many failures",
			"endpoint", endpoint, "scope", scope, "key", key, "retry_after", wait.Round(time.Second).String())
		h.logAuthEvent(r.Context(), r, nil, email, endpoint, "blocked",
			stringPtr("too many failed attempts from this "+scope))
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	_ = writeError(w, http.StatusTooManyRequests, "RateLimited", limitedMessage, nil)
}

// knownAddress reports whether addr (a limiter key: an IPv4 address or an
// IPv6 /64) has logged in to this account before, recently. It is what lets
// the account's owner at their usual place keep trying while someone else is
// spending the account's attempts. A lookup failure counts as unknown.
func (h *Handler) knownAddress(ctx context.Context, email, addr string) bool {
	if addr == "unknown" {
		return false
	}
	ok, err := h.repo.HasLoginSucceededFrom(ctx, email, addr, h.authLimits.now().Add(-knownAddressWindow))
	if err != nil {
		slog.Default().Warn("known-address lookup failed; treating the address as unknown", "error", err)
		return false
	}
	return ok
}
