# Limit failed logins

## Open questions

1. **A per-account limit can be used to keep a real user out.** Anyone who
   knows your email can spend the account's attempts and you get 429 on the
   password form. Three ways to go:
   - **A (recommended): limit per address and per account, and let a known
     address through.** When an account is blocked, an address that has
     logged in to that account successfully in the last 30 days is still
     allowed to try (under the per-address limit only). The attacker is
     stopped; you, at the office, are not. One extra indexed query, on the
     blocked path only.
   - B: per address and per account, no exception. Simplest; accepts the
     lockout. SSO and already-open sessions keep working either way.
   - C: per address only. No lockout risk, but someone with many addresses
     guesses at one account without limit.
2. **Keep the counters in memory?** Recommended: **yes**, as for agent
   registration. The API runs two replicas, so the effective allowance is up
   to twice the number below, and a deploy forgets it. Exact counting needs a
   table and a write per failed login; not worth it for this.
3. **The numbers.** Recommended: per address **10 failures, then one per
   minute**; per account **10 failures, then one per 5 minutes** (≈ 300–600
   guesses a day against one account at most, against an 8-character
   minimum). Knob: `AUTH_LOGIN_MAX_FAILURES` (default 10, `0` = off).
4. **Also limit sign-up?** `POST /auth/register` is open to the internet and
   hashes a password (bcrypt cost 12, ~¼ s of CPU) on every call.
   Recommended: **yes, in this PR** — per address, **5 requests, then one per
   10 minutes**, every request counts. Knob: `AUTH_SIGNUP_MAX_ATTEMPTS`.

## Goal

Password guessing against the login form is bounded per address and per
account, and neither login nor sign-up can be used to burn unlimited CPU.
Past the limit the API answers `429` before it looks anything up or hashes
anything. A correct login is never slowed, and never counts.

## What exists (checked 2026-10-05)

- `LoginUser` (`src/pkg/managementapi/auth_handler.go:167`): no limit of any
  kind — not per account, not per address, not at the ingress. Every failed
  attempt writes an `auth_audit_log` row (email, address, time). The same is
  true of register, forgot-password and reset-password.
- **There are no tests of the login handler at all.**
- **How a login reaches the API in prod:** browser → ingress-nginx → the UI
  pod's nginx (`/api/` proxy) → management-api. So at the API:
  - `X-Real-IP` is the *ingress controller pod's* address — useless here,
    unlike at the agent gateway, which sits directly behind the ingress;
  - `X-Forwarded-For` is `<client>, <ingress pod>`. The client entry is
    trustworthy only because ingress-nginx overwrites the header.
- Three helpers read the address today (`getClientIP`, `clientIP`, and one
  inline in `tenant_data_handler.go`); all take the **leftmost**
  `X-Forwarded-For` entry. Right in prod by accident; client-controlled the
  day forwarded headers are passed through.
- The limiter from #301 lives in `cmd/remote-agent` (package `main`), so the
  API cannot import it.
- The API runs **2 replicas**; the login form shows whatever message the API
  returns.

## Approach

### 1. Share the limiter — `src/pkg/failurelimit`

Move `failureLimiter` and `addressKey` out of the gateway unchanged
(`New(burst, refill)`, `Take`, `Refund`, `AddressKey`), with their unit
tests. The gateway imports it; its behaviour and its handler tests do not
change, and it needs no redeploy.

### 2. One correct client address — `clientAddr(r)` in `managementapi`

Read the hops right to left: `RemoteAddr`, then `X-Forwarded-For` from its
last entry backwards. Skip private, loopback and link-local addresses (our
own proxies). **The first public address is the client.** If there is none
(the dev stack), use the furthest hop.

This holds whether a proxy overwrites the header or appends to it, and
through one proxy or two, because an internet client cannot have a private
source address. `getClientIP` and `clientIP` become calls to it, so audit
rows get the same address the limit uses.

### 3. Login

```
addr := clientAddr(r)                    // spend one, before the body is read
if !addrLimit.Take(addr)  → 429
…decode…
acct := lower(trim(email))               // the submitted string, existing or not
if !acctLimit.Take(acct) && !knownAddress(acct, addr) → 429
…look up, CanLogin, verify password…
on success: Refund(addr), Refund(acct)
```

- **429** with `Retry-After`, code `RateLimited`, and one message for both
  cases — "too many failed sign-in attempts — wait a few minutes and try
  again" — so the answer does not say whether the account exists. Keying on
  the submitted email, registered or not, keeps it that way.
- `knownAddress` (option A): one query on `auth_audit_log` for a successful
  `login` by this email from this address (its /64 for IPv6) in 30 days.
- A wrong password, an unknown email, an unverified or disabled account and
  an unreadable body all count. A successful login counts for nothing.
- When a block starts: one `Warn` log line and one `auth_audit_log` row
  (`login` / `blocked`), not one per refused request. Counter
  `vrsky_auth_limited_total{endpoint,scope}` on the existing `/metrics`.

### 4. Sign-up (question 4)

