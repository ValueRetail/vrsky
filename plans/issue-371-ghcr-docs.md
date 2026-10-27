# DEPLOYMENT_GUIDE.md still describes GHCR (#371)

No open questions.

## Goal

`docs/DEPLOYMENT_GUIDE.md` describes a GHCR pipeline that never existed in
production: `ghcr.io/ValueRetail/vrsky/...` images, semver and `sha-` tags,
a GitHub Actions build matrix with a `type=gha` cache, a GHCR pull secret.
Production images are built in ACR (`vrskyprodacr`) and rolled out by the
deploy job. Make the guide describe what the workflows and scripts do.

## Acceptance criteria (from the issue)

- Every registry, image name, tag, build and deploy step in the guide
  matches `.github/workflows/build-push.yml` and `infrastructure/azure/`.
  Each one checked against those files. Sections describing a process that
  no longer exists are replaced with a pointer to `docs/operator/deploy.md`
  where that page covers it.
- `docker-compose` becomes `docker compose` in commands.
- `mkdocs build --strict` passes.
- `plans/backlog.md` item 18 struck with `(#371)`.

## Files

- `docs/DEPLOYMENT_GUIDE.md`
- `plans/issue-371-ghcr-docs.md` (this file; `.gitignore`'s `*GUIDE*.md`
  matches `deploy-guide` on a case-insensitive filesystem)
- `plans/backlog.md`: strike item 18; add what turned up along the way

Not `docker-compose.ui.yml`: #370 fixes the `ui-prod` build context.

## Approach

Section by section:

| Section | What's wrong | Change |
|---|---|---|
| Title, status, TOC | "PHASE 12, implementation in progress" | Plain title and intro; mkdocs builds the TOC |
| Architecture overview | 9 services that mostly don't exist; GHCR registry; semver/sha tags | Image table from `build-push-acr.sh` / `affected.sh`: ACR, `:latest` only, core vs connectors, namespaces |
| Local development | `docker-compose`; `--profile` after `up` (Compose v2 rejects it) | `docker compose`, global flags before `up`; Option 2 gets only that fix (its compose file is #370's) |
| Docker image building | Go 1.21, "tests run during build", "non-root enforced", `ui/.dockerignore` | Real base images, no tests in builds, Go images run as root, root `.dockerignore` is the one used |
| CI/CD pipeline | GHCR build matrix, gha cache, made-up tag list | The jobs `build-push.yml` runs, ACR builds per image, pointer to deploy.md |
| Kubernetes deployment | k3d UI apply, "K3s production (Oslo)", GHCR `kubectl set image` | By-hand ACR build and deploy scripts, first bring-up order, what `deploy-ui-azure.sh` does with each UI manifest, why the manifests can't be applied to AKS or run the UI on k3d |
| Versioning | semver tags, `get-version.sh` (nothing calls it) | Tags and digests: `:latest` + digest pinning; rollback pointer to deploy.md |
| Troubleshooting | GHCR secret, `docker build ui/` (wrong context), `/health` probe | Root context, `acr-pull` secret, probes on `/index.html`, digest-pinned restarts |
| Summary / Next steps | PHASE 12 checklist | Removed |
| Encryption at rest, Disaster recovery | not about images or deploys | Unchanged |

## Risks

Docs only. DEPLOYMENT_GUIDE is in the mkdocs nav, so a bad link or anchor
fails `--strict`.

## Verification

- An independent checker subagent gets only the new guide, checks every
  claim against the repo and dry-runs every command: `docker compose ...
  --dry-run`, `make -n`, `bash -n` on scripts, `DRY_RUN` paths only where
  they don't reach Azure. Nothing against Azure, a registry or the cluster.
  Fix and repeat until clean.
- `mkdocs build --strict` in a scratch venv with `pip install
  mkdocs-material`, as `docs.yml` does.
