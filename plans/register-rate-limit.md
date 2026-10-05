# Rate limit on agent registration

## Open questions

1. **Count failed attempts only, or every request?** Recommended: **failed
   only**. A shop rolling out 30 tills from one office address sends 30
   registrations in a few minutes, each with its own valid token; a limit on
   all requests would stop that rollout, a limit on failures never sees it.
   The cost: while an address is blocked, a valid token from that address is
   refused too (429) until the block eases.
2. **The numbers.** Recommended: **10 failed attempts per address, then one
   more per minute.** Someone fumbling an expired token gets ten tries; a
   script gets 60 an hour. One knob, `AGENT_REGISTER_MAX_FAILURES` (default
   10, `0` turns the limit off) so it can be changed with `kubectl set env`
   without a build.
3. **Wrong credentials on the other agent routes** (`/work`, `/uploads`, …)
   also cost one database lookup each and are just as unauthenticated in
   practice. Recommended: **not in this PR.** Blocking an address there can
   cut off every healthy till behind the same shop router because one
   machine has a stale credential, and that stops deliveries. It needs its
   own design (a short memory of unknown credentials, not an address block).

## Goal

A single address cannot make the gateway do unlimited database work, or fill
the log, by posting made-up registration tokens. After a handful of failures
it is answered `429` with no database access, and it recovers by itself.
Genuine registrations are never slowed.

Not the goal: stopping token guessing. A token is 256 random bits and lives
an hour; guessing is not a realistic attack with or without a limit.

## What exists (checked 2026-10-05)

- `POST /agent/v1/register` (`src/cmd/remote-agent/register.go`) is the only
  agent route without a credential. A request whose token starts with
  `vrsky_reg_` opens a transaction and runs the token `UPDATE` every time.
  Nothing limits it; nothing counts it. The gateway has no metrics of its own.
- The gateway runs as **one replica** (pending work is held in memory), so a
  limit kept in memory is exact — no shared store needed.
- **The client address is trustworthy in prod.** The `ingress-nginx` Service
  has `externalTrafficPolicy: Local` and its ConfigMap has no overrides, so
  nginx sees the real client and sets `X-Real-IP` to it, replacing whatever
  the client sent. `X-Forwarded-For` is *not* safe to read first: the
  management API's `getClientIP` takes its leftmost entry, which a client
  controls if forwarded headers are ever enabled.
- `golang.org/x/time/rate` is already a dependency (`rate_limiter.go`, per
  connection, for tenant data ingestion). It has no "give the token back"
  that is simple to reason about, which this design needs.
- The agent CLI prints any JSON error as
  `VRSky answered <status> <code>: <message>`, so agents already installed
  show a 429 readably. No agent change is required.
- ingress-nginx can rate limit at the edge (`limit-rpm`), but only per
  address for *all* requests (open question 1), it answers an HTML 503, and
  it does nothing on the dev stack. See Non-goals.

## Approach

### 1. The limiter — `src/cmd/remote-agent/ratelimit.go`

A small token bucket per address, on the gateway's own clock (`s.now`, so
tests do not sleep):

```go
type failureLimiter struct {
    mu      sync.Mutex
    burst   float64            // AGENT_REGISTER_MAX_FAILURES, 0 = off
    perMin  float64            // 1
    buckets map[string]*bucket // address → tokens left, last refill
}
func (l *failureLimiter) take(addr string, now time.Time) (retryAfter time.Duration, ok bool)
func (l *failureLimiter) refund(addr string, now time.Time)
```

- `take` spends one attempt up front; `refund` gives it back once the token
  turned out to be valid. Spending first is what makes a burst of 1 000
  simultaneous requests stop at 10 — checking first and counting afterwards
  would let all of them through.
- Memory is bounded: a bucket that has refilled completely is forgotten on
  the next sweep (at most once a minute, inside `take`), and the map is capped
  at 50 000 addresses; past that, new addresses share one overflow bucket.

### 2. Whose address — `clientAddress(r)` in the same file

`X-Real-IP` if it parses as an IP, otherwise the host part of `RemoteAddr`.
`X-Forwarded-For` is not consulted. An IPv6 address is reduced to its /64, so
one customer line with a whole prefix is one address.

### 3. In `handleRegister`

First thing, before the body is read:

```go
addr := clientAddress(r)
if wait, ok := s.registerLimit.take(addr, s.now()); !ok {
    w.Header().Set("Retry-After", strconv.Itoa(seconds(wait)))
    writeErr(w, http.StatusTooManyRequests, agentproto.ErrRateLimited,
        "too many failed registration attempts from this address — wait a few minutes, "+
        "then try again with a new token from Settings → Remote agents")
    return
}
```

and `s.registerLimit.refund(addr, s.now())` right after the token `UPDATE`
returned a row. So these do **not** count: a successful registration, a name
that is taken (409), a bad group after a valid token. These **do**: a token
without the prefix, an unknown/used/expired token, an unreadable body, and a
database error before the token was known (when the database is struggling,
fewer attempts is the right direction).

