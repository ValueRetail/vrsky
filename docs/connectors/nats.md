# NATS JetStream (external)

The NATS connector (`nats`) reads events from a NATS JetStream server that
VRSky does not run, such as a partner's account. It is a source only. It was
built for Retailhero, which publishes `rh.evt.v1.<tenant>.<owner>.<event-type>`
events and gives each tenant a partner account to read them from.

VRSky's own NATS is internal and is not what this connector talks to.

!!! warning "The first start reads the whole stream"
    When VRSky creates the durable, `deliver_policy` decides where it starts.
    The default, `all`, sends every message the stream still holds into the
    pipeline on the first start: for a stream with months of events, months of
    events. Use `new` to take only what arrives from now on, or
    `by_start_time` to start at a point in time.

## As a source (consumer)

A consumer node binds a durable pull consumer on `stream` and turns each
message into a pipeline message.

Config bullets:

- `servers`: one or more server URLs (at most 16), `tls://host:4222` or
  `nats://host:4222`, one per entry. No user name, password, path, query or
  comma in a URL.
- `stream`: the JetStream stream to read.
- `durable`: the durable consumer name. If it does not exist, a start of the
  pipeline creates it (pull, explicit ack, `deliver_policy`, AckWait 30 s,
  unlimited redeliveries). Use one durable per connection: two connections
  on one durable split its messages between them. An existing durable is
  used as it is and never changed. If it has a
  MaxDeliver limit, the connector logs a warning: a connection loss or a
  crash spends a delivery, and the server drops a message once the limit is
  reached. Ask for MaxDeliver -1.
- `filter_subject` (optional): the subject filter, e.g. `rh.evt.v1.acme.>`.
  Used when VRSky creates the durable. For an existing durable it must match
  the durable's own filter, or the start fails.
- `auth_type`: `none`, `creds` (a `.creds` file with the user JWT and NKey
  seed, as issued for a NATS account) or `user_password`.
- `username`, plus `password_secret_id` or `creds_secret_id`: the credentials,
  always as stored secrets.
- `ca_cert` (optional): a PEM CA certificate, for a server whose certificate is
  not signed by a public CA.
- `deliver_policy` (optional): where a durable VRSky creates starts. `all`
  (default) is the whole stream, `new` only what arrives after the durable
  exists, `by_start_time` the messages from `opt_start_time` (RFC 3339, e.g.
  `2026-11-01T00:00:00Z`). It does nothing for an existing durable.
- `batch_size` (optional): the most messages one pull takes, default 10, at
  most 256. The connector lowers it so that one pull holds at most 32 MiB:
  the batch × the largest message the server (or the stream's
  MaxMsgSize) allows. On a server with 1 MB messages that is 32; with 64 MB
  messages, one at a time. It also stays within a durable's
  MaxRequestBatch and MaxRequestExpires.

```json
{
  "type": "nats",
  "nats": {
    "servers": ["tls://nats.retailhero.example:4222"],
    "auth_type": "creds",
    "creds_secret_id": "<secret id>",
    "stream": "RH_EVENTS",
    "durable": "vrsky-acme-prices",
    "filter_subject": "rh.evt.v1.acme.>",
    "deliver_policy": "new"
  }
}
```

## Delivery

- **At least once, no duplicates in the pipeline.** A message is acked on the
  external server only after VRSky's own JetStream has acknowledged the
  publish into the pipeline. The envelope id is derived from the connection
  and the message's `Nats-Msg-Id` (from the stream name and sequence when the
  publisher set none), so a redelivery publishes the same id and VRSky's
  JetStream drops it. The key is marked as either kind (`id:` or `seq:`), so
  a Msg-Id that looks like a stream and sequence cannot collide with the
  fallback. A publisher that reuses one Msg-Id for two different messages
  within five minutes gets only the first into the pipeline.
- **The dedup window is five minutes.** A message redelivered later than that
  after its first publish reaches the pipeline again. That needs a crash in
  the moment between VRSky's publish and the ack, followed by more than five
  minutes of downtime.
- **Order.** Messages are forwarded one at a time in the order the durable
  delivers them. When a publish into the pipeline fails, the same message is
  tried again (after 1 s, doubling up to 30 s or half the durable's AckWait,
  whichever is less) and nothing behind it goes first. Meanwhile the held
  messages are kept in progress on the server, so no delivery is spent. On a
  stop, held messages are handed back at once. After a crash or a lost
  connection they come back when their AckWait runs out, and newer messages
  can be delivered before them in that time.
- **Envelope.** The payload is the message body. The content type comes from
  a `Content-Type` header, else JSON when the body starts with `{` or `[`.
  Metadata: `nats_subject`, `nats_stream`, `nats_stream_seq`, and
  `nats_msg_id` when the message had one.

## Errors

- A publish into the pipeline that gets no answer within 30 s (VRSky's
  stream without a leader, say) counts as failed and is retried as above.
- A lost connection, a server restart or a timeout (a TLS handshake timeout
  included) is retried: the connector redials after 1 s, doubling up to
  30 s, and the pipeline stays `running`. A dead connection is noticed
  within about a minute (pings every 20 s).
- These stop the pipeline with status `error` and the reason in `last_error`:
  credentials the server refuses, a stream that does not exist, JetStream not
  enabled for the account, a permission violation (including a user that may
  not ack: without acks every message would come back each AckWait and be
  published again), a certificate that cannot
  be verified, a server without TLS, a server address that is private, a
  durable that does not fit (a push consumer, not explicit ack, another
  filter), and a message too large for VRSky's NATS. Fix the cause and start
  the pipeline again.
- **A deleted durable.** Only a start of the pipeline creates the durable. If
  it disappears from the server while the pipeline runs, or while VRSky
  restarts (a deploy, the cluster's morning start), the pipeline stops with
  status `error`. Stopping and starting it creates the durable anew from
  `deliver_policy`, which with `all` delivers the whole stream again:
  everything older than five minutes reaches the pipeline a second time.
  Have the durable recreated on the server at the right position first, or
  choose `new` or `by_start_time` before the start.

## Notes

- **Secrets.** The password or `.creds` file typed in the builder is minted
  into an encrypted tenant secret at deploy and stored as `password_secret_id`
  or `creds_secret_id`. A stored config with either in plaintext is refused at
  start. Credentials are never logged.
- **TLS and addresses.** TLS is required. The URLs must not point at a
  private, loopback or link-local address, and the check is repeated on the
  address a host name resolves to. Servers that a cluster announces are not
  dialled; list every server you want in `servers`. The operator's
  `ALLOW_PRIVATE_UPSTREAMS` switch lifts both rules for local development.
- **Test connection.** The button connects, looks up the stream and the
  durable, and disconnects. It creates nothing. A server it cannot reach is
  reported without its address.
- **Permissions.** The NATS user needs the JetStream API for the stream
  (`$JS.API.STREAM.INFO.<stream>`, `$JS.API.CONSUMER.INFO.<stream>.<durable>`,
  `$JS.API.CONSUMER.MSG.NEXT.<stream>.<durable>`), acks (`$JS.ACK.>`),
  subscribe on `_INBOX.>` for the replies, and
  `$JS.API.CONSUMER.CREATE.<stream>.>` if VRSky is to create the durable.
  An account without the create permission needs the durable made for it.
