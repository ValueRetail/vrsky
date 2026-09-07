# HTTP & Webhooks

The HTTP connector (`config.type: "http"`) ingests inbound webhooks as a source and makes outbound HTTP calls as a destination.

## As a source (consumer)

A consumer node exposes an inbound **webhook ingress**. After deploy, the connection accepts POST requests at `<webhook-ingress>/webhook/{connectionId}`:

- **Deployed** — the same host that serves the UI, e.g. `https://20.251.107.2.sslip.io/webhook/{connectionId}`. The `/webhook` route is served by the `vrsky-webhooks` Ingress (`infrastructure/kubernetes/ingress/webhooks-ingress.yaml`). This URL is stable for as long as the pipeline is deployed and is the one to give a partner.
- **Local compose** — `http://localhost:9100/webhook/{connectionId}`.

The editor's Webhook (HTTP) source panel shows the URL for the environment you are in, once the pipeline is deployed. When developing locally you can also start a cloudflared quick tunnel from that panel to get a temporary public URL; it points at your machine and dies with the tunnel, so it is for testing a sender, not for handing out.

!!! warning "`sslip.io` hosts can be blocked by ISP DNS filters"

    While the public host is a `sslip.io` address, some ISP filters (Telenor's
    Nettvern among them) resolve it to a block page. A sender behind one cannot
    deliver, and sees only a TLS certificate error — nothing that points at DNS.
    See [Troubleshooting](../operator/troubleshooting.md) for how to confirm it.
    A real DNS name removes the problem.

You can optionally verify request signatures with HMAC and require client certificates with mutual TLS.

Config reference:

- `type` — `"http"`.
- `http.signature` — optional HMAC signature verification (#67):
    - `header` — request header carrying the signature (e.g. `X-Signature`).
    - `algorithm` — hash algorithm (e.g. `sha256`).
    - `encoding` — signature encoding (e.g. `hex`).
    - `prefix` — optional string prepended to the signature value.
    - `secret_secret_id` — reference to the shared signing secret (minted from the plaintext secret you enter in the editor).
- `tls.client_ca_secret_id` — optional mutual TLS (#89). When set, the connection is reachable on the worker's dedicated TLS port (`9101` locally) and clients must present a certificate signed by this CA.

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

## Notes

- **Secrets.** Credential and key material (signing secret, TLS cert/key, CA) are entered as plaintext in the editor and, at deploy, minted into encrypted tenant secrets and replaced with `<field>_secret_id` references. Stored and example JSON therefore shows only the `_secret_id` form.
- **mTLS port.** Connections using `tls.client_ca_secret_id` listen on the worker's dedicated TLS port (`9101` locally), separate from the standard webhook ingress.
- **Example flow.** A common pattern is webhook (consumer) to HTTP forward (producer): receive a signed webhook, then forward the payload to a downstream service over OAuth or mTLS.
