# #720: allowlist registries in the mirror guard; dev flag on management-api

Backlog items 174 and 188.

## Acceptance criteria

- `FROM` lines pass only from `mirror.gcr.io/`, `mcr.microsoft.com/`,
  `ghcr.io/`, `localhost/` or `localhost:`. A lookalike
  (`mirror.gcr.io.example.com/`) and an unlisted registry fail.
- The same allowlist covers workflow `services:`/`container:`/`docker://`
  images, `docker run|pull|create` in workflow and composite-action scripts,
  and the image of every compose service a workflow starts, including its
  `depends_on`. One explicit exception: `vrsky-cli:drill` (built by the job).
- Parser tests are table-driven, with the lookalike cases.
- `management-api` in `docker-compose.yml` sets `ALLOW_PRIVATE_UPSTREAMS`
  (default true, dev only). Nothing under `infrastructure/` sets it.

## Touches

`src/pkg/managementapi/nodeconfig_test.go`, new `mirror_guard_test.go`,
`docker-compose.yml`, `.github/workflows/load-smoke.yml`, `tests/load/lib.sh`,
`docs/operator/deploy.md`, `docs/SECURITY.md`, `plans/backlog.md`.

## Decisions

- The new guard found three Docker Hub images CI starts: compose `httpbin` and
  `prometheus` (load-smoke) and `docker pull grafana/k6`. All three exist on
  the mirror (checked with `docker manifest inspect`), so they move there
  instead of becoming exceptions. `tests/load/lib.sh` gets the same k6 default.
- Compose images outside what CI starts (grafana, tempo, loki, otel, mailpit,
  ...) stay as they are; the guard follows the services workflows name.
- A bare `docker compose up` (no service names) counts as all services.

## Verify

`go test -count=1 -run 'Docker|Mirror|Workflow|Compose'`, vet, golangci-lint,
`docker compose config -q`, `make lint-connector`, `make lint-tenant`; the
lookalike case run against the old `FROM` rule.
