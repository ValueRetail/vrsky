# Two auth tests take six minutes of every Go Tests run (#405)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Cut the time the login, change-password and limit tests in
`src/pkg/managementapi` spend in bcrypt at cost 12 under `-race`, without
touching production code and without changing a single assertion.

## Acceptance criteria (from the issue)

1. No production code changes (`src/pkg/auth`, the handlers, anything that
   isn't a `_test.go` file).
2. Every test asserts what it asserts today: same limits, same outcomes,
   same error codes. No assertion line is added, removed or edited.
3. The unknown-email path still spends the same work as a real check, and
   a test still proves it at the real cost.
4. `go test -race ./pkg/managementapi/...` drops by several minutes;
   before and after numbers go in the PR.
5. No new `time.Sleep`.

## Where the time goes

Users added by `loginRepo.addUser` are already hashed at `bcrypt.MinCost`.
The cost-12 work comes from two places the fixtures don't control:

- **The login dummy hash.** `loginDummyHash` (`auth_handler.go`) is built
  once with `auth.HashPassword`, at cost 12. Every login with an unknown
  email verifies against it. `TestAuthLimitsFromEnv` sends 50 of those,
  `TestLogin_BlocksAnAddressAfterTooManyFailures` 11,
  `TestLogin_UnknownEmailIsLimitedLikeARealOne` 11.
- **Changed passwords.** `ChangePassword` stores
  `auth.HashPassword(new)`, so from the first change on the user's hash is
  cost 12. `TestChangePassword_SuccessRefunds` changes the password 20
  times and logs in before each change, so it pays about 20 hashes and
  50 verifies at cost 12.

## Approach (test files only)

`auth_limit_test.go`:

- `newLoginEnv` installs the existing `Handler.verifyPassword` seam. When
  the handler verifies against the production dummy hash, the seam
  verifies the same password against a test dummy hash made once at
  `bcrypt.MinCost`. Every other hash goes to `auth.VerifyPassword`
  unchanged. The handler still calls `verify` once for an unknown email,
  with the production dummy hash; only the cost of that one compare
  changes.
- `TestLogin_UnknownEmailStillVerifiesAPassword` already installs its own
  seam over this one and asserts that the dummy hash is the real one at
  `auth.DefaultBcryptCost`. It is untouched, so the "same work" property
  stays tested at the real cost.

`auth_reset_test.go`:

- `resetRepo.UpdateUserPassword` remembers which user it changed.
  `resetEnv.change` clears that before each request, and a 200 that stored
  nothing fails the test, so re-storing can't hide a skipped store (review
  finding; a "skip the store on the second call" mutation now fails).
- `resetEnv.change`: after a 200, the first change in a test keeps the
  hash `ChangePassword` stored. Every later one re-stores the new password
  for that user at `bcrypt.MinCost`. So each test still logs in at least
  once against a hash the real `ChangePassword` → `auth.HashPassword` path
  wrote (in `TestChangePassword_SuccessRefunds`, the login and change in
  the second round), and later rounds verify cheaply.
- The `auth.HashPassword` call inside `ChangePassword` can't be avoided
  without a production change (the cost is a constant and there is no
  seam), so `TestChangePassword_SuccessRefunds` still pays 20 cost-12
  hashes. That is the floor for a test-only fix.

## Not changed

- `TestForgotPassword_SendsALinkThatResets` (about 13 s): its cost-12 work
  is the reset path itself (two `ResetPassword` hashes, the explicit
  `auth.VerifyPassword` check and the login with the new password), all on
  purpose. The loginEnv seam doesn't touch it: the user exists.
- Signup tests: `TestSignup_LimitedPerAddressBeforeHashing` hashes once on
  purpose (the valid sign-up from another address).
- `auth.DefaultBcryptCost`, `HashPassword`, `loginDummyHash`, the handlers.

## Decisions

- The dummy-hash swap goes through the existing `verifyPassword` seam
  rather than storing fake users for the unknown emails: the tests are
  about emails nobody registered, and a stored user would change what they
  test.
- Lowering the dummy hash's cost in these tests hides nothing: none of
  them measure time. They check status codes, bodies, `Retry-After`,
  lookups, logs and audit rows. The property that an unknown email costs a
  verify at the real cost is checked by
  `TestLogin_UnknownEmailStillVerifiesAPassword`, which bypasses the cheap
  seam.
- Re-storing a changed password at MinCost skips the per-round proof that
  `ChangePassword` stored a hash of the new password. The first change in
  each test keeps that proof against the real stored hash; a handler that
  stored the wrong hash would fail there.

## Risks

- A future test that relies on `e.h.verifyPassword` being nil after
  `newLoginEnv`. None does today; the one test that sets the seam replaces
  it.

## Verification

- Before/after: `go test -race -count=1 -run
  'TestChangePassword|TestAuthLimitsFromEnv|TestLogin_|TestForgotPassword'
  ./pkg/managementapi/ -v`.
- `go test -race -count=1 ./pkg/managementapi/...`, `go test -race ./...`,
  `go vet ./...`, `golangci-lint run ./pkg/managementapi/...`, gofmt.
- `git diff` shows no changed assertion line (`wantStatus`, `t.Fatal*`,
  `t.Error*`).
