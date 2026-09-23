# Running VRSky on a Windows machine

For a **test machine** — a laptop or shop PC where you want to see a pipeline
run end to end. Production still means Kubernetes; see
[Install](install.md).

The whole platform runs in Linux containers, so Windows only has to host
Docker. What follows is the short list of places where that is not quite true.

!!! note "This has not been run on Windows yet"

    Every step below is derived from the compose stack and the two portability
    fixes that went with this page, and each has been checked against a
    `HOME`-less environment. Nobody has yet run it on a Windows machine
    start to finish. Treat it as instructions for the first attempt.

## What you need

| | |
|---|---|
| **Docker Desktop** | with the WSL2 backend (the installer's default). One reboot. |
| **Git for Windows** | |
| **Node 22 LTS** | only for the UI dev server |
| Disk | ~20 GB for images and the build cache |
| RAM | 8 GB is enough for the core set — see below |

`make` is **not** installed on Windows. Either `winget install GnuWin32.Make`,
or use the `docker compose` command spelled out below; they do the same thing.

## Cap Docker's memory first

WSL2 will otherwise grow until Windows is swapping, which looks like the
platform being slow when it is not. Create `C:\Users\<you>\.wslconfig`:

```
[wsl2]
memory=4GB
```

then `wsl --shutdown` once. The ten core services use **~600 MB** between them;
4 GB leaves room for the build.

## Set it up

```powershell
git clone https://github.com/ValueRetail/vrsky.git
cd vrsky
copy .env.example .env
```

Edit `.env` and set `ENCRYPTION_KEY` to 64 hex characters, plus the passwords.
In PowerShell:

```powershell
-join ((1..32) | ForEach-Object { '{0:x2}' -f (Get-Random -Maximum 256) })
```

!!! danger "Back that key up somewhere else, now"

    Every credential you enter in the editor is encrypted with it. Lose the
    key and the secrets are unrecoverable — there is no reset.

## Start the core set

Ten services, not the full 68 — the rest are brokers and test fixtures that a
first pipeline does not touch.

```powershell
docker compose up -d --build nats postgres-management management-api webhook-consumer http-producer file-producer data-filter data-converter business-central-consumer business-central-producer httpbin
```

or, with `make` installed, `make up-core`. The first build takes 10–15 minutes;
afterwards it starts in about a minute. Then the UI:

```powershell
cd ui
npm install
npm run dev
```

- UI — <http://localhost:5173>
- Webhook ingress — `http://localhost:9100/webhook/{connectionId}`

Stop it again with `make down-core`, or `docker compose stop`. **Never
`docker compose down -v`** unless you mean it: `-v` deletes the named volumes,
which is your account, workspaces, pipelines and secrets.

## Prove the machine with a pipeline

Do this before adding anything of your own, so that a Docker problem and an
integration problem cannot be mistaken for one another. In the editor:

1. **Input** node → source type **Business Central (ERP)**. Fill in tenant ID,
   company GUID, client ID and client secret — see
   [Business Central](../connectors/business-central.md) for where those come
   from — with `entity` = `items` and a 60-second poll.
2. **Output** node → **Webhook (HTTP)** → `http://httpbin:80/post`.
3. Connect them, **Save Configuration**, **Deploy**.

Within a minute:

```powershell
docker logs vrsky-business-central-consumer 2>&1 | findstr "fetch complete"
docker logs vrsky-http-producer 2>&1 | findstr "HTTP request sent"
```

`records:N` on one side and `status:200` on the other means the machine is
good: container networking, the database, NATS, secrets decryption and outbound
TLS have all just been exercised.

`httpbin` logs nothing per request — its container output is only the gunicorn
boot lines. The producer's `status:200` is the evidence it answered.

## Windows-specific things that bite

**Line endings.** `.gitattributes` normalises the working tree to LF. Without
it, Git for Windows checks out `entrypoint.sh` with CRLF and the management-api
container exits reporting that a file which plainly exists cannot be found. If
you cloned before that file existed, `git rm --cached -r . && git reset --hard`
re-normalises.

**`HOME` is not set in PowerShell.** The file connectors mount your home
directory so a path typed in the editor means the same thing inside the
container. Where `HOME` is unset they fall back to the repository's
`data/input`, so file pipelines read and write there instead. Set `HOME` in
`.env` if you want the mount.

**Ports.** The core set binds 3000, 4222, 5434, 8080, 8222, 9100, 9101, 9310,
9400, 9600, 9700 and 9900 on loopback. `netstat -ano | findstr :3000` finds a
conflict.

**Docker Desktop must be running** before any `docker compose` command;
otherwise you get `Cannot connect to the Docker daemon`.

**Defender prompts** on first run are for Docker's own listeners. Allow them
for private networks; nothing here needs to be reachable from outside the
machine.
