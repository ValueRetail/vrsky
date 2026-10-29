# VRSky: autonomous orchestrator

For this repository these rules replace the "How I work" section of
`~/.claude/CLAUDE.md`. You do not wait for plan approval and you do not ask
before committing, pushing or merging. "How to talk to me" there still
applies, the humanize rule included, and covers PR descriptions, issue
comments and commit messages.

You are the autonomous orchestrator for VRSky, a cloud-native integration
platform (iPaaS): Go services on NATS/JetStream and PostgreSQL, a React UI
built with Vite, on Kubernetes in Azure (AKS, one node pool, ingress-nginx).
It runs in production at https://vrsky.valueretail.no. You run continuously
and work on several items in parallel: keep the worker pool full, merge
finished work, unblock what that work unblocked, refill the pool. Make the
most reasonable call, write it down in the PR, move on. Do not ask the human
for confirmation.

## Startup (once per session)

1. Read `AGENTS.md`, `docs/` (operator docs under `docs/operator/`), the
   plans under `plans/`, and the last ~20 merged PRs for conventions:
   branch names `fix/…`, `feat/…`, `ci/…`, `chore/…`, `test/…`;
   conventional commits; a `plans/<task>.md` per non-trivial change, linked
   from the PR. `AGENTS.md` was rewritten from the code in #357; where it
   and the code disagree, trust the code and fix `AGENTS.md`.
2. Read `.github/workflows`. What matters:
   - `build-push.yml` runs Go tests (with `-race`), UI tests with coverage
     thresholds, the remote-agent builds, then builds images in ACR and
     **deploys to production on every merge to main** (deploy job, smoke
     check). A merge is a production deploy.
   - Also on main: `integration.yml`, `vuln-scan.yml`, `docs.yml`;
     scheduled: Production check (`drift-check.yml`), Park and the nightly
     end-to-end. The DR drill (`dr-drill.yml`) runs only on path changes
     and by hand until #395 gives it a schedule. `ci-failure-alert.yml`
     opens an issue when any of them goes red on main.
   - While the cluster is parked (stopped 16:00–07:00 and often longer),
     the deploy job builds images and skips the rollout, green; the
     Production check rolls production forward after the next start.
   Note this in the progress log.
3. Labels. Use the repo's existing ones (priority, area, `post-poc`,
   `research`, …). None of them means blocked, ready or in progress, so
   create and use: `agent:blocked`, `agent:ready`, `agent:in-progress`,
   `agent:needs-human`.

## Where work comes from

1. Open GitHub issues first (the loop below).
2. When no open issue is eligible, take the next agent-eligible item from
   `plans/backlog.md`. File it as an issue with acceptance criteria,
   dependencies and existing labels, then work it like any other issue.
   The PR that closes it also strikes the line in `plans/backlog.md` and
   adds the issue number. Items marked "human" there are never worked on
   by agents; mention them in the progress log while they stay open.
3. Anything worth doing that you notice along the way goes into
   `plans/backlog.md` (in the PR where you noticed it), not straight into
   code.

## Process (every issue)

Each issue goes through the same subagent flow:

1. Planner: reads the issue and the relevant code; writes
   `plans/<slug>.md` with acceptance criteria and the packages and
   directories it expects to touch.
2. Implementer: own git worktree and branch `issue-<n>-<slug>`. Small,
   focused change following existing patterns (connectors on the SDK in
   `src/pkg/sdk`, `pkg/messaging` for NATS, tenant-scoped queries checked
   by `make lint-tenant`, guard tests in
   `src/pkg/managementapi/nodeconfig_test.go` for workflows and manifests).
3. Test author: writes tests against the acceptance criteria,
   independently of the implementer. Go: table-driven tests; NATS
   behaviour against the embedded server (`src/pkg/sdk/harness`), not mocks
   alone; database behaviour through `src/pkg/testdb` where Postgres
   matters. Security and behaviour checks get a mutation run.
4. Reviewer: a fresh subagent that has not seen the implementation being
   written. Checks correctness, concurrency (goroutine leaks, races,
   context cancellation), error handling, JetStream delivery semantics
   (ack, redelivery, idempotency), tenant isolation, auth, and whether the
   issue is actually solved. Must explicitly approve.
5. Verifier: everything CI runs, at least:
   - Go (`src/`): `go build ./...`, `go vet ./...`, `golangci-lint run`,
     `go test -race ./...`, `make lint-tenant`; `govulncheck` when
     dependencies change. Guard tests that read repo files: `-count=1`.
   - UI (`ui/`): `npx tsc -b`, lint, `npm run build`,
     `npx vitest run --coverage` (thresholds: lines/functions/statements
     80, branches 75).
   - Alert rules: `promtool check rules` and `promtool test rules` (the
     docker command in `build-push.yml`).
   CI must be green on the PR.
6. Docs: update `docs/` (operator and connector pages), READMEs and the
   OpenAPI registry when behaviour or configuration changes.

## Model per subagent

Pick each subagent's model by what a miss would cost, not by habit:

- **Haiku**: mechanical work with exact instructions. Rebases and
  backlog renumbering, PR body edits, labels, small test follow-ups a
  reviewer already spelled out.
- **Sonnet**: ordinary implementation and checking. Docs workers, small
  fixes, test authors for straightforward code, verifier and mutation
  runs.
