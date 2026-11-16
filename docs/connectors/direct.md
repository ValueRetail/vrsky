# Published into VRSky

A source of type `direct` is a pipeline input that another system writes into
itself. The publisher puts finished VRSky envelopes on the main JetStream
stream, and the pipeline's filters, converters and destination take them from
there. VRSky runs no connector for this source.

The first publisher is Bifrost's Basket service (Bifrost ADR 0002); the
[Bifrost sales → Retailhero](bifrost-sales-to-retailhero.md) template starts
with this source.

```json
{
  "type": "direct"
}
```

There are no settings.

## What the publisher sends

| | |
|---|---|
| Stream | `VRSKY_DATA` |
| Subject | `vrsky.data.<workspace id>.pipeline.<connection id>` |
| Envelope `tenant_id` | the workspace id |
| Envelope `integration_id` | the connection id |
| Envelope `payload` | the message, base64 in the JSON envelope (`src/pkg/envelope`) |
| Envelope `metadata._last_processed_by` | absent or empty: the message has not been through any node yet |

The builder shows the subject on the source's panel once the pipeline is
deployed, and the onboarding wizard shows the connection id and the workspace
id after it creates a pipeline from a template with this source.

`tenant_id` has to be the workspace that owns the connection: the destination
resolves the pipeline's stored secrets under the envelope's tenant, so with
another id it cannot read its credentials.

Give each message a stable `Nats-Msg-Id` (Basket uses the event id).
`VRSKY_DATA` drops a repeat inside its five-minute duplicate window. The
destination's own idempotency (an operation id, a dedupe field) is what holds
after that.

A payload over 256 KiB goes through the claim check (`payload_ref`), as for
any other source.

## Starting and stopping

Start checks the pipeline and marks it running; there is nothing to launch and
nothing it waits for. A message whose connection does not exist, or has no
node fed by the source, is acknowledged and dropped by the workers.

Stopping the connection does **not** stop the flow. The filters, converters
and destinations process whatever arrives on the subject, whatever the
connection's status; with other sources, stop works by stopping VRSky's own
connector, and here there is none. To stop deliveries, stop the publisher (for
Basket, stop its VRSky publishing) or delete the connection.

## Notes

- **No sample data.** VRSky keeps a preview of a source's last message only
  when one of its own connectors read it, so "Load structure" and "Fetch
  sample" have nothing to show for this source. Type field paths by hand, or
  copy them from the publisher's documentation.
- **Who may publish.** VRSky does not check who wrote a message, and the
  workers find the connection by `integration_id` without checking that the
  envelope's tenant owns it. Give a publisher NATS credentials for its own
  workspace's subjects (`vrsky.data.<workspace id>.>`) and nothing more, and
  treat those credentials as write access to that workspace's pipelines.
- **Failed messages** are retried and dead-lettered by the node that failed,
  as with every source
  ([which failed messages reach the DLQ](../operator/troubleshooting.md#which-failed-messages-reach-the-dlq)).
