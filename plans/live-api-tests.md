# Live api.met.no tests out of CI

Seen 2026-10-20: the `main` run for #330 failed in *Go Tests* on
`TestRealAPI_MetNo_LocationForecast` and `TestRealAPI_MetNo_Sunrise`
(`Read() error = context deadline exceeded`), so nothing deployed and the
CI alert filed #331. The tests call https://api.met.no, a public service
whose slowness has nothing to do with the merge.

## Open questions

1. **How to opt in.** Recommended: **an environment variable,
   `VRSKY_TEST_LIVE_APIS=1`**, mirroring `VRSKY_TEST_POSTGRES_URL`: unset,
   the tests skip with a message that says how to run them; set, they run
   as today. The `testing.Short()` skip stays. Alternative: a build tag
   (`//go:build live`), which hides the tests from `go vet ./...` and from
   editors until the tag is passed; the variable keeps them compiled.
2. **Run them anywhere on a schedule?** Recommended: **no.** Neither the
   nightly end-to-end nor the integration workflow sets the variable; a
   public weather API is not something this repository needs to monitor.
   Alternative: set it in `e2e-nightly.yml`, so a real met.no regression
   shows up overnight instead of never; a red nightly files an issue, not
   a blocked deploy.

## Goal

A merge never waits on, or fails because of, a service outside GitHub and
the repository's own containers. The three live tests remain one command
away for whoever changes the API consumer.

## What exists (checked)

- `src/pkg/io/api_input_test.go` lines 738–900: `TestRealAPI_MetNo_LocationForecast`,
  `TestRealAPI_MetNo_MultipleRecords`, `TestRealAPI_MetNo_Sunrise`, each
  gated by `testing.Short()` only. CI runs `go test -v -race -cover ./...`
  without `-short`.
- Every other test file that mentions a public URL (OAuth providers,
  cloud-storage events, Business Central, SAP, Graph, OIDC, notifications)
  uses it as a config literal against an `httptest` server or never opens
  a connection; `api.met.no` is the only host a test actually reaches.
- `src/pkg/testdb` is the opt-in precedent: `EnvVar`, a skip with the
  variable's name, CI sets it and fails if the skip message appears.
- `AGENTS.md` "Tests that need a real Postgres" documents that pattern.

## Approach

### 1. The skip

In `api_input_test.go`:

```go
// liveAPIEnv opts the tests that reach api.met.no in. CI never sets it: a
// public service's slowness must not block a deploy (#331).
const liveAPIEnv = "VRSKY_TEST_LIVE_APIS"

func skipUnlessLiveAPIs(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping real API test in short mode")
	}
	if os.Getenv(liveAPIEnv) == "" {
		t.Skipf("%s not set; set it to 1 to run the live api.met.no tests", liveAPIEnv)
	}
}
```

Each of the three tests calls `skipUnlessLiveAPIs(t)` in place of its
`testing.Short()` block. Nothing else in the tests changes.

### 2. The guard

`TestLiveAPITestsAreOptIn` in the same package reads `api_input_test.go`,
takes every `func TestRealAPI_` body (up to the next top-level `func`),
and fails unless it calls `skipUnlessLiveAPIs(t)` before anything else.
It also greps every `*_test.go` under `src` for `api.met.no` and fails if
the host appears in a file other than this one, so a copied test in a new
connector cannot reintroduce the dependency unnoticed.

### 3. Docs

`AGENTS.md`, after the Postgres section: *Tests that reach the public
internet* — the variable, the command, and that CI never sets it.

## Files

| File | Change |
|---|---|
| `src/pkg/io/api_input_test.go` | `skipUnlessLiveAPIs`, three call sites |
| `src/pkg/io/live_api_guard_test.go` (new) | `TestLiveAPITestsAreOptIn` |
| `AGENTS.md` | the section |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestLiveAPITestsAreOptIn` | every `TestRealAPI_*` opts in; no other test file names api.met.no | remove the call from one test; add the host to another test file |
| `go test ./pkg/io/ -run TestRealAPI -v` unset / set | skip with the message / the three tests run | — |

Then `gofmt`, `go vet`, `golangci-lint run ./pkg/io/`.

## Verify

The PR's *Go Tests* log shows the three tests as `SKIP` with the
variable's name; locally `VRSKY_TEST_LIVE_APIS=1 go test ./pkg/io/ -run
TestRealAPI -v` reaches met.no and passes.

## Risks

- **The live tests rot** once nobody runs them. They were already
  optional under `-short`; the guard keeps them compiled, and the
  alternative in question 2 is one line in the nightly if that matters.

## Non-goals

Mocking api.met.no; changing the API consumer; touching the other
`testing.Short()` gates (NATS and end-to-end tests that need the local
stack).
