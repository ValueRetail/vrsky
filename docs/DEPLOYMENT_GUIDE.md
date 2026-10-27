# Deployment guide

How VRSky's container images are built, where they live and how they reach
production. Production is the AKS cluster `vrsky-prod`, which pulls its
images from the Azure Container Registry `vrskyprodacr`, and a merge to
`main` builds and deploys the images it touched. The day-to-day side of
that (what a merge deploys, watching a run, the parked cluster, rolling
back) is in [Deploying](operator/deploy.md). This page covers the parts
around it: local builds, the images, the pipeline, and the scripts an
operator runs by hand.

No workflow pushes to GitHub Container Registry or Docker Hub, whatever
older versions of this page said.

---

## Images

Every VRSky image is `vrskyprodacr.azurecr.io/vrsky/<name>:latest`.
There are no version or commit tags. Each build overwrites `:latest`, and a
deploy pins the core deployments to the digest `:latest` pointed at when it
ran (see [Tags and digests](#tags-and-digests)).

`infrastructure/azure/build-push-acr.sh` builds them in two groups, and
`infrastructure/azure/affected.sh` and the deploy scripts use the same
split:

| Group | Images | Rolled out by |
|---|---|---|
| core | `management-api`, `ui`, `data-filter`, `data-converter`, `alert-responder` | `deploy-core-azure.sh`, by digest |
| connectors | `sitoo`, `front-systems`, `business-central`, `visma`, `brightpearl` and `sap-s4hana`, each as `-consumer` and `-producer`; `api-consumer`, `webhook-consumer`, `file-consumer`, `db-consumer`, `tenant-consumer`, `cloud-storage-consumer`, `sftp-consumer`, `kafka-consumer`, `rabbitmq-consumer`, `salesforce-consumer`, `remote-agent`; `http-producer`, `db-producer`, `file-producer`, `cloud-storage-producer`, `sftp-producer`, `kafka-producer`, `rabbitmq-producer`, `salesforce-producer` | `deploy-connectors-azure.sh`, or a restart of the deployment |

The UI is built from `ui/Dockerfile`, every other image from
`src/cmd/<name>/Dockerfile`. The UI runs in namespace `vrsky-ui`; the other
core services and all connectors run in `vrsky-platform`, as
`vrsky-<name>`.

Four directories under `src/cmd` have a Dockerfile but no image in ACR:
`postgres-consumer`, `postgres-producer`, `mock-sap` (a fake SAP endpoint
for the local stack) and `vrsky-cli`.

!!! note "postgres-consumer / postgres-producer are not built here"
    `postgres-consumer` (CDC, configured from `POSTGRES_INPUT_*`) and
    `postgres-producer` (a writer, `POSTGRES_OUTPUT_*`) predate the
    connection-config model. No connector type claims them, so no tenant
    pipeline can use them. `postgres-consumer` is a load-harness fixture
    (`tests/load/`), and `docker compose` builds both from source, so no
    published image is needed.

---

## Local development

### Prerequisites

- Docker with Compose v2 (the `docker compose` subcommand)
- Node.js 20 and Go 1.26 if you build or test outside containers
- Git

### Running locally

The local stack is `docker-compose.yml` at the repo root. It runs on
built-in development defaults; to set your own `ENCRYPTION_KEY` or
passwords, put them in a `.env` file at the repo root
([Install](operator/install.md) lists what matters).

#### Option 1: Build UI Only (Dev Mode)

```bash
# Serves UI on http://localhost:5173 with hot reload
docker compose -f docker-compose.ui.yml --profile dev up -d ui
```

#### Option 2: Build UI Production Image

```bash
# Builds optimized production image
docker compose -f docker-compose.ui.yml --profile prod up -d --build ui-prod

# Access on http://localhost:80
```

#### Option 3: Build All Services (Current Flow)

```bash
# Build and start the full stack from docker-compose.yml
docker compose build
docker compose up -d
```

The full stack is large. `make up-core` starts the smaller set a laptop
can run (`docker compose up -d --build` with a fixed service list; see the
root `Makefile`).

### Building One Image by Hand

There is no make target that only builds backend images (`make up-core`
builds the core set as part of starting it).

Every Dockerfile uses the repo root as its build context, so run these from
the repo root:

```bash
# A backend service, the way the local stack builds it
docker compose build http-producer

# The same with plain docker
docker build -t vrsky/http-producer:latest -f src/cmd/http-producer/Dockerfile .

# The UI (not part of docker-compose.yml)
docker build -t vrsky/ui:latest -f ui/Dockerfile .
```

The service names are the ones in `docker-compose.yml`; `docker compose
config --services` lists them.

---

## What the Dockerfiles do

### Go services

This holds for every Go image in ACR. Two of the four Dockerfiles outside
ACR differ: `mock-sap` has no health check, and `vrsky-cli` has none either
and runs on `postgres:18-alpine`.

- Build stage `golang:1.26-alpine`: `go mod download` (up to three attempts),
  then a static `linux/amd64` binary (`CGO_ENABLED=0`).
- Runtime stage `alpine:3.18` or `alpine:3.19` with `ca-certificates` and
  `curl`, and a `HEALTHCHECK` on the service's health port. The Go images
  set no `USER`, so they run as root unless the manifest overrides it:
  `management-api`, `data-filter` and `data-converter` run as UID 1000 on
  Kubernetes.
- `management-api` also builds the Windows remote agent into the image,
  stamped with the `AGENT_VERSION` build argument (`dev` unless the ACR
  build passes `git describe`), and bundles `infrastructure/migrations`
  with `golang-migrate`, which applies them on every start.

### UI

- Build stage `node:20-alpine`: `npm ci`, then `npm run build`. No tests
  run during the image build; CI runs them (below).
- Runtime stage `nginx:1.27-alpine`, serving the bundle as the `nginx`
  user with `ui/nginx.conf` and `ui/nginx.default.conf`.

### Build context

The context is always the repo root, and the root `.dockerignore` is the
only one in the repo. It excludes everything, then lets back in `src/go.mod`,
`src/go.sum`, `src/cmd`, `src/pkg`, `src/internal`, `ui` and
`infrastructure/migrations`. Under `ui` it still drops `node_modules`,
`dist`, test coverage, `.env*` files, logs and editor files. Docker reads
only the `.dockerignore` at the root of the context, so don't add one under
`ui/` or `src/`; it would be ignored.

---

## CI/CD pipeline

`.github/workflows/build-push.yml` runs on pushes to `main`, `develop` and
`feature/**`, on tags matching `v*`, and on pull requests to `main` and
`develop`. Only a push to `main` builds images; everything else runs the
tests and stops.

| Job | Runs | What it does |
|---|---|---|
| Detect changed paths | always | Decides whether `ui/**` changed |
| Test UI | when `ui/**` changed | `npm ci`, `npm run test:coverage` (thresholds in `ui/vitest.config.ts`), `npm run build` |
| Go Tests | pushes and PRs | `promtool` on the alert rules, `gofmt`, `go test -v -race -cover ./...` against a Postgres service, the `pkg/io` state-store tests, `golangci-lint`, the OpenAPI and tenant-filter lints |
| Remote agent on Windows | pushes and PRs | Agent tests and the install script on a Windows runner |
| Build remote agent binaries | pushes and PRs | `make build-agent`, uploaded as the `vrsky-agent` artifact |
| Plan the rollout | pushes to `main`, once the jobs above passed or were skipped | Feeds the merge's changed paths to `affected.sh` |
| Build `<image>` in ACR | one runner per image the plan named | `az acr build --registry vrskyprodacr --image vrsky/<image>:latest` with the image's Dockerfile and the repo root as context |
| Deploy to production | after the builds | Rolls out what the plan named, then the smoke check |

`az acr build` runs in the registry: the runner uploads the build context
and ACR builds it for `linux/amd64`. There is no layer cache on the GitHub
side. What a merge deploys, how the deploy job reaches the cluster and what
happens while the cluster is parked are all in
[Deploying](operator/deploy.md).

---

## Building and deploying by hand

A merge to `main` does all of this for you. By hand is for a rollback in a
hurry, the UI's nginx config and Ingress (the deploy job doesn't apply
`infrastructure/kubernetes/ui/`), and a first bring-up on an empty cluster,
which has its own section below. Run the scripts from a repo checkout,
signed in with `az login` to an account that can build in `vrskyprodacr`;
the deploy scripts also run `kubectl`, so they need a kubeconfig for
`vrsky-prod` from an address the API server accepts.
[How the job reaches Azure](operator/deploy.md#how-the-job-reaches-azure)
explains that limit.

From anywhere else, `infrastructure/azure/in-cluster.sh` runs a script
inside the cluster, but only the ones written for its runtime, which has no
`az`, `sed` or `perl`: `deploy-core-azure.sh` with `DIGESTS_FILE` (digests
resolved beforehand, as the deploy job does), `deploy-connectors-azure.sh`,
`install-monitoring.sh` and `smoke-prod.sh`. `deploy-azure.sh` and
`deploy-ui-azure.sh` can't run through it.

```bash
# Build in ACR: core, connectors, or all (the default)
infrastructure/azure/build-push-acr.sh core

# Roll the core services to what :latest points at now
DRY_RUN=1 infrastructure/azure/deploy-core-azure.sh   # show what would change
infrastructure/azure/deploy-core-azure.sh             # all five
infrastructure/azure/deploy-core-azure.sh ui          # or a subset

# Regenerate and apply the connector manifest, then restart the fleet
infrastructure/azure/deploy-connectors-azure.sh
```

`deploy-core-azure.sh` reads each digest from ACR, refuses anything that
isn't a `sha256:` digest, sets the image, waits for the rollout and reads
the deployment back to check what landed. It only updates deployments that
already exist.

To change the UI's nginx config, Service, PDB or Ingress on a running
cluster, re-run `bash infrastructure/azure/deploy-ui-azure.sh`. It applies
the deployment with `ui:latest` rather than a digest, so run
`deploy-core-azure.sh ui` afterwards to pin it again. It also applies
`ui/ingress.yaml`, whose header says to diff it against the live object
first.

### First bring-up

Only on an empty cluster, in this order:

1. `infrastructure/azure/deploy-azure.sh`: NATS, Postgres, MinIO, the
   transforms and the management API, from a copy of
   `infrastructure/kubernetes/` with the image references rewritten to ACR,
   plus the `acr-pull` pull secret. Never run it, or
   `infrastructure/kubernetes/deploy-vrsky-platform.sh` which it wraps,
   against production (see the warning below).
2. `bash infrastructure/azure/deploy-ui-azure.sh`: the UI and the public
   Ingress (below). The file isn't executable, hence `bash`.
3. `infrastructure/azure/deploy-connectors-azure.sh`: the connectors.
4. `PROFILE=azure infrastructure/kubernetes/monitoring/install-monitoring.sh`:
   monitoring and the alert rules. It stops unless the `vrsky-monitoring`
   namespace and its `alerts-webhook-token` and `grafana-admin` Secrets
   exist; "Azure profile (prod)" in
   `infrastructure/kubernetes/monitoring/README.md` lists what to create
   first.

Then by hand:

- The alert responder: its secrets and a `kubectl apply` of its manifest,
  as [Alert responder](operator/alert-responder.md) describes.
  `deploy-core-azure.sh` updates it afterwards but can't create it, so on a
  cluster without it, a run over all five core services reports it missing
  and exits 1.
- The webhook and remote-agent Ingresses,
  `infrastructure/kubernetes/ingress/webhooks-ingress.yaml` and
  `agent-ingress.yaml`; read each file's header before applying it.

None of the Azure scripts installs ingress-nginx or cert-manager. The UI's
Ingress assumes both, with a `letsencrypt-prod` ClusterIssuer
(`infrastructure/kubernetes/ingress/cert-issuers.yaml`;
`install-cert-manager.sh` next to it installs cert-manager, and
`infrastructure/kubernetes/ingress/README.md` has the `helm` command for
ingress-nginx).

`deploy-azure.sh` starts on the committed development secrets; replace them
before any real connection (the script's closing message says which).

!!! danger "On a running cluster, use `deploy-core-azure.sh` and `deploy-ui-azure.sh`"

    Don't re-run `deploy-azure.sh` or `deploy-vrsky-platform.sh` on
    production. Without a local `secret.yaml` they apply
    `secret.example.yaml`, which overwrites the `postgres-credentials`
    Secret in `vrsky-platform` (`connection_string`, `encryption_key`) with
    the committed development values: stored credentials stop decrypting
    and the management API loses its database.

    Don't `kubectl apply` the core and UI manifests either.
    `infrastructure/kubernetes/{management-api,data-filter,data-converter,ui}/`
    are shared with the local k3d path and name `ghcr.io` images that AKS
    cannot pull, so the pods end up in `ImagePullBackOff`
    (`InvalidImageName` for the UI, whose reference has capital letters).
    `deploy-ui-azure.sh` applies the UI manifests from a rewritten copy.

    And don't `kubectl rollout restart` a core deployment to ship a new
    image: it is pinned by digest, so a restart redeploys the image it
    already runs. `deploy-core-azure.sh` sets the new digest.

---

## Kubernetes manifests

### The UI manifests

`infrastructure/kubernetes/ui/` holds the UI's manifests.
`deploy-ui-azure.sh` uses them like this:

| File | On AKS |
|---|---|
| `namespace.yaml` | Not applied; the script creates `vrsky-ui` itself and copies the `acr-pull` secret into it |
| `configmap.yaml` | Applied. Its `VITE_*` values have no effect; the file's header explains why |
| `deployment.yaml` | Applied from a copy: image `vrskyprodacr.azurecr.io/vrsky/ui:latest`, `imagePullPolicy: Always`, 2 replicas, and the `vrsky-ui-nginx` ConfigMap mounted as the nginx config, which proxies `/api` and `/ws` to the management API |
| `service.yaml` | Applied: ClusterIP `vrsky-ui`, port 80 |
| `pdb.yaml` | Applied: at least one replica stays up during a drain |
| `ingress.yaml` | Applied: Ingress `vrsky` for `vrsky.valueretail.no` and `20.251.107.2.sslip.io`, class `nginx`, certificate from cert-manager (`letsencrypt-prod`) in secret `vrsky-tls` |
| `hpa.yaml` | Not applied by any script |

After that first run, `deploy-core-azure.sh` only changes the UI
deployment's image, pinned to a digest.

### Local cluster (k3d)

`infrastructure/scripts/local-up.sh` creates the k3d cluster `vrsky-dev`
from `infrastructure/kubernetes/k3d-config.yaml`, and
`infrastructure/kubernetes/deploy-vrsky-platform.sh` deploys the platform
onto it. The cluster can't pull the registries the manifests name, so
`infrastructure/scripts/k3d-load-images.sh [core|connectors|all]` builds
images from your checkout and imports them into the cluster.

Only `management-api` lines up today. The script imports `data-filter` and
`data-converter` as `localhost:5000/vrsky/...` while their manifests pull
`ghcr.io/valueretail/vrsky/...`. The `connectors` scope imports nine
connectors as `localhost:5000/vrsky/...`, which nothing on k3d deploys (the
connector manifest names ACR and only `deploy-connectors-azure.sh` applies
it). And the UI doesn't run on k3d at all: the
script loads no UI image, and the manifest's
`ghcr.io/ValueRetail/vrsky/ui:latest` has capital letters, which Docker and
Kubernetes reject in an image reference. Use the compose options above for
the UI.

---

## Tags and digests

Images carry one tag, `:latest`. Git tags and branches add none, and
nothing reads `scripts/get-version.sh`.

- Core services run by digest:
  `vrskyprodacr.azurecr.io/vrsky/<name>@sha256:…`, set by
  `deploy-core-azure.sh`. What runs only changes when that script (or the
  deploy job, or Catch up, which call it) sets a new digest.
- Connectors run `vrskyprodacr.azurecr.io/vrsky/<name>:latest` with
  `imagePullPolicy: Always`, so a restart pulls whatever `:latest` is.
- Rolling back means getting an older build into `:latest` and rolling it
  out. Normally that is a revert merged to `main`; see
  [Rolling back](operator/deploy.md#rolling-back).
- **Production check** (`drift-check.yml`) compares what each container
  runs with the registry's `:latest` and catches up when they differ; see
  [Deploying](operator/deploy.md#while-the-cluster-is-parked).

---

## Troubleshooting

### `npm ci` fails in the UI image build

The UI image copies `ui/package*.json` from the repo root, so build with
the repo root as context. `docker build ui/` fails at the first `COPY`.

```bash
# package-lock.json must not be excluded by the root .dockerignore
grep -n 'package-lock' .dockerignore   # should print nothing

docker build -t vrsky/ui:latest -f ui/Dockerfile .
```

### UI tests

They don't run in the image build. Run them in `ui/` the way CI does:

```bash
cd ui && npm ci && npm run test:coverage
```

### A pod stays `Pending`

```bash
kubectl describe pod -n vrsky-ui <pod-name>
kubectl describe nodes | grep -A 5 "Allocated resources"
```

Resource requests are set in the manifests; `plans/cpu-request-headroom.md`
explains how they were sized.

### The UI's probes fail

The liveness and readiness probes fetch `/index.html` on port 80.

```bash
kubectl port-forward -n vrsky-ui svc/vrsky-ui 8080:80
curl -f http://localhost:8080/index.html
kubectl logs -n vrsky-ui deployment/vrsky-ui -f
```

### Image pull errors on AKS

Pods pull from ACR with the `acr-pull` secret on the `default` service
account of `vrsky-platform`, `vrsky-storage` and `vrsky-ui`, and on the
management API's own `vrsky-management-api` service account. An image
reference that names `ghcr.io` means a manifest was applied without the
rewrite (see the warning above). A tag ACR doesn't
have means the image was never built: check with
`az acr repository show-tags -n vrskyprodacr --repository vrsky/<name>`.

### A rollout succeeded but the old version is still served

A digest-pinned deployment was restarted instead of updated. Run
`deploy-core-azure.sh` for that service; it sets the current digest and
checks it landed. To see which UI bundle is actually served:

```bash
kubectl -n vrsky-ui exec deploy/vrsky-ui -- ls /usr/share/nginx/html/assets
```

### Debugging commands

```bash
kubectl get events -n vrsky-ui --sort-by='.lastTimestamp'
kubectl describe deployment vrsky-ui -n vrsky-ui
kubectl logs -n vrsky-ui deployment/vrsky-ui -f --all-containers
kubectl exec -it -n vrsky-ui deploy/vrsky-ui -- /bin/sh
```

---

## Encryption at rest (Phase 1F / issue #71)

VRSky processes credentials, user data, and cross-tenant payloads. Customer
compliance reviews (SOC 2 CC6.1, ISO 27001 A.10.1, GDPR Art. 32) require
encryption of every stateful component. The layers below are stacked:

| Layer | What's encrypted | Implementation |
|-------|------------------|----------------|
| Application | Connection credentials (DB passwords, bearer tokens, OAuth secrets, webhook signing keys) | AES-256-GCM via `pkg/crypto` (#66 secrets table). Master key from `ENCRYPTION_KEY`. |
| Block device | Postgres `$PGDATA`, MinIO `/data`, NATS JetStream `/data`, KES key store | Encrypted StorageClass (cloud KMS) or LUKS on bare metal. |
| Object storage | MinIO bucket contents | SSE-S3 via KES sidecar wrapping a KMS data key. |
| Transit | All HTTP between services | TLS terminated at Traefik, internal mTLS via the cluster mesh (cluster-trust CA). |

### Per-target setup

#### Docker Compose (development only)

**Not for production.** Volumes live on whatever filesystem hosts your Docker
data root. The application-layer encryption from #66 still applies — secret
ciphertexts and audit records are protected from a casual `pg_dump` — but
the disk itself is plaintext. Document this clearly when handing the stack
to evaluators.

#### K3s / self-hosted (LUKS)

1. Stop k3s.
2. Format a dedicated partition with LUKS — see
   `infrastructure/kubernetes/encryption/storage-classes/self-hosted-luks.yaml`
   for the exact commands.
3. Mount it on `/opt/local-path-provisioner` (the default `nodePath` for
   Rancher's `local-path` provisioner).
4. Use a TPM-bound passphrase (clevis + tang) or a network escrow for
   unattended reboots. **Never** put a plaintext passphrase in
   `/etc/crypttab`.
5. Start k3s. The existing `storageClassName: local-path` on the base
   manifests now lands on LUKS-backed disk automatically.

Verify: `lsblk -o NAME,FSTYPE,TYPE,MOUNTPOINT` should show the mount as
type `crypt`.

#### K3s / managed cloud (AWS / GCP / Azure)

1. Pick the appropriate StorageClass template from
   `infrastructure/kubernetes/encryption/storage-classes/`:
   - `aws-ebs-kms.yaml` — gp3 + customer-managed KMS key
   - `gcp-pd-cmek.yaml` — pd-ssd + customer-managed encryption key
   - `azure-cmk.yaml` — Premium_LRS + disk encryption set
2. Replace the placeholder key ARN / resource path with your real key.
3. `kubectl apply -f` the file. The provisioner driver must already be
   installed in the cluster.
4. Re-create the PVCs in `vrsky-storage` / `vrsky-platform` /
   `vrsky-management` so they bind to the new `vrsky-encrypted` class.
   (PVCs are immutable wrt `storageClassName` — this is destructive in a
   running cluster, so do it during a maintenance window.)
5. Verify by inspecting the underlying disk in the cloud console — the
   "encryption" field should report your customer key.

#### MinIO SSE-S3 (compliance mode)

After the StorageClass is in place:

```bash
kubectl apply -f infrastructure/kubernetes/encryption/kes-deployment.yaml
kubectl apply -f infrastructure/kubernetes/encryption/minio-encrypted.patch.yaml
```

KES is the broker between MinIO and the real KMS. The reference manifest
uses a file-backed key store on disk for dev convenience — for production
swap it to HashiCorp Vault, AWS KMS, GCP KMS, or Azure Key Vault. The
KES config sits in a ConfigMap so you only have to edit + restart.

Verify:

```bash
# Upload an object and check the metadata for the SSE header.
mc cp Makefile s3/test/Makefile
mc cat --metadata s3/test/Makefile | grep -i sse
# Expected: X-Amz-Server-Side-Encryption: AES256
```

#### Postgres native encryption

The management Postgres deployment uses `local-path` by default. Once the
underlying StorageClass is encrypted (any of the steps above), Postgres
runs unmodified. No `pgcrypto` config or per-column encryption is needed
at this layer — the secrets table from #66 takes care of column-level
secrecy for the handful of fields that warrant it.

For managed Postgres (RDS / Cloud SQL / Azure Database), enable the
provider's native at-rest encryption with a customer-managed key. Point
`MGMT_API_DB_URL` at the managed endpoint and remove the in-cluster
`postgres-management` StatefulSet.

### Compliance verification checklist

Before claiming "encryption at rest" to an auditor:

- [ ] `kubectl get sc vrsky-encrypted -o yaml` shows your KMS key.
- [ ] `kubectl get pvc -A -o wide | grep -v vrsky-encrypted` returns
      nothing for VRSky namespaces (no plaintext PVCs slipped in).
- [ ] KES is running and reachable from MinIO; MinIO refuses uploads
      when KES is intentionally scaled to 0.
- [ ] Postgres data volume sits on the encrypted class (`kubectl
      describe pvc` shows the right `StorageClass`).
- [ ] `ENCRYPTION_KEY` is supplied via a Kubernetes Secret (not a plain
      env var literal in the manifest).
- [ ] Off-cluster backups (Postgres dumps, MinIO replication targets)
      go to buckets / disks with SSE enabled.

Encryption-at-rest and audit logging map to the common SOC 2 / ISO 27001
control families.


---

## Disaster Recovery

The management Postgres (tenants, connections, secrets, OAuth grants,
notification targets, audit log) is backed up daily and its restore path is
tested in CI. See **[`docs/DR.md`](DR.md)** for the full runbook — backup flow,
the `vrsky-cli backup`/`restore`/`list` commands, RPO ≤ 24h / RTO ≤ 1h, the
critical `ENCRYPTION_KEY` caveat, and the step-by-step recovery procedure. The
backup CronJob + its config Secret are documented in
[`infrastructure/kubernetes/backup/README.md`](https://github.com/ValueRetail/vrsky/blob/main/infrastructure/kubernetes/backup/README.md).
