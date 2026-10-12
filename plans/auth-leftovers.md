# Auth leftovers: a password reset that works, a limit on change-password, and the sign-up answer

Checked in the code on 2026-10-12 (`main` at `af648a9`).

## What is actually there

- **Forgot password is broken end to end.** The login page links to
  `/forgot-password`; no such page or route exists in the UI, so the link
  lands on *Not found*. The API's `ForgotPassword` (`auth_handler.go:390`)
  creates a reset token and then throws it away (`_ = rawToken // Use this
  token in the email link`) while answering "a password reset link will be
  sent". `ResetPassword` (`:441`) and the token table are complete and
  unused. There is no reset page either.
- **There is a mailer.** `pkg/notify/email.go` sends plain-text mail with
  one platform-wide identity from `SMTP_HOST/PORT/FROM/USERNAME/PASSWORD`
  (`smtpFromEnv`, `notifications_handler.go:446`); dev's compose has
  mailpit on it. **Prod has no `SMTP_*` set**, so nothing can send mail
  there today.
- **No public URL is configured anywhere** in the management API; a reset
  link needs one and must not be derived from the request's `Host` header
  (an attacker-supplied host would put their domain in a victim's reset
  mail).
- **Change-password** (`:493`) verifies the current password with no
  failure limit: a stolen session can try the current password
  indefinitely, at bcrypt speed, to turn a session into the password.
- **Sign-up** (`:76`) answers `409 "email already registered"`.

## Open questions

1. **Mail in prod.** The reset mail needs real SMTP credentials:
   `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM`, `SMTP_USERNAME`, `SMTP_PASSWORD`
   on the management API (a Kubernetes secret; the manifest will reference
   it as optional). Your call which sender: Microsoft 365 SMTP AUTH with an
   app password on the `valueretail.no` tenant, Azure Communication
   Services Email, or SendGrid. **Until it is set, the forgot-password page
   says "Password reset is not available on this installation yet; contact
   your workspace owner"** (a 503 from the API, identical for every email),
   which is honest and leaks nothing. Nothing in this PR waits on the
   credentials.
2. **`PUBLIC_URL`**: recommended value `https://vrsky.valueretail.no`,
   set in the manifest (not a secret). Required for the link; with it unset
   the API also answers 503.
3. **Sign-up's 409.** Recommended: **leave it.** Telling a returning user
   "this email already has an account, sign in instead" is the right
   answer for them, and the alternative (answer success and mail the
   existing owner) only works once prod can send mail — and every mainstream
   sign-up form reveals this. The sign-up limiter already caps probing at 5
   per address per 10 minutes. Alternative: once SMTP is live, return the
   generic success and mail "someone tried to sign up with your email".
4. **Change-password limit**: 10 wrong current passwords per user, refilled
   one per 5 minutes, same numbers as the login account limit; a correct
   one hands the attempt back. Blocked → 429 with the existing
   `limitedMessage`.

## Goal

A user who forgot their password gets a mail with a link, sets a new
password, and is signed out everywhere else — on an installation that can
send mail; on one that cannot, a clear "not available" instead of a
promise. A stolen session cannot grind the current password out of
change-password. Sign-up stays as it is, deliberately.

## Approach

### 1. API: `ForgotPassword` sends, or says it cannot

- `notify.SendText(ctx, cfg SMTPConfig, to, subject, body string) error`
  extracted from `Email.Send` (which then calls it). Same `sendMail` seam
  for tests.
- Handler gets `mailer func(ctx, to, subject, body string) error` and
  `publicURL string`, wired in `cmd/management-api/main.go` from
  `smtpFromEnv()` and `PUBLIC_URL`. `mailer` is nil when `SMTP_HOST` or
  `SMTP_FROM` is empty; `publicURL` is validated once (`https://`, no
  path) and empty when unset.
- `ForgotPassword`:
  1. limits first: per address (reuse the sign-up bucket's shape: 5 per
     10 minutes, every request counts) and per submitted email (3 per
     10 minutes), both before any lookup, so the endpoint cannot be used to
     flood someone's inbox or the SMTP quota;
  2. with no mailer or no public URL → `503 ResetUnavailable` with the
     message above, **before** the email is looked up, so the answer is the
     same for every address;
  3. otherwise as today, then `mailer(ctx, user.Email, "Reset your VRSky
     password", body)` with the link `<PUBLIC_URL>/reset-password?token=<raw>`
     and the expiry; a send failure is logged (`password_reset_requested`
     / `send_failed` audit row) and still answers the generic 200.
- `ChangePassword`: `h.verify(...)` through the seam from #310, wrapped by a
  per-user `failurelimit` bucket (`changePassword` on `authLimits`); refund on
  success; blocked → `refuseLimited(..., "change_password", "account", ...)`.
  The counter `vrsky_auth_limited_total` gains `endpoint="change_password"`.
- Sign-up: unchanged (question 3).

### 2. UI: the two missing pages

- `pages/ForgotPasswordPage.tsx` at `/forgot-password`: one email field;
  on 200 shows the generic "if that address has an account, a reset link is
  on its way"; on 503 shows the API's message; on 429 the limited message.
