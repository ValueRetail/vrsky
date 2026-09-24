# VRSky Remote Agent (#266) — implementation plan

## Open questions

None blocking. Two decisions were taken with Ludvik on 2026-09-24 and are baked
in below: **HTTPS to a gateway service** (not NATS-over-WebSocket) and
**JetStream per-connection durables** for offline delivery (not a Postgres
outbox).

Deferred, decide during PR3: Linux packaging beyond a systemd unit; whether
`register` should also `install` in one step.

## Context

A pipeline can only read and write where VRSky itself runs. The Remote Agent
is a small Go binary on a customer machine that dials **out** to VRSky and
makes that machine selectable as a pipeline input (watch a directory) or output
(write a file). It unblocks three real cases: the SuperPOS integration (whose
agreed architecture *is* this component), the Windows LTSC 2019 test PC that
cannot run Docker at all, and customers who will not open a firewall.

Exploration corrected the brief in ways that shape the design:

- **NATS has no auth at all today** — no accounts, no credentials, no
  WebSocket listener. Exposing it to the internet would mean building a NATS
  auth layer first. So the agent never speaks NATS; it speaks HTTPS to a
  standing connector, and NATS stays inside the cluster.
- **The SDK's producer path cannot hold a message for an offline agent** —
  `RunProducer` NAKs on error and DLQs after 5 tries (~3 minutes). The output
  direction must own per-connection durables, exactly as `tenant-consumer`
  already does (`tenant-bridge-<connID>`, `lint:connector-ok`).
- **`pkg/messaging` dispatches a fetched batch sequentially and heartbeats only
  the message in the handler** (`subscriber.go:304-316`). A handler that blocks
  for hours would let any prefetched sibling expire and burn toward the DLQ.
  Therefore agent durables run `MaxAckPending: 1` and "hold while offline" is
  implemented by **blocking the handler**, never by NAK.
- **No presigned URLs exist** (`objectstore.ObjectStore` has no presign). Large
  bodies stream *through* the gateway.
- **Four artefacts must change in step or tests fail**: the UI dropdown, the
  `nodeConfigRules` table, the `GENERIC` row in `deploy-connectors-azure.sh`,
  and `build-push-acr.sh` (`TestNodeConfigRulesCoverUI`,
  `TestNodeConfigRulesAreDeployed`, `TestConnectorImagesAreBuilt`).
- `deploy-core-azure.sh` only runs `kubectl set image`; new env or ingress must
  be applied explicitly (bit us on 2026-09-24).

## Architecture

```
Windows PC                      ingress-nginx (443)              vrsky-platform
┌─────────────┐   HTTPS         /agent  ──────────►  vrsky-remote-agent:9330
│ vrsky-agent │ ──────────────► (Bearer vrsky_agent_…)   │  sdk.RunConsumer + StreamingConsumer
│  register   │                                          │  ├─ input:  upload → publish/publishStream (claim-check)
│  poll/work  │ ◄── long-poll 25s (= heartbeat)          │  ├─ output: durable remote-agent-out-<connID>
│  upload     │ ──► streaming body                       │  │          FilterSubject vrsky.data.<t>.pipeline.<connID>
│  body/ack   │ ◄──►                                     │  │          MaxAckPending 1, handler blocks until agent acks
└─────────────┘                                          │  └─ /events/{connID} SSE → worker_events_proxy
                                                         ▼
                                              Postgres: agents, agent_registration_tokens
                                              management-api: /api/v1/agents/* (UI CRUD, token minting)
```

One new standing service `remote-agent` serves **both** `{"consumer","remote_agent"}`
and `{"producer","remote_agent"}` rules with a single GENERIC row
`remote-agent consumer 9330` (a port ⇒ 1 replica + Service; pending
deliveries are in-memory so it must not be 2 replicas). Port 9330 is unused.

