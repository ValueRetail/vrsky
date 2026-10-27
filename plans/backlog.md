# Backlog

Work that has no GitHub issue yet. When no open issue is eligible, the
orchestrator (see `CLAUDE.md`) takes the top **agent** item, files it as an
issue with acceptance criteria and works it. Items marked **human** wait on
Ludvik; agents never work them, the progress log lists them. The PR that
closes an item strikes its line and adds the issue number.

Last updated 2026-10-27.

## Agent

1. **Stop the cluster at 16:00 from inside the cluster.** GitHub fires only
   5–6 of Park's 32 daily runs and the first lands hours late (2026-10-22:
   20:28 Oslo). A Kubernetes CronJob with `timeZone: Europe/Oslo` at 16:00
   calls `az aks stop` through a managed identity with workload identity;
   Park stays as the backstop. Plan first (`plans/cost-cap.md` has the
   context). Touches manifests and Azure identity: PR plus
   `agent:needs-human`, never merged by an agent.
2. **`messaging.ensure` updates every existing stream on every connector
   start** (`src/pkg/messaging/messaging.go`). Skip `UpdateStream` when the
   existing config already matches; it was the trigger for the nats-server
   race fixed in #339 and is log noise. NATS stream configuration: PR plus
   `agent:needs-human`.
3. ~~**`AGENTS.md` is out of date.**~~ (#354) It says "research phase, no code exists
   yet" and describes a planned layout. Rewrite it from the code: the real
   layout under `src/`, `ui/`, `infrastructure/`, the make targets, the
   test-database and live-API opt-ins, the guard tests.
4. ~~**Check the builder's "Deploy as new" path.**~~ (#355) After `ConnectionGone`,
   the confirm dialog calls `deployPipeline()` from the closure that saw the
   old `deployedConnectionId`; it may PUT the deleted id again instead of
   creating. Reproduce with a test first; fix only if it is real
   (`ui/src/pages/PipelineBuilder.tsx`).
11. ~~**Untrack the Go binaries in `src/`.**~~ (#360) `src/remote-agent`,
    `src/vrsky-cli` and `src/spill-e2e` are 34–64 MB Mach-O build outputs
    committed to git. Remove them from the index and add them to `.gitignore`.
12. ~~**The root Makefile forwards targets that don't exist.**~~ (#361) Eight of them
    (`docker-build`, `docker-push`, `run`, `e2e-test` and the `*-consumer`
    ones) are missing from `src/Makefile`, and root `make help` misses
    `up-core`, `up-core-remote` and `down-core`.
13. **`TestFileConsumer_WatchRoundTrip` looks flaky** (`src/cmd/file-consumer`).
    It failed once in a full `go test -race ./...` ("no matching envelope
    within 12s") and passed alone. Make it deterministic.
14. **Fix the stale comment on `messaging.MainRetention`**
    (`src/pkg/messaging/messaging.go`). It says WorkQueue retention removes
    acked messages at once, but `VRSKY_DATA` uses Limits retention and an
    ack deletes nothing. Comment only; it misled the first draft of #354.
15. ~~**Docs still name make targets that are gone.**~~ (#367) `docs/DEPLOYMENT_GUIDE.md`
    (lines ~120–129, in the mkdocs nav) and `PROJECT_STRUCTURE.md` (~127,
    238, 241, 286) tell readers to run `make docker-build`,
    `docker-build-consumer`, `docker-push*` and
    `docker-build-filter`/`-converter`, none of which exist any more. Point
    them at what builds images now, or drop the sections. Found in review of
    #365.
16. **`make docker-build-ui` can't build the UI** (`src/Makefile`). It runs
    `docker build -t vrsky/ui:latest ../ui`, so the build context is `ui/`,
    but `ui/Dockerfile` copies `ui/...` from the repo root. It fails with
    `"/ui/nginx.default.conf": not found`. `docker-push-ui` pushes to
    Docker Hub `vrsky/`, which nothing uses. Fix the context or drop both
    targets (and the root forwards). Found in #367.
17. ~~**The `ui-prod` service in `docker-compose.ui.yml` has the same broken
    context**~~ (#370) (`context: ./ui` with `dockerfile: Dockerfile`), so "Option 2:
    Build UI Production Image" in `docs/DEPLOYMENT_GUIDE.md` fails. The
    `ui` dev service already uses `context: .`. Found in #367.
18. **`docs/DEPLOYMENT_GUIDE.md` still describes GHCR.** The registry,
    image naming, image tags and Kubernetes sections name
    `ghcr.io/ValueRetail/vrsky/...`, but no workflow pushes there; images
    are built in ACR (`vrskyprodacr`). The local-development options also
    spell it `docker-compose` rather than `docker compose`. Found in #367.

## Human

5. **Grant the deploy identity Cost Management Reader** on both resource
   groups, then set the repository variable `MONTHLY_BUDGET_NOK=1500`:
   ```bash
   for rg in vrsky-prod MC_vrsky-prod_vrsky-prod_norwayeast; do az role assignment create --assignee-object-id 647ea665-ddad-4eed-8dad-2c244e882b51 --assignee-principal-type ServicePrincipal --role "Cost Management Reader" --scope /subscriptions/8c020b54-b237-4d6e-82ba-190f6a415d1d/resourceGroups/$rg; done
   ```
6. **First start on the ephemeral OS disk.** After the next `az aks start`,
   check every pod comes back and the smoke check passes.
7. **Rename the three production pipelines** in the builder (open, rename
   the tab, Deploy); they are still named after the time of a deploy.
8. **Delete the four disk snapshots after 2026-10-29**
   (`nats-{0,1,2}-before-shrink-20261022`, `minio-before-shrink-20261022`
   in `MC_vrsky-prod_vrsky-prod_norwayeast`).
9. **Sitoo ⇄ Business Central templates.** Need a field mapping tested
   against a real Sitoo sandbox and the BC trial before they return to the
   gallery (`plans/template-fields.md`).
10. **Close #266** (the remote agent shipped).
