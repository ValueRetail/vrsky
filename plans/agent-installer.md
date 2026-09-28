# Remote agent: one-line Windows install (option A)

## Decisions (2026-09-28)

1. **Code signing: deferred.** Revisit the first time a customer's AV blocks
   the unsigned `.exe`. Nothing here changes if we sign later.
2. **Public download: yes.** The page with the command stays behind the login;
   the files it points to are fetchable without one, because the machine being
   set up has no session. The registration token remains the only secret.

## Goal

A new user sets up a Windows machine by pasting **one command** into an
Administrator PowerShell, copied from Settings → Remote agents. The command
downloads the agent from VRSky itself (no GitHub login), asks which folders to
use, registers the machine, installs the service and starts it. Running it again
later **upgrades** the agent. A matching one-liner uninstalls.

Today this is ~8 manual steps and the copy-paste of the bare
`vrsky-agent register …` line went wrong three times on the first real install
(2026-09-28).

## Approach

### Where the files live

The management-api image builds the Windows agent in its own Dockerfile
(cross-compiled, same commit as the server) and serves it from `/app/agent`.
Four **public** routes, all under `/api/` so they reach the API in every
environment without new ingress rules (UI nginx proxies `/api/`, vite proxies
`/api`, k3d/compose likewise):

| Route | Serves |
|---|---|
| `GET /api/v1/agents/release` | `{ "version", "sha256", "size_bytes" }` for `windows-amd64` |
| `GET /api/v1/agents/download/windows-amd64` | the `.exe` (`Content-Disposition: attachment; filename=vrsky-agent.exe`, `X-Checksum-Sha256`) |
| `GET /api/v1/agents/install.ps1` | the install script (`text/plain`) |
| `GET /api/v1/agents/uninstall.ps1` | the uninstall script |

Why management-api and not the UI image or GitHub: the UI image has no Go
toolchain; GitHub artifacts need a GitHub login the machine's operator may not
have. The gateway (`/agent`) was considered too: it is already public, but the
Settings page and the token live in management-api, and `/api/` is proxied
everywhere already.

### The command shown in Settings

Generating a token now shows, as the primary command:

```powershell
& ([scriptblock]::Create((irm https://vrsky.valueretail.no/api/v1/agents/install.ps1))) -Url https://vrsky.valueretail.no -Token vrsky_reg_…
```

- Works in Windows PowerShell 5.1 (LTSC 2019). `irm` is `Invoke-RestMethod`.
- Runs the script in memory, so no `Set-ExecutionPolicy` is needed.
- The token appears **once**, inside a `-Token` argument, so the earlier
  "paste doubled the command" mistake yields a clear error instead of a 401.

Under it, a smaller "Already have the agent installed? Register with:" line
keeps the existing `vrsky-agent register …` command, and a **Download
vrsky-agent.exe (version …)** link points at the download route.

### What `install.ps1` does

```
install.ps1 -Url <VRSky> [-Token <vrsky_reg_…>] [-Name <agent name>]
            [-Inbox <path>] [-Outbox <path>] [-ExePath <local .exe>] [-NoService]
```

1. Refuses to run without Administrator rights, with a one-line explanation.
2. Forces TLS 1.2 (needed for PowerShell 5.1 on older Windows 10).
3. Downloads the `.exe` to `C:\Program Files\VRSky\vrsky-agent.exe.download`,
   checks its SHA-256 against `/release`, then moves it into place and clears
   the download mark. If the service is running (upgrade), it is stopped first
   and started again at the end. `-ExePath` uses a local file instead (USB
   stick, CI).
4. Config: if `C:\ProgramData\VRSky\agent\config.json` does not exist, asks
   for a read folder and a write folder (defaults `C:\VRSky\inbox`,
   `C:\VRSky\outbox`; `-Inbox`/`-Outbox` skip the questions), creates them,
   writes the config. An existing config is **kept** — paths stay the
   operator's decision, and a re-run is an upgrade.
5. `vrsky-agent check`.
6. If not registered and `-Token` was given: `register --url --token --name`
   (`-Name` defaults to the computer name). Already registered: says so and
   skips. Not registered and no token: says where to get one.
7. Unless `-NoService`: `install` (already installed is fine), `start`,
   `status`.
8. Prints the log path and "it now shows under Settings → Remote agents".

`uninstall.ps1 [-Purge]`: `stop`, `uninstall`, delete the `.exe`. `-Purge` also
removes config, credential and logs. Reminds the user to revoke the agent in
Settings, which the script cannot do without a login.

### Files

