# ADR 0001 — Connector SDK package structure

Status: accepted (PR 1/3 of #83)

## Context

Workers under `src/cmd/` each re-implement NATS/JetStream wiring, config
loading, health server, signal handling and lifecycle (~250 boilerplate lines
apiece), and there's no way to unit-test a connector without docker-compose.
We want one stable contract + runtime that future connectors build on.

Much of what an SDK needs already exists internally: `pkg/component`
(Component/Producer/Input/Output interfaces), `pkg/messaging` (durable
subscribe + DLQ), `pkg/health`, `pkg/runtime`, `pkg/crypto`, `pkg/envelope`.

> Note (2026-08-27): `pkg/runtime` has since been deleted. It read the
> per-connection worker env contract (`INPUT_NATS_SUBJECT` and friends), which
> went away with the workers themselves — see ADR 0004. The SDK never depended
> on it.

## Decision

1. **`pkg/sdk` is a thin public surface that composes the internal packages**,
   not a rewrite. It adds what was missing — a `Consumer` interface, embeddable
   `Base*` structs, the `Run*` runner, typed errors, and a testing harness —
   and re-exports the common types (`Envelope`, `HealthStatus`). The internal
   packages stay intact; the orchestrator and existing workers are untouched.
   Rejected: folding everything into `pkg/component` (would turn a tiny
   interface package into a heavy runtime) or deprecating it (high-risk
   flag-day rename for no user value).

2. **The harness is a subpackage (`pkg/sdk/harness`)** so its heavy test-only
   dependency (the embedded `nats-server`) never enters the SDK's production
   build. The harness drives connectors through the public `Run*` API with
   injected NATS/DB and the health server disabled (`WithNATSConn`, `WithDB`,
   `WithoutHealthServer`).

3. **Config model: `Configure(ctx, *Resources)`, not `runtime.Config`.** The
   `runtime.Config` package mandates orchestrator-injected env vars
   (`TENANT_ID`, `CONNECTION_ID`, `NODE_ID`, subjects) that fleet-style workers
   — file-producer, http-producer, db-producer, the consumers — do not have.
   Those workers subscribe to all of `vrsky.data.>` with a service-named
   durable consumer and look up per-connection config from the database. So the
   SDK runner does **not** call `runtime.Config.Validate()`; it provides NATS +
   DB + health + lifecycle and hands the connector a `Resources` struct, leaving
   the connector to read whatever env/DB it needs in `Configure`. This is a
   deliberate deviation from the early plan sketch (which assumed
   `runtime.Config`); it's what made refactoring a real fleet worker possible.
   Orchestrator-style single-node workers (`data-filter`, `data-converter`)
   keep using `runtime.Config` directly for now; unifying the two models is a
   future concern.

## Consequences

- New connectors implement one interface + `Configure` and call one `Run*`.
- Connectors are unit-testable without Docker via the harness.
- `connectNATS` / `openDB` (production dialing) are integration code, lightly
  covered by unit tests; the harness exercises the rest.
- Filter/Converter runners exist and are tested via the harness, but no
  production filter/converter has been refactored onto them yet (PRs #2/#3).

## Addendum — PR 2/3 and 3/3

Two small, additive runner capabilities were added while refactoring real
workers; neither changes the contract for connectors that don't need them:

1. **`run()` calls a connector's `Stop(ctx)` on graceful shutdown** (after the
   subscription/ingestion loop drains). `Base*` ship a no-op `Stop`, so this is
   inert unless overridden. `db-producer` and `db-consumer` use it to close the
   per-target/source SQL pools they open at runtime.

2. **`Resources.NATS` exposes the live connection.** Fleet-style *consumers* are
   command-driven: they subscribe to `vrsky.commands.*.connection.{start,stop}`
   to start/stop per-connection ingestion, and some (`tenant-consumer`) run
   their own durable JetStream subscriptions. That genuinely needs the raw
   connection — the injected `publish` closure (the one data-emit path) is not
   sufficient. Producers/filters/converters and simple consumers ignore it.
   Rejected alternative: a second NATS connection per consumer (wasteful, and
   the runner already holds one).

`cmd/example-connector` is the reference Consumer. The orchestrator-style
`cmd/producer`/`cmd/postgres-producer`/`cmd/postgres-consumer` remain on
`runtime.Config`/`pkg/io` and are out of scope. The webhook/api/file consumers
were deferred to a follow-up issue to keep live-ingestion risk bounded.
