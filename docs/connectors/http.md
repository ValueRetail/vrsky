# HTTP & Webhooks

The HTTP connector (`config.type: "http"`) ingests inbound webhooks as a source and makes outbound HTTP calls as a destination.

## As a source (consumer)

A consumer node exposes an inbound **webhook ingress**. After deploy, the connection accepts POST requests at `<webhook-ingress>/webhook/{connectionId}`:

- **Deployed** — the same host that serves the UI, e.g. `https://vrsky.valueretail.no/webhook/{connectionId}`. The older `https://20.251.107.2.sslip.io/...` form still routes, but hand out the real name: see the warning below. The `/webhook` route is served by the `vrsky-webhooks` Ingress (`infrastructure/kubernetes/ingress/webhooks-ingress.yaml`). This URL is stable for as long as the pipeline is deployed and is the one to give a partner.
- **Local compose** — `http://localhost:9100/webhook/{connectionId}`.

The editor's Webhook (HTTP) source panel shows the URL for the environment you are in, once the pipeline is deployed. When developing locally you can also start a cloudflared quick tunnel from that panel to get a temporary public URL; it points at your machine and dies with the tunnel, so it is for testing a sender, not for handing out.

!!! warning "Use the real hostname, not `sslip.io`"

    `vrsky.valueretail.no` is the name to give a partner. The `sslip.io` host
    still routes and is kept so URLs already handed out keep working, but some
    ISP filters (Telenor's Nettvern among them) resolve `sslip.io` to a block
    page: a sender behind one cannot deliver, and sees only a TLS certificate
    error — nothing that points at DNS. See
    [Troubleshooting](../operator/troubleshooting.md).

You can optionally verify request signatures with HMAC and require client certificates with mutual TLS. A connection that requires a client certificate has its own host on the hosted service: see [Mutual TLS](#mutual-tls-client-certificates) below.

### The URL is stable

The connection id in the URL survives Stop/Start, Deploy from the builder
(which updates the connection in place) and restarts of the webhook service
(which brings running pipelines back from the database by itself). Only
**deleting** the connection changes it: the builder then asks before creating
a new one, and says that the URL will change. Keys remembered for the old
connection (below) go with it.

### Retries and the `Idempotency-Key` header

A sender whose outbox delivers *at least once* — a till that lost the `202`,
timed out, or restarted — sends the same request again. If it repeats an
`Idempotency-Key` header (any string up to 64 characters, unique per event;
an outbox entry id is ideal), VRSky treats the repeat as already delivered:

- **First time:** the signature is checked as usual, the message is published,
  the key is remembered — then `202`.
- **Seen before:** nothing is published; `202` again, with
  `Idempotency-Replayed: true` and `"replayed": true` in the body, so the
  sender marks it sent and stops retrying.
- **The publish failed:** `5xx` and the key is *not* remembered, so the retry
  is a real second attempt.

Keys are remembered per connection for 30 days, in the database. A second,
shorter layer catches two identical requests that arrive at the same instant:
the message id on the stream is derived from the key, and JetStream drops a
duplicate id for five minutes. The key is only consulted after the signature
check, so an unsigned request can neither learn that a key exists nor plant
one. A key reused with a different body is treated as a replay — that is what
the header means. Requests without the header behave exactly as before.

Config reference:

- `type` — `"http"`.
- `http.signature` — optional HMAC signature verification (#67):
    - `header` — request header carrying the signature (e.g. `X-Signature`).
    - `algorithm` — hash algorithm (e.g. `sha256`).
    - `encoding` — signature encoding (e.g. `hex`).
    - `prefix` — optional string prepended to the signature value.
    - `secret_secret_id` — reference to the shared signing secret (minted from the plaintext secret you enter in the editor).
- `tls.client_ca_secret_id` — optional mutual TLS (#89, #454). When set, callers must present a client certificate signed by this CA. The URL changes: see [Mutual TLS](#mutual-tls-client-certificates).

Other request handling options not listed here are set via the in-app pipeline editor (Property panel).

```json
{
  "type": "http",
  "tls": {
    "client_ca_secret_id": "sec_clientca_01"
  },
  "http": {
    "signature": {
      "header": "X-Signature",
      "algorithm": "sha256",
      "encoding": "hex",
      "prefix": "",
      "secret_secret_id": "sec_hmac_01"
    }
  }
}
```

### Mutual TLS (client certificates)

With `tls.client_ca_secret_id` set, every request must come with a client
certificate that chains to that CA, is within its validity dates and allows
client authentication. Anything else is refused (`400` from the ingress or
`401` from the webhook service, see below), and nothing is published.

If your client certificates are issued by an intermediate CA, put that
intermediate in `client_ca` (with the root, or on its own). The ingress
passes only the client's own certificate to the webhook service, not the
chain, so a `client_ca` holding only the root refuses them with `401`.
Rejections count in `webhook_client_cert_failures_total{connection_id,reason}`
(`missing_cert`, `untrusted_cert`, `malformed_cert`, `untrusted_edge`).

Where to send the requests:

- **Hosted service** — `https://mtls.vrsky.valueretail.no/webhook/{connectionId}`.
  The ingress checks the certificate in the TLS handshake and passes it to the
  webhook service, which checks it again against this connection's own CA. A
  certificate from another tenant's CA gets through the ingress but not the
  second check. The ingress only asks for certificates from CAs it knows, so
  an operator adds your CA to its list before the first request
  ([operator steps](../operator/webhook-mtls.md)). Until then, and until that
  host is live, the connection refuses every request. The ordinary URL on
  `vrsky.valueretail.no` never works for such a connection: that host doesn't
  ask for certificates.
- **Local compose** — `https://localhost:9101/webhook/{connectionId}`, the
  webhook service's own TLS port. It asks for any client certificate in the
  handshake and checks it against the connection's CA. The server certificate
  is self-signed unless `WEBHOOK_TLS_CERT_FILE` and `_KEY_FILE` are set.

Test with curl:

```bash
curl --cert client.crt --key client.key \
  -H 'Content-Type: application/json' -d '{"ping":true}' \
  https://mtls.vrsky.valueretail.no/webhook/<connectionId>
```

`202` means accepted. A `400` with "No required SSL certificate was sent", or
one about the certificate, comes from the ingress (no certificate, or its CA
isn't on the list). A `401` comes from the webhook service (the certificate
doesn't match this connection's CA, or has expired).

A CA that two tenants both use (a public CA, say) lets each reach the other's
mTLS webhooks; use a CA of your own, or add HMAC signing as well.

## As a destination (producer)

A producer node sends each message as an outbound HTTP request. It supports OAuth-authenticated output and client-certificate mutual TLS.

Config reference:

- `type` — `"http"`.
- `http.url` — target URL.
- `http.method` — HTTP method (e.g. `POST`).
- `http.headers` — object of request headers.
- `http.auth_type` — `"none"` or `"oauth"`.
- `http.oauth_grant_id` — OAuth grant UUID (#97), required when `auth_type` is `"oauth"`.
- `http.tls` — optional client-certificate mTLS:
    - `cert_secret_id` — client certificate.
    - `key_secret_id` — client private key.
    - `client_ca_secret_id` — CA to validate the server.

```json
{
  "type": "http",
  "http": {
    "url": "https://example.com/ingest",
    "method": "POST",
    "headers": { "Content-Type": "application/json" },
    "auth_type": "oauth",
    "oauth_grant_id": "a1b2c3d4-0000-1111-2222-333344445555",
    "tls": {
      "cert_secret_id": "sec_clientcert_01",
      "key_secret_id": "sec_clientkey_01",
      "client_ca_secret_id": "sec_serverca_01"
    }
  }
}
```

### Failed deliveries

A target that never answers is cut off after 30 seconds, whether it never
accepts the connection or accepts it and goes quiet. That counts as a network
error: it is retried and dead-lettered like one, and a node that timed out is
not marked as having taken the message (#763). The 30 seconds is fixed; it is
not a node setting.

For a normal (buffered) payload the 30 seconds cover the whole request. A
large payload streamed from object storage (ADR 0001) is held to three limits
instead (#774):

- 30 seconds without progress. Progress means body bytes leaving the producer
  for the network, so a target that stops reading stalls the upload once the
  socket buffers are full.
- 60 seconds after the last byte has left the producer, for what is still in
  the socket buffers to reach the target and for the target to answer.
- An overall limit per attempt: 30 seconds plus the payload size at
  256 KiB/s, and never more than 15 minutes (15 minutes when the size is not
  known). An upload slower than 256 KiB/s on average is cut off here, so a
  target that reads a few KB/s cannot hold the producer, which sends one
  message at a time, for hours. A payload over about 218 MiB gets the full
  15 minutes and needs more than 256 KiB/s to make it: 1 GiB needs about
  1.2 MiB/s, 10 GiB about 11.8 MiB/s.

Hitting any of them is a timeout like the one above: retried, then
dead-lettered. None of them is a node setting.

A `401` or `403` is retried too, so a bad credential means 5 attempts per
message, each sending it. With OAuth every attempt forces a token refresh,
about 5 refreshes per message, so a burst of messages can hit the identity
provider or lock out a basic-auth account. A `403` from a WAF or from
per-record authorization is retried and dead-lettered the same way.

A `5xx`, a `429`, a network error and a failure to get an OAuth token are
retried on the usual backoff and then dead-lettered with the payload. So is a
`401` or `403` (a wrong or expired credential; with OAuth, one still refused
after the one token refresh), and a node the request cannot be built for: no
`url`, a URL the request cannot be built from, or a `tls` certificate or key
that does not parse (nothing is sent to that node then). Fix the node, wait
5 minutes (the worker keeps a connection's config that long), and retry the
message from the DLQ (#734). Any other answer below `500` (so every other
`4xx`: `400`, `404`, `422`, ...) is logged, shown in the activity feed and
acked: the target refused the message itself, and it is dropped, not
dead-lettered. Which errors reach the DLQ for every destination is in the
[operator troubleshooting guide](../operator/troubleshooting.md#which-failed-messages-reach-the-dlq).

**Several HTTP nodes on one connection.** The message is acked only when no
node failed in a way that is retried. When one node fails and another takes
the message, the message is retried for the failing node only: a node that
took it (a `2xx`, or a `4xx` other than `401`/`403`, which is final) is not sent
it again on the next attempts (#730). A node whose request failed on the way (the connection
dropped, a timeout) has not taken it and is tried again. The DLQ copy's last
error names every node that still failed, as `node <node id>: <error>`. That
header is cut at 512 bytes, so with many failing nodes the later ones show
only in the worker's log (`HTTP destination refused`, `HTTP request failed`
or `HTTP request returned error`, with `connection_id` and `node_id`). A retry
from the DLQ skips the nodes that took the message as well.

This memory is kept by the `http-producer` process: one entry per node per
message, up to 10 000 entries, for 7 days (the DLQ's retention). If the
worker restarts (or a rolling deploy hands the message to the new pod)
between two attempts, every node is sent the message again. A node whose `2xx` got lost (a timeout
after the partner had accepted the request) is sent it again too. So a node
receives a message at least once; use `X-Message-ID`, which is the same on
every attempt, to drop a repeat on the receiving side. Keeping the memory
across restarts is tracked for the remote agent in #727.

### Which addresses it will call

The destination URL is yours to set, and the request carries your headers and
OAuth token, so the producer checks where it is going (#660):

- It will not connect to loopback, private (10/8, 172.16/12, 192.168/16,
  fc00::/7), link-local (169.254/16, which includes the cloud metadata address
  169.254.169.254, and fe80::/10), carrier-grade NAT (100.64/10), unspecified or
  multicast addresses, or to the Azure platform address 168.63.129.16. This is
  checked on the address it actually connects to, after the name is resolved,
  so a hostname that points at one of these is refused too. It applies to every
  node, mTLS included: the client certificate and CA are set up on top of the
  same checked connection.
- Both `http` and `https` are allowed. The scheme does not change which
  address is reached, and some partners only speak `http`. An `http` endpoint
  receives your headers and token in clear text, so use `https` wherever the
  partner offers it.
- Redirects are followed only when they stay on the same host (and port; an
  `http` to `https` upgrade on the default ports counts), never from `https`
  back to `http`, at most five. A redirect to another host is an error, so the
  headers and token are never sent somewhere the node did not name.
- Proxy environment variables are ignored.
- A node whose `http.url` is not an absolute `http(s)` URL, or is a literal
  private address, is refused when the connection is started, with the reason
  in the error.
- A delivery that is refused is an ordinary failed delivery: the activity feed
  shows "Destination refused", the message is retried on the usual backoff and
  then lands in the dead-letter queue with its payload, and nothing is sent.
  Fix the URL and replay it from there.

**Local development with a mock endpoint.** The operator (not the node) can set
`ALLOW_PRIVATE_UPSTREAMS=true` on `http-producer` and `management-api`. That
allows private and loopback addresses. Never set it in production; it is off in
the Kubernetes manifests. `docker-compose.yml` turns it on for `http-producer`
only, because the bundled `httpbin` sink (`http://httpbin/post`) is a private
address on the compose network.

**Existing connections.** After this change is deployed, these stop working:

- A node that points at a private, loopback or link-local address, an in-cluster
  service name that resolves to one (for example `http://management-api:3000`),
  or a host on a private network. A running connection keeps running, but each
  delivery to such a node fails with "connection to a private, loopback or
  link-local address refused" (or "Destination refused" for a literal
  address). The management API refuses to start a connection whose URL is a
  literal private address. A hostname that resolves to one is refused when
  delivering.
- An endpoint that answers with a redirect to a different host. The delivery
  fails with "redirect to another host refused". Set the node to the final URL.

Public `http` and `https` endpoints, and redirects within one host, keep
working.

## Notes

- **Secrets.** Credential and key material (signing secret, TLS cert/key, CA) are entered as plaintext in the editor and, at deploy, minted into encrypted tenant secrets and replaced with `<field>_secret_id` references. Stored and example JSON therefore shows only the `_secret_id` form.
- **mTLS host.** Connections using `tls.client_ca_secret_id` take requests on `mtls.vrsky.valueretail.no` on the hosted service and on the worker's TLS port (`9101`) locally, not on the ordinary webhook URL. See [Mutual TLS](#mutual-tls-client-certificates). Outbound mTLS (the producer's `http.tls`) works everywhere.
- **Example flow.** A common pattern is webhook (consumer) to HTTP forward (producer): receive a signed webhook, then forward the payload to a downstream service over OAuth or mTLS.
