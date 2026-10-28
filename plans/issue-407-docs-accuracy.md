# Issue #407: docs overstate production's TLS and encryption

Backlog items 43, 45, 46 and 47. No open questions; decisions are listed at
the end.

## Goal

Four places describe security controls production doesn't run. Make each say
what production on AKS does, name the optional overlays as optional, and claim
nothing that isn't applied. Docs only: no manifest, script or workflow
changes.

## Claims, evidence, new wording

### 1. management-api README: Traefik and sticky sessions (item 43)

Where: `infrastructure/kubernetes/management-api/README.md:14` ("N≥2 replicas
behind the Service / Traefik gateway") and `:45-52` (sticky sessions for SSE
"at Traefik").

What the repo shows:

| Fact | File:line |
|------|-----------|
| The browser reaches the API through the UI pod's nginx, which proxies `/api/` and `/ws` to the management-api Service | `infrastructure/azure/deploy-ui-azure.sh:17,36-48` |
| The UI is published by the `vrsky` Ingress, class `nginx` | `infrastructure/azure/deploy-ui-azure.sh:86`, `infrastructure/kubernetes/ui/ingress.yaml` |
| Production runs 2 UI replicas | `infrastructure/azure/deploy-ui-azure.sh:74` |
| Production runs 1 management-api replica (the manifest says 2) | `infrastructure/azure/deploy-azure.sh:90`, `infrastructure/kubernetes/management-api/deployment.yaml:16` |
| The CI deploy only swaps images, so it keeps that replica count | `infrastructure/azure/deploy-core-azure.sh:22,134` |

So cookie affinity on the Ingress pins a browser to a UI pod, not to a
management-api replica. `sessionAffinity: ClientIP` on the management-api
Service sees the UI pod's IP, not the browser's. Each hop alone pins nothing
end to end; both together would.

New wording: the manifest default is 2 replicas, production runs 1. Describe
the real path (ingress-nginx, UI nginx, Service). For SSE under more than one
replica: fan out over NATS (the real fix), or as a stopgap pin both hops, with
the cost that one UI pod's users all land on one replica.

### 2. DEPLOYMENT_GUIDE encryption table (item 45)

Where: `docs/DEPLOYMENT_GUIDE.md` "Encryption at rest", table rows "Block
device" and "Object storage", the managed-cloud and MinIO SSE subsections,
"Postgres native encryption", and the compliance checklist.

What the repo shows:

| Fact | File:line |
|------|-----------|
| AKS rewrites every `local-path` PVC to `managed-csi` | `infrastructure/azure/deploy-azure.sh:8,31,71-77` |
| The connectors' shared file volume is `azurefile-csi` | `infrastructure/kubernetes/connectors/connectors.yaml:10`, `infrastructure/azure/deploy-connectors-azure.sh:40,189` |
| Monitoring PVCs on Azure use `managed-csi` | `infrastructure/kubernetes/monitoring/prometheus-values.azure.yaml:20,45`, `grafana-values.azure.yaml:13` |
| Nothing applies `infrastructure/kubernetes/encryption/` (StorageClass `vrsky-encrypted`, KES, MinIO SSE patch) | `git grep -n -E 'encryption/\|kes-deployment\|minio-encrypted\|vrsky-encrypted' -- '*.sh' '*.yml' '*.yaml' Makefile` finds only files inside `infrastructure/kubernetes/encryption/` |
| The MinIO manifests set no KMS | `git grep -n -i -E 'kms\|kes' -- infrastructure/kubernetes/minio` (empty) |

Outside the repo: Azure encrypts every managed disk and every Azure Files share
at rest by default (Azure Storage service-side encryption, platform-managed
keys); it can't be turned off. The built-in `managed-csi` class names no disk
encryption set, so the repo asks for no customer-managed key. Whether someone
added one outside the repo can't be told from here.

New wording: the table says what production has per layer, and calls the
customer-key StorageClass, LUKS and KES "optional overlays, not applied in
production". The subsections are marked optional. The checklist says
production doesn't meet its `vrsky-encrypted` and KES items today.

