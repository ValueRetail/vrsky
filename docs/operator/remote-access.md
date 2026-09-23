# Reaching a local stack from another machine

The compose stack binds every port to `127.0.0.1`. That is deliberate: a dev
stack holds real credentials in a database whose password is in a file called
`.env.example`, and it should not appear on a network because somebody ran
`docker compose up`.

Sometimes it has to. A till on another machine posting into a webhook; a
colleague's laptop driving the UI; a Windows PC that cannot run Docker itself
(see [Running on Windows](windows.md)). This is the opt-in for that.

## Start it

```bash
VRSKY_BIND=<address> make up-core-remote
```

which is `docker compose -f docker-compose.yml -f docker-compose.remote.yml`
with the core service list. The extra `-f` is the gate: the default stack is
unchanged, and nothing is reachable off the machine unless you asked.

`VRSKY_BIND` is the address to answer on:

| Value | Who can reach it |
|---|---|
| A **Tailscale** address (`100.x.y.z`) | Only devices in your tailnet, from anywhere. **Prefer this.** |
| A LAN address (`192.168.x.y`, `10.x.y.z`) | Everyone on that network |
| Unset | Every interface — including whatever café wifi the laptop is on |

Tailscale is worth the five minutes. It removes the firewall question, works
when the two machines are not on the same network, and the address does not
change when you move between them. `tailscale ip -4` prints it.

## What this exposes

Only the **webhook ingress** — ports 9100 and 9101 (the latter for connections
using mutual TLS). That is the door a sender outside the machine needs, and
nothing else moves.

The management API stays on loopback. The UI's dev server proxies `/api` from
wherever it runs, so a browser on another machine reaches the API through the
UI without the API being exposed on its own.

To serve the UI to another machine as well:

```bash
cd ui && npm run dev -- --host <the same address>
```

Then browse to `http://<address>:5173`. The UI uses relative API paths, so
there is nothing else to configure.

## Check it

From the other machine:

```bash
curl -s -o /dev/null -w "%{http_code}\n" -X POST http://<address>:9100/webhook/nope -d '{}'
```

`404` is the right answer — the ingress is alive and there is no such
connection. A hang or a refusal means the address, a firewall, or the
`-f docker-compose.remote.yml` is missing.

On the machine running the stack, `docker port vrsky-webhook-consumer` shows
the bindings Docker actually made, which is the claim worth checking rather
than what the compose file says.

macOS prompts once for the firewall on first bind. Allow it for private
networks.

## What this is not

A way to run VRSky for other people. There is no TLS on the ingress here, the
stack's passwords are the ones from `.env.example` unless you changed them, and
a webhook connection without HMAC configured accepts whatever is posted to its
URL. It is a test stack you have chosen to let one other machine reach.

For anything real, deploy it: see [Install](install.md).
