# #381: the docs tell people to copy a `.env.example` that doesn't exist

Backlog item 24, found in #371.

## Problem

`README.md`, `docs/operator/install.md` and `docs/operator/windows.md` start
with `cp .env.example .env` (Windows: `copy`). There is no root
`.env.example`; the only one is `ui/.env.example`, for the Vite dev server.
The first command of the quick start fails.

Two more claims around it are wrong:

- The docs say to set "ENCRYPTION_KEY + passwords" in `.env`. The database
  passwords in `docker-compose.yml` are literal values, not `${VAR}`
  references, so `.env` cannot change them.
- `docs/operator/remote-access.md` says the database password "is in a file
  called `.env.example`". It is in `docker-compose.yml`.

## Choice: add a root `.env.example`

Changing the docs alone would be smaller, but the docs have a real reason to
point at an env file: they want the operator to generate their own
`ENCRYPTION_KEY` and back it up before storing credentials, and the dev key
in compose is the same on every checkout. Compose reads `.env` from the
project directory on its own, so one file sets the key for the API and every
worker at once, which is the thing that's easy to get wrong by hand.

So the file is added, kept to the variables a local operator would change:

| Variable | Why it's there |
| --- | --- |
| `ENCRYPTION_KEY` | the one the docs tell you to set |
| `OAUTH_TOKEN_SERVICE_SECRET` | dev default says "change-me" |
| `ALERTS_WEBHOOK_TOKEN` | must match `infrastructure/alertmanager.yml`; the alert responder needs it and gets an empty default without it |
| `ANTHROPIC_API_KEY`, `RESPONDER_MODE`, `RESPONDER_TENANT_KEYS` | the opt-in `responder` profile needs them |

Left out: `OTEL_EXPORTER_OTLP_ENDPOINT` and `PROMETHEUS_URL` (internal
service addresses), `HOME`, `UID`, `GID` (come from the shell),
`VRSKY_BIND` (read by `docker-compose.remote.yml`, documented with its
command in `remote-access.md`).

`HOME` stays out on Windows too. Compose mounts `${HOME:-./data/input}` at
`${HOME:-/data/input}`, the same path on both sides, and the Linux engine
rejects a Windows path like `C:/Users/you` as a mount target. With `HOME`
unset (PowerShell's default) the file connectors use the repo's
`data/input`. `windows.md` used to say to set `HOME` in `.env`; that advice
is removed here and a proper Windows home mount goes to the backlog.

Values are empty or the compose dev default. Every reference uses `:-`, which
treats an empty value as unset, so a fresh copy of the file starts the same
stack as no `.env` at all. No real secret goes in the file.

## Acceptance criteria

1. A root `.env.example` exists, is tracked (not matched by `.gitignore`),
   and holds no real secret: values are empty or dev placeholders.
2. Every variable it sets is read by `docker-compose.yml` as `${NAME...}`.
3. `docker compose --env-file .env.example config -q` passes, and the
   resolved config with it equals the one without it (default profile; under
   `--profile responder` the responder's `ALERTS_WEBHOOK_TOKEN` goes from
   empty to the dev token, on purpose).
4. The docs no longer say `.env` sets the database passwords;
   `remote-access.md` points at `docker-compose.yml` for them.
5. A guard test in `src/pkg/managementapi/` fails if any doc (README.md,
   `docs/**/*.md`) tells people to copy a root `.env.example` that doesn't
   exist, and fails if `.env.example` names a variable `docker-compose.yml`
   doesn't read. Both checked by mutation.
6. `mkdocs build --strict` passes.
7. Backlog item 24 struck with "(#381)".

## Files

- `.env.example` (new)
- `README.md`, `docs/operator/install.md`, `docs/operator/windows.md`,
  `docs/operator/remote-access.md`
- `src/pkg/managementapi/envexample_guard_test.go` (new)
- `plans/backlog.md`

## Risks

- Someone sets `ENCRYPTION_KEY` after storing secrets under the dev key:
  those secrets stop decrypting. The file's comment and the docs say to set
  it before storing real credentials.
- `.env` is git-ignored already (`.gitignore` line `.env`); `.env.example`
  matches none of the `.env*` rules. The root `.dockerignore` is an
  allowlist, so neither file goes into an image build context.
- Setting `ALERTS_WEBHOOK_TOKEN` to its dev default changes nothing for the
  API; for the alert responder it replaces an empty value that made it
  refuse to start.

## Verification

- `docker compose --env-file .env.example config -q`, and a diff of
  `config` with and without it.
- From `src/`: `go test -count=1 ./pkg/managementapi/...`, `go vet ./...`,
  `golangci-lint run ./pkg/managementapi/...`, `gofmt -l`.
- Mutation: delete `.env.example` (test fails), add a variable compose
  doesn't read (test fails), restore.
- `mkdocs build --strict` in a scratch venv outside the repo.
