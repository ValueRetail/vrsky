# Apache Kafka

The Kafka connector (`kafka`) consumes from and produces to Kafka topics. It
supports plaintext and several SASL/TLS authentication mechanisms.

| `auth.mechanism` | Fields used |
| ---------------- | ----------- |
| `none`           | — |
| `sasl_plain`     | `username` + `password` |
| `sasl_scram`     | `username` + `password` |
| `mtls`           | `ca_cert`, `client_cert`, `client_key` |

## As a source (consumer)

A consumer node subscribes to `topic` as part of the consumer group named by
`group_id`, so offsets are tracked and load is shared across replicas. Each
record becomes a message.

Config bullets:

- `brokers` — list of bootstrap brokers (e.g. `["host:9092"]`).
- `topic` — the topic to subscribe to.
- `group_id` — consumer group id (consumer only).
- `auth.mechanism` — `none`, `sasl_plain`, `sasl_scram`, or `mtls`.
- `auth.username` — SASL username.
- Secret/cert fields — see Notes.

```json
{
  "type": "kafka",
  "kafka": {
    "brokers": ["broker1:9092", "broker2:9092"],
    "topic": "events",
    "group_id": "vrsky",
    "auth": {
      "mechanism": "sasl_scram",
      "username": "vrsky",
      "password_secret_id": "<field>_secret_id"
    }
  }
}
```

## As a destination (producer)

A producer node writes each incoming message to `topic`. `group_id` is not
used for producers.

Config bullets:

- `brokers` — bootstrap brokers.
- `topic` — the topic to write to.
- `auth` — same options as the consumer.

```json
{
  "type": "kafka",
  "kafka": {
    "brokers": ["broker1:9092"],
    "topic": "events-out",
    "auth": {
      "mechanism": "mtls",
      "ca_cert": "-----BEGIN CERTIFICATE-----...",
      "client_cert_secret_id": "<field>_secret_id",
      "client_key_secret_id": "<field>_secret_id"
    }
  }
}
```

## Notes

- **Secrets.** `password`, `client_cert`, and `client_key` typed in the UI are
  minted into encrypted tenant secrets at deploy and replaced with
  `<field>_secret_id` references. `ca_cert` and `username` are stored as
  plain config.
- **Test connection.** The Kafka worker exposes a `/test-connection`
  endpoint — use the **Test connection** button to verify broker reachability
  and auth before deploying.
- Partitioning, message keys, and other producer/consumer tuning are
  configured via the in-app pipeline editor (Property panel).
