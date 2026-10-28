# Backlog

Work that has no GitHub issue yet. When no open issue is eligible, the
orchestrator (see `CLAUDE.md`) takes the top **agent** item, files it as an
issue with acceptance criteria and works it. Items marked **human** wait on
Ludvik; agents never work them, the progress log lists them. The PR that
closes an item strikes its line and adds the issue number.

Last updated 2026-10-28.

## Agent

1. ~~**Stop the cluster at 16:00 from inside the cluster.**~~ (#352) GitHub fires only
   5–6 of Park's 32 daily runs and the first lands hours late (2026-10-22:
   20:28 Oslo). A Kubernetes CronJob with `timeZone: Europe/Oslo` at 16:00
   calls `az aks stop` through a managed identity with workload identity;
   Park stays as the backstop. Plan first (`plans/cost-cap.md` has the
   context). Touches manifests and Azure identity: PR plus
   `agent:needs-human`, never merged by an agent.
2. ~~**`messaging.ensure` updates every existing stream on every connector
   start**~~ (#353) (`src/pkg/messaging/messaging.go`). Skip `UpdateStream` when the
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
13. ~~**`TestFileConsumer_WatchRoundTrip` looks flaky**~~ (#362) (`src/cmd/file-consumer`).
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
16. ~~**`make docker-build-ui` can't build the UI**~~ (#369) (`src/Makefile`). It runs
    `docker build -t vrsky/ui:latest ../ui`, so the build context is `ui/`,
    but `ui/Dockerfile` copies `ui/...` from the repo root. It fails with
    `"/ui/nginx.default.conf": not found`. `docker-push-ui` pushes to
    Docker Hub `vrsky/`, which nothing uses. Fix the context or drop both
    targets (and the root forwards). Found in #367.
17. ~~**The `ui-prod` service in `docker-compose.ui.yml` has the same broken
    context**~~ (#370) (`context: ./ui` with `dockerfile: Dockerfile`), so "Option 2:
    Build UI Production Image" in `docs/DEPLOYMENT_GUIDE.md` fails. The
    `ui` dev service already uses `context: .`. Found in #367.
18. ~~**`docs/DEPLOYMENT_GUIDE.md` still describes GHCR.**~~ (#371) The registry,
    image naming, image tags and Kubernetes sections name
    `ghcr.io/ValueRetail/vrsky/...`, but no workflow pushes there; images
    are built in ACR (`vrskyprodacr`). The local-development options also
    spell it `docker-compose` rather than `docker compose`. Found in #367.
19. ~~**The file watcher can publish a half-written file**~~ (#374)
    (`src/cmd/file-consumer/service.go`, `watchDirectory`). A new name is
    read on the first poll that sees it, with no check that the writer is
    done, and once the name is known the finished file is never published.
    Wait until size and mtime are unchanged across two polls (or skip
    dotfiles and temp names) before ingesting. Found during #362.
20. ~~**Two starts for one file connection can race**~~ (#375)
    (`src/cmd/file-consumer/service.go`, `startConnection`). The "already
    active" check and the insert are separate lock sections, and the boot
    restore runs alongside the start-command handler, so a start command
    arriving during restore could start a second watcher for the same
    connection. Check and insert under one lock. Found during #362.
21. ~~**The `ui-prod` image exits at once when run locally.**~~ (#377) nginx runs as a
    non-root user and can't write `/var/run/nginx.pid` (`Permission
    denied`). Production avoids it with an emptyDir at `/var/run`
    (`infrastructure/kubernetes/ui/deployment.yaml`). A tmpfs on `/var/run`
    (uid/gid 101) for `ui-prod` in `docker-compose.ui.yml`, or a pid path
    the image can write, would make it run. Found in #370.
22. ~~**The Dockerfile guard test doesn't apply `.dockerignore`**~~ (#385)
    (`src/pkg/managementapi/makefile_guard_test.go`). It checks that each
    COPY/ADD source in `ui/Dockerfile` exists under the build context, but
    not that the root `.dockerignore` lets it through. Changing `!ui` to
    `!ui/src` still passes the guard while `COPY ui/package*.json` would
    fail, and the first place that shows up is a failed ACR build on main
    after the merge. Match paths the way Docker does (probably
    `github.com/moby/patternmatcher`) and check every Dockerfile, not only
    the UI's. Found in review of #369.
23. ~~**`ui/.dockerignore` is dead.**~~ (#380) Every image, the UI included, now builds
    with the repo root as context, so Docker reads only the root
    `.dockerignore`. Delete it, or fold anything it still says into the
    root file. Found in review of #369.
24. ~~**The docs tell people to copy a `.env.example` that doesn't exist.**~~ (#381)
    `README.md`, `docs/operator/install.md` and `docs/operator/windows.md`
    say `cp .env.example .env`; there is no root `.env.example` (only
    `ui/.env.example`), and `docker-compose.yml` has defaults for every
    variable. Add the file with the variables that matter or change the
    docs. Found in #371.
25. **`infrastructure/azure/deploy-ui-azure.sh` isn't executable** (mode
    100644; every other script there is 100755). The guide now says `bash
    ...`. `infrastructure/azure/**`: PR plus `agent:needs-human`. Found in
    #371.
26. **The k3d path doesn't line up with its manifests.**
    `k3d-load-images.sh` imports `data-filter` and `data-converter` as
    `localhost:5000/vrsky/...` while their manifests pull
    `ghcr.io/valueretail/vrsky/...`, loads no UI image, and the UI manifest's
    `ghcr.io/ValueRetail/...` is an invalid (uppercase) reference.
    `TestUIDeployRewritesMatchManifest` and `deploy-ui-azure.sh`'s rewrite
    depend on that string, so change them together. Manifests: PR plus
    `agent:needs-human`. Found in #371.
27. ~~**The backup CronJob's image is built nowhere.**~~ (#395)
    `infrastructure/kubernetes/backup/cronjob.yaml` runs
    `ghcr.io/valueretail/vrsky/vrsky-cli:latest`, and its README says CI
    builds it, but no workflow or script builds `vrsky-cli` into any
    registry. Find out whether the CronJob runs on AKS; if it does, it can't
    pull. Manifests: PR plus `agent:needs-human`. Found in #371.
28. ~~**Two out-of-date claims left in `docs/DEPLOYMENT_GUIDE.md`.**~~ (#386) #371
    rewrote the guide down to "Encryption at rest" and left the rest alone.
    "Disaster Recovery" says the management Postgres "is backed up daily",
    but the backup CronJob's image is built nowhere (item 27), so check
    whether any backup runs in prod. The encryption table says TLS
    terminates at Traefik; production runs ingress-nginx. Fix both (and
    `docs/DR.md` if it says the same). Found in review of #376.
29. **A watched file replaced under the same name is never published again**
    (`src/cmd/file-consumer/service.go`, `pollOnce`). The watcher keys on
    the name only: once `orders.csv` is published, a writer that overwrites
    it or renames a new `orders.csv` over it is ignored, unless the name
    disappeared for at least one poll in between. Decide whether a changed
    size or mtime on a known name means a new file. Found during #374.
30. ~~**Files still settling at a stop or restart are never published**~~ (#391)
    (`src/cmd/file-consumer/service.go`, `startConnection`). The baseline
    taken at start counts every name in the directory as already there, so
    a file that landed before a stop or restart but had not been published
    yet is skipped for good. That was true before #374 too, but the window
    has grown from at most 5 s to at most 10 s now that a file needs two
    unchanged polls. Persisting what was published, or treating files newer
    than the last stop as new, would close it. Found in review of #374.
31. ~~**`docker-compose.ui.yml` sets `VITE_API_BASE_URL` and
    `VITE_ENVIRONMENT`**~~ (#392) on both the `ui` dev service and `ui-prod`. The UI
    never reads either name (`ui/src/config/env.ts` reads `VITE_API_URL`,
    `VITE_WS_URL` and others), so they do nothing and suggest the API URL
    can be set there. Drop them from both services. The file's `version:`
    key is obsolete too (compose warns).
    `infrastructure/kubernetes/ui/configmap.yaml` has the same two keys,
    already commented as dead; dropping them there is a manifest change
    for a human-reviewed PR. Found in #377.
32. **The production UI bundle ships its source maps.** `ui/vite.config.ts`
    sets `build.sourcemap: true`, so the image serves
    `assets/index-*.js.map` (about 3.8 MB) next to the bundle, and anyone can
    read the original TypeScript. Decide whether that's wanted; if not, turn
    it off for the production build or upload the maps somewhere private.
    Found in #380.
33. ~~**The PowerShell key generator in `docs/operator/windows.md` isn't
    cryptographic.**~~ (#387) It builds `ENCRYPTION_KEY` from `Get-Random`, which is
    `System.Random`, not a CSPRNG. Use
    `[System.Security.Cryptography.RandomNumberGenerator]` instead
    (`::Create().GetBytes($b)` works on Windows PowerShell 5.1 as well as 7),
    and say so next to the `openssl rand -hex 32` advice. Found in review of
    #381.
34. **No way to mount a Windows home folder for the file connectors.**
    `docker-compose.yml` mounts `${HOME:-./data/input}` at
    `${HOME:-/data/input}` for `file-consumer` and `file-producer`, the same
    path on both sides. A Windows path can't be a target in the Linux
    engine ("mount path must be absolute"), so setting `HOME` on Windows
    breaks `make up-core`, and the stack falls back to `data/input`. A
    proper fix needs a separate source and target (for example a host-path
    variable mounted at a fixed Linux path, with `HOST_HOME` translating
    editor paths), plus a check that a Unix-style shell's
    `HOME=/c/Users/...` doesn't hit the same trap. Found in review of #381.
35. ~~**The other consumers have the start race #375 fixed in the file
    consumer.**~~ (#397) db, api, salesforce, cloud-storage, sftp, sitoo, brightpearl,
    visma, business-central, kafka, rabbitmq, sap-s4hana and tenant each
    check "already active" under `RLock`, release it, do the slow work and
    insert later, while `sdk.RestoreRunning` runs alongside the
    start-command handler. A start command during boot restore can start a
    second poller for one connection. Port the reservation from `src/cmd/file-consumer/service.go`
    (`reserveStart`/`activateStart`/`releaseStart`), or move it into the SDK
    so each consumer gets it once. Until then the comment on
    `RestoreRunning` (`src/pkg/sdk/restore.go`) that a start command during
    the scan is harmless holds only for the file consumer. Found during #375.
36. **A stop just after a file connection becomes active can leave the
    database saying `running`** (`src/cmd/file-consumer/service.go`,
    `startConnection`). The `running` status write comes after the
    connection is in `activeConnections`. A stop landing in between cancels
    the watcher and writes `stopped`, then the start writes `running`. Older
    than #375, which closed the same gap for a stop that lands earlier in
    the start. Found during #375.
37. **A start refused while another start is in flight is lost if that one
    fails** (`src/cmd/file-consumer/service.go`, `startConnection`). Since
    #375 a second start for a connection that is already starting returns
    at once. If the first then fails (say a transient database error during
    boot restore), the second start, which might have succeeded, is not
    retried and the connection stays down until someone starts it again.
    A "start again" flag on the reservation would cover it. Found by the
    test author on #375.
38. **A test-only change rebuilds every Go image**
    (`infrastructure/azure/affected.sh`, the `src/pkg/*` case around line
    36). Any change under `src/pkg/`, `_test.go` files included, marks all
    Go images as affected, so a PR that only adds a test (#379's guard test,
    for one) rebuilds about 37 images in ACR and restarts every connector
    whose image digest changed. Skip `_test.go` files (and probably
    `testdata/`) there and in the `src/cmd/*` case. The script is under
    `infrastructure/azure/**`, so the fix is a PR labelled
    `agent:needs-human`, not an agent merge. Found during #375.
39. ~~**The `ui` dev service in `docker-compose.ui.yml` can't reach the API.**~~ (#399)
    The axios client (`ui/src/services/api.ts`) always sends relative
    `/api/...` requests, and Vite's proxy in `ui/vite.config.ts` forwards
    them to a hardcoded `http://localhost:3000`. Inside the container that
    is the container itself, so every API call fails; no `VITE_*` variable
    changes it. `npm run dev` on the host doesn't have the problem, since
    the local stack publishes the management API on `127.0.0.1:3000`.
    `host.docker.internal` won't help: that port is published on
    127.0.0.1 only. A fix that works on every platform: let
    `vite.config.ts` take the proxy target from an env var (default
    `http://localhost:3000`), put the `ui` service on the stack's external
    `vrsky-network`, and set the target to `http://management-api:3000`.
    Or drop the dev service and point "Option 1" in
    `docs/DEPLOYMENT_GUIDE.md` at `npm run dev`. Found in #392.
40. **Two file-consumer pods watch the same folders during a deploy.**
    The file-consumer Deployment (`infrastructure/kubernetes/connectors/connectors.yaml`)
    has one replica and the default RollingUpdate strategy, so the new pod
    starts and restores every running file connection before the old one
    stops. For those seconds both pods poll the same folders on the shared
    volume, and a file that lands then can be published by both. #391 has
    each watcher re-read the saved done set before publishing, which
    narrows this, but only `strategy: Recreate` (or a per-connection lease)
    closes it. Manifest change: PR plus `agent:needs-human`. Found during
    #391.
41. **The file consumer's saved done set is rewritten and re-read in full
    on every busy poll** (`src/cmd/file-consumer/known.go`, `saveKnown`
    and `mergeSaved`). The whole name list is one jsonb value in
    `connection_node_checkpoints.state`: about 3 MB per 100 000 names,
    every 5 s per connection that is publishing, and past jsonb's size
    limit the save fails. Store it incrementally (a row per name, or only
    the changes) or save less often. Found in review of #391.
42. ~~**One failed directory read republishes the whole folder**~~ (#398)
    (`src/cmd/file-consumer/service.go`, `listFiles`). It returns an empty
    set when `os.ReadDir` fails, so `pollOnce` drops every known name as
    deleted and publishes them all again on the next good read. Older than
    #391, which now also saves that empty set. A failed read should skip
    the poll. Found in review of #391.
43. **The management-api README still places it behind Traefik.**
    `infrastructure/kubernetes/management-api/README.md` says the replicas
    run "behind the Service / Traefik gateway" and advises sticky sessions
    for SSE "at Traefik". Production runs ingress-nginx, and the browser
    reaches the API through the UI pod's nginx (`deploy-ui-azure.sh`
    proxies `/api` and `/ws`), so affinity set on the ingress wouldn't pin a
    client to one management-api replica. Rework the advice for the real
    path. Production runs one replica today (`deploy-azure.sh`), so nothing
    is broken yet. Found in #386.
44. ~~**The backup Secret example names the wrong database for AKS.**~~ (#395)
    `infrastructure/kubernetes/backup/README.md` builds `BACKUP_DB_URL` as
    `postgres://postgres:…/management_db`, but on AKS the database and its
    user are both `vrsky` (`infrastructure/kubernetes/postgresql/configmap.yaml`,
    `infrastructure/azure/rotate-secrets.sh`). Fix it together with item 27,
    and have `docs/DR.md` use the same names. Found in #386.
45. **The encryption-at-rest table reads as if every layer is in place.**
    `docs/DEPLOYMENT_GUIDE.md` lists an encrypted StorageClass and MinIO
    SSE through KES, but on AKS `deploy-azure.sh` uses `managed-csi`, and no
    script or workflow applies `infrastructure/kubernetes/encryption/`. Say
    which layers production has (Azure disks are encrypted at rest by the
    platform; whether with a customer key can't be told from the repo) and
    which are optional overlays. Found in #386.
46. **The whitepaper promises inbound mTLS for webhooks that production
    can't do.** `docs/security/whitepaper.md` section 4 says a webhook
    consumer can require a client certificate. That needs the worker's TLS
    listener, but no Kubernetes manifest sets `WORKER_MTLS_PORT`, and the
    webhook Ingress has no SSL passthrough or `auth-tls` annotations, so
    ingress-nginx terminates TLS and the worker never sees a client cert.
    Either wire it up or say it's compose-only for now. Found in review of
    #386.
47. **The encryption-at-rest checklist says `sslmode=require`; production
    uses `disable`.** `docs/operator/encryption-at-rest.md` tells operators
    to use TLS to the databases in production, but the AKS connection
    strings use `sslmode=disable` (`infrastructure/azure/rotate-secrets.sh`,
    `infrastructure/kubernetes/postgresql/secret.example.yaml`). Either turn
    on TLS for Postgres or make the checklist say where production stands.
    Found in review of #386.
48. **The CNPG manifest calls the restore drill a backup.** The comment in
    `infrastructure/kubernetes/postgresql/cnpg-cluster.yaml` says backups
    are "covered by the vrsky-cli drill (#86)". The drill tests the tooling
    in CI and backs nothing up (`docs/DR.md`). Fix the comment. It's a
    manifest, so PR plus `agent:needs-human`. Found in review of #386.
49. **`ui/src/services/websocket.ts` is never imported, and `VITE_WS_URL`
    exists only for it.** It opens an `EventSource` on
    `${config.wsUrl}/api/v1/connections/{id}/metrics/stream`, default
    `http://localhost:3000`: a cross-origin URL in dev that wouldn't carry
    the session cookie, and a wrong host in production. Delete the file and
    `wsUrl` from `ui/src/config/env.ts` (and the `VITE_WS_URL` mentions in
    the docs), or, if something should use the stream, make it relative
    like `api.ts`. Found during #399.
50. ~~**A file folder that can't be read only shows up in the log**~~ (#409)
    (`src/cmd/file-consumer/service.go`, `noteReadFailure`). Since #398 the
    connection shows as `running` while its folder is unreadable, and the
    only sign is one Warn in the file-consumer log. `noteReadFailure` could
    emit a `FileEvent{Type: "error"}` on each failed poll. The File watcher
    panel already shows those in red (`ui/src/pages/PipelineBuilder.tsx`),
    and `emitEvent` only reaches live subscribers, so it costs nothing
    when no panel is open. Found in review of #398.
51. ~~**A failed checkpoint read during a deferred baseline loses files**~~ (#409)
    (`src/cmd/file-consumer/known.go`, `loadKnown`). When the start could
    not read the folder (#398), the first good read calls `loadKnown`. If
    the checkpoint read fails then, it falls back to "everything present is
    done", and that set is saved on the first real change, so files that
    landed during the outage are never published. `takeDeferredBaseline`
    already retries every poll, so it could stay deferred on a checkpoint
    error instead. Found in review of #398.
52. **An ending poller can drop a newer poller's map entry.** api
    (`pollConnection` one-shot), sftp (one-shot `runPoller`), cloud-storage
    (`finishConn`) and tenant (`runBridge` error paths) delete their
    connection from the active map without checking the entry is still
    theirs. After a stop and a new start, the old poller finishing removes
    the new one's entry; the next start then runs a second poller beside
    it. Older than #397. Delete only when the stored value is this
    poller's own (compare a pointer). Found during #397.
53. **Items 36 and 37 now hold for every consumer on `sdk.StartGuard`,
    and three consumers are not on it yet.** Since #397 all command-driven
    consumers refuse a start while one is in flight (37) and write
    `running` after activating (36), like the file consumer. The webhook
    consumer still checks and inserts in two steps (a double start only
    overwrites the map entry, but a stop mid-start is lost) and the Front
    Systems consumer can register its webhooks twice. The remote agent
    checks and inserts in two steps too (`src/cmd/remote-agent/service.go`
    around 312-315 and 397-400) and runs each start on its own goroutine,
    so two start commands can race and leak the overwritten session's
    subscription; its fingerprint/replace model doesn't fit `StartGuard`
    as is. Found during #397 and its review.
54. **`affected.sh` ignores `src/internal`.** Seven Dockerfiles under
    `src/cmd` copy `src/internal/` (`vrsky-cli` among them), but a change
    there maps to no target, so the deploy job rebuilds none of those images.
    Treat `src/internal/*` like `src/pkg/*` (every Go image) and add a case
    to `TestAffectedServices`. Touches `infrastructure/azure`: PR plus
    `agent:needs-human`. Found in #395.
55. **`vrsky-cli backup` holds the whole dump in memory.** `pg_dump`'s
    output, the gzipped copy and the encrypted copy all sit in byte slices
    (`src/cmd/vrsky-cli/backup.go`, `pgDump`, `gzipBytes` and the
    `EncryptBytes` call), so a dump of roughly a third of the CronJob's
    512Mi limit or more gets the Job OOM-killed. Fine for today's small
    management DB. Stream pg_dump through gzip and encryption into
    `PutStream`, or at least alert on the dump size. Found in review of #404.
56. **The copied `acr-pull` in `vrsky-database` only refreshes on Catch up or
    a backup change.** `deploy-backup-azure.sh` copies it from
    `vrsky-platform`, and nothing else runs that script. After an ACR
    password rotation the backup Job can't pull until the next Catch up.
    Run the copy wherever `acr-pull` is rotated, or give the script a step
    in every deploy. Workflow change: PR plus `agent:needs-human`. Found in
    review of #404.
57. **A backup CronJob left suspended is silent.** `MgmtBackupMissing` is
    quiet while `vrsky-mgmt-backup` is suspended, by design, so a CronJob
    suspended by hand (or never re-enabled after a restore) and left that
    way for more than 7 days after `vrsky-backup-config` exists pages
    nobody. Add a rule for "suspended over 7 days while the Secret exists"
    (kube-state-metrics has `kube_secret_info`), or have Catch up warn.
    Alert rules: PR plus `agent:needs-human` if it touches workflows. Found
    in review of #404.
58. **The File watcher panel's `connected` event shows the full volume
    path** (`src/cmd/file-consumer/server.go`, `handleEvents`): "Watching
    /data/input/<tenant-id>/orders". It's the tenant's own folder, but the
    mount layout and tenant id are internals. Use `folderLabel` (the path
    relative to the tenant root), as the error events from #409 do. Found
    during #409.

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
