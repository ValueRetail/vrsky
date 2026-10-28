# #446: Go lint is the slowest job before a deploy

## Open questions

None blocking. Touches `.github/workflows/`, so the PR waits for a human
(`agent:needs-human`).

## Where the time goes

Go lint on main, run 40702108450 (commit 8db0e02f), job 122864366291,
6 m 38 s in total. From the job log:

| Step | Time | Note |
|---|---|---|
| Set up job + checkout | 2 s | |
| Alert rules (promtool) | **4 m 05 s** | image pull 2 s, `check rules` 0.2 s, `test rules` 4 m 02 s |
| Set up Go | 6 s | cache hit, 349 MB |
| Check gofmt | 1 s | |
| Download Go modules | <1 s | already in the restored cache |
| Run Go linter | **2 m 20 s** | `Ran golangci-lint in 138463ms`; its own cache restored 1.4 MB |
| Lint OpenAPI annotations (`go run`) | 0.2 s | |
| Lint tenant filters | 0.6 s | |

For comparison the slowest test shard (managementapi) took 4 m 56 s.

So the issue's guesses were partly off: the promtool image pull is two
seconds and `go run ./cmd/lint-openapi` a fifth of a second. Caching the
image or building lint-openapi once would save nothing measurable. The time
is `promtool test rules`, which got slow when the backup alert tests (#404)
landed: they evaluate up to 13 800 minutes (9.6 days) of samples at the
one-minute evaluation interval. After that comes golangci-lint.

## Approach

Run the two slow steps side by side instead of one after the other:

- New job `alert-rules` ("Alert rules (promtool)"): checkout plus the same two
  `docker run … promtool` commands, unchanged. No Go setup.
- `go-lint` keeps gofmt, golangci-lint, the OpenAPI lint and the tenant
  lint, in that order, unchanged.
- The "Go Tests" aggregator needs `alert-rules` too and fails unless its
  result is `success`. `plan` needs it as well.

Expected critical path for lint: the slower of the two, about 4 m 15 s,
under the slowest test shard.

Not done, and why:

- `only-new-issues`, dropping linters, loosening `.golangci.yml`: weaken the
  gate (ruled out by the issue).
- Caching the promtool image: the pull is 2 s.
- Building lint-openapi once: it already runs in 0.2 s from the restored
  build cache.
- golangci-lint cache: its own cache entry is only 1.4 MB, and setup-go's
  build cache is one key shared by every Go job, so the lint job's build
  output is never saved. A lint-specific GOCACHE entry might save a minute,
  but after the split golangci-lint is off the critical path. Backlog item.
- Splitting `promtool test rules` over matrix legs by test group: would cut
  the new critical path further but needs every group named and a guard that
  each runs exactly once. Backlog item, if Alert rules ever becomes the
  slowest job.

## Affected files

- `.github/workflows/build-push.yml`: new `alert-rules` job, promtool step
  out of `go-lint`, aggregator and `plan` needs.
- `src/pkg/managementapi/gotestshards_guard_test.go`: the aggregator and
  `plan` must need `alert-rules` too.
- `src/pkg/managementapi/lintgates_guard_test.go` (new):
  `TestEveryLintCheckStillGates`. Every check (gofmt, golangci-lint, OpenAPI
  lint, tenant lint, promtool check, promtool test) must sit in a job that
  runs on pushes and PRs, without a step-level `if` or `continue-on-error`,
  and that both the aggregator and `plan` need. The aggregator must compare
  each needed job's result to `success`. golangci-lint must run in `src`
  without `only-new-issues` or narrowing flags, and `.golangci.yml` must keep
  `linters.default: standard` with nothing disabled wholesale.
- `docs/DEPLOYMENT_GUIDE.md`: job table.
- `plans/backlog.md`: three follow-ups (lint-connector not in CI, lint
  caches, promtool split).

## Risks

- A check moved between jobs and silently lost: the new guard.
- A lint job that fails but does not stop the deploy: the guard checks both
  the aggregator and `plan`.
- Branch protection names checks: it does not require "Go lint" or "Go
  Tests" today (see the aggregator comment), so the new check name breaks
  nothing.

## Verification

- `go test -count=1 ./pkg/managementapi/...` from `src/`.
- Guard mutations, locally: dropping `alert-rules` from `plan`, adding
  `--new` to golangci-lint's args, deleting the `test rules` command,
  `continue-on-error` on the tenant lint, `default: none` in
  `.golangci.yml`, and dropping the `RULES` comparison from the aggregator
  each turn `TestEveryLintCheckStillGates` red.
- CI mutations on the PR branch, one scratch commit per check (gofmt stops
  the job before the later steps, so they cannot share one commit), each
  reverted by a new commit: an unformatted file, a lint error, an OpenAPI
  drift, a tenant-unsafe query, a broken alert rule. Each must turn the
  right job red.
- Before/after timings from real CI runs, in the PR.
