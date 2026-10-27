# Rewrite AGENTS.md from the code (#354)

No open questions.

## Goal

`AGENTS.md` still describes the research phase ("no code exists yet") and a
layout that never existed (`cmd/api-gateway`, `internal/`, `connectors/`,
Kong, Loki). Rewrite it so it describes the repository as it is today and
every command in it works from the directory it names.

## Acceptance criteria (from the issue)

- Describes the real layout: `src/` (Go module, `cmd/<service>`, `pkg/…`),
  `ui/` (Vite React), `infrastructure/` (migrations, Kubernetes manifests,
  `azure/` scripts), `docs/`, `plans/`.
- Every command runs from the stated directory: build, `go test -race ./...`,
  `make lint-tenant`, `golangci-lint run`, the UI's `npm run build` and
  `npx vitest run --coverage`, the promtool check, the local docker-compose
  stack.
- Keeps the sections on `VRSKY_TEST_POSTGRES_URL` and `VRSKY_TEST_LIVE_APIS`;
  adds the guard tests in `src/pkg/managementapi/nodeconfig_test.go`, tenant
  isolation (`make lint-tenant`) and how a merge to main deploys.
- Points to `CLAUDE.md` for how agents work here; no dated status or
  timeline lines.

## Files

- `AGENTS.md` (rewrite)
- `plans/backlog.md` (strike the first line of item 3, add "(#354)")
- this plan

## Approach

Write it from the sources, not from the old file: `src/Makefile`, the root
`Makefile`, `docker-compose.yml`, `ui/package.json`, `ui/vitest.config.ts`,
`src/.golangci.yml`, `.github/workflows/build-push.yml`,
`docs/operator/deploy.md` and `docs/operator/cost-parking.md`. Keep the
code-style guidance that still holds (Go conventions, `%w` wrapping, tenant
isolation, conventional commits); drop Kong, Loki, `internal/`,
`connectors/`, the 11-week timeline and the phase lines. Short sections,
commands in code blocks, each block saying where it runs.

## Verification

Docs only, so no tests. An independent checker subagent gets only the new
`AGENTS.md` and runs every command in it from the stated directory.
Commands that need the docker-compose stack or a Postgres URL are checked
for existence (make target or script present, flags valid) instead of run.
Fix what it reports, repeat until clean. The PR lists which commands ran
and which were only checked to exist.

## Risks

Small. A command that works on one machine but not another (tool not
installed) is called out as a prerequisite rather than dropped.
