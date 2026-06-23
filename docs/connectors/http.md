# HTTP & Webhooks

The HTTP connector (`config.type: "http"`) ingests inbound webhooks as a source and makes outbound HTTP calls as a destination.

## As a source (consumer)

A consumer node exposes an inbound **webhook ingress**. After deploy, the connection accepts POST requests at `http://<webhook-ingress>/webhook/{connectionId}` (locally `http://localhost:9100/webhook/{connectionId}`). You can optionally verify request signatures with HMAC and require client certificates with mutual TLS.

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
