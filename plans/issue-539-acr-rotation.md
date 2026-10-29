# No runbook for rotating the ACR password (#539)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Production pulls from `vrskyprodacr` with the admin user's password, kept
as the `acr-pull` Secret in `vrsky-platform`, `vrsky-storage`,
`vrsky-database` and `vrsky-ui`. Nothing says what to do after
`az acr credential renew`. Write `docs/operator/acr-rotation.md` with
commands only a person runs, and link it from the deploy docs.

## Acceptance criteria

1. The runbook covers: renew without printing the password; update
   `vrsky-platform` and `vrsky-storage` with the same commands
   `deploy-azure.sh` step 2/2b and `minio-images-acr.sh --repoint` run;
   refresh the `vrsky-database` and `vrsky-ui` copies now, with the next
   deploy or Catch up as the safety net (#497); check that every namespace
   pulls again.
2. No secret values, only placeholders and values read from `az` straight
   into the Secret. No step prints a secret.
3. Every command checked against the scripts in `infrastructure/azure/` by
   a reviewer.
4. Linked from `docs/operator/deploy.md`, `docs/operator/backup.md`,
   `docs/DEPLOYMENT_GUIDE.md` and the mkdocs nav; `mkdocs build --strict`
   passes.
5. Backlog item 99 struck with `(#539)`.

## Files

- `docs/operator/acr-rotation.md` (new)
- `docs/operator/deploy.md`, `docs/operator/backup.md`,
  `docs/DEPLOYMENT_GUIDE.md`, `mkdocs.yml`
- `plans/backlog.md`

## Decisions

- Targeted commands rather than re-running `deploy-azure.sh`, which
  redeploys the core platform and must never run against production.
- Renew `password` (`passwords[0]`), the one the scripts read. A no-gap
  variant through `password2` is described as an option.
- The copies are refreshed by running `copy-acr-pull.sh` straight away.
  Catch up does it too, but it also rolls everything and re-decides the
  backup CronJob's `suspend`, so it isn't the default.
- The check compares checksums of the four Secrets and starts a scratch
  pod per namespace with `imagePullPolicy: Always`, so the registry has to
  accept the credentials even when the node has the image.
- `deploy.md` said only `vrsky-platform` needs updating after a rotation.
  `vrsky-storage` has its own Secret, not a copy, so that sentence is fixed.
- No new automated test: this is docs only, and the docs workflow's
  strict build is the check that runs on it.
