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
| `src/pkg/**`, `src/go.mod`, `src/go.sum`, any `src/cmd/*/Dockerfile` | every Go image, core and connectors |
| `infrastructure/prometheus-rules.yml`, `infrastructure/kubernetes/monitoring/**` | `install-monitoring.sh` (values and alert rules) |
| `infrastructure/azure/deploy-connectors-azure.sh`, `infrastructure/kubernetes/connectors/**` | the full connector manifest and a fleet restart |
| anything else (docs, plans, workflows, tests only) | nothing; the job runs and reports "no targets" |

Not deployed automatically, and still done by hand as before: environment
variables and secrets on the management API (`kubectl set env` / a patch,
see [mail](mail.md) and [billing](billing.md)), node pools and other Azure
resources. Database migrations run when the management API starts, so they
ride along with its image as they always have.

Connectors restart on their own and restore their running pipelines on boot,
so a rollout needs no redeploy from the builder afterwards.

## Watching a deploy

GitHub → Actions → the run for the merge. The *Plan the rollout* job lists
the changed paths and the targets; *Build … in ACR* runs one image per
runner; *Deploy to production* shows each rollout and the smoke output.
About four minutes for a UI change, ten for a change under `src/pkg`.

`infrastructure/azure/smoke-prod.sh [deployment …]` is the same check run
by hand: `/readyz` and the UI answer 200, each named consumer logged
*Restored running pipelines*, and nothing is firing besides the Watchdog.

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
tokens GitHub issues for `repo:ValueRetail/vrsky:ref:refs/heads/main`, so no
password or secret is stored anywhere. The app holds `AcrPush` on
`vrskyprodacr` and *Azure Kubernetes Service Cluster User Role* on
`vrsky-prod`. One-time setup, signed in as an owner:

```bash
APP=$(az ad app create --display-name "VRSky deploy" --query appId -o tsv)
az ad sp create --id "$APP" >/dev/null
az ad app federated-credential create --id "$APP" --parameters '{"name":"github-main","issuer":"https://token.actions.githubusercontent.com","subject":"repo:ValueRetail/vrsky:ref:refs/heads/main","audiences":["api://AzureADTokenExchange"]}'
SP=$(az ad sp show --id "$APP" --query id -o tsv)
az role assignment create --assignee-object-id "$SP" --assignee-principal-type ServicePrincipal --role AcrPush --scope "$(az acr show -n vrskyprodacr --query id -o tsv)"
az role assignment create --assignee-object-id "$SP" --assignee-principal-type ServicePrincipal --role "Azure Kubernetes Service Cluster User Role" --scope "$(az aks show -g <resource group> -n vrsky-prod --query id -o tsv)"
```

If the cluster is Entra-integrated with Azure RBAC, add *Azure Kubernetes
Service RBAC Writer* on the namespaces `vrsky-platform`, `vrsky-ui` and
`vrsky-monitoring` as well; with local accounts the Cluster User role is
enough.

Then in GitHub → Settings → Secrets and variables → Actions → **Variables**
(none of these are secret): `AZURE_CLIENT_ID` (the app id), `AZURE_TENANT_ID`,
`AZURE_SUBSCRIPTION_ID`, `AKS_RESOURCE_GROUP`. And Settings → Environments →
New environment → `production`.