- `pages/ResetPasswordPage.tsx` at `/reset-password?token=…`: new password
  twice; on 200 "password reset, sign in"; on 400 the API's reason (invalid,
  used, expired) with a link back to forgot-password. Mirrors
  `InviteAcceptPage` for structure and tests.
- `authService.forgotPassword/resetPassword` already exist.

### 3. Config, docs

- `deployment.yaml`: `PUBLIC_URL` with the prod value; `SMTP_*` from a
  secret `smtp-credentials` with `optional: true` so the pod starts without
  it. `docker-compose.yml`: `PUBLIC_URL: http://localhost:5173` next to the
  existing `SMTP_HOST: mailpit`.
- `docs/operator/billing.md`-style page `docs/operator/mail.md`: what the
  platform sends (alerts, password resets), the five variables, how to
  create the secret, and what users see while it is unset. The
  troubleshooting guide's "forgot password" entry rewritten.

## Files

| File | Change |
|---|---|
| `src/pkg/notify/email.go` (+test) | `SendText`; `Email.Send` uses it |
| `src/pkg/managementapi/auth_handler.go` | `ForgotPassword` limits, 503, mail; `ChangePassword` limit via the seam |
| `src/pkg/managementapi/auth_limit.go` | `forgot` (address), `forgotEmail`, `changePassword` buckets; env overrides follow the existing pattern |
| `src/pkg/managementapi/handler.go` | `mailer`, `publicURL` fields + setters |
| `src/cmd/management-api/main.go` | wiring from env |
| `src/pkg/managementapi/auth_reset_test.go` (new) | the tests below |
| `src/pkg/managementapi/auth_limit_test.go` | change-password limit tests |
| `ui/src/pages/ForgotPasswordPage.tsx`, `ResetPasswordPage.tsx` (+tests), `App.tsx` | the pages and routes |
| `infrastructure/kubernetes/management-api/deployment.yaml`, `docker-compose.yml` | `PUBLIC_URL`, optional `SMTP_*` secret |
| `docs/operator/mail.md` (new), `docs/guide/troubleshooting.md`, `docs/SECURITY.md` | as above |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestForgotPassword_SendsALinkThatResets`: fake mailer captures the mail; the token in its link goes through `ResetPassword` and the mock repo's password changes; sessions invalidated | the round trip | drop the token from the link |
| `TestForgotPassword_UnknownEmailAnswersTheSameAndSendsNothing` | no enumeration | send on unknown too / differ the answer |
| `TestForgotPassword_WithoutMailIs503ForEveryone`: no mailer; known and unknown email → identical 503 body, no token created | honest, no leak | look the user up before the check |
| `TestForgotPassword_LinkUsesPublicURLNotHost`: request with `Host: evil.example` → link starts with the configured URL | no host poisoning | build the link from `r.Host` |
| `TestForgotPassword_IsLimitedPerAddressAndPerEmail` | flood protection | remove either `Take` |
| `TestChangePassword_BlocksAfterTenWrongCurrentPasswords` then correct after refill; `TestChangePassword_SuccessRefunds` | the limit | remove the bucket / the refund |
| `TestSendText_FormatsAPlainMail` (notify) | headers, UTF-8 | — |
| UI vitest: forgot page 200/503/429 renderings; reset page reads the token from the query, 200/400 renderings | the pages | — |

Then `gofmt`, `go vet`, `golangci-lint`, `make lint-tenant`, `go test -race
./...`; UI `npm run lint`, `npx vitest run`, **`npm run build`**.

## Verify

- Dev stack: sign up a throwaway, request a reset, open mailpit
  (`http://localhost:8025`), follow the link, set a new password, old
  session rejected.
- Prod after deploy (on your word), before SMTP is set: the forgot page
  shows the "not available" message; `POST /api/v1/auth/forgot-password`
  with a nonsense email → 503 with the same body as with your email.
- Prod after you create `smtp-credentials` and `kubectl rollout restart`: a
  reset mail arrives for your account; the link works.

## Rollout

management-api and UI images; `deploy-core-azure.sh management-api ui`;
`kubectl apply` of the manifest for `PUBLIC_URL` (the secret reference is
optional, so this is safe before the secret exists). SMTP credentials are a
separate, later step for you: `kubectl create secret generic
smtp-credentials -n vrsky-platform --from-literal=…` then a restart.

## Risks

- **A reset mail is a sign-in.** Token is 32 random bytes, stored hashed,
  single-use, expires per `CalculatePasswordResetTokenExpiry` (today's
  value kept); `ResetPassword` already invalidates every session.
- **SMTP quota**: the two forgot-password limits bound outbound mail to 3
  per email and 5 per address per 10 minutes.
- **503 reveals that mail is unset**, not anything about accounts; it is
  the same for every request.

## Non-goals

Email verification at sign-up (the `auto_verify` TODO; a product decision
once mail is live); HTML mail; a "someone tried to sign up with your
email" mail (question 3's alternative); magic-link sign-in; requiring the
current password on `DELETE /auth/me`.
