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
- Acknowledgement mode, prefetch, and durability settings are configured via
  the in-app pipeline editor (Property panel).
