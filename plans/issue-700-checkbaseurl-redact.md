# Issue 700: CheckBaseURL quotes the raw base URL

Closes #700 (backlog items 170 and 163).

## Problem

`safeurl.CheckBaseURL` builds every error with `%q` on the raw input. The
"must not carry a user name or password" error holds the credential by
definition, and the scheme error does too when the URL has userinfo. The
callers (Sitoo, Brightpearl, Visma, Front Systems, SAP S/4HANA, Business
Central, the management API) put that text in logs, activity events and the
DLQ header.

`RedactURL` also lets `http://apikey:1234#secret@host` through unchanged:
url.Parse reads host `apikey`, port `1234` and fragment `secret@host`, so the
password sits in the fragment.

`pkg/vismaurl` names `token_url` by trimming `base_url` off the front of
`CheckBaseURL`'s message, which breaks silently if the wording changes.

## Acceptance criteria

- No `CheckBaseURL` error contains userinfo. The userinfo error does not name
  the URL at all; the others show scheme, host and path only, through
  `RedactURL` (a query or fragment may hold a key; added after review).
- `RedactURL` returns `[invalid URL]` when the fragment contains `@`.
- New `safeurl.CheckURL(field, raw, allowPrivate)` names the field in its
  errors; `CheckBaseURL` is `CheckURL("base_url", …)`.
- `vismaurl` calls `CheckURL("token_url", …)`; the `renamed` wrapper goes.
- Message wording otherwise unchanged, so callers' tests pass as they are.

## Approach

- `CheckURL` keeps the order of checks. Messages: `<field> "<redacted>" must
  use https`, `<field> must not carry a user name or password`, etc.
- Base URLs never legitimately hold `@` outside userinfo (a query or fragment
  is refused anyway), so `CheckURL` shows `[invalid URL]` whenever the input
  has `@` and url.Parse did not put it in `u.User`. This also covers
  `https://apikey:1234?secret@host`, a password with `?`, which `RedactURL`
  keeps because the HTTP producer's test pins `?email=a@b` as shown.
- Not touched: SAP S/4HANA and Business Central wrap the error as
  `token_url refused: base_url …`; switching them to `CheckURL` is for later
  (other PRs in flight there).

## Files

`src/pkg/safeurl/safeurl.go`, `src/pkg/safeurl/redact_test.go`,
`src/pkg/vismaurl/vismaurl.go`, `plans/backlog.md`.

## Verify

- Table over every `CheckBaseURL` error path with userinfo-bearing input
  (including `@` in the query and fragment); the secret never appears. Fails
  on main.
- `RedactURL` fragment rows. Fail on main.
- `CheckURL` names its field; vismaurl test unchanged.
- From `src/`: build, vet, gofmt, golangci-lint, `go test -race -count=1` on
  safeurl, vismaurl and every caller package, `make lint-tenant`.