One `Warn` log line when an address becomes blocked — not one per refused
request — and a counter `vrsky_agent_register_limited_total` on the gateway's
existing `/metrics` (already scraped by the connector PodMonitor).

### 4. Protocol and docs

- `agentproto.ErrRateLimited = "rate_limited"`. Additive; no protocol bump.
- `docs/operator/remote-agent.md` troubleshooting row: `429 rate_limited` →
  wait, generate a new token; and a short "Limits" note with the numbers and
  the env knob.
- Comment on `agent-ingress.yaml`: the gateway trusts `X-Real-IP` from this
  ingress, and what breaks that (see Risks).

## Files

| File | Change |
|---|---|
| `src/cmd/remote-agent/ratelimit.go` (+`ratelimit_test.go`) | limiter, `clientAddress`, counter |
| `src/cmd/remote-agent/register.go` | `take` first, `refund` on a valid token |
| `src/cmd/remote-agent/service.go` | `registerLimit` on the gateway, read `AGENT_REGISTER_MAX_FAILURES` |
| `src/cmd/remote-agent/gateway_test.go`, `gateway_db_test.go` | tests below |
| `src/pkg/agentproto/proto.go` | `ErrRateLimited` |
| `docs/operator/remote-agent.md`, `infrastructure/kubernetes/ingress/agent-ingress.yaml` | docs / comment only |

Not changed: the agent binary, the ingress rules, the management API, the UI.

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestRegister_BlocksAnAddressAfterTooManyFailures` | 10 bad tokens → 401 each; the 11th → 429, `Retry-After`, `rate_limited`, and **no database call** (sqlmock expects none) | remove the `take` check |
| `TestRegister_LimitIsPerAddress` | a second address still gets 401 while the first is blocked | key every bucket by a constant |
| `TestRegister_SuccessfulRegistrationsDoNotCount` (real Postgres, `pkg/testdb`) | 30 valid tokens from one address → 30 × 201 | remove the `refund` |
| `TestRegister_BlockedAddressRecovers` | clock + 1 min → one more attempt, then 429 again | never refill |
| `TestRegister_ConcurrentFailuresStopAtTheLimit` | 200 at once from one address → at most 10 reach the database | check first, count afterwards |
| `TestClientAddress_IgnoresForwardedFor` | a different `X-Forwarded-For` on every request does not dodge the block | read `X-Forwarded-For` first |
| `TestClientAddress_IPv6IsOnePrefix` | two addresses in one /64 share a bucket | key by the full address |
| `TestFailureLimiter_ForgetsIdleAddresses` | the map shrinks after refill; the cap holds | skip the sweep |
| `TestRegister_LimitCanBeTurnedOff` | `AGENT_REGISTER_MAX_FAILURES=0` → never 429 | — |

Then `gofmt`, `go vet`, `golangci-lint`, `make lint-tenant`,
`go test -race ./...` (the Postgres test via `make test-db`).

## Verify in prod (after deploy, on your word)

1. From this laptop: 11 registrations with a made-up token, each with a
   different fake `X-Real-IP` and `X-Forwarded-For` header. Expect ten 401s
   and then 429 — which proves both the limit and that nginx replaces the
   header. (This address is then blocked from registering for ~10 minutes.)
2. `vrsky_agent_register_limited_total` > 0 in Prometheus; one Warn line in
   the gateway log.
3. POS-PC stays Online throughout — polling is untouched.

## Rollout

Gateway only: `az acr build … vrsky/remote-agent` → `kubectl rollout restart
deploy/vrsky-remote-agent`. The restart drops the in-memory pending work, as
any gateway restart does; the durables re-deliver. No migration, no manifest
change, no pipeline redeploys.

## Risks

- **Everything behind one address shares a bucket** (a shop's NAT, a
  corporate proxy). Because only failures count, the worst case is one
  misbehaving machine delaying its neighbours' *registrations* by minutes.
  Running tills are unaffected.
- **If something is ever put in front of ingress-nginx** (Front Door, a CDN)
  or forwarded headers are enabled, `X-Real-IP` becomes the proxy's address
  and all registrations share one bucket. Still failure-only, so it degrades
  to "a flood blocks registration for everyone" rather than breaking normal
  use — and prod check 1 would start failing, which is the signal.
- **A flood from many addresses** gets 10 attempts per address. That is the
  ordinary DDoS case and belongs at the edge (Non-goals).
- **The block also refuses a valid token** from that address until it eases
  (open question 1). The message says to wait and why.

## Non-goals

- Limiting failed credentials on the authenticated routes (open question 3).
- An edge limit in ingress-nginx or Azure (DDoS protection). Worth doing
  separately as a generous cap on the whole `/agent` path.
- Sharing one limiter package with the management API's per-connection one.
- Fixing `getClientIP` in the management API (leftmost `X-Forwarded-For`).
  Noted here because it was found here; it only feeds audit rows today.
- An alert rule on the new counter. Add after seeing what normal looks like.
