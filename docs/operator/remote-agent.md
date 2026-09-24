# Remote agent

The remote agent is a small program that runs on another machine — a till, a
warehouse PC, a customer's server — and connects that machine's folders to
VRSky pipelines:

- a **read** folder: files that appear in it are sent into a pipeline, then
  moved to `processed/` (or deleted);
- a **write** folder: files a pipeline produces are written into it.

It is one `.exe` with no dependencies. It needs **no Docker, no WSL, no
virtualisation**, so it runs on machines that cannot run VRSky itself — such as
Windows 10 LTSC 2019. It only ever connects **out** to VRSky over HTTPS, so no
firewall port has to be opened on the machine.

!!! info "What VRSky can and cannot do on the machine"

    The agent's own config file lists the folders VRSky may use. VRSky only
    ever refers to them by name — it never sees or sends a path — and it can do
    exactly two things: take new files from a read folder, and write files into
    a write folder. There is no way for VRSky to run a program, read any other
    folder, or write outside the ones listed.

## 1. Get the agent

Download `vrsky-agent-windows-amd64.exe` from the latest successful **Build
remote agent binaries** run in GitHub Actions (the run's *Artifacts*), or build
it yourself with `make -C src build-agent`.

Copy it to the machine, for example to `C:\Program Files\VRSky\vrsky-agent.exe`.

## 2. Write the config

Create `C:\ProgramData\VRSky\agent\config.json`:

```json
{
  "agent_name": "LAGER-SERVER-01",
  "directories": {
    "superpos-out": { "path": "D:\\SuperPOS\\export", "mode": "read" },
    "superpos-in":  { "path": "D:\\SuperPOS\\import", "mode": "write" }
  }
}
```

- The **names** (`superpos-out`, `superpos-in`) are what you pick in the
  pipeline editor. Letters, digits, `-` and `_`.
- **Paths** must be absolute, must exist, and write folders must be writable.
  Backslashes are doubled in JSON.
- Optional: `poll_interval_seconds` (default 5), `log` (`file`,
  `max_size_mb` 20, `max_files` 5), `data_dir` (default
  `C:\ProgramData\VRSky\agent`).

Check it:

```powershell
& "C:\Program Files\VRSky\vrsky-agent.exe" check
```

It prints the folders exactly as VRSky will see them — names and directions,
no paths.

## 3. Register it

In VRSky, open **Settings → Remote agents → Generate registration token**. It
shows a command; run it on the machine within an hour:

```powershell
& "C:\Program Files\VRSky\vrsky-agent.exe" register --url https://vrsky.valueretail.no --token vrsky_reg_…
```

The token works once. Registration stores the agent's credential in
`C:\ProgramData\VRSky\agent\credential.json`, readable only by Administrators
and the SYSTEM account. The agent now appears under Settings → Remote agents.

## 4. Try it in a window

```powershell
& "C:\Program Files\VRSky\vrsky-agent.exe" run
```

It logs to the window. In VRSky, build a pipeline with **Remote Agent** as
input or output, pick this agent and a folder, and deploy. Drop a file into the
read folder and watch it go. Ctrl-C stops the agent.

## 5. Run it as a service

From a PowerShell opened with **Run as administrator**:

```powershell
& "C:\Program Files\VRSky\vrsky-agent.exe" install
& "C:\Program Files\VRSky\vrsky-agent.exe" start
& "C:\Program Files\VRSky\vrsky-agent.exe" status
```

The **VRSky Agent** service starts automatically at boot (after the network is
up) and is restarted by Windows if it fails. It runs as LocalSystem.

- Logs: `C:\ProgramData\VRSky\agent\logs\agent.log` (rotated at 20 MB, five
  kept).
- Warnings and errors also go to **Event Viewer → Windows Logs → Application**,
  source **VRSkyAgent**.
- `stop`, `uninstall` do what they say. Uninstalling leaves the config,
  credential and logs in place.

On Linux, `install` writes a systemd unit (`vrsky-agent.service`) instead.

## How it behaves

**Read folders.** A file is taken once its size and time have been unchanged
for two scans in a row (5 s apart by default) — the program writing it must be
finished. That is a heuristic, not a lock: a program that pauses mid-write for
longer than that can have a partial file picked up. Where you control the
writing program, have it write to a temporary name ending in `.tmp` or `.part`
and rename it when done; the agent never takes those, nor hidden files.

Files already in a read folder when the agent starts **are** picked up — a file
still there has not been processed. After sending, the file is moved into
`processed/` inside the folder (a name already there gets a timestamp suffix),
or deleted if every pipeline watching the folder asked for that. A file VRSky
refuses outright (too large, an unusable name) goes to `rejected/`, with a
`.reason.txt` beside it, instead of being retried forever.

If the agent restarts between sending a file and moving it, it remembers, and
does not send it again.

**Write folders.** A delivered file is written under a temporary name in the
same folder, checked against the checksum VRSky sent, and only then renamed to
its real name. Whatever reads that folder never sees a half-written file. A file
of the same name is replaced.

**While the agent is offline** — machine off, network down — files for it wait
in VRSky and are written when it reconnects. That waiting has limits: **up to
72 hours, and 24 hours for files over 256 KB**. Past that they are lost. Files
waiting for a read folder simply wait on the machine.

**Revoking.** Revoking the agent in Settings → Remote agents cuts it off on its
next request. It stops, writes the reason to its log and the Event Log, and
refuses to start again until the machine is registered anew (delete
`credential.json`, then `register` with a new token).

## Testing against a local stack over Tailscale

To try the agent against VRSky running on a laptop (see
[Reaching a local stack remotely](remote-access.md)), start the stack with
`make up-core-remote` — it publishes the agent gateway on port 9330 — and point
the agent at the laptop's Tailscale address over plain HTTP:

```json
{
  "server_url": "http://100.x.y.z:9330",
  "allow_insecure_http": true,
  "directories": { … }
}
```

`allow_insecure_http` is required for any `http://` address other than
localhost, because the credential then crosses the network unencrypted. Use it
only on a private network like a tailnet, never for production.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `access denied — run this … as administrator` | `install`, `start`, `stop` need an elevated prompt. |
| `this agent is not registered` | Run `register` first; check `data_dir` if you changed it. |
| `registration token is unknown, already used, or expired` | Tokens are single-use and last an hour. Generate a new one. |
| `an agent called … already exists` | Pass `--name` with another name, or revoke the old agent first. |
| Agent shows **Offline** in VRSky | The service is not running, or cannot reach VRSky. Check `status` and the log. |
| Pipeline refuses to start: *no folder named …* | The folder name in the pipeline is not in the agent's config, or has the wrong direction. Fix the config and restart the agent, which re-announces its folders. |
| A file never leaves a read folder | Still changing, still open in another program, or named `*.tmp` / `*.part` / starting with `.`. The log says which. |
| `VRSky does not recognise this agent's credential` | The agent was removed on the VRSky side (not revoked). Register again. |
