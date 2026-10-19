# Automatic rollout to production from GitHub Actions

Checked in the repo and the cluster on 2026-10-19.

## Open questions

1. **Fully automatic, or an approval click?** Recommended: **fully
   automatic** on every merge to `main` whose tests pass — that is the point
   of the change — serialised by a concurrency group and followed by a smoke
   check that fails the job loudly (the existing *CI failure alert* workflow
   turns a red `main` into a Teams card). Alternative: a GitHub *production*
   environment with you as required reviewer, one click per deploy. Both
   use the same job; the difference is one setting on the environment,
   changeable later without a code change.
2. **How Actions authenticates to Azure.** Recommended: **OpenID Connect
   federation** — an Entra app registration "VRSky deploy" with a federated
   credential for `repo:ValueRetail/vrsky:ref:refs/heads/main`, so there is
   **no secret to store or rotate**, only three role assignments: `AcrPush`
   on `vrskyprodacr`, *Azure Kubernetes Service Cluster User Role* on
   `vrsky-prod`, and the in-cluster rights `deploy-core-azure.sh` needs
   (set image, rollout status, get pods, apply the connector manifest,
   helm for the rules). Alternative: a service principal secret in GitHub
   Secrets; works the same, expires, and leaks if the repo ever does.
3. **What auto-deploys.** Recommended: **what the merge touched** — the
   UI, the core services, the connectors whose source changed (all of them
   when `src/pkg/**`, `go.mod` or a Dockerfile changed), and the alert
   rules / monitoring values. **Not** auto-deployed: Kubernetes env or
   secret changes to the management API (still a patch by hand, as today),
   node-pool or Azure resource changes. Database migrations already run on
   management-api start, so they ride along as they do now.
4. **Rebuilding 31 connector images** takes ~90 minutes sequentially
   (`az acr build` ≈ 3 min each). Recommended: a **matrix job, one image
   per runner**, so a full rebuild is ~5 minutes wall-clock. ACR builds the
   image server-side; the runner only uploads the context.

## What the first runs found (2026-10-19)

- A job bound to the `production` environment presents the OIDC subject
  `repo:…:environment:production`, not the branch: a second federated
  credential for it.
- `success()` is implicit on a job without a status function and looks at
  every ancestor; the skipped UI test job (no UI change) skipped the image
  build: `!cancelled()` on it.
- `AcrPush` cannot queue a server-side build; `Contributor` on the registry.
- The API server's authorized IP ranges exclude GitHub runners. Rather than
  opening ranges per run, the deploy job runs every `kubectl`/`helm`
  **inside the cluster** via `az aks command invoke` (`in-cluster.sh`);
  the runner resolves digests (`DIGESTS_FILE` in `deploy-core-azure.sh`)
  and uploads the staged files. Needs `Contributor` on the cluster. The
  smoke check uses jq (the runtime has no python3).

## Goal

Merge to `main` → tests → the affected images are built in ACR → the
affected deployments roll by digest → a smoke check → green, or a red run
and a Teams card. Nobody runs `build-push-acr.sh` or `deploy-core-azure.sh`
by hand for an ordinary change. The sentence "automatisk utrulling via
GitHub Actions" becomes true.

## What exists (checked)

- `build-push.yml` job `build-and-push` builds **six** images with Docker
  on the runner and pushes them to `ghcr.io/valueretail/vrsky/*`. **Nothing
  in production reads them**: prod pulls 36 images from `vrskyprodacr`,
  built by `infrastructure/azure/build-push-acr.sh` (`az acr build`,
  server-side). The ghcr images were the pre-Azure path (#223 notes the
  manifests still name ghcr images, which is why the deploy script sets
  images by digest instead of applying files).
- `deploy-core-azure.sh <service…>`: reads each `:latest` digest from ACR,
  validates it, `kubectl set image` by digest, waits for the rollout,
  verifies. `deploy-connectors-azure.sh`: renders and applies the
  connector manifest, restarts the fleet; the connectors restore their
  pipelines on boot (#306). `install-monitoring.sh`: helm + the
  PrometheusRule. All three are what a human runs today and all three are
  what the job will run — the repo's guards (`TestConnectorImagesAreBuilt`,
  `TestNodeConfigRulesAreDeployed`) keep watching the same files.
- The `changes` job already classifies a PR's paths (`ui`, `src`); it runs
  on pull requests only.
- Rollback today: rebuild from the previous commit by hand.
- The `az` CLI on this machine is signed out (your password reset on
  2026-10-18 revoked its token); `kubectl` still works on its own kubeconfig.

## Approach

### 1. `infrastructure/azure/affected.sh`: which services a change touches

Reads changed paths on stdin (the workflow feeds `git diff --name-only
<before> <after>`), prints one target per line:

| Path | Target |
|---|---|
| `ui/**` | `ui` |
| `src/cmd/<name>/**` | `<name>` (any of the 36 services) |
| `src/pkg/**`, `src/go.mod`, `src/go.sum`, any `Dockerfile` | `all-go` (every Go image) |
| `infrastructure/prometheus-rules*.yml`, `infrastructure/kubernetes/monitoring/**` | `monitoring` |
| `infrastructure/azure/deploy-connectors-azure.sh`, `infrastructure/kubernetes/connectors/**` | `connectors-manifest` |
| anything else | nothing |

