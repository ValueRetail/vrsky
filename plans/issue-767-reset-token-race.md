# #767: one-time auth tokens can be spent twice at once

## Goal

`UsePasswordResetToken` and `UseEmailVerificationToken` read the token, check
`used_at` in Go, then mark it used with a plain `UPDATE ... WHERE id = $1`.
Two requests with the same link can both pass the check, so both set a
password (last one wins) or both verify. Make spending a token atomic and put
the follow-up writes in the same transaction.

## Acceptance criteria

- Spending a token happens in one transaction: the token row is read with
  `SELECT ... FOR UPDATE`, checked for used and expired, marked used, and the
  user row is written, all before commit.
- A reset's password write and session invalidation are in that transaction.
  A failed write rolls back and leaves the token unused; a spent token never
  sets a password.
- Email verification's user update is in the same kind of transaction.
- HTTP answers for used, expired and unknown tokens don't change (the same
  `auth.ErrTokenUsed`, `auth.ErrTokenExpired`, `auth.ErrInvalidToken` come
  back from the repository; handlers are untouched).
- Postgres tests (`src/pkg/testdb`): many concurrent uses of one token behind
  a start barrier, over several tokens; exactly one wins, the rest get
  `ErrTokenUsed`, and the stored password is the winner's. Same for
  verification. A failed user write leaves the token usable.
- Handler-level check that only the winning request writes a `success` row
  to `auth_audit_log` (#766) and the others write `token already used`.
- Mutation check: the race tests fail against the old code.

## Approach

Why `FOR UPDATE` rather than a conditional `UPDATE ... AND used_at IS NULL`:
the lock keeps the three error cases distinct without a second query. A
waiting request blocks on the row lock, and under READ COMMITTED Postgres
re-reads the locked row once the winner commits, so it sees `used_at` set and
returns `ErrTokenUsed`. If the winner rolls back, the waiter sees the token
unused and can spend it.

`UpdateUserPassword`, `VerifyUserEmail` and `InvalidateAllUserSessions` keep
their signatures; their SQL moves into small helpers that take an `execer`
(`*sql.DB` or `*sql.Tx`) so the token functions reuse the same statements
inside the transaction.

## Files

- `src/pkg/managementapi/repo_user.go`
- `src/pkg/managementapi/auth_token_race_db_test.go` (new)

## Risks

- The row lock is held only for the few statements in the transaction; the
  bcrypt hash is computed in the handler before the call, so it is not held
  during hashing.
- A cancelled request context while waiting on the lock returns an error
  (500), same as any DB error today.
- No schema change, no migration.

## Verification

In `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race -count=1 ./pkg/managementapi/...` with
`VRSKY_TEST_POSTGRES_URL` set. From the root: `make lint-tenant`,
`make lint-connector`. Mutation: restore the old function bodies, run the race
tests, see them fail, restore the fix.
