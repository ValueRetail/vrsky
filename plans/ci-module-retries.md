# Retried module downloads in every Go job

Checked in the workflows on 2026-10-14 (`main` at `fa835682`).

## Open questions

1. **One composite action or four copies of the loop.** Recommended: **a
   composite action** `.github/actions/go-modules` holding the retry loop
   the `Go Tests` job has today, used by every Go job. One place to change,
   and the guard test checks for the `uses:` line instead of grepping four
   shell blocks. Alternative: paste the loop into each job, as #300 did for
   one.
2. **The DR drill** (`dr-drill.yml`, runs on push to main when its paths
   change) builds `vrsky-cli` with one unretried fetch. Recommended:
   **include it**; it is one `uses:` line and the same flake would fail a
   drill for nothing.
3. **`govulncheck@latest`** in the scan job is fetched through the proxy on
   every run, unretried, and *should* float (newest scanner). Recommended:
   keep `@latest`, wrap the `go install` in the same three-attempt loop.

## What the build found (2026-10-14)

The generalised guard turned up **two more Go jobs** the plan missed, and
they are the most exposed: the connector integration tests
(`integration.yml`) and the nightly SAP end-to-end (`e2e-nightly.yml`) run
`go test` inside a `docker run … golang:1.26` container with **no module
cache at all**, so every run fetches every module, unretried. Both now
carry the same three-attempt loop inside the container followed by
`GOPROXY=off`; the guard accepts a step that is self-contained that way
and insists on seven Go jobs, not five. The loop was run under a real
`golang:1.26` locally to prove the quoting.

## Goal

A single dropped download from proxy.golang.org never fails a CI job. #300
fixed `Go Tests`; the four other Go jobs still fetch modules one attempt
each, so the flake that reddened main on 2026-10-05 (#299) can still land
on any of them.

## What exists (checked)

| Job (workflow) | Fetches modules how | Retry |
|---|---|---|
| `Go Tests` (`build-push.yml`) | `Download Go modules` step, three attempts, then `GOPROXY=off` on both test steps | yes (#300) |
| `Remote agent on Windows` (`build-push.yml:291`) | `go test ./pkg/agent/…` on `windows-latest`, then `go build` in PowerShell | **no** |
| `Build remote agent binaries` (`build-push.yml:339`) | `make build-agent` | **no** |
| `govulncheck` (`vuln-scan.yml:43`) | `go install golang.org/x/vuln/cmd/govulncheck@latest`, then `govulncheck ./...` loads every module | **no** |
| DR drill (`dr-drill.yml:82`) | `go build ./cmd/vrsky-cli` | **no** |

`actions/setup-go` restores the module cache from `src/go.sum`, so on a
cache hit nothing is fetched; the flake bites on a cache miss (a `go.sum`
change, which is exactly when a PR touches dependencies, as #313 did) and
on `@latest` installs, which are never cached. The Dockerfiles already
retry (`TestDockerfilesRetryModuleDownload`).

The guard `TestGoTestsJobRetriesModuleDownload` (`nodeconfig_test.go:802`)
parses `build-push.yml`, looks at the `go-tests` job only, and requires: a
`go mod download` step with a loop, before the first `go test`, and
`GOPROXY=off` on every `go test` step.

## Approach

### 1. Composite action `.github/actions/go-modules/action.yml`

```yaml
name: Download Go modules
description: go mod download with three attempts, then the job runs with GOPROXY=off
inputs:
  working-directory: { default: src }
runs:
  using: composite
  steps:
    - shell: bash
      working-directory: ${{ inputs.working-directory }}
      run: |
        for i in 1 2 3; do
          go mod download && exit 0
          echo "::warning::go mod download failed (attempt $i/3)"
          [ "$i" = 3 ] || sleep $((i * 10))
        done
        echo "::error::go mod download failed three times — proxy.golang.org is likely down"
        exit 1
```

`shell: bash` runs on `windows-latest` too (Git Bash is on the runner), so
the Windows job uses the same action.

### 2. Every Go job

- `Go Tests`: the inline step becomes `uses: ./.github/actions/go-modules`;
  `GOPROXY=off` stays on the test steps.
- `Remote agent on Windows`: the action after `setup-go`; `GOPROXY: "off"`
  in the env of the `go test` step and of the PowerShell step (its
  `go build`).
- `Build remote agent binaries`: the action, then `GOPROXY: "off"` on the
  `make build-agent` step.
- `govulncheck`: the action, then the `go install …@latest` wrapped in the
  same three-attempt loop (it is a separate fetch the action cannot cover),
  then `GOPROXY=off` for `govulncheck ./...`. The vulnerability database
  comes from vuln.go.dev over HTTPS, not the proxy, and is untouched.
- DR drill: the action before its `go build`, `GOPROXY=off` on that step.

### 3. The guard, generalised

`TestGoTestsJobRetriesModuleDownload` becomes `TestGoJobsRetryModuleDownloads`:
for every workflow under `.github/workflows`, every job with a step that
runs `go test`, `go build`, `go run`, `govulncheck` or `make build-agent`
must have, earlier in the job, either `uses: ./.github/actions/go-modules`
or an inline `go mod download` inside a loop; and each of those steps must
carry `GOPROXY: "off"`. A `go install …@latest` must sit inside a loop.
The action file itself is checked for the loop. The test lists what it
found, so a renamed job cannot silently drop out (today's test fatals when
`go-tests` is missing; the new one fatals when it finds fewer than five
Go jobs).

## Files

| File | Change |
|---|---|
| `.github/actions/go-modules/action.yml` (new) | the retry loop |
| `.github/workflows/build-push.yml` | three jobs use it; `GOPROXY=off` on their Go steps |
| `.github/workflows/vuln-scan.yml` | the action; retried `go install`; `GOPROXY=off` on the scan |
| `.github/workflows/dr-drill.yml` | the action; `GOPROXY=off` on the build |
| `src/pkg/managementapi/nodeconfig_test.go` | the generalised guard |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestGoJobsRetryModuleDownloads` | every Go job downloads with retries first and runs with `GOPROXY=off`; the `@latest` install is retried | remove the `uses:` line from any one job; drop `GOPROXY=off` from the Windows `go test`; unwrap the `go install` |
| The PR's own CI | the five jobs are green with the action in place (`actions/checkout` must precede `uses: ./…`, which it does everywhere) | — |

Then `go test ./pkg/managementapi/ -run TestGoJobs`, `gofmt`, `golangci-lint`.

## Verify

The PR's checks: all five Go jobs pass; the Windows job's log shows
`Download Go modules` before the tests. Nothing to deploy.

## Risks

- **A module missing from the download set** would fail under
  `GOPROXY=off` on every run, loudly, rather than working until the proxy
  hiccups. That is the point, and it is how `Go Tests` has run since #300.
- **`go install …@latest` under the loop** still floats; a broken
  govulncheck release would fail the scan the same as today.

## Non-goals

A Go module mirror or vendoring; pinning `govulncheck`; retries around
`npm ci` (a separate flake class, none seen).