**Tenant isolation, two independent halves, neither trusting a header:**
the credential resolves to an agent row (and therefore its tenant); the
connection is looked up scoped by the command's tenant; the agent named in a
node's config must be `WHERE id=$1 AND tenant_id::text=$2 AND revoked_at IS NULL`
with the directory present in its reported list. Pending work is keyed by the
*authenticated* agent first, so a foreign delivery ID is simply unknown (404).

## PR plan — four PRs, each independently deployable

### PR1 — Schema + management API + Settings → Remote agents page

Inert on its own (nothing consumes tokens yet). Tenants can mint registration
tokens and list / rename / revoke agents.

**New:** `infrastructure/migrations/000022_remote_agents.{up,down}.sql`,
`src/pkg/managementapi/{repo_agents.go, agents_handler.go, agents_handler_test.go}`,
`ui/src/services/agentService.ts`, `ui/src/pages/AgentsPage.tsx`.
**Modified:** `handler.go` (routes), `openapi_registry.go`,
`src/cmd/lint-tenant-filter/main.go` (`tenantScopedTables` += both tables),
`ui/src/App.tsx` (`/settings/agents`), `ui/src/components/Layout/Sidebar.tsx`,
`isolation_test.go`.

**Schema.** `tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE` — the
shape every settings table since multi-tenancy uses (`secrets`, `tenant_invites`,
`oauth_providers`); `connections.tenant_id VARCHAR` is the legacy exception and
the one join to it casts `a.tenant_id::text`. Directories are a JSONB snapshot
(names + modes only — **paths never leave the machine**).

