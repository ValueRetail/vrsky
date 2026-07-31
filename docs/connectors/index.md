# Connectors

A connector is a worker that implements one node kind in a pipeline. The kind is
set by `config.type` on the node. Sources are **consumers**, destinations are
**producers**; some types support both.

| Connector | Source | Destination | `config.type` |
|-----------|:------:|:-----------:|---------------|
| [HTTP & webhooks](http.md) | ✓ (webhook) | ✓ | `http` |
| [REST API polling](api.md) | ✓ | — | `api` |
| [Databases](database.md) | ✓ | ✓ | `database` |
| [Files](file.md) | ✓ | ✓ | `file` |
| [SFTP](sftp.md) | ✓ | ✓ | `sftp` |
| [Cloud storage](cloud-storage.md) | ✓ | ✓ | `cloud_storage` |
| [Apache Kafka](kafka.md) | ✓ | ✓ | `kafka` |
| [RabbitMQ](rabbitmq.md) | ✓ | ✓ | `rabbitmq` |
| [Salesforce](salesforce.md) | ✓ | ✓ | `salesforce` |
| [Sitoo (POS)](sitoo.md) | ✓ (poll + webhook) | ✓ | `sitoo` |
| [Tenant-to-tenant](tenant.md) | ✓ | — | `tenant` |
| [Filters & converters](filters-converters.md) | — | — | `filter` / `converter` |

## Conventions

- **Config shape** — each node carries `{ "type": "<kind>", "<kind>": { …options… } }`.
- **Credentials** — type them in the editor; at deploy they're minted into
  encrypted tenant secrets and stored as `<field>_secret_id` references (never
  plaintext). Workers resolve them at runtime.
- **Test connection** — most connectors expose a "Test connection" button that
  validates reachability/credentials before you deploy.
- **OAuth** — HTTP, API, and Salesforce connectors can authenticate via an OAuth
  grant (`oauth_grant_id`); set providers up under Settings → OAuth providers.

New connectors are built on the [connector SDK](../sdk/README.md) — see
[Build your first connector](../sdk/tutorial/first-connector.md).