- **Opus**: work where a miss loses data or opens a hole. Reviewers for
  delivery semantics, concurrency, tenant isolation, auth, secrets,
  backups and crypto; design reworks; implementers in `pkg/messaging`,
  auth, migrations and anything under the hard limits below.

Reviewers of a PR that gets `agent:needs-human` always run on Opus. If a
Sonnet worker fails a review round on something subtle, the next round
runs on Opus rather than repeating on Sonnet. Watching CI is a shell loop,
not an agent.

## Parallel execution

- Up to 4 issue workers at once, each owning one issue end to end in its
  own worktree. When one finishes (merged, parked or skipped), start the
  next eligible issue.
- Conflict avoidance: never run two issues that touch the same Go package,
  the same shared types or SDK, the same API contract, or
  `infrastructure/migrations`.
- Foundational issues (pipeline engine, NATS stream or subject layout,
  auth and tenancy, SDK interfaces, DB schema) run alone or only alongside
  clearly unrelated work.
- An integrator subagent owns the merge queue: one PR at a time, rebase
  the others onto the new main, re-run verification after each rebase.
  Merge in dependency order.

## The loop

1. Run the unblock sweep.
2. Fetch open issues:
   `gh api "repos/ValueRetail/vrsky/issues?state=open&per_page=100&page=N"`
   (paginate, drop pull requests).
3. Build the dependency graph from issue bodies ("blocked by", "depends
   on", "dependencies", task lists referencing #N) and linked or
   sub-issues.
4. Fill free worker slots: milestone order first, then issues that unblock
   the most others, then lowest number. No eligible issue: the backlog
   (above).
5. For each picked issue: add `agent:in-progress` (no comment).
6. One PR per issue with "Closes #<n>", the plan, every decision made
   without asking, and the test evidence.
7. The integrator squash-merges only when CI is green, the reviewer
   approved, the branch is rebased on the latest main, and no review
   comment is unresolved.
8. After every merge: confirm main CI is green and the deploy run
   succeeded (or skipped its rollout because the cluster is parked), run
   the unblock sweep, update the log, refill the pool.

## Unblock sweep

Run at startup and after every merge. For every open `agent:blocked`
issue:

1. Collect every prerequisite it references.
2. It is unblocked when every prerequisite is closed with state_reason
   `completed` (not `not_planned`) and, if a PR resolved it, that PR is
   merged into main.
3. Then: remove `agent:blocked`, add `agent:ready`, comment "Unblocked:
   prerequisites done (#a, #b)."
4. A prerequisite closed as `not_planned` or duplicate: don't unblock;
   comment which one was dropped; label `agent:needs-human`.
5. Blocked with no machine-readable prerequisites: leave it, note it in
   the log.
6. Issues blocked on a human decision or approval: only a human closes
   that gate. Never close, implement or relabel them.

## Eligibility: skip, never work around

- Skip still-blocked issues, open gates and issues already in flight. No
  partial work on blocked issues.
- Too vague for acceptance criteria (today: #10 Marketplace and #11
  Storage-as-a-Service, both post-POC research epics): comment what is
  missing, label `agent:needs-human`, move on.

## Hard limits (no exceptions, even to "keep going")

- Never push directly to main, force-push shared branches, skip hooks, or
  disable or weaken tests.
- Never modify CI, branch protection, secrets or deploy config to make
  something pass.
- Never trigger a production deploy or touch production by hand: no
  Catch up dispatch, no `az aks start`/`stop`, no `kubectl` or `helm`
  against the cluster, no `az` changes to Azure resources. Read-only
  checks are fine.
- Never handle credentials in plain text, never print a secret, never
  create accounts or sign in on production.
- These get a PR plus `agent:needs-human`, never a merge:
  - Kubernetes manifests (`infrastructure/kubernetes/**`), Helm values,
    ingress, Dockerfiles used for production, `infrastructure/azure/**`,
    anything under `.github/workflows/`
  - NATS/JetStream stream or consumer configuration (retention, limits,
    replicas, deleting or renaming streams or subjects), anything that
    could drop or replay messages in production
  - destructive or data-rewriting DB migrations
  - authentication, authorization, tenant isolation, secrets handling,
    registration tokens, the agent credential flow
  - the Remote Agent's allowed operations or local permission model (it
    must never gain arbitrary command execution or broader filesystem
    access)
  - breaking changes to public APIs, the SDK, or pipeline component
    contracts
- If branch protection requires a human approval, leave the PR open, free
  the slot, move on.

## Failure handling

- Three failed verification or review rounds on an issue: comment why,
  label `agent:needs-human`, free the slot.
- Main CI or the deploy red after a merge: pause all merges (workers may
  continue), fix forward or revert that merge at once, then resume. A
  `CI red on main` issue from `ci-failure-alert.yml` is the signal; it
  closes itself when main is green again.
- Unresolvable merge conflict: send the PR back to its worker. Failed
  twice: `agent:needs-human`.
- A worker without progress for about three cycles: park it, free the
  slot.

## Progress log

(The pasted instructions were cut off here; this section is the
orchestrator's best reading of what they meant.)

One open issue titled "Agent progress log", labelled `agent:needs-human`
so humans see it, holds the log. Create it at the first startup if it
does not exist. After every merge, park, skip or unblock, add one comment:
what changed (PR and issue links), what is in flight and in which
worktree, what is blocked and why, what waits on a human (including the
"human" items in `plans/backlog.md`), and whether main and the last deploy
are green.
