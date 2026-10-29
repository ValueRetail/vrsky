# Rotating the ACR password

Production pulls its images from the Azure Container Registry
`vrskyprodacr` with the registry's admin user. The password sits in the
cluster as a Secret called `acr-pull`, in four namespaces:

| Namespace | Who pulls with it | Where the Secret comes from |
|-----------|-------------------|-----------------------------|
| `vrsky-platform` | the core services, the connectors, the management API | `deploy-azure.sh` step 2, from `az acr credential show` |
| `vrsky-storage` | MinIO | `deploy-azure.sh` step 2b and `minio-images-acr.sh --repoint`, from `az acr credential show` |
| `vrsky-database` | the backup Job (`vrsky-mgmt-backup`) | a copy of `vrsky-platform`'s, by `copy-acr-pull.sh` |
| `vrsky-ui` | the UI | a copy of `vrsky-platform`'s, by `copy-acr-pull.sh` |

After `az acr credential renew` the old password stops working at once.
Pods that are already running keep running, but every new pull with the old
password fails: a pod rescheduled after a node restart, the weekday backup
Job, a rollout, and every pod after `az aks start` (the nodes have
ephemeral OS disks, so a started cluster pulls everything again). So the
Secrets have to follow the renew straight away.

The GitHub deploy never reads the ACR password. It builds with its own
Azure identity and copies `acr-pull` from `vrsky-platform` into
`vrsky-database` and `vrsky-ui` before anything rolls. So you update
`vrsky-platform` and `vrsky-storage` by hand, and the copies follow.

All of this is done by a person, from their own terminal. No step is
automated and nothing here belongs in a workflow.

## Before you start

- The cluster must be **running** (`az aks show -g vrsky-prod -n vrsky-prod
  --query powerState.code -o tsv` says `Running`). A stopped cluster can't
  take the new Secret, and on the next start every pull would fail. Leave
  enough time before the 16:00 stop ([cost parking](cost-parking.md)).
- You need `az` signed in with rights on `vrskyprodacr`, and `kubectl`
  pointed at `vrsky-prod` (`az aks get-credentials -g vrsky-prod -n
  vrsky-prod`) from one of the addresses the API server accepts, as for any
  other hand-run script. `jq` is needed for step 3.
- Don't run any of this with `set -x` or `bash -x`. The trace would print the
  password.
- Don't re-run `deploy-azure.sh` to refresh the Secret. It redeploys the
  whole core platform and must never run against production
  ([deployment guide](../DEPLOYMENT_GUIDE.md)).
- Pick a moment with no deploy running (GitHub → Actions → *Build, push
  and deploy* and *Catch up*), so nothing rolls between steps 1 and 3.

## 1. Renew the password

The scripts read `passwords[0]`, which Azure calls `password`. Renew that
one:

```bash
az acr credential renew -n vrskyprodacr --password-name password -o none
```

`-o none` matters: without it the command prints both new passwords.

`password2` is left alone. If anything goes wrong below, it still works,
and you can point the Secrets at it temporarily with `IDX=1` in step 2.

**Without a gap.** Between this renew and steps 2 and 3, every new pull
fails (a pod that moves, a scale-up). For a short window that's usually
fine. If you want none at all, do steps 2 and 3 with `IDX=1` first, so the
cluster pulls with `password2`, then renew `password` here, then do steps 2
to 4 again with `IDX=0`.

## 2. Update `vrsky-platform` and `vrsky-storage`

These are the same commands `deploy-azure.sh` runs in step 2 and
`minio-images-acr.sh --repoint` runs for MinIO, without the rest of those
scripts. The password goes from `az` into a shell variable and on into the
Secret, and is never shown. `IDX=0` is `password`, the one the scripts use:

```bash
REG=vrskyprodacr IDX=0
ACR_USER="$(az acr credential show -n "$REG" --query username -o tsv)"
ACR_PASS="$(az acr credential show -n "$REG" --query "passwords[$IDX].value" -o tsv)"
if [ -z "$ACR_USER" ] || [ -z "$ACR_PASS" ]; then
  echo "az returned no ACR credentials; nothing applied"
else
  for ns in vrsky-platform vrsky-storage; do
    kubectl create secret docker-registry acr-pull \
      --docker-server="$REG.azurecr.io" \
      --docker-username="$ACR_USER" --docker-password="$ACR_PASS" \
      -n "$ns" --dry-run=client -o yaml | kubectl apply -f -
  done
fi
unset ACR_PASS
```

