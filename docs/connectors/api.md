# REST API polling

The API connector (`config.type: "api"`) is a source-only connector that polls a REST endpoint on a schedule and emits each response into the pipeline.

## As a source (consumer)

The api-consumer calls each configured endpoint on a fixed interval. It sets a `User-Agent` header automatically and supports OAuth bearer tokens, retrying a request once on a `401` response (to recover from an expired token).

Config reference (the `api` block):

- `base_url` — scheme and host of the API, without the path (e.g. `https://api.met.no`).
- `endpoints` — a list; each has `path` (appended to `base_url`, or a full `http(s)://` URL used as it is), `params` (query string), `auth_type` (`none`, `bearer`, `api_key` or `oauth`), the secret for bearer and API key, and `oauth_grant_id` for `oauth`. With no endpoint the consumer calls `/`.
- `poll_interval_seconds` — poll cadence.
- `one_time_only` — fetch once and stop.

Additional request options are set via the in-app pipeline editor (Property panel).

```json
{
  "type": "api",
  "api": {
    "base_url": "https://api.met.no",
    "endpoints": [
      {
        "path": "/weatherapi/locationforecast/2.0/compact",
        "params": "lat=51.5&lon=-0.1",
        "auth_type": "none"
      }
    ],
    "poll_interval_seconds": 60
  }
}
```

### Which addresses it will call

`base_url` and every endpoint path come from the node, and the request carries
the node's credential. So the consumer only calls addresses outside the cluster
and the cloud platform:

- It will not connect to loopback, private (10/8, 172.16/12, 192.168/16,
  fc00::/7), link-local (169.254/16, which includes the cloud metadata address
  169.254.169.254, and fe80::/10), carrier-grade NAT (100.64/10), unspecified or
  multicast addresses, or to the Azure platform address 168.63.129.16. This is
  checked on the address it actually connects to, after the name is resolved,
  so a hostname that points at one of these is refused too. It applies to
  polls, the retry after a `401`, and the editor's data preview.
- Both `http` and `https` are allowed. Many public APIs only speak `http`, and
  the scheme does not change which address is reached. An `http` endpoint sends
  its token or key in clear text, so use `https` wherever the API offers it.
- Redirects are followed only when they stay on the same host (and port; an
  `http` to `https` upgrade on the default ports counts), never from `https`
  back to `http`, at most five. A redirect to another host is an error, so the
  token or key is never sent somewhere the node did not name.
- Proxy environment variables are ignored.
- A node whose `base_url` or endpoint is not an absolute `http(s)` URL, or is a
  literal private address, is refused when the connection is started, with the
  reason in the error.

**Local development with a mock API.** The operator (not the node) can set
`ALLOW_PRIVATE_UPSTREAMS=true` on `api-consumer` and `management-api`. That
allows private and loopback addresses. Never set it in production; it is off in
`docker-compose.yml` and the Kubernetes manifests. Compose lists the environment
per service, so add

```yaml
      ALLOW_PRIVATE_UPSTREAMS: "true"
```

to the `environment:` block of `api-consumer` and `management-api` in
`docker-compose.yml` (or a local override file).

**Existing connections.** After this change is deployed, these stop working:

- A node that points at a private, loopback or link-local address, an in-cluster
  service name that resolves to one (for example `http://management-api:3000`),
  or a host on a private network. A running connection keeps its settings until
  the api-consumer restarts, then does not start; the management API refuses to
  start it. A connection that already polls fails each poll with "connection to
  a private, loopback or link-local address refused".
- An endpoint that answers with a redirect to a different host. The poll fails
  with "redirect to another host refused". Set the endpoint to the final URL.
- A `base_url` that is empty or has no scheme, with a relative path. It never
  worked; it is now refused at start instead of failing every poll.

Public `http` and `https` endpoints, and redirects within one host, keep
working.

## Notes

- **Source only.** There is no API producer; use the [HTTP connector](http.md) to make outbound calls as a destination.
- **Secrets.** OAuth credentials are referenced by `oauth_grant_id` rather than inline tokens. Any secret fields you enter in the editor are minted into encrypted tenant secrets at deploy and replaced with `<field>_secret_id` references.
- **Auth retry.** With `auth_type: "oauth"`, the consumer retries once on `401`, refreshing the token before giving up.
- **User-Agent.** A `User-Agent` header is set automatically, which satisfies public APIs (like met.no) that reject requests without one.