### 3. Whitepaper §4: inbound webhook mTLS (item 46)

Where: `docs/security/whitepaper.md:37-42`. Also the connector page it links,
`docs/connectors/http.md:66,126`, which gives only the local port.

What the repo shows:

| Fact | File:line |
|------|-----------|
| The mTLS listener runs only when `WORKER_MTLS_PORT` is set | `src/cmd/webhook-consumer/service.go:94,197-207` |
| Only compose sets it | `docker-compose.yml:580`; `git grep WORKER_MTLS_PORT -- infrastructure` (empty) |
| The webhook Ingress routes `/webhook` to the plain port 9100 and has no `ssl-passthrough` or `auth-tls-*` annotations | `infrastructure/kubernetes/ingress/webhooks-ingress.yaml:37-45,94-100` |
| TLS for that host ends at ingress-nginx (`vrsky` Ingress) | `infrastructure/kubernetes/ingress/webhooks-ingress.yaml:22-28` |
| A connection that requires a client cert rejects plain-port requests with 401, so it fails closed | `src/cmd/webhook-consumer/service.go:303-309`, `docs/SECURITY.md:417-424` |
| Inbound HMAC signature checks don't need the listener | `src/cmd/webhook-consumer/service.go:298-301` |
| Outbound client certs need no listener; the producer builds its own client | `docs/SECURITY.md:425-427` |

New wording: outbound mTLS works everywhere. Inbound client-cert checks need
the worker's own TLS port, which self-hosted deployments can expose; the hosted
service doesn't expose it yet. A connection set to require a client cert there
refuses every request rather than accepting one without a cert. Use HMAC
signing for inbound webhooks on the hosted service. The same note goes on the
connector page.

### 4. Encryption checklist: `sslmode=require` (item 47)

Where: `docs/operator/encryption-at-rest.md:18`, and whitepaper §2
(`docs/security/whitepaper.md:26-27`), which cites "TLS to the DB" from it.

What the repo shows:

| Fact | File:line |
|------|-----------|
| The production connection string uses `sslmode=disable` | `infrastructure/azure/rotate-secrets.sh:34` |
| So do the example Secrets | `infrastructure/kubernetes/postgresql/secret.example.yaml:16`, `infrastructure/kubernetes/management-api/secret.example.yaml:14` |
| The Postgres manifests configure no TLS | `git grep -n -i tls -- infrastructure/kubernetes/postgresql` (empty) |

New wording: keep the advice, and say production doesn't do it yet: Postgres
runs in the cluster, traffic to it stays on the cluster network unencrypted.
The whitepaper §2 sentence matches the "Transport & gateway" section, which
already says this.

## Files

- `infrastructure/kubernetes/management-api/README.md` (a README, not a manifest)
- `docs/DEPLOYMENT_GUIDE.md`
- `docs/security/whitepaper.md`
- `docs/operator/encryption-at-rest.md`
- `docs/connectors/http.md`
- `plans/backlog.md`: strike 43, 45, 46, 47; add the two follow-ups below

## Decisions made without asking

- Docs only. Turning on Postgres TLS or exposing the mTLS listener would touch
  manifests and the ingress (PR plus `agent:needs-human`), so both go into the
  backlog as new items instead.
- Whitepaper §2 gains one sentence on disk encryption. It rests on Azure's
  default, not on anything in the repo, and says so by naming Azure.
- `docs/connectors/http.md` isn't one of the four places, but the whitepaper
  links to it for the same promise, so it gets a one-line production note.
- `docs/SECURITY.md` already scopes the listener to compose ("9101 in
  compose"); left as is.

## Risks

- Customers reading the whitepaper see a narrower inbound mTLS promise. That
  is the point; the wording stays calm and gives the alternative (HMAC).
- The disk-encryption statement depends on Azure's defaults. Azure documents
  them as always on.

## Verify

- `mkdocs build --strict` in a scratch venv with mkdocs-material, outside the
  repo.
- `git diff --stat origin/main` shows only `.md` files.
- Re-read each new sentence against the table above.
