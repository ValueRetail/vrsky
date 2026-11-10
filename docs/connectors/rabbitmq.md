# RabbitMQ

The RabbitMQ connector (`rabbitmq`) consumes from and publishes to a RabbitMQ
broker over AMQP.

## As a source (consumer)

A consumer node subscribes to `queue` and turns each delivered message into a
pipeline message.

Config bullets:

- `url` — AMQP connection URL (e.g. `amqp://host:5672`).
- `queue` — the queue to consume from.

```json
{
  "type": "rabbitmq",
  "rabbitmq": {
    "url": "amqp://host:5672",
    "password_secret_id": "<field>_secret_id",
    "queue": "events"
  }
}
```

## As a destination (producer)

A producer node publishes each incoming message to `exchange` using
`routing_key`. Leave `exchange` empty to publish to the default exchange,
where `routing_key` names the target queue directly.

Config bullets:

- `url` — AMQP connection URL.
- `exchange` — target exchange (empty string = default exchange).
- `routing_key` — routing key for the publish.

```json
{
  "type": "rabbitmq",
  "rabbitmq": {
    "url": "amqp://host:5672",
    "password_secret_id": "<field>_secret_id",
    "exchange": "events",
    "routing_key": "orders.created"
  }
}
```

## Notes

- **Secrets.** The broker password typed in the UI is minted into an encrypted
  tenant secret at deploy and replaced with `password_secret_id`. Supply the
  password separately rather than embedding credentials in `url`.
- **Test connection.** The RabbitMQ worker exposes a `/test-connection`
  endpoint — use the **Test connection** button to verify the broker is
  reachable before deploying.
- **Failed deliveries.** A destination without `url`, or with neither
  `exchange` nor a queue, logs an error and fails each message, so it is
  retried and then dead-lettered with the error that names what is missing.
  Fix the node, wait 5 minutes (the worker keeps a connection's config that
  long) and retry the messages from the DLQ (#734). A failure to connect or to
  publish is retried and then dead-lettered too.
  Duplicates: when one node of a fan-out has a config error, the other nodes of that producer get the message again on each attempt (5 in all) and again on a DLQ replay, because this producer keeps no record of which node took it. Fix the node quickly or expect duplicates (skip set for these four: #758).
  See the [operator troubleshooting guide](../operator/troubleshooting.md#which-failed-messages-reach-the-dlq).
- Acknowledgement mode, prefetch, and durability settings are configured via
  the in-app pipeline editor (Property panel).