Pure text in, text out, so `TestAffectedServices` in Go drives it with
path lists and checks the output exactly.

### 2. The workflow: `deploy` job in `build-push.yml`

```yaml
deploy:
  name: Deploy to production
  needs: [go-tests, test, agent-windows, agent-binaries]   # all green on this commit
  if: github.event_name == 'push' && github.ref == 'refs/heads/main'
  environment: production          # the approval switch lives here
  concurrency: { group: production, cancel-in-progress: false }
  permissions: { id-token: write, contents: read }
  steps:
    - checkout (fetch-depth: 2)
    - affected = affected.sh < git diff --name-only HEAD^ HEAD
    - azure/login@v2 with client-id / tenant-id / subscription-id (OIDC, no secret)
    - az aks get-credentials; kubelogin convert-kubeconfig -l azurecli
    - build: matrix over the affected images → build-push-acr.sh style az acr build (one per runner)
    - deploy core: deploy-core-azure.sh <affected core names>
    - deploy connectors: deploy-connectors-azure.sh when any connector or the manifest changed
      (restarts only the affected deployments when the manifest did not change)
    - monitoring: PROFILE=azure install-monitoring.sh when `monitoring`
    - smoke: /readyz 200, UI 200, the restored-pipelines log line on each restarted consumer, no new alert firing
```

The `build-and-push` ghcr job is **removed**: it costs ~10 minutes a run
and ships to a registry nothing uses. (`docker-compose` and the k3d path
build locally; unchanged.)

`agent-binaries` and `agent-windows` stay as they are and gate the deploy.

### 3. Azure side (one-time, with you signed in)

```bash
az login --tenant 093542aa-492d-4505-8c37-80d4b0eeaef0     # you, interactively
az ad app create --display-name "VRSky deploy"
az ad sp create --id <appId>
az ad app federated-credential create --id <appId> --parameters '{"name":"github-main","issuer":"https://token.actions.githubusercontent.com","subject":"repo:ValueRetail/vrsky:ref:refs/heads/main","audiences":["api://AzureADTokenExchange"]}'
az role assignment create --assignee <spId> --role AcrPush --scope <acr id>
az role assignment create --assignee <spId> --role "Azure Kubernetes Service Cluster User Role" --scope <aks id>
```

plus, if the cluster is Entra-integrated with Azure RBAC, *Azure Kubernetes
Service RBAC Writer* on the namespaces `vrsky-platform`, `vrsky-ui`,
`vrsky-monitoring`; if it uses local accounts, the Cluster User role is
already enough. Confirmed during the build (the `az` session is needed
for that). The three IDs (client, tenant, subscription) go into GitHub as
*variables*, not secrets — they are not sensitive.

If you choose the approval gate: GitHub → Settings → Environments →
`production` → required reviewers: you.

### 4. Docs

`docs/operator/deploy.md` (new, or the section in the install doc):
what deploys automatically, what does not, how to roll back (`git revert`
and merge; or `deploy-core-azure.sh` by hand from a previous commit), how
to pause (disable the environment or the workflow), and the Azure identity.
`README` deploy section and the memory note updated.

## Files

| File | Change |
|---|---|
| `infrastructure/azure/affected.sh` (new) | path → target mapping |
| `.github/workflows/build-push.yml` | `deploy` job; `build-and-push` (ghcr) removed |
| `src/pkg/managementapi/nodeconfig_test.go` | `TestAffectedServices`; `TestDeployJobGatesOnTests` (the job `needs` every test job and runs only on push to main) |
| `docs/operator/deploy.md`, `README.md` | as above |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestAffectedServices` (table: ui-only, one connector, `src/pkg` change, rules change, docs-only, mixed) | the mapping | map `src/pkg` to nothing |
| `TestDeployJobGatesOnTests` | `deploy.needs` includes every job that runs tests; `if` pins push + main; `environment: production`; `concurrency` set | drop a job from `needs` |
| Existing `TestConnectorImagesAreBuilt`, `TestNodeConfigRulesAreDeployed` | the scripts the job calls still cover every connector | — |
| The first real run | a docs-only merge deploys nothing; a UI-only merge rolls the UI in ~4 minutes | — |

## Verify

1. Merge the PR itself (it touches `src/pkg` via the test file → `all-go`):
   the matrix builds every Go image in parallel, the fleet rolls, the
   smoke check passes. Expect ~10 minutes.
2. A docs-only follow-up merge: the deploy job runs and does nothing.
3. Pull the plug once on purpose: `kubectl scale deploy/vrsky-ui --replicas=0`
   then a UI merge → the smoke check fails the run → Teams card.

## Risks

- **Every merge is a production deploy.** That is the request; the gates
  are the test jobs, the serialised concurrency, the digest validation
  already in the script, and the smoke check. The approval switch is one
  setting away if it ever feels wrong.
- **A half-built matrix** (one image fails to build) must not deploy the
  rest: the deploy steps `need` the whole matrix.
- **Two merges in quick succession** queue, never interleave (concurrency
  group, no cancel).
- **The federated identity** is scoped to `main` on this repository; a PR
  or a fork cannot assume it.

## Non-goals

Deploying to a TEST environment first (there is none today); canary or
blue-green; deploying Kubernetes env/secret changes; pinning versions
(`:latest` + digest stays the model); the agent binaries' release flow.