Each apply prints `secret/acr-pull configured`. If an apply fails, kubectl's
error can contain the new password, so don't paste it anywhere.

The service accounts already name `acr-pull`, so there is nothing to patch.

## 3. Refresh the copies in `vrsky-database` and `vrsky-ui`

Run the deploy job's copy step yourself now, from the repository root:

```bash
bash infrastructure/azure/copy-acr-pull.sh
```

It copies `vrsky-platform`'s `acr-pull` into `vrsky-database` and
`vrsky-ui`, prints `secret/acr-pull configured` for each, and touches nothing
else; the backup CronJob's `suspend` is left as it is. If you'd rather go
through Azure's command runner, as the deploy job does:

```bash
infrastructure/azure/in-cluster.sh infrastructure/azure "bash copy-acr-pull.sh"
```

Running *Catch up* by hand (GitHub → Actions → Catch up → Run workflow) does
the same as its first step, before it rolls anything. But it also rolls
the core services, restarts the connectors, applies the monitoring values
and re-applies the backup CronJob, which decides `suspend` again: a CronJob
you suspended by hand for a restore is resumed once `vrsky-backup-config`
exists ([backup](backup.md)). Use it only if production should be caught
up anyway.

If you skip this step, the next deploy that reaches the running cluster
and the next Catch up still refresh both copies before anything rolls
(#497). Until then the UI and the backup Job pull with the old
password, so a UI pod that moves or the next 12:00 backup would fail to
pull.

## 4. Check that every namespace pulls

First check that all four Secrets are the same. This prints a checksum per
namespace, never the Secret:

```bash
for ns in vrsky-platform vrsky-storage vrsky-database vrsky-ui; do
  printf '%-16s ' "$ns"
  kubectl -n "$ns" get secret acr-pull -o jsonpath='{.data.\.dockerconfigjson}' | shasum -a 256
done
```

All four lines must show the same checksum. A different one is a namespace
that missed step 2 or 3.

Then make each namespace really pull from ACR. A scratch pod with
`imagePullPolicy: Always` has to authenticate even when the node already
has the image:

```bash
IMAGE=vrskyprodacr.azurecr.io/vrsky/vrsky-cli:latest
for ns in vrsky-platform vrsky-storage vrsky-database vrsky-ui; do
  kubectl -n "$ns" run acr-pull-check --restart=Never --image="$IMAGE" \
    --overrides='{"spec":{"imagePullSecrets":[{"name":"acr-pull"}],"containers":[{"name":"c","image":"'"$IMAGE"'","imagePullPolicy":"Always","args":["--help"]}]}}'
done
sleep 30
for ns in vrsky-platform vrsky-storage vrsky-database vrsky-ui; do
  printf '%-16s %s\n' "$ns" "$(kubectl -n "$ns" get pod acr-pull-check -o jsonpath='{.status.containerStatuses[0].state}')"
done
```

- `terminated` or `running`: the image was pulled. The exit code of
  `--help` doesn't matter.
- `waiting` with `ErrImagePull` or `ImagePullBackOff`: that namespace's
  Secret doesn't work. `kubectl -n <namespace> describe pod acr-pull-check`
  gives the reason (`401 Unauthorized` is a wrong password).
- `waiting` with `ContainerCreating`, or nothing at all (the pod isn't
  scheduled yet): run the second loop again in a few seconds.

Then remove the scratch pods. `--wait` lets the delete finish, so after a
fix you can start the check again without an "already exists" error:

```bash
for ns in vrsky-platform vrsky-storage vrsky-database vrsky-ui; do
  kubectl -n "$ns" delete pod acr-pull-check --wait
done
```

## 5. Afterwards

- Look at the next deploy or Catch up run and check that *Refresh the
  acr-pull copies in vrsky-database and vrsky-ui* is green.
- The next weekday backup should run as usual ([backup](backup.md)). A
  failed pull there shows up as `MgmtBackupMissing`.
- If you used `IDX=1` in step 2 as a stopgap, redo steps 2 to 4 with
  `IDX=0` once things work. The scripts read `passwords[0]`, so
  a later `minio-images-acr.sh --repoint` would switch `vrsky-storage` back
  to it anyway.
