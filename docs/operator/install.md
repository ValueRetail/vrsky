# Install

VRSky ships as a set of Go microservices, a React UI, NATS JetStream, and
Postgres, orchestrated by Docker Compose (local/single-host) or Kubernetes
(production).

## Local / single host (Docker Compose)

```bash
git clone https://github.com/ValueRetail/vrsky.git
cd vrsky
cp .env.example .env          # set ENCRYPTION_KEY + passwords (see below)
docker compose up -d --build  # detached; -d is important (see Troubleshooting)
```

The UI dev server runs separately:

```bash
cd ui && npm install && npm run dev   # http://localhost:5173
```

Core endpoints (local): Management API `http://localhost:3000`, gateway
(Traefik) `http://localhost:8090`, Grafana `http://localhost:3001`,
Prometheus `http://localhost:9099`, webhook ingress `http://localhost:9100`.

## Required configuration

- **`ENCRYPTION_KEY`** — 64 hex chars (32 bytes). The same value must be set on
  the management-API and every worker that resolves secrets. **Generate once and
  store it safely — losing it makes all stored secrets unrecoverable.** See the
  [encryption-at-rest checklist](encryption-at-rest.md).
- Database passwords and any object-storage credentials.

## Production (Kubernetes)

Manifests live under `infrastructure/kubernetes/`. See the detailed
[deployment guide](../DEPLOYMENT_GUIDE.md) for the Helm/kustomize layout,
the kube-prometheus monitoring stack, and the Loki/Tempo wiring.

## Verify the install

```bash
curl -fsS http://localhost:3000/healthz   # liveness
curl -fsS http://localhost:3000/readyz    # readiness (drains on shutdown)
curl -fsS http://localhost:3000/openapi.json | head   # API is up
```

Then follow [Build your first pipeline](../tutorials/first-pipeline.md).
