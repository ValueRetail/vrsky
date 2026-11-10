# #454 Inbound webhook mTLS at the edge

Backlog item 59. A webhook connection with `tls.client_ca_secret_id` can't be
reached in production: no manifest sets `WORKER_MTLS_PORT`, and the webhook
Ingress terminates TLS, so the worker never sees a client certificate and
refuses every request.

Ludvik decided on 2026-11-03 (comment on #454): check the client certificate
at the edge with ingress-nginx `auth-tls-*` annotations, with the CA in a
Secret named by `auth-tls-secret`, `auth-tls-verify-client` set, and the
verified certificate passed to the worker in a header. The worker trusts that
header only from the ingress. No SSL passthrough.

Manifests and auth: PR plus `agent:needs-human`. A human applies the
Kubernetes pieces; the deploy job only rolls the connector manifest.

## Open questions for Ludvik

None block the PR. Each has a default the PR follows.

1. **The hostname.** `auth-tls-*` is per host in ingress-nginx (its docs: "It
   is not possible to specify rules that differ for individual paths"), and
   `vrsky.valueretail.no` also serves the UI. Turning client-cert checks on
   there would break the UI. Default: a new host, `mtls.vrsky.valueretail.no`,
   which needs a DNS record to the ingress LoadBalancer (step 1 below).
2. **`on` with a CA bundle, not `optional_no_ca`.** This follows the decision
   literally. The cost: the bundle Secret is maintained by hand, so a tenant
   who adds or rotates a client CA needs an operator to add it before their
   callers get through. `optional_no_ca` would not remove that step: nginx
   still needs a CA Secret and sends its names in the certificate request, and
   Go and Java clients only send a certificate whose issuer is on that list.
   Syncing the bundle from the connections automatically is a follow-up.
3. **The ingress-to-worker hop is mTLS too.** The header carries only the
   public certificate, so anything that can reach the port that trusts it can
   claim to be any caller. I could not check whether the cluster enforces
   NetworkPolicy (AKS needs a policy engine chosen at create time; the repo
   doesn't say, and I may not run `az`). So nginx presents its own client
   certificate to the worker (`proxy-ssl-secret`), from a private CA that
   cert-manager creates, and the worker trusts the header only on a TLS
   connection that presented that certificate. This is not passthrough: nginx
   still terminates the caller's TLS. It does add a worker port (9102). A
   NetworkPolicy for that port is in the PR as a second layer, optional.
4. **The direct-TLS mode (`WORKER_MTLS_PORT`) stays** for Docker Compose and
   tests. Removing it would change the compose stack and its docs for no
   production gain. Production never sets it, and a guard test keeps it that
   way.

## Design

```
caller --TLS + client cert--> ingress-nginx (host mtls.vrsky.valueretail.no)
           auth-tls-verify-client: on, against Secret webhook-client-ca
           sets ssl-client-cert / ssl-client-verify (replacing any the caller sent)
       --TLS, nginx presents webhook-edge-client--> webhook-consumer :9102
           RequireAndVerifyClientCert against the edge CA
           handler: parse ssl-client-cert, verify against THIS connection's CA
```

- **Edge.** A new Ingress `vrsky-webhooks-mtls` on its own host, path
  `/webhook` only, backend `vrsky-webhook-consumer:9102`. Annotations:
  `auth-tls-secret: vrsky-platform/webhook-client-ca`,
  `auth-tls-verify-client: "on"`, `auth-tls-verify-depth: "2"` (one
  intermediate), `auth-tls-pass-certificate-to-upstream: "true"`,
  `backend-protocol: HTTPS`, `proxy-ssl-secret:
  vrsky-platform/webhook-edge-client`, `proxy-ssl-verify: "on"`,
  `proxy-ssl-name: vrsky-webhook-consumer.vrsky-platform.svc`,
  `proxy-ssl-server-name: "on"`. Its own `tls:` block and cert-manager
  certificate, since it is a new host. No snippets.
- **Header stripping.** For a location with client-cert auth, ingress-nginx
  writes `proxy_set_header ssl-client-cert $ssl_client_escaped_cert` (and
  `ssl-client-verify`, `-subject-dn`, `-issuer-dn`). `proxy_set_header`
  replaces a caller's header of the same name, and an empty value drops it.
  Headers with underscores are dropped by default (`ignore_invalid_headers`).
  On the main host there is no auth-tls, so nginx passes a caller's
  `ssl-client-cert` through. That is why the main host routes only to 9100,
  where the worker never reads the header.
- **Worker, edge port.** `WORKER_MTLS_EDGE_PORT` (9102) starts a TLS listener
  with `RequireAndVerifyClientCert` against `WEBHOOK_EDGE_CA_FILE` and the
  server identity from `WEBHOOK_EDGE_CERT_FILE` / `_KEY_FILE`, all from the
  cert-manager Secret `webhook-edge-server` (mounted, optional). It serves
  `/webhook/` only. If the port is set and the files are missing or bad, it
  logs an error and does not listen: mTLS connections stay unreachable (as
  today), every other webhook keeps working, and the pod does not crash-loop
  before the human steps are done.
- **Worker, per connection.** For a connection with a client CA, a request on
  the edge port must:
  1. have arrived over TLS with a verified ingress certificate (checked again
     in the handler, not only at the handshake);
  2. carry `ssl-client-verify: SUCCESS`, exactly once;
  3. carry exactly one `ssl-client-cert`, which URL-decodes (`PathUnescape`,
     so `+` stays `+`) to exactly one PEM `CERTIFICATE` block and nothing
     else, and parses as X.509;
  4. verify against the connection's own CA: chain, validity dates, client
     auth EKU (`tlsconfig.VerifyClientCert`).
  Anything else is 401. `ssl-client-verify: SUCCESS` alone is never enough.
  A connection without a client CA works on the edge port as on 9100.
- **Worker, other ports.** 9100 and the direct-TLS port never read the
  header. On 9100 an mTLS connection is still refused (no TLS).
- **Logging.** Subject and SHA-256 fingerprint only, never the certificate.
- **Metrics.** `webhook_client_cert_failures_total` gains reasons
  `malformed_cert` and `untrusted_edge` next to `missing_cert` and
  `untrusted_cert`.
- **Cross-tenant.** Tenant A's certificate passes the edge (its CA is in the
  bundle) but fails on tenant B's connection, because step 4 uses B's CA.
  Two tenants who configure the same CA can reach each other's webhooks;
  that was already true of the direct-TLS mode. Pinning a subject or
  fingerprint per connection would close it and is a follow-up.

## Acceptance

- Ingress, cert-manager resources, example CA Secret and NetworkPolicy are in
  the repo; nothing in them holds real CA material.
- Guard tests pin: the mTLS Ingress annotations and that it has no snippet;
  that it routes only `/webhook` to 9102; that no other Ingress routes to
  9102; that the main webhook Ingress routes the webhook consumer to 9100;
  the edge certificate usages and names; the Deployment's edge env, mount and
  Service port; that production doesn't set `WORKER_MTLS_PORT`; the
  NetworkPolicy's 9102 rule.
- Worker tests (table-driven): a valid certificate for this connection's CA
  passes; another tenant's CA is refused; an expired certificate is refused; a
  malformed header is refused; a header on a non-edge port, or on the edge
  handler without the ingress certificate, is refused; a missing certificate
  is refused. A real TLS listener test: the ingress certificate gets through,
  no certificate or a foreign one fails the handshake.
- Each security check is broken once (mutation), a test fails, and it is
  restored.
- Docs: `docs/connectors/http.md` (inbound mTLS only, #773 edits the same
  file), whitepaper §4, `docs/SECURITY.md`, an operator page with the rollout.

## Files

- `src/cmd/webhook-consumer/service.go`, `server.go`, `metrics.go`; new
  `mtls_edge_test.go`.
- `src/pkg/tlsconfig/tlsconfig.go` (+ test): `EdgeServerConfig`,
  `ParseForwardedCert`, `Fingerprint`.
- `infrastructure/azure/deploy-connectors-azure.sh` and the generated
  `infrastructure/kubernetes/connectors/connectors.yaml`.
- New, applied by hand: `infrastructure/kubernetes/ingress/webhooks-mtls-ingress.yaml`,
  `webhook-edge-tls.yaml`, `webhook-client-ca.example.yaml`,
  `webhook-consumer-networkpolicy.yaml`.
- New guard test `src/pkg/managementapi/webhook_mtls_edge_guard_test.go`.
- Docs listed above, new `docs/operator/webhook-mtls.md`, `plans/backlog.md`.

## Risks

- The merge changes the connector manifest, so the deploy job redeploys the
  whole connector fleet (rolling restarts, as for any manifest change). The
  webhook consumer gains an env block, a port and an optional Secret mount.
  Until the human steps run, the edge listener logs an error and stays off.
- If `auth-tls-secret` is missing, ingress-nginx denies the location (503)
  rather than skipping the check. I read that from its source, not a
  cluster. Even if it skipped the check and passed a forged header, the
  worker would still verify that certificate against the connection's CA;
  what the caller could not prove is possession of the key. Step 6 of the
  rollout tests this.
- The ingress-nginx version in production is unknown. The annotations used
  exist since 0.2x; `annotations-risk-level` (1.12+) rates them below
  Critical, so the default allows them.
- Certificates renew in place (cert-manager); the worker reads its files at
  start, and pods restart at every cluster start, well inside the 90-day
  renewal window. nginx reloads Secrets by itself.

## Rollout (Ludvik)

In `docs/operator/webhook-mtls.md`: DNS, cert-manager resources, CA bundle
Secret, restart the webhook consumer, Ingress, optional NetworkPolicy, curl
tests, rollback. Rollback is deleting the new Ingress; the worker change is
inert without it.

## Verification

`go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race -count=1 ./cmd/webhook-consumer/ ./pkg/tlsconfig/
./pkg/managementapi/...`, `make lint-tenant`, `make lint-connector`, a
mutation run, and a local rehearsal with plain nginx in Docker configured the
way the ingress-nginx template renders these annotations.
