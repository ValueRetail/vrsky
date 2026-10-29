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
14. ~~**Fix the stale comment on `messaging.MainRetention`**~~ (#363)
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
25. ~~**`infrastructure/azure/deploy-ui-azure.sh` isn't executable**~~ (#418) (mode
    100644; every other script there is 100755). The guide now says `bash
    ...`. `infrastructure/azure/**`: PR plus `agent:needs-human`. Found in
    #371.
26. ~~**The k3d path doesn't line up with its manifests.**~~ (#419)
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
29. ~~**A watched file replaced under the same name is never published again**~~ (#420)
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
34. ~~**No way to mount a Windows home folder for the file connectors.**~~ (#422)
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
36. ~~**A stop just after a file connection becomes active can leave the
    database saying `running`**~~ (#423) (`src/cmd/file-consumer/service.go`,
    `startConnection`). The `running` status write comes after the
    connection is in `activeConnections`. A stop landing in between cancels
    the watcher and writes `stopped`, then the start writes `running`. Older
    than #375, which closed the same gap for a stop that lands earlier in
    the start. Found during #375.
37. ~~**A start refused while another start is in flight is lost if that one
    fails**~~ (#424) (`src/cmd/file-consumer/service.go`, `startConnection`). Since
    #375 a second start for a connection that is already starting returns
    at once. If the first then fails (say a transient database error during
    boot restore), the second start, which might have succeeded, is not
    retried and the connection stays down until someone starts it again.
    A "start again" flag on the reservation would cover it. Found by the
    test author on #375.
38. ~~**A test-only change rebuilds every Go image**~~ (#410)
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
40. ~~**Two file-consumer pods watch the same folders during a deploy.**~~ (#425)
    The file-consumer Deployment (`infrastructure/kubernetes/connectors/connectors.yaml`)
    has one replica and the default RollingUpdate strategy, so the new pod
    starts and restores every running file connection before the old one
    stops. For those seconds both pods poll the same folders on the shared
    volume, and a file that lands then can be published by both. #391 has
    each watcher re-read the saved done set before publishing, which
    narrows this, but only `strategy: Recreate` (or a per-connection lease)
    closes it. Manifest change: PR plus `agent:needs-human`. Found during
    #391.
41. ~~**The file consumer's saved done set is rewritten and re-read in full
    on every busy poll**~~ (#426) (`src/cmd/file-consumer/known.go`, `saveKnown`
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
43. ~~**The management-api README still places it behind Traefik.**~~ (#407)
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
45. ~~**The encryption-at-rest table reads as if every layer is in place.**~~ (#407)
    `docs/DEPLOYMENT_GUIDE.md` lists an encrypted StorageClass and MinIO
    SSE through KES, but on AKS `deploy-azure.sh` uses `managed-csi`, and no
    script or workflow applies `infrastructure/kubernetes/encryption/`. Say
    which layers production has (Azure disks are encrypted at rest by the
    platform; whether with a customer key can't be told from the repo) and
    which are optional overlays. Found in #386.
46. ~~**The whitepaper promises inbound mTLS for webhooks that production
    can't do.**~~ (#407) `docs/security/whitepaper.md` section 4 says a webhook
    consumer can require a client certificate. That needs the worker's TLS
    listener, but no Kubernetes manifest sets `WORKER_MTLS_PORT`, and the
    webhook Ingress has no SSL passthrough or `auth-tls` annotations, so
    ingress-nginx terminates TLS and the worker never sees a client cert.
    Either wire it up or say it's compose-only for now. Found in review of
    #386.
47. ~~**The encryption-at-rest checklist says `sslmode=require`; production
    uses `disable`.**~~ (#407) `docs/operator/encryption-at-rest.md` tells operators
    to use TLS to the databases in production, but the AKS connection
    strings use `sslmode=disable` (`infrastructure/azure/rotate-secrets.sh`,
    `infrastructure/kubernetes/postgresql/secret.example.yaml`). Either turn
    on TLS for Postgres or make the checklist say where production stands.
    Found in review of #386.
48. ~~**The CNPG manifest calls the restore drill a backup.**~~ (#427) The comment in
    `infrastructure/kubernetes/postgresql/cnpg-cluster.yaml` says backups
    are "covered by the vrsky-cli drill (#86)". The drill tests the tooling
    in CI and backs nothing up (`docs/DR.md`). Fix the comment. It's a
    manifest, so PR plus `agent:needs-human`. Found in review of #386.
49. ~~**`ui/src/services/websocket.ts` is never imported, and `VITE_WS_URL`
    exists only for it.**~~ (#428) It opens an `EventSource` on
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
52. ~~**An ending poller can drop a newer poller's map entry.**~~ (#408) api
    (`pollConnection` one-shot), sftp (one-shot `runPoller`), cloud-storage
    (`finishConn`) and tenant (`runBridge` error paths) delete their
    connection from the active map without checking the entry is still
    theirs. After a stop and a new start, the old poller finishing removes
    the new one's entry; the next start then runs a second poller beside
    it. Older than #397. Delete only when the stored value is this
    poller's own (compare a pointer). Found during #397.
53. ~~**Items 36 and 37 now hold for every consumer on `sdk.StartGuard`,
    and three consumers are not on it yet.**~~ (#429) Since #397 all command-driven
    consumers refuse a start while one is in flight (37) and write
    `running` after activating (36), like the file consumer. The webhook
    consumer still checks and inserts in two steps (a double start only
    overwrites the map entry, but a stop mid-start is lost) and the Front
    Systems consumer can register its webhooks twice. The remote agent
    checks and inserts in two steps too (`src/cmd/remote-agent/service.go`
    around 312-315 and 397-400) and runs each start on its own goroutine,
    so two start commands can race and leak the overwritten session's
    subscription; its fingerprint/replace model doesn't fit `StartGuard`
    as is. Found during #397 and its review. Items 36 and 37 are fixed in
    the guard (#423, #424), but the Sitoo consumer still calls `Reserve`
    rather than `ReserveOrRetry`, so a refused start there is still lost if
    the start in flight fails; switching it is a one-line change, held back
    while another branch works on the Sitoo connector. #429 put webhook and
    Front Systems on the guard and gave the remote agent per-connection
    command lanes (`src/cmd/remote-agent/lanes.go`); the Sitoo switch is
    still open.
54. ~~**`affected.sh` ignores `src/internal`.**~~ (#410) Seven Dockerfiles under
    `src/cmd` copy `src/internal/` (`vrsky-cli` among them), but a change
    there maps to no target, so the deploy job rebuilds none of those images.
    Treat `src/internal/*` like `src/pkg/*` (every Go image) and add a case
    to `TestAffectedServices`. Touches `infrastructure/azure`: PR plus
    `agent:needs-human`. Found in #395.
55. ~~**`vrsky-cli backup` holds the whole dump in memory.**~~ (#430) `pg_dump`'s
    output, the gzipped copy and the encrypted copy all sit in byte slices
    (`src/cmd/vrsky-cli/backup.go`, `pgDump`, `gzipBytes` and the
    `EncryptBytes` call), so a dump of roughly a third of the CronJob's
    512Mi limit or more gets the Job OOM-killed. Fine for today's small
    management DB. Stream pg_dump through gzip and encryption into
    `PutStream`, or at least alert on the dump size. Found in review of #404.
56. ~~**The copied `acr-pull` in `vrsky-database` only refreshes on Catch up or
    a backup change.**~~ (#431) `deploy-backup-azure.sh` copies it from
    `vrsky-platform`, and nothing else runs that script. After an ACR
    password rotation the backup Job can't pull until the next Catch up.
    Run the copy wherever `acr-pull` is rotated, or give the script a step
    in every deploy. Workflow change: PR plus `agent:needs-human`. Found in
    review of #404.
57. ~~**A backup CronJob left suspended is silent.**~~ (#432) `MgmtBackupMissing` is
    quiet while `vrsky-mgmt-backup` is suspended, by design, so a CronJob
    suspended by hand (or never re-enabled after a restore) and left that
    way for more than 7 days after `vrsky-backup-config` exists pages
    nobody. Add a rule for "suspended over 7 days while the Secret exists"
    (kube-state-metrics has `kube_secret_info`), or have Catch up warn.
    Alert rules: PR plus `agent:needs-human` if it touches workflows. Found
    in review of #404.
58. **Production talks to Postgres without TLS.** The AKS connection
    strings use `sslmode=disable` (`infrastructure/azure/rotate-secrets.sh`)
    and the in-cluster Postgres has no certificate. Turn on TLS for it (a
    cert-manager certificate, `ssl = on`, `sslmode=verify-full` in the
    Secrets) or move to a managed Postgres. Then drop the "doesn't yet" notes
    in `docs/operator/encryption-at-rest.md` and the whitepaper. Manifests
    and secrets: PR plus `agent:needs-human`. Found in #407.
59. **Inbound webhook mTLS can't reach production.** No manifest sets
    `WORKER_MTLS_PORT`, and the webhook Ingress terminates TLS, so the
    worker never sees a client certificate. Wiring it up needs the TLS port
    on the webhook-consumer Service plus SSL passthrough (or a separate
    LoadBalancer) for those connections, or a switch to ingress-nginx
    `auth-tls-*` with the CA check moved to the edge. Then update the
    whitepaper §4 and `docs/connectors/http.md`. Ingress and manifests: PR
    plus `agent:needs-human`. Found in #407.
60. ~~**The File watcher panel's `connected` event shows the full volume
    path**~~ (#455) (`src/cmd/file-consumer/server.go`, `handleEvents`): "Watching
    /data/input/<tenant-id>/orders". It's the tenant's own folder, but the
    mount layout and tenant id are internals. Use `folderLabel` (the path
    relative to the tenant root), as the error events from #409 do. Found
    during #409.
61. ~~**An old poller still writes its final status after a newer start.**~~ (#456)
    Since #408 an old poller ending late leaves the newer poller's map entry
    alone, but it still writes status `stopped` (one-shot done) or `error`
    (setup failed) after the new start wrote `running`. api
    (`pollConnection`), sftp (`runPoller`), cloud-storage (`finishConn`) and
    tenant (`runBridge`) do it, so the UI and the boot restore can see a
    running connection as stopped. Write the status only when the entry was
    still this poller's own (`dropActive` could report it). Found during
    #408.
62. ~~**The Sitoo poller re-reads the whole collection every poll**~~ (#457)
    (`src/cmd/sitoo-consumer/service.go`, `fetchAndPublish`). The POS sales
    template sends every receipt to Business Central every 5 minutes, and
    only `dedupe_fields` keeps them from becoming duplicate invoices, at one
    BC GET per receipt per poll. A cursor (Sitoo's modified-after filter,
    stored in `connection_node_checkpoints` like the BC consumer's) would
    send each receipt once. The `resource` key can't carry a query string
    today: the poller appends `?start=…`. Found during #435.
63. ~~**The converter editor doesn't know `each` or `split_records`**~~ (#458)
    (`ui/src/components/Pipeline/PropertyEditor.tsx`). A retail template's
    converter opened in the builder shows the `each` mapping as "Rename"
    (the type select has no `each` option) without its nested mappings, and
    `split_records` not at all. Saving keeps both (`updateMapping` spreads
    the old mapping), but picking another type in that select drops the
    lines. Show them, read-only at least. Found during #435.
64. ~~**Check the other `messaging.Subscribe` callers for cancel-before-Stop.**~~ (#459)
    #444 found the remote-agent gateway cancelling what its handlers wait
    on before stopping the subscriber, so the loop re-fetched the NAK'd
    message until it was dead-lettered. `Subscriber.Halt` now exists for
    this. Check `pkg/sdk/lifecycle.go`, `cmd/data-filter`,
    `cmd/data-converter` and `cmd/tenant-consumer/bridge.go` for a handler
    that returns an error once a context is cancelled ahead of `Stop`.
    Found in #444.
65. ~~**Two remote-agent tests sit on fixed 4 s waits.**~~ (#460)
    `TestOutput_HoldsWhileAgentOffline_NoRedeliveryCount` sleeps four
    AckWaits and `TestOutput_TakesOnlyItsPredecessorsMessage` polls for 4 s
    to prove a message never comes; with cleanup each takes ~6 s. The
    second could wait for the consumer's ack floor to pass both messages
    instead. Not flaky, just slow. Found in #444.

66. ~~**The remote agent's stop tests don't pin cancel-after-halt, and a
    reconcile can start a member on a cancelled context.**~~ (#461) The test seam
    in `src/cmd/remote-agent/service.go` (`afterCancel`) fires after the
    halts, so moving `sess.cancel()` back to the top of `endSession`
    passes both stop tests. Separately, if `endSession` runs during a group
    reconcile, a member subscribed at `groups.go:~235` starts with an
    already-cancelled context and NAKs until the lock at `:~252` halts it
    (older than #447, tiny window). Add a seam right after the cancel and a
    reconcile test. Found in review of #447.
67. ~~**CI never runs `make lint-connector`.**~~ (#462) `make lint` runs it beside
    lint-tenant and lint-openapi, but the Go lint job in `build-push.yml`
    does not, so a connector that bypasses the SDK passes CI. Add it as a
    step (and to `TestEveryLintCheckStillGates`) once it is clean on main.
    Found during #446.
68. ~~**golangci-lint's caches barely help.**~~ (#463) On main the action restores a
    1.4 MB `golangci-lint.cache-…` entry and still spends 2 m 18 s, and
    setup-go's Go build cache uses one key for every Go job, so whichever
    job saves first (a `-race` test shard) wins and the lint job's own
    build output is never kept. A lint-specific `GOCACHE` entry could take
    a minute off; measure before and after. Off the critical path since
    #446. Found during #446.
69. ~~**`promtool test rules` takes four minutes**~~ (#464) because the backup alert
    tests (#404) evaluate up to ten days at one-minute steps. If the
    Alert rules job ever becomes the slowest before a deploy, name the
    test groups and split them over matrix legs with `--run`, with a guard
    that every group runs in exactly one leg. Found during #446.
70. ~~**An open UI tab never learns that a newer UI is deployed.**~~ (#551) The bundle
    is one file with no lazy chunks, so a tab left open across deploys
    keeps running the old code against the new API. That is how #450's
    "Pipeline 15:26:55" happened: a pre-2026-10-14 tab still sent a
    generated name on every Deploy. Write the build's commit to a
    `version.json` beside `index.html`, poll it (and check on focus), and
    show a "A newer version is available, reload" banner. Found during #450.
71. ~~**`ui/src/pages/Dashboard.tsx` is dead code**~~ (#513): no route renders it.
    Delete it, or route it if a dashboard is wanted. Found during #450.
72. ~~**A linked builder tab's `deployedName` never re-syncs with the
    server.**~~ (#502) Opening a connection whose canvas is already linked
    (`PipelineBuilder.tsx` edit flow) only switches to it, so a name changed
    elsewhere leaves the tab and `deployedName` agreeing with each other and
    not with the server; Deploy then sends no name. Refresh `deployedName`
    from the loaded connection on edit-load (and show the server's name if
    the tab was never renamed). Found in review of #450.
73. ~~**A failed workspace delete leaves state as it was**~~ (#514) although
    `TenantSelector`'s catch says "checkAuth will refresh state
    regardless"; it never calls checkAuth. Fix the comment or the code.
    Found during #450.
74. ~~**The Connections list can show the previous workspace's rows.**~~
    (#501)
    `ConnectionsList.tsx`'s own load effect has no latest-only or cancel
    guard, so on a slow switch from A to B, A's answer can land after B's
    and the list shows A's connections under B. Give it the same guard as
    the connections store's `load()`. Found in review of #450 (#466).
75. ~~**The sidebar's status counts cover at most 100 connections.**~~ (#541) They
    come from the store's loaded items (`limit=100`), while the badge shows
    the API total, so a workspace with more than 100 shows a badge that
    disagrees with the running/stopped/error cards. Count by status on the
    server, or label the cards as a sample. Found in review of #450 (#466).
76. ~~**A file-consumer pod can adopt a replacement without publishing it.**~~ (#500)
    If a pod's stat of a known name fails at load (so it has no stamp),
    another pod saved stamp S, and the file is now a newer S', `mergeSaved`
    leaves it alone and the next poll adopts S' as done. Needs a stat
    failure, two pods and a replacement at once. Skip adoption in
    `pollOnce` when the saved row has a different stamp. Found in review of
    #420 (#465).
77. ~~**Every file-consumer poll stats every file in the folder.**~~ (#537) Since
    #420 known files are checked for replacement too, so a share with
    thousands of files costs one SMB round trip per file per poll. Consider
    a cheaper change signal (directory mtime, or stat known files less
    often). Related to #426. Found in review of #420 (#465).
78. **Nothing in the UI reads `GET /api/v1/connections/{id}/metrics/stream`
    any more.** Its only client was `ui/src/services/websocket.ts`, deleted
    in #428 without ever being imported. `HandleMetricsSSE` is still
    mounted (`src/pkg/managementapi/handler.go`) and listed in the OpenAPI
    registry, and `infrastructure/scripts/e2e-test.sh` probes it. Either
    wire the builder's live metrics to it through a relative URL, the way
    `services/workerEvents.ts` does, or drop the endpoint. Dropping it is
    a public API change, so that PR gets `agent:needs-human`. Found in
    #428.
79. ~~**`TestEveryLintCheckStillGates` matches raw step text**~~ (#462)
    (`src/pkg/managementapi/lintgates_guard_test.go`), so a commented-out
    `gofmt -l .`, `make lint-tenant` or `check rules` line still satisfies
    it. Strip shell comments first, as the shard guard does since #443
    (`stripShellComments` in `gotestshards_guard_test.go`). The alert-rules
    step is already covered by running it (`TestRuleTestGroupsRunOnce`).
    Found during #443.
80. ~~**Every new alert test edits the workflow.**~~ (#495) Since #464 a new test
    group in `infrastructure/prometheus-rules-test.yml` must also be listed
    in a leg of the `alert-rules` matrix in `.github/workflows/build-push.yml`,
    so any PR that adds an alert test touches `.github/` and is
    `agent:needs-human`. Move the leg lists into a file under
    `infrastructure/` (say `prometheus-rules-test-legs.yml`) that the job
    reads, keeping only the leg count in the workflow, and point
    `TestRuleTestGroupsRunOnce` at it. Found in review of #468.
81. ~~**A subscriber's `Stop` waits out its fetch.**~~ (#494) `Subscriber.loop` in
    `pkg/messaging` fetches with `MaxWait(2s)` and `Halt` cannot cut that
    short, so every stop takes up to 2 s even when nothing is held. It is
    most of what the remote-agent tests still spend (a stop in each
    cleanup) and it slows every pipeline stop and redeploy. A fetch that
    also ends on the stop channel (a context-aware `Fetch`) would make
    `Stop` return at once. Shared code: every connector's subscriber.
    Found during #460. Done with short polls instead; the remote-agent
    gateway still ends its handlers' scope right after `Halt`, which is
    #576.
82. ~~**GCS `PutStream` commits a partial object when the body fails.**~~ (#524)
    `pkg/objectstore/gcs.go` calls `w.Close()` after a failed `io.Copy`,
    which finalises the upload; aborting needs the writer's context
    cancelled instead. S3 and Azure abort. A failed `vrsky-cli backup` on
    GCS can leave a cut-off archive that fails restore loudly (no final
    chunk) but still looks like a backup in `list`. Found during #430.
83. ~~**`pgRestore` in `vrsky-cli` has no unit test.**~~ (#523) Its error precedence
    (an archive read error wins over pg_restore's exit status) and the
    drain after pg_restore exits, which is where a chunked archive's final
    chunk and the gzip checksum get checked, are only exercised by the DR
    drill's happy path. Test them with a fake `pg_restore` script first on
    `PATH`: one that exits 0 early, one that fails, one that reads all of
    stdin, each against a good and a damaged archive. Found in review of
    #471.
84. ~~**A backup archive isn't bound to its object key.**~~ (#549) Anyone who can
    write to the bucket can copy an older valid archive over the newest
    key, and `verify` and `restore` accept it. Put the stamp (and the key
    name) in the `VRSKYBK1` header, so it is part of every chunk's AAD,
    and have `backup verify` and `restore` check it against the key they
    were given. Changes the format (`VRSKYBK2`; keep reading v1 until it
    ages out). Found in review of #471.
85. **Delete the legacy single-seal restore path** in `vrsky-cli`
    (`openArchive`'s `crypto.DecryptBytes` branch) once the 14-day
    retention holds only chunked archives, i.e. 14 days after #471 reaches
    production and the CronJob has run on it. Until then restore still
    opens archives sealed directly under `ENCRYPTION_KEY`; removing it
    finishes the key separation. Found in review of #471.
86. ~~**The file destination's host folder is fixed to `data/output`.**~~ (#553)
    #422 lets `.env` pick the host folder behind `/data/input`
    (`VRSKY_FILES_HOST_DIR`), but file-producer still mounts the
    repository's `data/output`, so a Windows user can read from their home
    folder and not write to it. A second variable for `/data/output`, with
    the same fixed target and the guard in
    `compose_files_mount_guard_test.go`, would close that. Found in #422.
87. ~~**Rename and `each` leave a dotted source in place.**~~ (#525) With the node's
    `drop_unmapped` off, both read `source` as a path but remove it with
    `delete(result, m.Source)` (`src/cmd/data-converter/main.go`, the
    `rename` and `each` cases), which only deletes a top-level key. So
    `order.items` → `lines` keeps `order.items` next to the new `lines`.
    Either delete along the path (a behaviour change for existing
    pipelines, so say so in the PR) or document it. The editor's `each`
    help text says so since #458. Found in #458.
88. ~~**Converter editor accessibility.**~~ (#542) Nested mapping rows repeat the
    accessible names "Mapping type" and "Remove mapping" with no row
    context, so a screen reader can't tell rows apart, and a row's
    warnings aren't tied to its inputs with `aria-describedby`
    (`ui/src/components/Pipeline/ConverterMappingsEditor.tsx`). Name rows
    by position or target ("Remove mapping 2", "Item mapping type for
    lineObjectNumber") and point the inputs at the warning list. Found in
    #458.
89. ~~**A deleted backup CronJob is silent.**~~ (#526) If `vrsky-mgmt-backup` is
    deleted (by hand, or a failed re-apply), its kube-state-metrics series
    end, and both `MgmtBackupMissing` and `MgmtBackupSuspended` match
    nothing, so no backups run and nobody is paged. An `absent()` rule on
    `kube_cronjob_created{namespace="vrsky-database", cronjob="vrsky-mgmt-backup"}`
    while `vrsky-backup-config` exists would catch it; mind the parked
    nights (no samples at all then). Alert rules only. Found in review of
    #473.
90. ~~**The Postgres manifests call the StatefulSet production uses
    "dev-only".**~~ (#527) `statefulset.yaml` opens with "DEV / SINGLE-NODE ONLY" and
    `cnpg-cluster.yaml` says it replaces it, but production runs that
    StatefulSet (`postgresql`, the disk Azure Disk Backup snapshots, see
    `docs/DR.md`) and no CNPG cluster. Either say so in both headers and the
    README, or plan the move to CNPG with its own backup. Manifests: PR plus
    `agent:needs-human`. Found during #427.
91. ~~**A late status write can still land over a newer start's `running`.**~~ (#423)
    #456 left a window in api, sftp, cloud-storage and tenant: with the
    6-connection DB pool saturated, a new start's lookup and its `running`
    UPDATE can both get a connection before an old poller's `stopped` or
    `error` UPDATE does, and the old write lands last. `handleStopCommand`'s
    `stopped` write races a new start's `running` the same way. Fix with a
    conditional UPDATE: the `running` write returns `started_at`, and the
    ending and stop writes add `AND started_at = $n`. Or route the writes
    through the StatusWrite ordering #423/#424 adds to `sdk.StartGuard`.
    Found in review of #456.
92. ~~**The SFTP and cloud-storage consumers forget what they published on
    every restart.**~~ (#538) Their done set is the in-memory `processed` map in
    `runPoller` (`src/cmd/sftp-consumer/service.go`,
    `src/cmd/cloud-storage-consumer/service.go`, poll mode). With `after_action:
    none`, every restart or deploy publishes every file still in the
    remote folder or bucket again. With `delete` or `move` they also have
    #425's overlap: during a rolling update both pods list the same file
    and both can publish it before one removes it. `strategy: Recreate`
    alone would only fix the second, so port the file consumer's saved
    done set (#391, `src/cmd/file-consumer/known.go`) first, then decide on
    Recreate. The other polling consumers (api, db, salesforce, the ERP
    and retail ones, tenant) were not checked for the overlap; each one's
    cursor handling decides whether two pods for a few seconds can publish
    twice. Found in #425.
93. ~~**The `acr-pull` copy in `vrsky-ui` never refreshes.**~~ (#497)
    `deploy-ui-azure.sh` copies it from `vrsky-platform` once, and only a
    human runs that script; the deploy job rolls the UI with
    `deploy-core-azure.sh`, which doesn't copy. After an ACR password
    rotation the next UI rollout can't pull. Add `vrsky-ui` to
    `copy-acr-pull.sh` (#431), which already runs on every deploy. There
    is also no written runbook for rotating the ACR password itself (now
    item 98).
    `infrastructure/azure/**`: PR plus `agent:needs-human`. Found in #431.
94. ~~**A failed `acr-pull` apply can print the registry password.**~~ (#496)
    `deploy-backup-azure.sh` pipes the copied Secret into a bare
    `kubectl apply -f -`. On an existing object a failed apply can print
    "error when applying patch:" with the patch, new `.dockerconfigjson`
    included, into the deploy log, which anyone with repo access can read.
    Capture the output and print only a generic line and the exit code, as
    `copy-acr-pull.sh` does since #431, with a stub test like
    `TestCopyACRPullScript`'s "apply fails". `deploy-ui-azure.sh`'s copy
    into `vrsky-ui` has the same leak; fix it with item 93.
    `infrastructure/azure/**`: PR plus `agent:needs-human`. Found in review
    of #431.
95. ~~**A stop command cancels a connection whatever tenant it names.**~~ (#498) Every
    command-driven consumer's `handleStopCommand` looks the connection up in
    its active map by ID alone and cancels it; only the `stopped` UPDATE
    filters on the command's `tenant_id`. A stop carrying the wrong tenant
    (it would have to be published on internal NATS; the management API
    sends the right one) stops another tenant's poller and leaves its row
    saying `running`. Keep the tenant on each active entry and ignore a stop
    whose tenant doesn't match. Tenant isolation: PR plus
    `agent:needs-human`. Since #423 the status order is already keyed by
    tenant and connection, so such a stop can't reorder the real tenant's
    writes. Found in review of #487.
96. ~~**Two more file-consumer messages show the path on the volume.**~~ (#499) A
    watched file that fails to ingest sends an `error` event with the raw
    Go error (`src/cmd/file-consumer/service.go`, the `ingErr` event in
    `pollOnce`), and `os.Stat`, `os.ReadFile` and `os.Open` put
    `/data/input/<tenant-id>/…` in it. The sample-data endpoint returns
    `pickSampleFile`'s errors as they are ("directory does not exist:
    /data/input/<tenant-id>/…", or `os.ReadDir`'s own text). Name the
    folder with `folderLabel` and keep only the operating system's reason,
    as `readFailureMessage` does since #409. Found in review of #455.
97. ~~**Front Systems keeps its webhooks after a stop.**~~ (#503) A stop forgets the
    connection but never unregisters its callback at Front Systems, and
    `/frontsystems/events/{id}` publishes for any connection that exists,
    running or not. So a stopped pipeline still takes in events, and a
    stop then start registers the callback a second time (Front Systems may
    then deliver each event twice). Unregister on stop, or look up existing
    registrations before posting, and refuse events for connections that
    aren't running. Older than #429, which only stopped two concurrent
    starts from both registering. Found during #429.
98. ~~**Two small leftovers in the Connections list.**~~ (#515) A delete that answers
    after a workspace switch still runs `setTotal(prev => prev - 1)` on the
    new workspace's total, which can skew its page count. And a failed load
    shows "No connections yet" next to the error toast; an error state with
    a retry would read better. Found in review of #501.
99. ~~**No runbook for rotating the ACR password.**~~ (#539) The password enters the
    cluster in `deploy-azure.sh` step 2 (`vrsky-platform`, `vrsky-storage`)
    and `minio-images-acr.sh --repoint` (`vrsky-storage`), and every deploy
    copies it on into `vrsky-database` and `vrsky-ui` (#497). Nothing says
    which of these to run after `az acr credential renew`, in which order,
    or how to check every namespace pulls again. Write it in
    `docs/operator/`. Split out of item 93.
100. ~~**The builder's edit route loads only the first connection it sees.**~~ (#516)
    `useEditConnectionLoad` sets `editLoadDone` once and never resets it
    when `:id` changes, so going from `/connections/A/edit` to
    `/connections/B/edit` on an open builder never switches to or imports
    B; the page stays on A's canvas. Reset it per id (and drop the linked
    canvas id with it). The #502 name sync stays safe meanwhile, because the
    store only applies a name to a canvas linked to that connection. Found
    in review of #502.
101. ~~**A stale edit-route answer can undo a rename deployed meanwhile.**~~ (#540) The
    #502 sync reads the canvas when the GET answers. If the user renames the
    tab and Deploys before that, `deployedName` already equals the new tab,
    so the old answer counts as "not renamed" and puts the old name back on
    both. The server keeps the new name; the tab is wrong until the next
    edit-route open. Capture `deployedName` when the request goes out and
    skip the sync if it changed. Found in review of #502.
102. ~~**A file-consumer start that couldn't read its checkpoint saves its
    guess anyway.**~~ (#536) `loadKnown`'s fallback after a failed checkpoint read
    takes every file in the folder as done with its stamp now, and its
    comment says this isn't saved. But the first real change saves it:
    `saveKnown` takes the `savedGen == ""` path, which writes a snapshot
    with no merge (a deletion-only poll writes it with no read at all). That
    snapshot can replace another pod's stamp S for a file with an
    unpublished S', which that pod then adopts and never publishes, and it
    makes the other pod skip new files the fallback took as done. Merge
    before that first snapshot, or keep the fallback's names out of it.
    Found in review of #500.
103. ~~**A workspace deleted elsewhere stays in the selector.**~~ (#552) Deleting it
    again answers 403 or 404, and since #514 `TenantSelector` only shows
    that error. The server did answer, so a `checkAuth` on 403/404 can't
    turn a network blip into a logout; it would drop the stale workspace.
    Found in review of #514.
104. ~~**The Postgres README's "Architecture" list is out of date.**~~ (#554) It says
    50GB "via Longhorn distributed storage" with "Full durability via
    Longhorn replication", but `statefulset.yaml` asks for `local-path` and
    production runs on `managed-csi` (the rewrite in
    `infrastructure/azure/deploy-azure.sh`), one Azure disk with no
    replication. Its resources line says requests of 2 CPU and 4GB, but the
    StatefulSet requests 250m and 512Mi. Fix the three lines. Manifest
    directory, so PR plus `agent:needs-human`. Found during #527.
105. ~~**The MinIO manifest calls itself dev-only too.**~~ (#555)
    `infrastructure/kubernetes/minio/deployment.yaml` opens with "MinIO —
    DEV / SINGLE-NODE ONLY (#136)" and "NOT for production", but
    `deploy-azure.sh` installs it on AKS and production keeps its data on a
    managed disk (`docs/operator/cost-parking.md`). Check what production
    runs, then fix the header as #527 did for Postgres. Manifest, so PR
    plus `agent:needs-human`. Found in review of #527.
106. **Run the SFTP and cloud-storage consumers with `strategy: Recreate`.**
    Since #538 they keep their done set, so a restart no longer publishes
    everything again, but a rolling deploy still runs two pods for a few
    seconds. With `after_action: delete` or `move` both can list and
    publish the same file before either removes it (#425's overlap). Set
    `strategy: Recreate` on `vrsky-sftp-consumer` and
    `vrsky-cloud-storage-consumer` in `connectors.yaml`, as #425 did for the
    file consumer. Manifest, so PR plus `agent:needs-human`. Found in #538.
107. ~~**The SFTP consumer publishes a file still being uploaded.**~~ (#562) It has no
    stability check: a file listed mid-upload is published half-written.
    Since #538 the next poll sees the new size and publishes the finished
    file as well, so the complete file arrives, after a partial one. Wait
    for the same size and mtime on two polls first, like the file consumer
    (#374). Found in #538.
108. ~~**The SFTP and cloud-storage connector pages name fields the code
    doesn't read.**~~ (#564) `docs/connectors/sftp.md` says `sftp.path`; the consumer
    reads `remote_dir` (and `file_pattern`, `poll_interval_seconds`,
    `move_dir`, `host_key`, none documented). `cloud-storage.md` says
    `event_mode`; the consumer reads `mode`, and `after_action`,
    `move_prefix`, `file_pattern` and `poll_interval_seconds` are missing.
    Check against `getSFTPConfig`/`getConfig` and the UI property panel.
    Found in #538.
109. **A done set's changes can be written on a snapshot nobody reads.**
    `ownsSaved` (`src/pkg/doneset`, and `src/cmd/file-consumer/known.go`)
    reads only the changes row. If another poller's snapshot and changes
    reset land between that read and the changes write, or another poller
    writes a snapshot and its changes reset fails, this poller keeps writing
    changes on its old snapshot and every reader ignores them until its next
    snapshot, which on a quiet folder can be days. A restart in between
    publishes those names again. Bound it: compare the snapshot's gen every
    Nth save, or force a snapshot after N changes-only saves or X minutes.
    Found in review of #538.
110. **The SFTP and cloud-storage consumers never retry a failed
    after-action.** A file whose `delete` or `move` failed is done, so it
    stays in the folder for good; before #538 a restart published it again
    and retried. Retry the after-action for done names still listed (with
    `delete`/`move` a done name should not be there), with a backoff.
    Found in review of #538.
113. ~~**The file connector page still says every check looks at every
    file.**~~ (#564) Since #537 a folder with more than 1 000 published files has
    them checked for replacement in turns, so a replaced file there goes
    out up to `ceil(files/1000)` checks later (about 50 s at 10 000 files),
    not after two. `docs/connectors/file.md`, the paragraphs on moving files
    out and on replaced files. Left out of #537 because another worker had
    `docs/` at the time.
114. ~~**A file-consumer checkpoint row that can't be decoded is never
    replaced.**~~ (#563) Since #536 a start that couldn't read its checkpoint saves
    nothing until a read works. For a row that fails to decode that is
    never: the watcher logs an Error every poll and falls back to its guess
    at every start. Before #536 the first change overwrote it. Nobody can
    read such a row, so a decode error (not a store error) could count as
    "no checkpoint" in `settleGuess`, the deferred baseline (#409) and
    `keepSavedStamps` (#500). Found in review of #536.
111. **The rest of the Postgres README still talks about Longhorn.** #554
    fixed the "Architecture" list and the Storage section's first line, but
    `infrastructure/kubernetes/postgresql/README.md` still says "PVC not
    bound: Verify Longhorn is running", has Longhorn in "Increase Storage"
    (expansion on `managed-csi` works differently, and needs checking), and
    links Longhorn's docs under References. Manifest directory, so PR plus
    `agent:needs-human`. Found in #554.
112. **The MinIO README's "Architecture" list is out of date too.**
    `infrastructure/kubernetes/minio/README.md` says 100GB "via Longhorn
    distributed storage" and requests of 1 CPU and 2GB, but
    `deployment.yaml` claims 8Gi on `local-path` (`managed-csi` in
    production) and requests 50m and 512Mi. Its "High Availability" section
    still reads as if production didn't run `deployment.yaml`, and its
    bucket tree gives `temp/` a 15-minute TTL where `setup-job.yaml` sets
    one day. Manifest directory, so PR plus `agent:needs-human`. Found in
    #555.
115. **A reload during a UI rollout can load a blank page.** With three UI
    replicas and `maxSurge: 1`, `index.html` from a new pod can name
    `/assets/index-<new>.js`, which an old pod answers with 404 (its
    `error_page 404 /index.html` sends HTML for the script). The #551 banner
    can show as soon as the first new pod serves, so a quick Reload can hit
    this. Keep the previous build's assets in the new image, or have the
    banner wait for two matching checks. Found in review of #551.
116. **`checkAuth` signs the user out when `/me` fails for any reason.**
    `getMe` returns null on every failure, and `ui/src/store/authStore.ts`
    clears the user and workspaces whenever it gets null, so a network
    error or a 5xx on `/me` (at app load, after a workspace delete, after a
    403/404 on a delete since #552) sends a user with a working session to
    the login page. The session token stays, so a reload recovers. `getMe`
    drops the token only on a 401 of its own, so `checkAuth` can tell the
    two apart the way `reportUnauthorized` in `services/sessionExpiry.ts`
    does: keep the state while the token is still there and say the
    refresh failed. Auth state, so PR plus `agent:needs-human`. Found in
    #552.
118. **An SFTP or cloud-storage done set that can't be decoded stops the
    poller for good.** `doneset.Set.Load` (`src/pkg/doneset`) returns the
    same error for a row it can't decode as for a store error, and both
    pollers publish nothing until `Load` works, logging an Error every poll.
    A row that fails to decode never loads, so the connection publishes
    nothing until someone fixes the row by hand. Treat a decode error as no
    saved set there too, as #563 does for the file consumer (`loadSaved`
    and `errSavedUndecodable` in `src/cmd/file-consumer/known.go`): the
    first-start rule for these pollers publishes every file in the folder,
    so decide first whether that or "everything present is done" fits.
    Found in #563.
119. **The SFTP and cloud-storage editors show a poll interval they don't
    save.** Both consumer panels in
    `ui/src/components/Pipeline/PropertyEditor.tsx` show
    `poll_interval_seconds ?? 60` but write the field only when it is
    edited. The consumers read unset as 0, "poll once", so a node saved
    without touching the field shows 60 in the editor and polls once, then
    stops. Write 60 on mount, as `PollOrMethod` in the same file does for
    the retail connectors' 300, or show 0. The connector pages describe the
    current behaviour (#564). Found in #564.
120. **Cloud-storage event mode ignores `prefix` and `file_pattern`.**
    `runEventLoop` (`src/cmd/cloud-storage-consumer/service.go`) ingests
    every key a notification names, and the S3 parser takes every record,
    not only `ObjectCreated` ones (Azure and GCS filter by event type). The
    editor still shows Prefix in event mode. Either apply `prefix` and
    `file_pattern` to event keys and skip non-create S3 records, or say in
    the panel that the notification must be scoped. Documented as is in
    #564. Found in #564.
124. **A refresh after a refused workspace delete resets the page.**
    `TenantSelector` calls `checkAuth()` after a 403 or 404, `checkAuth`
    sets `isLoading: true`, and `ui/src/components/Auth/ProtectedRoute.tsx:24`
    swaps the page for the spinner and remounts the builder, so open
    panels, the event history and the selection are lost (the canvas
    survives in `canvasStore`). Since #552 a non-owner's 403 does this too.
    A refresh that doesn't touch `isLoading` would avoid it; that changes
    the auth store, so PR plus `agent:needs-human`. Found in review of #552.
125. **`checkAuth` can select a workspace that isn't active.** When the saved
    workspace is gone it falls back to `meData.current_tenant`
    (`ui/src/store/authStore.ts` ~193), the server's `tenants[0]`, without
    checking `status === 'active'`, which `switchTenant` would refuse. Auth
    state, so PR plus `agent:needs-human`. Found in review of #552.
130. ~~**Refuse a cloud-storage `move_prefix` inside `prefix`.**~~ (#567) The listing
    is recursive, so an object moved to a `move_prefix` under `prefix` is
    a new key at the next poll and is published again. Its second move
    copies it onto its own key and then deletes it: on Azure and GCS that
    copy succeeds and the object is gone (S3 refuses a copy onto itself, so
    there the after-action fails and the object stays). `start` in
    `src/cmd/cloud-storage-consumer/service.go` already refuses `move`
    without `move_prefix`; it could refuse one inside `prefix` the same
    way, and the editor could say so. The docs warn about it (#564).
    Found in review of #564.
131. **Stop reading `VRSKYBK1` backups** in `vrsky-cli` once the 14-day
    retention holds only `VRSKYBK2` archives, i.e. 14 days after #549
    reaches production and the CronJob has run on it. Delete the v1 branch
    of `readBackupHeader` and `backupKDFInfo`, and with item 85 the
    `--allow-unbound` flag and the unbound warning, so every archive
    restore accepts is bound to its key. Update `docs/DR.md` and
    `docs/operator/backup.md`. Found in #549.
133. **A cloud-storage start the consumer refuses leaves the connection
    `running`, ingesting nothing.** `StartConnection` in
    `src/pkg/managementapi/handler.go` writes `running` once the
    orchestrator has deployed, and `RestoreRunning` keeps it. The consumer's
    refusals in `startConnection` (`src/cmd/cloud-storage-consumer/service.go`,
    `move` without `move_prefix`, and since #567 a `move_prefix` the listing
    of `prefix` returns) return before `claim.Activate` and write no status,
    so only the worker log says why. Refuse these configs in the start-time
    validator (`src/pkg/managementapi/nodeconfig.go`, so the user gets a 400
    with the reason) and/or have the worker write `error`; and show the
    rule at the "Move to prefix" field in
    `ui/src/components/Pipeline/PropertyEditor.tsx`. The rule is the
    consumer's `validateMoveOutsideListing`. Found in review of #567.
134. **Check `move_prefix` in cloud-storage event mode once it applies
    `prefix`.** Event mode ignores `prefix` (item 120), so #567 checks the
    start in poll mode only and relies on `afterAction` refusing a move
    onto the object's own key. A moved object that the bucket notification
    covers is still ingested a second time. If item 120 makes event mode
    apply `prefix`, run the same start check there. Found in #567.
126. **Turn on Sitoo incremental polling in the POS sales template**
    (`ui/src/onboarding/templates.ts`). #457 added `incremental` for
    `orders`, but the template polls `transactions`, which the Sitoo API
    reference does not list. Waits on human item 9: the sandbox has to show
    which collection holds receipts and that `orderidfrom` with
    `sort=orderid` behaves as documented before the template changes. The
    `incremental` key also goes into `consumer/sitoo` in
    `ui/src/types/nodeConfig.ts`, or templates.test.ts rejects it. Found
    during #457.
127. **Sitoo incremental for collections other than `orders`.** #457 ships
    it for `orders` only, because `orderid` is unique and the API reference
    documents both its filter and its sort. `products` has a
    `datelastmodified` filter but no documented sort, and a timestamp cursor
    is not unique, so records sharing a second need a tiebreaker. Needs a
    Sitoo sandbox to check sort and tie order first. Found in review of
    #457 (#472).
128. **A far-future cursor value freezes the BC consumer's cursor**
    (`cursorTracker`). One record stamped years ahead sets the watermark
    there, and every later poll asks only for records after it. Ignore or
    log values above now + overlap. Found in review of #457 (#472).
129. **The Sitoo `base_url` is unvalidated tenant config.** A tenant can
    point the consumer, with its Basic-auth credential, at any host,
    internal ones included (SSRF). The webhook path also builds
    `resourceURL` with the event's id unescaped; `url.PathEscape` it. Both
    touch connector security, so the PR gets `agent:needs-human`. Found in
    review of #457 (#472).
117. **The SFTP consumer publishes temp names too.** The file consumer
    skips dotfiles, `~*`, `*.tmp` and `*.part` until they are renamed
    (#374); the SFTP consumer has no such skip, so an uploader that writes
    `x.csv.part` and renames it gets both names published unless
    `file_pattern` excludes the temp one. Since #562 a temp file is waited
    on until it stops changing, but one left unchanged for the settle gap
    (5 to 30 s; a slow uploader, or a rename that comes late) still goes out
    under its temp name, and again under its final name. Skip the same names in
    `pollOnce`. Found in #562.
135. **The other consumers still give up on a failed start, and a failed
    restore list restores nothing.** #503 retries a Front Systems start that
    fails for a reason that passes (`src/cmd/front-systems-consumer/retry.go`).
    Every other command-driven consumer still gives up when its start fails
    (a database blip at boot restore, a partner API down) while the row says
    `running`, until someone stops and starts it. And `sdk.RestoreRunning`
    returns 0 when its list query fails, which looks the same as "nothing
    running", so a blip at that one query restores nothing on any consumer,
    Front Systems included. Move the retry loop next to `StartGuard` in the
    SDK (each consumer keeps its own transient/permanent split) and have
    `RestoreRunning` retry its list with the same backoff. SDK change, so
    alone. Found in #503.
136. **Replay Front Systems events refused while a connection wasn't
    running.** Front Systems doesn't resend: an event answered 503 (start in
    progress or waiting to retry, boot restore, shutdown) stays failed in
    `GET /api/WebhooksEvents/{webhookId}?success=false` until someone resends
    it. #503 left the replay out. The resend call and both response bodies
    are undocumented beyond the API catalog and there is no live account to
    check them against, and an event whose 2xx got lost on the way back
    would be published twice, so it needs the event id as an idempotency key
    first. After a start activates, read the failed events since the
    connection last stopped and resend those; events refused with 410 after
    a stop were refused on purpose and stay. Found in #503.
137. **Decide whether `ui/src/pages/PipelineBuilder.tsx` goes into
    `coverage.exclude` in `ui/vitest.config.ts`.** A page-level deploy test
    (real page and stores, mocked canvas widgets) works, but rendering the
    builder counts about 1,667 more lines and takes global coverage to
    78.4 / 66.8 / 76.8 (statements / branches / functions) against the
    80 / 75 / 80 thresholds. Excluding the page would let that test be
    committed, but it also hides a ~2000 line component from the gate, so it
    needs a human decision. Until then the builder's deploy wiring is pinned
    from `services/deployPipeline.test.ts`. Found in review of #580.
138. **Remember multi-target writes across remote-agent restarts.** Since
    #587 a message NAK'd after some of its targets wrote it skips those
    targets on redelivery, but only within one gateway process (in memory).
    After a restart or a rolling deploy every target writes it again. Keeping
    it across processes needs a JetStream key-value bucket (or a table) keyed
    by stream, sequence and target, with a TTL of the main stream's 72 h.
    A KV bucket is stream configuration: PR plus `agent:needs-human`. Worth
    it only if duplicates after a deploy turn out to matter to a customer.
    Found in #587.

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
8. ~~**Delete the four disk snapshots after 2026-10-29**~~ (#434, done 2026-10-29)
   (`nats-{0,1,2}-before-shrink-20261022`, `minio-before-shrink-20261022`
   in `MC_vrsky-prod_vrsky-prod_norwayeast`).
9. **Sitoo ⇄ Business Central templates.** Need a field mapping tested
   against a real Sitoo sandbox and the BC trial before they return to the
   gallery (`plans/template-fields.md`). While there, check which
   collection holds POS receipts (`transactions` or `orders`) and that
   `incremental: true` sends each one once (#457, agent item 126).
10. **Close #266** (the remote agent shipped).
