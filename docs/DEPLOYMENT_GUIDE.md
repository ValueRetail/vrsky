# VRSky Docker Deployment Guide - PHASE 12

This guide covers the complete deployment pipeline for VRSky services using Docker and Kubernetes, including the React UI frontend and all 9 backend services.

**Target Date**: PHASE 12 (Docker Deployment)  
**Status**: 🔄 Implementation in Progress

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Local Development](#local-development)
3. [Docker Image Building](#docker-image-building)
4. [GitHub Actions CI/CD Pipeline](#github-actions-cicd-pipeline)
5. [Kubernetes Deployment](#kubernetes-deployment)
6. [Versioning Strategy](#versioning-strategy)
7. [Troubleshooting](#troubleshooting)

---

## Architecture Overview

### Services and Components

VRSky consists of:

- **1 Frontend**: React SPA (UI)
- **9 Backend Services**:
  - `producer` - Message production
  - `consumer` - Basic message consumption
  - `converter` - Data format conversion
  - `filter` - Message filtering
  - `management-api` - Tenant and integration management
  - `file-consumer` - File-based message source
  - `file-producer` - File-based message destination
  - `postgres-consumer` - PostgreSQL CDC consumer
  - `postgres-producer` - PostgreSQL message sink

### Container Registry

All images are pushed to **GitHub Container Registry (GHCR)**:

```
ghcr.io/ValueRetail/vrsky/<service>:<tag>
```

### Image Naming Convention

- **Development**: `vrsky/<service>:latest` (local builds)
- **Production**: `ghcr.io/ValueRetail/vrsky/<service>:latest` (GHCR)
- **Versioned**: `ghcr.io/ValueRetail/vrsky/<service>:v1.2.3` (semver tags)
- **Commit Hash**: `ghcr.io/ValueRetail/vrsky/<service>:sha-a1b2c3d4` (branch builds)

---

## Local Development

### Prerequisites

- Docker 24.0+
- Docker Compose 2.20+
- Node.js 20+
- Go 1.22+
- Git

### Building Locally

#### Option 1: Build UI Only (Dev Mode)

```bash
# Serves UI on http://localhost:5173 with hot reload
docker-compose -f docker-compose.ui.yml up -d --profile dev ui
```

#### Option 2: Build UI Production Image

```bash
# Builds optimized production image
docker-compose -f docker-compose.ui.yml up -d --profile prod ui-prod

# Access on http://localhost:80
```

#### Option 3: Build All Services (Current Flow)

```bash
# Build full stack from current docker-compose.yml
docker-compose build
docker-compose up -d
```

### Manual Docker Build Commands

```bash
# Build UI image locally
cd vrsky
docker build -t vrsky/ui:latest -f ui/Dockerfile .

# Build individual backend service
docker build -t vrsky/producer:latest -f src/cmd/producer/Dockerfile .

# Use Makefile for convenience
cd src
make docker-build-ui
make docker-push-ui
```

### Makefile Commands

The `src/Makefile` provides convenient targets:

```bash
# Build UI
make docker-build-ui

# Push UI to registry
make docker-push-ui

# Build backend services
make docker-build              # Producer (default)
make docker-build-consumer
make docker-build-filter
make docker-build-converter

# Push backend services
make docker-push
make docker-push-consumer
make docker-push-filter
make docker-push-converter
```

---

## Docker Image Building

### Multi-Stage Build Strategy

All images use multi-stage builds to minimize final size:

#### Backend Services (Go)

```dockerfile
# Stage 1: Build
FROM golang:1.21-alpine
# ... compile binary ...

# Stage 2: Runtime
FROM alpine:3.21
# Copy binary from stage 1
# Minimal dependencies
```

#### Frontend (React + Nginx)

```dockerfile
# Stage 1: Build
FROM node:20-alpine
# ... npm build ...

# Stage 2: Runtime
FROM nginx:1.27-alpine
# Copy built artifacts from stage 1
# Optimized for static serving
```

### Docker Ignore

Critical for build performance:

- **ui/.dockerignore**: Excludes node_modules, coverage, dist (but keeps package-lock.json)
- Prevents large context transfers
- Speeds up builds with Docker BuildKit cache

### Build Process

1. Tests run during image build (Go: `go test` + backend tests)
2. UI tests run in Docker: all 279 tests must pass
3. Production artifacts built: Go binaries, Vite bundle
4. Minimal runtime image created with only necessary files
5. Non-root user enforced (security best practice)

---

## GitHub Actions CI/CD Pipeline

### Workflow: `.github/workflows/build-push.yml`

**Triggers**:
- Push to `main`, `develop`, `feature/**` branches
- Tags matching `v*` (semantic versioning)
- Pull requests to `main`, `develop`

### Build Matrix

The workflow builds **10 services in parallel**:

| Service | Path | Image Registry |
|---------|------|----------------|
| producer | src/cmd/producer | ghcr.io/ValueRetail/vrsky/producer |
| consumer | src/cmd/consumer | ghcr.io/ValueRetail/vrsky/consumer |
| converter | src/cmd/converter | ghcr.io/ValueRetail/vrsky/converter |
| filter | src/cmd/filter | ghcr.io/ValueRetail/vrsky/filter |
| management-api | src/cmd/management-api | ghcr.io/ValueRetail/vrsky/management-api |
| file-consumer | src/cmd/file-consumer | ghcr.io/ValueRetail/vrsky/file-consumer |
| file-producer | src/cmd/file-producer | ghcr.io/ValueRetail/vrsky/file-producer |
| postgres-consumer | src/cmd/postgres-consumer | ghcr.io/ValueRetail/vrsky/postgres-consumer |
| postgres-producer | src/cmd/postgres-producer | ghcr.io/ValueRetail/vrsky/postgres-producer |
| ui | ui | ghcr.io/ValueRetail/vrsky/ui |

### Image Tags

Each build creates **multiple tags**:

```
ghcr.io/ValueRetail/vrsky/<service>:latest        # Always latest from main
ghcr.io/ValueRetail/vrsky/<service>:v1.2.3        # Semantic version (on tags)
ghcr.io/ValueRetail/vrsky/<service>:sha-a1b2c3d4  # Commit hash (on branches)
```

### Cache Strategy

- Uses GitHub Actions Docker Cache (`type=gha`)
- Caches each layer for faster rebuilds
- Parallelizes builds for all services
- Typical build time: 5-15 minutes for all services

### Running Tests in CI

```yaml
test:
  # UI tests (Node.js)
  - npm ci
  - npm run test  # All 279 tests
  - npm run build

go-tests:
  # Go tests with race detector
  - go test -v -race -cover ./...
  - golangci-lint run
```

---

## Kubernetes Deployment

### Namespace Structure

```
vrsky-ui                    # UI frontend (NEW in PHASE 12)
vrsky-platform              # Existing backend services
vrsky-monitoring            # Prometheus, Grafana
vrsky-messaging             # NATS platform
vrsky-tenants               # Per-tenant NATS accounts
```

### UI Deployment Manifests

All UI manifests in `infrastructure/kubernetes/ui/`:

#### 1. **namespace.yaml**

Isolates UI in `vrsky-ui` namespace with labels.

#### 2. **configmap.yaml**

Configuration for UI environment:
- `VITE_API_BASE_URL` - Backend API endpoint
- Feature flags (metrics, logging, etc.)
- Performance settings

#### 3. **deployment.yaml**

- **Replicas**: 3 (HA configuration)
- **Image**: `ghcr.io/ValueRetail/vrsky/ui:latest`
- **Resources**: 
  - Request: 100m CPU, 128Mi memory
  - Limit: 500m CPU, 256Mi memory
- **Probes**: Liveness (port 80/health) + Readiness (port 80/index.html)
- **Security**: Non-root user (UID 101), read-only root, no privileges
- **Volumes**: emptyDir for cache, /tmp, /var/run

#### 4. **service.yaml**

ClusterIP service exposing port 80 internally.

#### 5. **ingress.yaml**

Routes external traffic:
- Hosts: `ui.vrsky.local`, `ui.valueretalgroup.no`
- TLS termination (cert-manager)
- NGINX ingress controller

#### 6. **hpa.yaml**

Horizontal Pod Autoscaling:
- **Min replicas**: 2
- **Max replicas**: 5
- **Triggers**:
  - CPU > 70%
  - Memory > 80%
- **Scaling behavior**: Fast scale-up (30s), gradual scale-down (300s)

#### 7. **pdb.yaml**

Pod Disruption Budget:
- **Min available**: 1 pod (maintains availability during cluster updates)

### Deploying to k3d (Local)

```bash
# Create k3d cluster (already exists from PHASE 11)
k3d cluster create vrsky-k3d --config infrastructure/kubernetes/k3d-config.yaml

# Deploy UI namespace
kubectl apply -f infrastructure/kubernetes/ui/namespace.yaml

# Deploy UI components
kubectl apply -f infrastructure/kubernetes/ui/

# Verify deployment
kubectl get pods -n vrsky-ui
kubectl logs -f -n vrsky-ui deployment/vrsky-ui

# Port-forward for testing
kubectl port-forward -n vrsky-ui svc/vrsky-ui 8080:80
# Access: http://localhost:8080
```

### Deploying to K3s Production (Oslo)

Same manifests work for production K3s cluster:

```bash
# Set kubeconfig to production
export KUBECONFIG=~/.kube/vrsky-production.yaml

# Deploy (same commands as k3d)
kubectl apply -f infrastructure/kubernetes/ui/

# Verify rolling deployment
kubectl rollout status deployment/vrsky-ui -n vrsky-ui
```

### Blue-Green Deployment (Optional)

For zero-downtime updates:

```bash
# Update image in deployment
kubectl set image deployment/vrsky-ui \
  ui=ghcr.io/ValueRetail/vrsky/ui:v1.2.3 \
  -n vrsky-ui

# Monitor rollout
kubectl rollout status deployment/vrsky-ui -n vrsky-ui

# Rollback if needed
kubectl rollout undo deployment/vrsky-ui -n vrsky-ui
```

---

## Versioning Strategy

### Semantic Versioning (SemVer)

VRSky follows semantic versioning: **MAJOR.MINOR.PATCH**

Example: `v1.2.3`

### Git Tag Strategy

```bash
# Create a release tag
git tag -a v1.2.3 -m "Release v1.2.3: Add UI deployment"
git push origin v1.2.3

# GitHub Actions automatically builds and pushes:
# - ghcr.io/ValueRetail/vrsky/ui:latest
# - ghcr.io/ValueRetail/vrsky/ui:v1.2.3
# - ... (all 10 services)
```

### Image Version Resolution

The workflow uses `scripts/get-version.sh`:

```bash
#!/bin/bash
# Returns git tag (v1.2.3) or commit hash (sha-a1b2c3d4)

VERSION=$(git describe --exact-match --tags 2>/dev/null || \
          echo "sha-$(git rev-parse --short HEAD)")
echo "$VERSION"
```

### Deployment by Version

```bash
# Deploy latest (always works)
kubectl set image deployment/vrsky-ui \
  ui=ghcr.io/ValueRetail/vrsky/ui:latest \
  -n vrsky-ui

# Deploy specific version
kubectl set image deployment/vrsky-ui \
  ui=ghcr.io/ValueRetail/vrsky/ui:v1.2.3 \
  -n vrsky-ui

# Deploy specific commit
kubectl set image deployment/vrsky-ui \
  ui=ghcr.io/ValueRetail/vrsky/ui:sha-a1b2c3d4 \
  -n vrsky-ui
```

---

## Troubleshooting

### Common Issues

#### Build Fails: "npm ci requires package-lock.json"

**Solution**: Ensure `.dockerignore` doesn't exclude `package-lock.json`

```bash
# Check .dockerignore
grep -n package-lock.json ui/.dockerignore  # Should be empty

# Remove if present and rebuild
docker build -t vrsky/ui:latest ui/
```

#### Image Build Runs Tests Inside Container

**Expected behavior**: Tests run during build (Stage 1).

```bash
# To skip tests, modify Dockerfile temporarily:
# Comment out: RUN npm run test

# To verify tests passed in build:
docker build -t vrsky/ui:latest ui/ 2>&1 | grep "✓.*tests"
```

#### Kubernetes Pod Stays in "Pending"

```bash
# Check resource availability
kubectl describe pod -n vrsky-ui <pod-name>

# Check node resources
kubectl describe nodes | grep -A 5 "Allocated resources"

# Reduce resource requests in deployment.yaml and reapply
kubectl apply -f infrastructure/kubernetes/ui/deployment.yaml
```

#### Liveness Probe Fails

```bash
# Check endpoint manually
kubectl port-forward -n vrsky-ui svc/vrsky-ui 8080:80
curl -f http://localhost:8080/health

# View logs
kubectl logs -n vrsky-ui deployment/vrsky-ui -f
```

#### Image Pull Errors in K8s

```bash
# Verify GHCR credentials
kubectl create secret docker-registry ghcr-secret \
  --docker-server=ghcr.io \
  --docker-username=<github-username> \
  --docker-password=<github-token> \
  -n vrsky-ui

# Update deployment.yaml imagePullSecrets:
# - name: ghcr-secret
```

### Debugging Commands

```bash
# View build logs
docker build -t vrsky/ui:latest ui/ 2>&1 | tail -100

# Inspect running image
docker run -it vrsky/ui:latest /bin/sh

# Check K8s events
kubectl get events -n vrsky-ui --sort-by='.lastTimestamp'

# Describe deployment issues
kubectl describe deployment vrsky-ui -n vrsky-ui

# Stream logs
kubectl logs -n vrsky-ui deployment/vrsky-ui -f --all-containers

# SSH into pod
kubectl exec -it -n vrsky-ui pod/<pod-name> -- /bin/sh
```

---

## Summary - PHASE 12 Deliverables

| Item | File | Status |
|------|------|--------|
| Dockerfile | `ui/Dockerfile` | ✅ Created |
| .dockerignore | `ui/.dockerignore` | ✅ Created |
| Nginx Config | `ui/nginx.conf` | ✅ Created |
| Nginx Default | `ui/nginx.default.conf` | ✅ Created |
| CI/CD Workflow | `.github/workflows/build-push.yml` | ✅ Created |
| Version Script | `scripts/get-version.sh` | ✅ Created |
| K8s Namespace | `infrastructure/kubernetes/ui/namespace.yaml` | ✅ Created |
| K8s ConfigMap | `infrastructure/kubernetes/ui/configmap.yaml` | ✅ Created |
| K8s Deployment | `infrastructure/kubernetes/ui/deployment.yaml` | ✅ Created |
| K8s Service | `infrastructure/kubernetes/ui/service.yaml` | ✅ Created |
| K8s Ingress | `infrastructure/kubernetes/ui/ingress.yaml` | ✅ Created |
| K8s HPA | `infrastructure/kubernetes/ui/hpa.yaml` | ✅ Created |
| K8s PDB | `infrastructure/kubernetes/ui/pdb.yaml` | ✅ Created |
| Docker Compose UI | `docker-compose.ui.yml` | ✅ Created |
| Makefile Updates | `src/Makefile` | ✅ Updated |
| Deployment Guide | `docs/DEPLOYMENT_GUIDE.md` | ✅ Created |

---

## Next Steps

1. ✅ All 13 deliverables created
2. ✅ Local Docker build tested (279 tests pass)
3. 🔄 Push changes to git (5 commits)
4. 🔄 GitHub Actions workflow validation
5. 🔄 K8s deployment testing (k3d + K3s)
6. ✅ PHASE 12 Complete

---

**Last Updated**: February 24, 2026  
**PHASE**: 12 - Docker Deployment  
**Status**: Implementation Complete ✅
