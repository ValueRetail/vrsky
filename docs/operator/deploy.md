# Deploying to production

Production deploys itself. A merge to `main` runs every test job on the
merge commit; when all are green, the **Deploy to production** job in
`.github/workflows/build-push.yml` builds the images the merge touched in
the Azure Container Registry, rolls the matching deployments by digest with
the same scripts an operator runs by hand, and finishes with a smoke check.
Red stays red: a failed build, rollout or smoke check fails the run, and the
*CI failure alert* workflow posts it to Teams.

## What a merge deploys

`infrastructure/azure/affected.sh` maps the merge's changed paths to targets:

| Changed | Deployed |
|---|---|
| `ui/**` | the UI image and Deployment |
| `src/cmd/<service>/**` | that service's image; a core service is rolled by digest, a connector is restarted |
| `infrastructure/migrations/**`, `src/cmd/vrsky-agent/**` | the management API image and rollout (it runs the migrations at start and serves the Windows agent) |
| `src/pkg/**`, `src/internal/**`, `src/go.mod`, `src/go.sum`, any `src/cmd/*/Dockerfile` | every Go image, core and connectors |
| `infrastructure/prometheus-rules.yml`, `infrastructure/kubernetes/monitoring/**` | `install-monitoring.sh` (values and alert rules) |
| `infrastructure/azure/deploy-connectors-azure.sh`, `infrastructure/kubernetes/connectors/**` | the full connector manifest and a fleet restart |
| `*_test.go` and `testdata/**` under `src/`, on their own | nothing (#410); a rename counts both paths, so moving code into a `_test.go` file still deploys |
| anything else (docs, plans, workflows) | nothing; the job runs and reports "no targets" |

Not deployed automatically, and still done by hand as before: environment
variables and secrets on the management API (`kubectl set env` / a patch,
see [mail](mail.md) and [billing](billing.md)), node pools and other Azure
resources. Database migrations run when the management API starts, so they
ride along with its image as they always have.

Before anything rolls, every deploy that reaches a running cluster copies
`acr-pull` from `vrsky-platform` into `vrsky-database` and `vrsky-ui`
(`copy-acr-pull.sh`), and so does every Catch up. After an ACR password
rotation you update `acr-pull` in `vrsky-platform` only; the next deploy or
Catch up carries it over before the first pod pulls, so the backup Job and
the UI keep pulling. A namespace that doesn't exist yet is skipped. It
touches only those two Secrets ([backup](backup.md)). A failed copy fails
the run before the rollout. The log says which namespace and the exit
code, and nothing else: kubectl's
own error can contain the Secret, and the deploy log is readable by anyone
with repo access. To see kubectl's reason, run the apply yourself in your
own terminal.

Connectors restart on their own and restore their running pipelines on boot,
so a rollout needs no redeploy from the builder afterwards.

Most connectors roll: the new pod starts before the old one stops. The file
consumer doesn't (#425). It uses the `Recreate` strategy, so the old pod
stops first and nothing watches the file folders for the few seconds until
the new one is up. Files that land in that gap are published by the new
pod's first checks; uploads from the editor fail until it is up. With a
rolling update both pods watched the same folders for a moment, and a file
could be published twice. As a safeguard, `deploy-connectors-azure.sh`
switches a live Deployment that is still on RollingUpdate before it applies
the manifest. A plain `kubectl apply` handles the switch on its own; the
patch is the one Grafana needed under helm (see [monitoring](monitoring.md))
and does nothing once the Deployment is Recreate.

One case loses a file that a rolling update would have caught: a file that
lands in the gap while the new pod can't read the saved list of published
files at start (a database error, logged at Error). It counts as already
there and is never published.

## Watching a deploy

GitHub → Actions → the run for the merge. The *Plan the rollout* job lists
the changed paths and the targets; *Build … in ACR* runs one image per
runner; *Deploy to production* shows each rollout and the smoke output.
About four minutes for a UI change, ten for a change under `src/pkg`.

`infrastructure/azure/smoke-prod.sh [deployment …]` is the same check run
by hand: `/readyz` and the UI answer 200, each named consumer logged
*Restored running pipelines*, and nothing that matters is firing. What
matters is VRSky's own alerts (the `vrsky-*` rule groups) above `info`
and anything `critical` from any group; a stock warning that a fleet
restart produces by itself, such as `KubeClientErrors`, does not fail a
deploy (`smoke-alerts.sh`, table-tested). An alert that counts is re-read
every 15 seconds for up to two minutes before the check fails: a merge that
fixes a firing alert applies the new rule seconds before the check, and
Prometheus serves the old rule until it reloads. A clean run reads once.

## While the cluster is parked

The cluster is stopped between working sessions (`az aks stop`,
[cost parking](cost-parking.md)). A merge while it is stopped still builds
its images; the deploy job then reads the cluster's power state, deploys
nothing, and finishes **green** with a notice and a summary listing the
images waiting in the registry. After `az aks start`, one click brings
production to `main`:

**Actions → Catch up → Run workflow.** It refreshes the `acr-pull` copies
in `vrsky-database` and `vrsky-ui`, rolls every core service to its
current `:latest` digest (a no-op for anything already there), restarts
the connector fleet so it picks up its images (untick *connectors* to
skip that), applies the monitoring values and rules, and runs the smoke
check. Nothing is rebuilt. It refuses to run against a stopped cluster,
and it queues behind a deploy rather than interleaving with one.

You do not have to remember the click. **Production check**
(`drift-check.yml`) runs every half hour: while the cluster is running and
no build is in progress on `main`, it first checks the month's spend
against the budget and that the monitoring is alive (see
[cost parking](cost-parking.md) and [Monitoring](monitoring.md)), then
reads the digest every container actually runs
(`running-digests.sh`, in-cluster), compares it with the registry's
`:latest` (`drift.sh`), and when anything differs dispatches Catch up
itself, with the fleet restart only when a connector drifted. The run's
summary lists the drifted services; a clean run says "production is at
main". It writes nothing else. GitHub pauses scheduled workflows after
sixty days without a commit, which this repository has never gone.

## Rolling back

Revert the merge (`git revert`, open a PR, merge): the revert deploys like
any other change. For a single service in a hurry, an operator can still run
`infrastructure/azure/build-push-acr.sh` and `deploy-core-azure.sh` from the
previous commit; the deploy job and the scripts share the registry and the
digest discipline, so nothing fights.

## Pausing

GitHub → Settings → Environments → `production`: adding yourself as a
*required reviewer* turns every deploy into a click; disabling the workflow
stops them entirely. Both are settings, not code.

## How the job reaches Azure

OpenID Connect federation: an Entra app registration **VRSky deploy** trusts
tokens GitHub issues for this repository's `main` branch and for its
`production` environment (a job bound to an environment presents the
environment subject), so no password or secret is stored anywhere.

The cluster's API server accepts connections from a few approved addresses
only, and a GitHub runner is never one of them. So the job never talks to
the API server itself: every `kubectl` and `helm` runs **inside the
cluster** through Azure's command runner (`az aks command invoke`),
wrapped by `infrastructure/azure/in-cluster.sh`. The runner resolves image
digests (it has `az`; the in-cluster runtime does not), stages the deploy
scripts, manifests and monitoring values with their paths intact, uploads
them, and runs the same scripts an operator runs by hand. The in-cluster
runtime has bash, awk, grep, curl, jq, kubectl and helm 3, and **no sed,
python or envsubst**; `TestInClusterScriptsUseOnlyRuntimeTools` keeps the
scripts the jobs run in-cluster inside that set.

Roles the app holds: `AcrPush` and `Contributor` on `vrskyprodacr` (a
server-side `az acr build` needs to read the registry and queue a run);
`Contributor` on `vrsky-prod` (the command runner). One-time setup, by a
subscription Owner:

```bash
APP=$(az ad app create --display-name "VRSky deploy" --query appId -o tsv)
az ad sp create --id "$APP" >/dev/null
for SUBJECT in "repo:ValueRetail/vrsky:ref:refs/heads/main" "repo:ValueRetail/vrsky:environment:production"; do
  az ad app federated-credential create --id "$APP" --parameters "{\"name\":\"github-$(echo $SUBJECT | tr ':/' '--')\",\"issuer\":\"https://token.actions.githubusercontent.com\",\"subject\":\"$SUBJECT\",\"audiences\":[\"api://AzureADTokenExchange\"]}"
done
SP=$(az ad sp show --id "$APP" --query id -o tsv)
ACR=$(az acr show -n vrskyprodacr --query id -o tsv); AKS=$(az aks show -g vrsky-prod -n vrsky-prod --query id -o tsv)
az role assignment create --assignee-object-id "$SP" --assignee-principal-type ServicePrincipal --role AcrPush --scope "$ACR"
az role assignment create --assignee-object-id "$SP" --assignee-principal-type ServicePrincipal --role Contributor --scope "$ACR"
az role assignment create --assignee-object-id "$SP" --assignee-principal-type ServicePrincipal --role Contributor --scope "$AKS"
```

Then in GitHub → Settings → Secrets and variables → Actions → **Variables**
(none of these are secret): `AZURE_CLIENT_ID` (the app id), `AZURE_TENANT_ID`,
`AZURE_SUBSCRIPTION_ID`, `AKS_RESOURCE_GROUP`. And Settings → Environments →
New environment → `production`.

`in-cluster.sh <dir> <command…>` also works from a laptop with `az` signed
in and Contributor on the cluster: `infrastructure/azure/in-cluster.sh
stage "bash infrastructure/azure/smoke-prod.sh"`.