| File | Change |
|---|---|
| `src/cmd/management-api/Dockerfile` | `COPY src/cmd/vrsky-agent/`; `ARG AGENT_VERSION=dev`; `GOOS=windows GOARCH=amd64 go build -ldflags "-s -w -X …/pkg/agent.Version=$AGENT_VERSION" -o agent/vrsky-agent-windows-amd64.exe ./cmd/vrsky-agent`; `COPY --from=builder /build/agent /app/agent` |
| `infrastructure/azure/build-push-acr.sh` | `build()` takes extra args; management-api gets `--build-arg AGENT_VERSION=$(git describe --always --dirty)` |
| `src/pkg/managementapi/agent_release.go` | The four handlers. Directory from `AGENT_RELEASE_DIR` (default `/app/agent`). SHA-256 and size computed once, lazily. Missing directory (dev without Docker) → 404 with a message saying so. `Cache-Control: no-cache` so an upgraded server is never served stale by a proxy. |
| `src/pkg/managementapi/agent_install.ps1`, `agent_uninstall.ps1` | The scripts, served via `//go:embed` |
| `src/pkg/managementapi/handler.go` | Four `mux.HandleFunc` routes, no role middleware |
| `src/pkg/managementapi/openapi_registry.go` | Four entries (lint-openapi) |
| `src/cmd/management-api/cors.go` | `TenantIDMiddleware` exemption for `/api/v1/agents/release`, `/download/`, `install.ps1`, `uninstall.ps1` |
| `ui/src/services/agentService.ts` | `installCommand(origin, token)`, `uninstallCommand(origin)`, `getAgentRelease()` |
| `ui/src/components/Agents/InstallCommand.tsx` (+ test) | The command box: one-liner, copy button, download link with version, the manual register line. Own file so the coverage gate is not hit by `AgentsPage`. |
| `ui/src/pages/AgentsPage.tsx` | Uses `InstallCommand` in the yellow box |
| `.github/workflows/build-push.yml` | In the existing `agent-windows` job: run `install.ps1 -ExePath bin\vrsky-agent-windows-amd64.exe -Url http://localhost -Inbox … -Outbox … -NoService`, assert config written and `check` OK, then `uninstall.ps1 -Purge`. Offline — no network, no token. |
| `docs/operator/remote-agent.md` | Steps 1–5 become "Quick install (one command)"; the current steps move to "Manual install"; "Upgrading" and "Uninstalling" sections; GitHub artifact mention replaced by the Download link |
| `docs/connectors/remote-agent.md` | One sentence pointing at the quick install |

Not changed: `docker-compose.yml` (build context is already the repo root),
k8s manifests (env default), ingress, the gateway, the agent binary itself.

## Risks

- **Executing a downloaded script** (`irm … | scriptblock`). Same trust as
  downloading the `.exe` over HTTPS from the same host; Chocolatey and Scoop
  install this way. The script is served from the VRSky origin only, and the
  `.exe` is checksum-verified against `/release` from that origin.
- **PowerShell 5.1 quirks** on LTSC 2019: TLS 1.2 default, `irm` returning a
  string for `text/plain`, `ConvertTo-Json` escaping backslashes. Mitigated by
  the offline CI run on `windows-latest` and a real run on the LTSC PC.
- **Unsigned binary** (open question 1). SmartScreen mostly triggers on
  double-click from Explorer, which this path avoids.
- **Image size and build time**: +7 MB, +~10 s for the cross-compile.
- **Public 7 MB download**: negligible load; ingress-nginx and the UI nginx
  already handle larger.
- **Version skew**: the served agent is built from the same commit as the
  server, so Settings always offers a matching build. The CI artifact stays for
  developers.

## How we verify it is done

**Go**
- `agent_release_test.go`: `/release` JSON matches the file in a temp dir;
  download serves the exact bytes with the attachment name and checksum header;
  scripts served as `text/plain`; missing dir → 404; the four paths pass
  `TenantIDMiddleware` **without** `X-Tenant-ID`.
- Mutations: wrong sha → test fails; exemption removed → 400 → test fails.
- `lint-openapi`, `lint-tenant`, `golangci-lint`, `TestDockerfilesRetryModuleDownload`, `go test -race ./...`.

**UI**
- `InstallCommand.test.tsx`: the one-liner contains the origin and the token
  exactly once each; the download link points at the download route and shows
  the version; the copy button copies the one-liner.
- `tsc`, `npm run build`, `npm run test:coverage` (gate 80/75/80/80).

**CI (Windows)**
- The offline install → check → uninstall run in the `agent-windows` job.

**Local**
- `docker compose up -d --build management-api`, then
  `curl -sI localhost:3000/api/v1/agents/download/windows-amd64` is 200 with
  the checksum header, and the sha matches `/release`.

**Prod (Ludvik, after merge)**
- `build-push-acr.sh core`, `deploy-core-azure.sh management-api ui`.
- On the LTSC PC, run the one-liner **without** a token: it must report
  "already registered", replace the `.exe`, restart the service; Settings shows
  online and a dropped file still flows. That proves the upgrade path.
- Then, on a second machine or after `uninstall.ps1 -Purge` + revoke, the full
  first-time path with a fresh token.

## Non-goals

MSI/installer UI, auto-update, code signing (open question 1), Linux/macOS
one-liners (the `.exe`-less `install` already writes a systemd unit; a
`install.sh` can follow the same shape later).
