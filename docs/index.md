# VRSky Documentation

VRSky is a multi-tenant integration platform (iPaaS): tenants build **pipelines**
that move data from a **source** (consumer) through optional **filters** and
**converters** to a **destination** (producer). Data flows through NATS
JetStream; large payloads spill to object storage.

## Where to start

| You are… | Start here |
|----------|------------|
| **New** and want a pipeline running | [Build your first pipeline](tutorials/first-pipeline.md) (≤10 min) |
| **Integrating** a specific system | [Connectors](connectors/index.md) |
| **Operating** VRSky in production | [Operator guide](operator/install.md) |
| **Building a connector** | [Build your first connector](sdk/tutorial/first-connector.md) (≤30 min) |
| **Calling the API** | [API reference](reference/api.md) |
| **Evaluating security** | [Security whitepaper](security/whitepaper.md) |

## Core concepts

- **Connection / pipeline** — a graph of nodes (`consumer` → optional
  `filter`/`converter` → `producer`) plus the edges between them. Created in the
  visual builder or the onboarding wizard, deployed as a running set of workers.
- **Connector** — a worker implementing one node kind, keyed by `config.type`
  (e.g. `http`, `database`, `kafka`). See [Connectors](connectors/index.md).
- **Tenant / workspace** — the isolation boundary. Every resource is scoped to a
  tenant; see the [tenant model](TENANT_MODEL.md).
- **Secrets** — credentials are stored encrypted and referenced as
  `<field>_secret_id`; workers resolve them at runtime. See the
  [security whitepaper](security/whitepaper.md).

## Platform reference

Architecture and internals — [NATS architecture](NATS_ARCHITECTURE.md),
[observability](OBSERVABILITY.md), [tracing](TRACING.md),
[load benchmarks](LOAD.md), [OAuth framework](oauth-framework.md),
[connector SDK](sdk/README.md), [scalability](scalability.md).

Design decisions — [ADR 0001: streaming payload contract](adr/0001-streaming-payload-contract.md),
[ADR 0002: large payloads through transforms](adr/0002-transform-large-payloads.md),
[ADR 0003: transform input formats](adr/0003-transform-input-formats.md).