```sql
CREATE TABLE agents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name            VARCHAR(255) NOT NULL,
    hostname        VARCHAR(255) NOT NULL DEFAULT '',
    os              VARCHAR(32)  NOT NULL DEFAULT '',
    arch            VARCHAR(32)  NOT NULL DEFAULT '',
    agent_version   VARCHAR(64)  NOT NULL DEFAULT '',
    credential_hash CHAR(64) NOT NULL UNIQUE,          -- auth.HashToken(raw); raw shown once, never stored
    directories     JSONB NOT NULL DEFAULT '[]'::jsonb, -- [{"name":"inbox","mode":"read"}]
    last_seen_at    TIMESTAMPTZ,
    registered_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at      TIMESTAMPTZ,
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX idx_agents_tenant ON agents(tenant_id);
CREATE UNIQUE INDEX idx_agents_tenant_name_live ON agents(tenant_id, lower(name)) WHERE revoked_at IS NULL;

CREATE TABLE agent_registration_tokens (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    token_hash     CHAR(64) NOT NULL UNIQUE,            -- the lookup index (tenant_api_keys forgot one)
    suggested_name VARCHAR(255),
    created_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at     TIMESTAMPTZ NOT NULL,
    used_at        TIMESTAMPTZ,
    used_by_agent  UUID REFERENCES agents(id) ON DELETE SET NULL
);
CREATE INDEX idx_agent_reg_tokens_tenant ON agent_registration_tokens(tenant_id);
```
Down drops tokens first (FK), then agents. Revoke keeps the row (history; a
pipeline still naming it can say why it's broken) and releases the name.

**Reuse:** `auth.HashToken` (`src/pkg/auth/token.go`). Token `vrsky_reg_<64 hex>`
from 32 random bytes, TTL 1 h. Do **not** copy `tenant_api_keys` (one per
tenant, hard-coded admin, no revoke) or invites (plaintext token, non-atomic
consume).

**Routes** (all under `X-Tenant-ID`; each gets an `apiRoutes` entry):
```go
mux.Handle("POST /api/v1/agents/registration-tokens", adminMW(h.CreateAgentRegistrationToken))
mux.Handle("GET /api/v1/agents",         viewer(h.ListAgents))    // online = last_seen_at > NOW()-90s AND revoked_at IS NULL
mux.Handle("PATCH /api/v1/agents/{id}",  editor(h.RenameAgent))   // {"name"}; 409 on live duplicate
mux.Handle("DELETE /api/v1/agents/{id}", adminMW(h.RevokeAgent))  // sets revoked_at; second call 404
```
`AgentStore` interface on the repo (pattern: `inviteStore()` in `invites_handler.go:32`).
Audit actions `agent.token.create | agent.rename | agent.revoke`.

**UI.** `AgentsPage` = `UsersPage` list/rename/revoke + `ApiKeyPage` show-once
yellow box, which also prints the exact
`vrsky-agent register --url <origin> --token <token>` line to copy.

**Tests.** `TestAgents_CreateRegistrationToken_ReturnsRawOnce`,
`TestAgents_Revoke_SecondCall404`, `TestAgents_Rename_ConflictOnDuplicateLiveName`,
`TestIsolation_AgentsAreTenantScoped` (tenant B on A's agent → 404/empty).
*Mutation:* remove `AND tenant_id = $1` from Get/Rename/Revoke → the isolation
test AND `make lint-tenant` must both fail.

**Rollout.** Migration runs on management-api start. TEST:
`build-push-acr.sh core` → `deploy-core-azure.sh management-api ui` → check
`\dt agents` and the page → prod. No env/secret/ingress changes.

### PR2 — `pkg/agentproto` + gateway `cmd/remote-agent` + node type + deploy plumbing

The service exists everywhere, serves the wire protocol, and `remote_agent` is
a valid source/destination. Deployable before any agent binary exists.

**New:** `src/pkg/agentproto/{proto.go, names.go, filename.go}` + tests;
`src/cmd/remote-agent/{main.go, service.go, auth.go, register.go, sessions.go,
output.go, input.go, http.go, events.go, Dockerfile}` + tests;
`infrastructure/kubernetes/ingress/agent-ingress.yaml`;
`ui/src/components/Pipeline/RemoteAgentConfigEditor.tsx`.
**Modified:** `deploy-connectors-azure.sh` (GENERIC row `remote-agent consumer 9330`),
`build-push-acr.sh` (`local generic=(… remote-agent)`),
`infrastructure/kubernetes/connectors/connectors.yaml` (regenerate with
`GENERATE_ONLY=1`), `docker-compose.yml` (service, `127.0.0.1:9330`,
`PAYLOAD_STORE_*` as file-consumer), root `Makefile` (`CORE_SERVICES += remote-agent`),
`src/Makefile` (`lint-tenant` also runs `LINT_ROOT=cmd/remote-agent`),
`nodeconfig.go` (two rules), `nodeconfig_test.go` (+`TestAgentIngressTargetsRealServices`),
`handler.go` `StartConnection` (early agent-ownership check),
`PropertyEditor.tsx` (both option arrays — single quotes, no `]` — + block).

**`pkg/agentproto`** (pure structs, no deps; imported by gateway and agent):
```go
const ProtoVersion = 1; HeaderProto = "X-Vrsky-Agent-Proto"
const PollHoldDefault = 25*time.Second   // < ingress-nginx 60s read timeout, < typical NAT idle
const LeaseDuration   = 2*time.Minute    // unacked delivery is re-offered
const InlineWorkBytes = 64<<10           // ≤ this rides inside the poll response
var   DirectoryName   = `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`
func  ValidFilename(name) error          // rejects "", ".", "..", any / or \, Base(name)!=name, <>:"|?*, control chars, CON/PRN/AUX/NUL/COM1-9/LPT1-9, >255
func  GenerateFilename(pattern, id, contentType, source, metaFilename, createdAt) string  // file-producer {id}{timestamp}{extension}{source} semantics

type RegisterRequest  { RegistrationToken, Name, Hostname, OS, Arch, Version string; Directories []Directory }
type RegisterResponse { AgentID, Name, Credential string }
type Directory        { Name, Mode string }              // read|write
type AnnounceRequest  { Hostname, OS, Arch, Version string; Directories []Directory }
type WorkResponse     { Watches []Watch; Deliveries []Delivery; NextPollMS int }
type Watch            { Op, ConnectionID, Directory, After string }   // op "watch_dir"; after move|delete
type Delivery         { Op, ID, ConnectionID, Directory, Filename, ContentType, Checksum string; Size int64; InlineBase64, BodyURL string }  // op "write_file"
type AckRequest       { Status, Error string }            // ok|failed
type UploadResponse   { EnvelopeID string }
type ErrorResponse    { Error, Message string }
```
`Op` fields exist from day one so future operation types are additive.
Error codes: `invalid_token, token_expired_or_used, unauthorized, agent_revoked,
unsupported_protocol, unknown_delivery, unknown_directory, invalid_filename,
no_active_watch, too_large`.

**Wire protocol** (`Authorization: Bearer vrsky_agent_…`, `X-Vrsky-Agent-Proto: 1`;
major mismatch → 426):

| Route | Purpose |
|---|---|
| `POST /agent/v1/register` (no bearer) | consumes token **atomically**, inserts agent, returns credential once |
| `POST /agent/v1/announce` | on start / reconnect: machine info + directory names → `UPDATE agents … WHERE id=$1` |
| `GET /agent/v1/work?wait=25` | **the heartbeat.** Blocks ≤25 s; returns the FULL desired watch set (idempotent, self-healing) + leased deliveries; throttled `last_seen_at` write (30 s) |
| `GET /agent/v1/deliveries/{id}/body` | streams inline bytes or `store.GetStream(PayloadRef)` through a checksum-verifying reader |
| `POST /agent/v1/deliveries/{id}/ack` | `ok` → handler returns nil → JetStream ack; `failed` → NAK → backoff → DLQ after 5 |
| `POST /agent/v1/uploads?connection_id=&directory=&filename=&upload_id=` | raw streaming body; `upload_id` (agent UUID) becomes envelope ID so a retry after a lost 2xx is deduped (5-min window) |
| `GET /events/{connID}` | SSE for the builder panel (proxied in PR4) |

**Gateway internals** (`service.go`):
```go
type remoteAgent struct {
    sdk.BaseConsumer
    db *sql.DB; js nats.JetStreamContext
    publish sdk.PublishFunc; publishStream sdk.PublishStreamFunc; inlineMax int
    store objectstore.ObjectStore          // claimcheck.OpenStoreFromEnv — SDK's store is unexported
    agents  map[string]*agentState         // agentID → pending deliveries + notify chan(cap 1)
    outputs map[string]*outputSession      // connID → durable session
    inputs  map[string]*inputWatch         // connID → desired watch
    events  *eventHub                      // per-connection SSE fan-out (file-consumer emitEvent)
}
```
- `RunStream`: keep closures; subscribe `vrsky.commands.*.connection.start|stop`;
  **rebuild on boot** from `SELECT … FROM connections WHERE status='running'`
  (idempotent: durables bind at kept position via `ensureConsumer` + `nats.Bind`).
  file-consumer's not doing this is a documented gap, not a design choice.
- Start: `getConnection(id, tenant)` scoped by tenant (tenant-consumer's) → per
  `remote_agent` node, verify agent `WHERE id=$1 AND tenant_id::text=$2 AND revoked_at IS NULL`
  and directory+mode in its list → else status `error`, don't start.
- Output durable: `messaging.Subscribe(js, SubscriberOpts{DurableName:"remote-agent-out-"+connID,
  FilterSubject: messaging.DataSubject(tenant, connID), MaxAckPending:1, AckWait:30s})`
  (bridge.go precedent, `// lint:connector-ok`). Handler: predecessor eligibility
  (file-producer `eligibleConfigs`); build delivery; invalid filename = poison
  (log, event, ack); push to the agent's pending map; **block on `result`**;
  nil → ack, error → NAK. On stop the handler returns error so `Stop()` completes.
- Upload: `inputs[connID]` must exist AND `AgentID == authenticated agent` AND
  directory matches → else one 404 `no_active_watch`. Envelope tenant comes from
  the connection row, never the request. `Content-Length > inlineMax` → `publishStream`.
- Auth: bearer → `auth.HashToken` → `SELECT id, tenant_id::text, name, revoked_at FROM agents WHERE credential_hash=$1`
  (`lint:tenant-ok`, one indexed read, no cache ⇒ revoke is immediate). Revoked → 403.
- Register: one transaction — `UPDATE agent_registration_tokens SET used_at=NOW()
  WHERE token_hash=$1 AND used_at IS NULL AND expires_at>NOW() RETURNING id, tenant_id, suggested_name`
  (0 rows → 401) → insert agent with `vrsky_agent_<64 hex>` → link token → commit.

**Node config** (`agent_name` display-only; no key named token/secret/etc.):
```json
{"type":"remote_agent","remote_agent":{"agent_id":"…","agent_name":"LAGER-SERVER-01","directory":"superpos-out","after":"move"}}
{"type":"remote_agent","remote_agent":{"agent_id":"…","agent_name":"LAGER-SERVER-01","directory":"superpos-in","filename_pattern":"orders-{timestamp}.{extension}"}}
```
```go
{"consumer","remote_agent"}: {"remote-agent", []configRequirement{{"remote_agent.agent_id",…},{"remote_agent.directory",…}}},
{"producer","remote_agent"}: {"remote-agent", []configRequirement{{"remote_agent.agent_id",…},{"remote_agent.directory",…}}},
```
`StartConnection` adds an early, friendly check via `h.repo.(AgentStore)`
(nodeconfig validation is presence-only and has no repo); the gateway repeats it —
the gateway's is the one that matters for isolation.

**UI editor:** `const c = (config.remote_agent as Record<string,unknown>) || {}` +
`update(patch)` (business_central pattern); agent `StyledSelect` from
`agentService.listAgents()` labelled `name · online/offline`; directory select
filtered by mode; source: `after` (move default | delete); destination:
`filename_pattern`; warn when revoked/offline.

**Ingress** `agent-ingress.yaml`: Ingress `vrsky-agent`, both hosts, `/agent`
Prefix → `vrsky-remote-agent:9330`; `force-ssl-redirect`, `proxy-body-size: "0"`,
`proxy-request-buffering: "off"` (stream uploads, don't spool to nginx disk),
`proxy-read-timeout/send-timeout: "300"`; no `tls:` block (same reasoning as
webhooks-ingress). Gateway applies `MaxBytesReader` (env `AGENT_UPLOAD_MAX_BYTES`, default 2 GiB).

**Tests.** agentproto: `TestValidFilename_RejectsTraversalAndReserved`,
`TestGenerateFilename_MatchesFileProducer`. Gateway (httptest + sqlmock +
`harness.StartEmbeddedJetStream`):
- `TestRegister_ConsumesTokenAtomically` — two concurrent registers, exactly one 201. *Mutation:* drop `AND used_at IS NULL` → fails.
- `TestAgentAuth_UnknownCredential401`, `TestAgentAuth_RevokedCredential403`. *Mutation:* ignore `revoked_at` → fails.
- `TestStart_RefusesAgentFromAnotherTenant`. *Mutation:* drop `AND tenant_id::text=$2` → fails.
- `TestStart_RefusesUnknownDirectoryOrWrongMode`.
- `TestOutput_AcksOnlyAfterAgentConfirms`.
- **`TestOutput_HoldsWhileAgentOffline_NoRedeliveryCount`** — publish; no poll for > 3×AckWait; `NumRedelivered==0`, `NumAckPending==1`; poll+ack succeeds. *This is the offline guarantee.*
- `TestOutput_FailedAckNaksThenDLQ` (5 fails → `vrsky.dlq.>`), `TestOutput_LeaseExpiryReoffers`, `TestOutput_RebuildsSessionsOnBoot`.
- `TestInput_UploadPublishesEnvelopeWithConnectionTenant` (large → PayloadRef; small → inline).
- `TestIsolation_WorkNeverCrossesAgents` — B polls, sees nothing of tenant 1. *Mutation:* key `pending` globally by delivery ID → fails.
- `TestIsolation_AckOrBodyForForeignDeliveryIs404`.
- `TestIsolation_UploadToOtherTenantsConnection404`. *Mutation:* drop `watch.AgentID == agent.ID` → fails.
- management-api: `TestStartConnection_RejectsAgentFromAnotherTenant`, `TestAgentIngressTargetsRealServices`; existing `TestNodeConfigRules*`, `TestDockerfilesRetryModuleDownload` (Dockerfile copied from sitoo-consumer with the retry loop).

**Rollout (TEST then prod).** `build-push-acr.sh connectors` + `core` →
`deploy-connectors-azure.sh` (new Deployment+Service; restarts all connectors —
**every running pipeline must be redeployed afterwards**) →
`kubectl apply -f infrastructure/kubernetes/ingress/agent-ingress.yaml` →
`deploy-core-azure.sh management-api ui` → verify
`curl -X POST https://vrsky.valueretail.no/agent/v1/register -d '{}'` returns the
gateway's JSON error (not nginx 404). No new secrets: agents authenticate against
DB hashes, not a shared worker token.

**Risks.** A rollout briefly runs two pods bound to the same durables — harmless
with `MaxAckPending:1` (old pod NAKs on SIGTERM, new pod re-fetches; costs one
`NumDelivered`). Unbounded `proxy-body-size` is deliberate and bounded by the
gateway's `MaxBytesReader`.

### PR3 — The agent binary `vrsky-agent`

Windows-first. End-to-end works after this PR.

**New:** `src/cmd/vrsky-agent/main.go` (stdlib `flag` subcommands: `register
--url --token [--name] [--config]`, `run`, `install`, `uninstall`, `start`,
`stop`, `status`, `version`); `src/pkg/agent/{config.go, credential.go,
credential_windows.go, credential_other.go, client.go, runner.go, watcher.go,
writer.go, logging.go, service_windows.go, service_other.go,
eventlog_windows.go, eventlog_other.go}` + tests; `docs/operator/remote-agent.md`.
**Modified:** `src/Makefile` (`build-agent`: windows-amd64 `.exe`, linux-amd64,
darwin-arm64; `CGO_ENABLED=0 -ldflags "-s -w -X main.version=…"`), root
`Makefile`, `.github/workflows/build-push.yml` (job `agent-binaries` →
`actions/upload-artifact@v4`), `go.mod` (`golang.org/x/sys` indirect → direct;
**no new module**), `docs/operator/windows.md` (link), `mkdocs.yml`.

**No third-party deps.** Windows service via `golang.org/x/sys/windows/svc`
(+ `svc/mgr` for install with `StartAutomatic` and restart-on-failure recovery,
`svc/eventlog` for Event Log) — already in the module graph, verified to resolve
under `GOOS=windows`. All Windows code behind `//go:build windows` with `_other.go`
stubs so CI's Linux `go test -race ./...` and `go vet` stay green. Watching by
polling (no fsnotify). Log rotation by size in stdlib.

**Config** `C:\ProgramData\VRSky\agent\config.json` / `/etc/vrsky-agent/config.json`:
```json
{
  "server_url": "https://vrsky.valueretail.no",
  "agent_name": "LAGER-SERVER-01",
  "poll_interval_seconds": 5,
  "log": { "file": "C:\\ProgramData\\VRSky\\agent\\logs\\agent.log", "max_size_mb": 20, "max_files": 5 },
  "directories": {
    "superpos-out": { "path": "D:\\SuperPOS\\export", "mode": "read",  "after": "move" },
    "superpos-in":  { "path": "D:\\SuperPOS\\import", "mode": "write" }
  }
}
```
`server_url` must be `https://` unless localhost or `--insecure-http`.
Directory names/modes are what `announce` reports; **paths never leave the machine.**

**Credential storage.** `credential.json` `{agent_id, credential, server_url, registered_at}`.
Windows: under `%ProgramData%\VRSky\agent\`, DACL `D:PAI(A;;FA;;;SY)(A;;FA;;;BA)`
(SYSTEM + Administrators, inheritance cut) via `windows.SetNamedSecurityInfo`.
Unix: `/var/lib/vrsky-agent/` `0700`/`0600`. Written temp+rename.

**Client** (`client.go`): backoff 1 s → 60 s full jitter on transport errors/5xx;
401 → loud log, back off 5 min; 403 `agent_revoked` → persist, Event Log error,
exit non-zero (service shows failed with a clear reason); 426 → back off 10 min
"upgrade the agent".

**Watcher** (`watcher.go`): poll every `poll_interval_seconds`; ignore subdirs,
dotfiles, `*.part`, `~vrsky-*`, `processed/`; a file is eligible when size+mtime
are unchanged across two consecutive scans (the stable-size check file-consumer
lacks); upload with fresh `upload_id`; on 2xx `move` → `processed/<name>`
(collision → `<name>.<unix>`) or `delete`; a post-upload move/delete failure is
recorded in `state.json` so a restart never re-ingests.

**Writer** (`writer.go`): `agentproto.ValidFilename` again (defence in depth);
directory must be a configured `write` dir; `final := Join(dir, name)`; assert
`Dir(final) == Clean(dir)`; write `~vrsky-<deliveryID>.part` in the **same
directory** (same volume ⇒ atomic rename), sha256 tee vs `Checksum`, `Sync`,
`Rename`; any error removes the `.part`; rename retried 3× (AV scanners); then ack.

**Tests.** `TestWrite_RejectsTraversalAndUnknownDirectory`,
`TestWrite_AtomicNoPartialVisible`, `TestWrite_ChecksumMismatchLeavesNothing`,
`TestWatcher_WaitsForStableSize`, `TestWatcher_MoveAfterUpload`,
`TestWatcher_DoesNotReingestAfterRestart`, `TestClient_RevokedStopsAndPersists`,
`TestClient_BackoffOn5xx`, `TestRunner_ReconcilesWatches` (httptest gateway stub),
`TestConfig_RejectsRelativePathsAndBadNames`.

**Rollout.** Nothing server-side. Download the CI artifact → Windows PC →
`vrsky-agent.exe register --url https://<TEST> --token …` → `run` in a console
first → `install` + `start` → Event Viewer → Application → VRSkyAgent; Settings →
Remote agents shows Online.

### PR4 — Builder panel, SSE events, connector docs

**Modified:** `worker_events_proxy.go` (`"remote-agent": {"remote-agent", 9330}`
+ registry summary), `ui/src/services/workerEvents.ts` (`EventWorker += 'remote-agent'`),
`PipelineBuilder.tsx` (`remoteAgentPanel` state, `useWorkerEvents(…, 'remote-agent', …)`,
bottom tab `{ id:'agent', label:'Remote Agent', color:'#0891b2' }`, header with
`agent_name · online/offline` refreshed from `listAgents()` every 15 s,
`consumerDetail/producerDetail = agent:directory`, close-button reset),
`docs/connectors/remote-agent.md` (`# Remote Agent` / `## As a source (consumer)` /
`## As a destination (producer)` / `## Notes` incl. the offline cap),
`docs/connectors/index.md` row `| Remote Agent | ✓ | ✓ | remote_agent |`, `mkdocs.yml`.
Gateway event types: `agent_online | agent_offline | delivered | ingested | failed`.

**Tests.** `TestProxyWorkerEvents_RemoteAgentAllowlisted`; vitest for `agentService`.

## Tenant-isolation matrix (where each check lives)

| Boundary | Check | Test |
|---|---|---|
| Registration token → tenant | atomic `UPDATE … RETURNING tenant_id`; agent inherits it in the same tx | `TestRegister_ConsumesTokenAtomically` |
| Credential → agent → tenant | `WHERE credential_hash=$1`; revoked → 403; tenant from the row, never a header | `TestAgentAuth_*` |
| Connection on start | `WHERE id=$1 AND tenant_id=$2` (tenant from the command) | `TestStart_RefusesAgentFromAnotherTenant` |
| Agent named in node config | `WHERE id=$1 AND tenant_id::text=$2 AND revoked_at IS NULL` + directory/mode present — gateway (authoritative) and `StartConnection` (early) | same + `TestStartConnection_RejectsAgentFromAnotherTenant` |
| Work / body / ack | pending map keyed by authenticated agent first | `TestIsolation_WorkNeverCrossesAgents`, `…ForeignDeliveryIs404` |
| Upload | `inputs[connID].AgentID == agent.ID && directory ==`; envelope tenant from the row | `TestIsolation_UploadToOtherTenantsConnection404` |
| Management API | every query `WHERE tenant_id=$1`; tables in `tenantScopedTables` | `TestIsolation_AgentsAreTenantScoped` + `make lint-tenant` |

## Offline semantics (document verbatim in the connector page)

Messages for an offline agent wait in `VRSKY_DATA` under that connection's
durable, which keeps its position across gateway restarts. The stream is bounded
by `MainRetention = 72h` and `MainMaxBytes = 512 MiB` with `DiscardOld`
(`pkg/messaging/messaging.go:44,54`), and spilled bodies (> 256 KiB) expire after
the bucket's 1-day lifecycle. **Effectively: small payloads survive 72 h offline,
large ones 24 h.** Raising the `spill/` TTL is a follow-up.

## Verification

**Unit/CI on every PR:** `gofmt -l .`, `go test -race ./...`, `golangci-lint run`,
`go run ./cmd/lint-openapi`, `make lint-tenant` (incl. `LINT_ROOT=cmd/remote-agent`),
`cd ui && npx tsc --noEmit && npx vitest run`. Each security check above gets
its mutation test run once and the failing output pasted into the PR.

**Local end-to-end (after PR3):**
```bash
make up-core                                  # remote-agent in CORE_SERVICES, 127.0.0.1:9330
cd ui && npm run dev                          # Settings → Remote agents → Generate token
cd src && make build-agent
mkdir -p ~/vrsky-agent/{inbox,outbox}         # config.json: inbox(read,move) / outbox(write)
bin/vrsky-agent-darwin-arm64 register --url http://localhost:9330 --token vrsky_reg_… --config ~/vrsky-agent/config.json
bin/vrsky-agent-darwin-arm64 run --config ~/vrsky-agent/config.json
# pipeline: Remote Agent(inbox) → Remote Agent(outbox); Deploy
cp sample.json ~/vrsky-agent/inbox/           # → outbox/, original → inbox/processed/
# offline proof: Ctrl-C the agent; send 3 test-messages to a pipeline ending in the agent
curl -s localhost:8222/jsz?consumers=true | jq '..|objects|select(.name?=="remote-agent-out-<connID>")'  # num_ack_pending 1, num_redelivered 0
# restart the agent → three files land, in order
```
Then TEST with the Windows LTSC PC over Tailscale/HTTPS, then prod.

## Non-goals / follow-ups

Rate limiting on `/agent/v1/register` and credential lookups; gateway-side audit
rows; raising `spill/` TTL / NATS retention for long outages; `MaxAckPending > 1`
(needs a concurrent dispatch loop in `pkg/messaging`); auth cache +
`vrsky.commands.<tenant>.agent.revoke`; MSI, auto-update, code signing; operations
beyond files (the `Op` field makes them additive); purge of revoked agents;
marking connections `error` on revoke; sharing `GenerateFilename` back into
file-producer; Linux packaging beyond a systemd unit.