`addr` only, every request counts, checked before the password is hashed.
Same 429 shape.

### 5. UI

No code change expected: the login and sign-up forms already show the API's
message. One test each that a 429 is shown as that message and not as
"Login failed".

## Files

| File | Change |
|---|---|
| `src/pkg/failurelimit/{limiter.go,limiter_test.go}` | moved from the gateway |
| `src/cmd/remote-agent/{ratelimit.go,ratelimit_test.go,register.go,service.go}` | use the shared package; gateway-specific parts stay |
| `src/pkg/managementapi/client_addr.go` (+test) | `clientAddr`; `getClientIP` and `clientIP` delegate |
| `src/pkg/managementapi/auth_limit.go` (+test) | the three limiters, `knownAddress`, counter, env knobs |
| `src/pkg/managementapi/auth_handler.go` | `LoginUser`, `RegisterUser` |
| `src/pkg/managementapi/auth_login_db_test.go` | real Postgres (`pkg/testdb`) |
| `ui/src/pages/{LoginPage,RegisterPage}.test.tsx` | 429 message |
| `docs/operator/troubleshooting.md`, `docs/SECURITY.md` (if present) | the limits and the knobs |

Not changed: OIDC login, sessions, API keys, the ingress, the UI's nginx,
`tenant_data_handler.go`, forgot/reset/change-password.

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestLogin_BlocksAnAddressAfterTooManyFailures` | 10 × 401 across different emails, then 429 + `Retry-After`, no user lookup | remove the address check |
| `TestLogin_BlocksAnAccountAcrossAddresses` | 10 failures on one email from 10 addresses, the 11th address gets 429 | remove the account check |
| `TestLogin_UnknownEmailIsLimitedLikeARealOne` | same status, body and timing path for a made-up email | key on the user id |
| `TestLogin_SuccessDoesNotCount` (Postgres) | 30 correct logins from one address, then the full allowance is still there | remove the refunds |
| `TestLogin_KnownAddressGetsThroughABlockedAccount` (Postgres) | account blocked by strangers; the address with a past success logs in; a stranger's does not; a success older than 30 days does not count | always true / drop the email condition / drop the address condition |
| `TestLogin_BlockedCorrectPasswordIsRefusedThenWorks` | 429 while blocked, 200 once it eases; no session created meanwhile | — |
| `TestLogin_BlockIsLoggedAndAuditedOnce` | one log line, one audit row, counter rises per refusal | log every refusal |
| `TestSignup_LimitedPerAddressBeforeHashing` | 6th request → 429 without a bcrypt call | check after hashing |
| `TestClientAddr_*` | two proxies; overwritten and appended headers; spoofed leftmost entry ignored; all-private dev chain; IPv6 /64; junk entries | take the leftmost entry / trust private hops |
| `failurelimit` unit tests | carried over from #301, same 11 mutations | — |

Test users get a minimum-cost bcrypt hash so the suite stays fast under
`-race`. Then `gofmt`, `go vet`, `golangci-lint`, `make lint-tenant`,
`lint-openapi`, `go test -race ./...` with the database, and the UI checks.

## Verify in prod (after deploy, on your word)

1. From this laptop: 11 logins for a made-up email with a wrong password and
   a different fake `X-Forwarded-For` each → ten 401s, then 429. The audit
   row must show this laptop's real address.
2. You log in normally afterwards. (This network's address is blocked for
   up to 10 minutes after step 1 — your open session is not affected, and
   with option A your address is known for your account anyway.)
3. `vrsky_auth_limited_total` in Prometheus; one Warn line per block.

## Rollout

`management-api` only (`build-push-acr.sh core` →
`deploy-core-azure.sh management-api`). No migration, no new env required,
open sessions untouched. The UI is rebuilt only if a UI file changes.

## Risks

- **Offices share an address.** Ten mistyped passwords in a row from one
  office block password login there for minutes. Successes do not count, so
  it takes ten failures with no success in between.
- **Lockout of an account** — question 1. With option A the remaining case
  is a user on an address they have never logged in from while someone is
  attacking their account.
- **Two replicas** double the allowance (question 2); more replicas, more so.
- **`clientAddr` assumes our proxies have private addresses.** True for the
  cluster. A proxy with a public address in front (a CDN) would be taken for
  the client: everyone would share one bucket. Still failure-only, and prod
  check 1 would show it.

## Found, not fixed here

- **Sign-up is open to anyone** who finds the URL, and answers "email
  already registered" — which tells a stranger who has an account. Whether
  sign-up should be open at all is a product decision.
- **Forgot-password sends nothing** (`TODO: Send password reset email`), so
  password reset does not work in prod. It also writes a token row per call,
  unlimited.
- **Login answers faster for an unknown email** (no password check), which
  also reveals who has an account.
- Change-password (signed in) can be used to guess the current password
  from a stolen session; unlimited.

## Non-goals

Limits on forgot/reset/change-password and on OIDC; MFA; CAPTCHA; an alert
rule on the counter (after seeing what normal looks like); an edge limit;
exact counting across replicas.
