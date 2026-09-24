# Route the file manager through the management API (and close the tenant hole behind it)

## Open questions

1. **Orphaned pre-tenant files.** `/data/output/Pipeline 10_58_50/test.csv` sits
   directly under the output root with no tenant segment — written before
   `tenantpath` existed. After this change nothing can reach it through the UI.
   Leave it, sweep it into a tenant, or delete it? Needs a look at what else is
   up there first.
2. **Role for delete.** Proposed `editor`. `DELETE /connections/{id}` is
   `adminMW`, but that destroys a pipeline; this destroys produced output a
   pipeline can regenerate. Say if you want admin instead.
3. **Issue number.** The comments in `worker_events_proxy.go` cite #205 and
   #221 for the same bug class. Worth opening an issue for this one so the
   next occurrence has a chain to follow.

## Goal

The pipeline builder's file manager shows "Load failed" on
https://vrsky.valueretail.no and lists an empty directory, while the pipeline's
output sits on the volume. Fix that — and fix the two problems underneath it,
because the cheap fix for the visible symptom makes one of them much worse.

Three separate defects, one shape:

**(a) Unreachable.** The browser calls the worker's auxiliary port directly:

```
ui/src/pages/PipelineBuilder.tsx:717   `${config.fileProducerUrl}/files?path=…`   (list)
ui/src/pages/PipelineBuilder.tsx:741   `${config.fileProducerUrl}/files?path=…`   (delete)
ui/src/pages/PipelineBuilder.tsx:599   `${config.fileConsumerUrl}/upload/{id}`    (upload)
```

`ui/src/config/env.ts:50,70` bake those in at `npm run build`, defaulting to
`http://localhost:9900` / `:9200`. The prod build never set them —
`infrastructure/kubernetes/ui/configmap.yaml:11` already warns that `VITE_*`
values do not reach a static nginx bundle. So the prod page asks for
`localhost`, and there is no ingress for either worker regardless. It works in
compose and nowhere else.

**(b) Wrong path.** Writes go through `tenantpath.Resolve` and land in
`/data/output/<tenant>/bc-items`. `handleListFiles`
(`src/cmd/file-producer/main.go:680`) reads the raw `path` query parameter.
Even reachable, it would list an empty directory.

**(c) No tenant boundary.** `handleListFiles` and `handleDeleteFiles` check a
caller-supplied absolute path only against `allowedRoots` (`/data/output`), so
`/data/output/<any-other-tenant>/…` passes. `handleUpload`
(`src/cmd/file-consumer/server.go:87`) has `Access-Control-Allow-Origin: *`, no
auth, and takes any connection ID that happens to be running. In prod
`FILE_PRODUCER_AUTH_TOKEN` is unset, which `authorizedFileRequest` treats as
allow-all, and the namespace has no NetworkPolicies. Anything that can reach
9900/9200 in the cluster can list and **delete** any tenant's files, and
**inject arbitrary content into any tenant's pipeline**.

(c) is contained today only because neither port has an ingress. Fixing (a) by
publishing those ports would put it on the internet.

This is the same defect `worker_events_proxy.go` was written to fix, one layer
further out — its header comment describes the identical failure for the SSE
streams. The fix is the same: proxy through the management API, do not expose
worker ports.

## Approach

### 1. New proxy: `src/pkg/managementapi/files_proxy.go`

Modelled directly on `worker_events_proxy.go`, reusing its `WORKER_ADDR_TEMPLATE`
(already set correctly in prod:
`http://vrsky-%s.vrsky-platform.svc.cluster.local:%d`) and its allowlist
discipline — service name and port come from a fixed map, never from the
request, so the endpoint cannot become an SSRF primitive.

Three handlers, each doing the ownership check that is the whole security model:

```
GET    /api/v1/connections/{id}/files?path=…    viewer   → file-producer:9900
DELETE /api/v1/connections/{id}/files?path=…    editor   → file-producer:9900
POST   /api/v1/connections/{id}/files/upload    editor   → file-consumer:9200
```

Each reads `tenant_id` from the session context, then re-reads the connection id
back out of the database scoped to that tenant — the same
`SELECT id::text FROM connections WHERE id::text = $1 AND tenant_id::text = $2`
pattern, so the value interpolated into the upstream URL is one the database
produced for this tenant. 404 for both missing and foreign, undistinguished.

Registered in `handler.go` beside the worker-events route (~line 933).

### 2. The workers resolve the tenant themselves

The proxy must not simply pass a trusted `X-Tenant-ID` header — anything that
reaches 9900 directly could set one. Instead the proxy forwards
`connection_id`, and the worker derives the tenant from its own database, which
`file-producer` already does at `main.go:228`:

```go
SELECT tenant_id::text FROM connections WHERE id = $1
```

then resolves the requested path with the existing
`tenantpath.Resolve(p.defaultOutputDir, tenantID, requested)` before touching
the filesystem. `ErrEscapesTenantRoot` becomes a 403.

