# Install

VRSky ships as a set of Go microservices, a React UI, NATS JetStream, and
Postgres, orchestrated by Docker Compose (local/single-host) or Kubernetes
(production).

## Local / single host (Docker Compose)

```bash
git clone https://github.com/ValueRetail/vrsky.git
cd vrsky
cp .env.example .env          # set ENCRYPTION_KEY (see below)
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
- Database passwords and any object-storage credentials. In the local compose
  stack these are fixed dev values written into `docker-compose.yml`, not
  read from `.env`; the stack binds its ports to `127.0.0.1` for that reason.

For the local stack, everything in `.env` is optional. `docker-compose.yml`
has a dev default for each variable it reads, and an empty value in `.env`
falls back to that default, so an unedited copy of `.env.example` starts the
same stack as no `.env`. To override one, set it in `.env` (or in the shell,
e.g. `ENCRYPTION_KEY=... docker compose up -d`). Set `ENCRYPTION_KEY` before
you store real credentials: secrets stored under one key don't decrypt under
another.

## Production (Kubernetes)

Manifests live under `infrastructure/kubernetes/`. See the detailed
[deployment guide](../DEPLOYMENT_GUIDE.md) for the Helm/kustomize layout,
the kube-prometheus monitoring stack, and the Loki/Tempo wiring. Once
installed, production updates itself from `main`: see [Deploying](deploy.md).

## Verify the install

```bash
curl -fsS http://localhost:3000/healthz   # liveness
curl -fsS http://localhost:3000/readyz    # readiness (drains on shutdown)
curl -fsS http://localhost:3000/openapi.json | head   # API is up
```

Then follow [Build your first pipeline](../tutorials/first-pipeline.md).
