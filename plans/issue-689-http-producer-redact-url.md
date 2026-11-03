# Issue 689: HTTP producer shows its URL with userinfo

Closes #689 (backlog item 161).

## Problem

`sendHTTPRequest` in `src/cmd/http-producer/main.go` puts `httpCfg.URL` raw in
activity events ("Sending N bytes to ...", "POST url -> 200") and in a Warn log.
It also shows the URL through errors: `http.NewRequest` parse errors quote the
whole input, `http.Client.Do` errors keep the user name (only the password is
hidden), and `safeurl.CheckRequestURL` quotes the URL with `%q`. Those errors
are logged, emitted as events and returned (they end up in the DLQ header the
tenant can read). Userinfo is often the credential, a user name included.

## Approach

- `safeurl.RedactURL(raw)`: parse, replace any userinfo with `xxxxx` (the user
  name too, since it is often the key; `Redacted()` alone keeps it), return a
  fixed `[invalid URL]` if it does not parse. Query and path are kept so the
  feed still tells nodes apart.
- Producer: one `shownURL` per send for events and logs. Parse errors show a
  fixed text. Transport errors are rewritten to `op "shownURL": cause`, the cause
  still wrapped so `errors.Is(err, safeurl.ErrBlockedAddress)` keeps working.
- `CheckRequestURL` messages use `RedactURL`. Shared with the API consumer and
  the management API; they only get a redacted URL in their errors.
- Requests use the real URL.

## Files

`src/cmd/http-producer/main.go`, `src/pkg/safeurl/safeurl.go`, tests,
`plans/backlog.md`.

## Verify

- `redact_test.go`: table over success, user name as secret, 4xx, 500, oauth
  without grant, refused connection, private and loopback refusal and an
  unparseable URL. Logs (slog buffer), events and returned errors never contain
  the secret; the server sees the real basic-auth credentials.
- Fails without the producer change (all rows fail on the old code).
- Go build, vet, gofmt, golangci-lint, `go test -race`, `make lint-tenant`.