Two independent checks, neither trusting a header: the proxy proves *who is
asking*, the worker proves *where that connection's files live*.

`tenantpath.Resolve` already maps `"/data/output/bc-items"` →
`"/data/output/<tenant>/bc-items"` via its base-prefix strip, so **stored
configs need no migration** and the path shown in the editor stays what people
typed.

`file-consumer`'s `handleUpload` gets the same treatment: look the connection's
tenant up, and keep the existing `getActiveConnection` check.

### 3. Harden the aux endpoints regardless

Defence in depth — the proxy is the boundary, but the workers should not be
open if something else reaches them:

- Require `FILE_PRODUCER_AUTH_TOKEN` / a new `FILE_CONSUMER_AUTH_TOKEN` when
  set, and **set them in prod** (k8s Secret, shared with management-api so the
  proxy can present it). Keep unset-means-open for local compose, but log a
  warning at startup so it is visible rather than silent.
- Replace `Access-Control-Allow-Origin: *` on the upload handler with the same
  origin-matched CORS the files handler already uses.
- Keep the "cannot delete root output directory" guard and extend it to the
  **tenant root**, so a delete cannot wipe a tenant's whole subtree in one call.
- **No ingress for 9900 or 9200.** Stated explicitly here because it is the
  obvious-looking shortcut that reopens everything.

### 4. UI

Delete `fileProducerUrl` and `fileConsumerUrl` from `ui/src/config/env.ts` —
the same removal the five per-worker SSE URLs already got (the comment at
`env.ts:59` records it). Point the three call sites at the same-origin API
routes so the session cookie carries the identity.

## Affected files

| File | Change |
|---|---|
| `src/pkg/managementapi/files_proxy.go` | new — three proxy handlers |
| `src/pkg/managementapi/files_proxy_test.go` | new |
| `src/pkg/managementapi/handler.go` | three routes (~L933) |
| `src/cmd/file-producer/main.go` | list/delete: `connection_id`, tenant lookup, `tenantpath.Resolve`, tenant-root delete guard |
| `src/cmd/file-producer/auth_test.go` | token-required cases |
| `src/cmd/file-consumer/server.go` | upload: tenant lookup, CORS, token |
| `src/cmd/file-consumer/server_test.go` | new/extended |
| `ui/src/config/env.ts` | remove both worker URLs |
| `ui/src/pages/PipelineBuilder.tsx` | three call sites (L599, L717, L741) |
| `infrastructure/kubernetes/…` | auth-token secret for both workers + management-api |
| `docker-compose.yml` | same vars for parity |
| `docs/connectors/file.md`, `docs/operator/troubleshooting.md` | the new paths and the "no ingress" rule |

## Risks

- **The delete path is destructive and now reachable from the UI in prod**,
  where before it was merely broken. The tenant-root guard and the `editor`
  role are what stand in front of it; both need tests, not just review.
- **Compose parity.** Local dev currently works by accident (browser really can
  reach localhost:9900). After this it routes through the API, so
  `WORKER_ADDR_TEMPLATE`'s default `http://%s:%d` must resolve `file-producer`
  and `file-consumer` on the compose network. It should — same as the five SSE
  workers — but it is the thing most likely to break your local flow.
- **Upload semantics.** `getActiveConnection` only matches *running*
  connections. Adding a tenant lookup must not change which uploads are
  accepted, or the file-consumer test panel starts refusing valid ones.
- **Token rollout order.** Set the secret and roll management-api *before* the
  workers start requiring it, or the proxy 401s against its own upstreams.
- Pre-tenant files become unreachable — open question 1.

## How we verify it's done

Unit, mirroring `worker_events_proxy_test.go`:

- lists files for a connection the tenant owns
- 404 for a connection belonging to another tenant (list, delete, upload)
- a `path` outside the tenant root → 403, filesystem untouched
- delete of the tenant root itself → refused
- upload to a foreign connection publishes nothing

Then `make test`, `golangci-lint run`, `gofmt -s -w .`, `make lint-tenant`.

Manual, in TEST before prod, per the usual rule:

1. The file manager on the deployed UI lists the four `*.json` envelopes under
   `bc-items` — the thing that failed today.
2. Delete one through the UI; confirm it is gone from the volume and the other
   three are not.
3. From a scratch pod: `curl` 9900 with another tenant's path → refused, both
   with and without the token.
4. `kubectl get ingress -A | grep -E '9900|9200'` → empty.

Done means the file manager works in prod **and** step 3 refuses.

## Not in scope

- The `d990ce21…` BC pipeline itself — working, leave it running.
- Symlink resolution inside a tenant root; `tenantpath`'s package comment
  already records that as a separate threat with a separate fix.
- Any change to how `tenantpath.Resolve` maps paths. It is correct and tested;
  this plan only makes the read side call it.
