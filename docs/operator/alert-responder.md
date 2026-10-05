# Alert responder

An AI on-call assistant. When an alert fires, it looks into the affected
workspace, works out what is wrong, optionally takes one of a few safe
actions, and posts a report to the same channel the alert went to (Teams,
Slack, …) a minute or two later.

It reacts to the alerts VRSky already receives from Prometheus
([Monitoring](monitoring.md)); it does not read the chat channel.

## What it may do

| Mode | Reads | Changes |
|---|---|---|
| `observe` (default) | pipeline status, last error, events, metrics, dead-letter reasons, remote agents | nothing — the action tools do not exist in this mode; the report says what it *would* have done |
| `act` | the same | **redeploy a pipeline** (once per pipeline per hour), **retry a dead-lettered message** (20 per pipeline per hour), **resend everything** from the source (once per pipeline per hour) |

It can never delete anything, change a pipeline's configuration, see or
change credentials, revoke an agent, or touch the cluster. These limits are
enforced in the service, not requested of the model: each alert gets one run
per hour, each run at most 25 tool calls and three minutes, and at most 20
runs happen per hour in total.

Everything goes through the management API with the workspace's own API key,
so each action appears in that workspace's **audit log**, and the responder
can only reach workspaces whose key it was given.

Alerts about the platform itself (`ConnectorUnavailable`, `DiskUsageHigh`, …)
have no workspace; for those it explains the alert and what to check, with no
tools.

## Reading a report

```
[FIRING:critical] ResponderReport — ConnectionInError: Catalogue to tills is
failing on a rejected Business Central secret; needs a new secret.

Cause — …the decisive error, quoted briefly.
Action — what it did and the result, or "none" and why.
Next — what a person should do now.
(observe mode · 4 tool calls · ~$0.11)
```

If it acted and the alert later clears, a short *resolved* card follows.
If a run fails (API outage, timeout) nothing is posted — the plain alert card
is already there — and the reason is in the pod's log.

## Setting it up

1. **Monitoring must be installed** (alerts have to arrive) and a
   notification target must exist in the workspace.
2. Create the Secrets (values never in git or chat):

   ```bash
   kubectl -n vrsky-platform create secret generic anthropic-api-key \
     --from-literal=api-key="<Anthropic API key>"
   # One line per workspace: <tenant id>=<API key from Settings → API Key>
   kubectl -n vrsky-platform create secret generic alert-responder-tenant-keys \
     --from-literal=keys="<tenant id>=<workspace API key>"
   ```

   `alerts-webhook-token` already exists from the monitoring install.
3. Build and apply:

   ```bash
   infrastructure/azure/build-push-acr.sh core
   infrastructure/azure/deploy-core-azure.sh management-api
   kubectl apply -f infrastructure/kubernetes/alert-responder/deployment.yaml
   ```

   Later upgrades: `deploy-core-azure.sh alert-responder`.
4. It starts in `observe`. To let it act:

   ```bash
   kubectl -n vrsky-platform set env deploy/vrsky-alert-responder RESPONDER_MODE=act
   ```

## Settings

| Env | Default | Meaning |
|---|---|---|
| `RESPONDER_MODE` | `observe` | `observe` or `act` |
| `RESPONDER_TENANT_KEYS` | — | `<tenant id>=<API key>` per line; alerts for other workspaces are skipped |
| `RESPONDER_MAX_RUNS_PER_HOUR` | 20 | hard cap on runs (and so on cost) |
| `RESPONDER_MAX_TOOL_CALLS` | 25 | per run |
| `RESPONDER_MODEL` | `claude-opus-5-5` | the Claude model |

## Cost and data

A run is roughly 20 000 input and 2 000 output tokens — about $0.10 — and the
hourly cap bounds the worst case. What is sent to the model: the alert, and
what the tools return (pipeline names and configuration with credentials
redacted, error messages, counters, agent names, and at most a 2 KB sample of
a dead-lettered message). Credentials are never sent.

## Troubleshooting

| Symptom | Check |
|---|---|
| No report after an alert | `kubectl -n vrsky-platform logs deploy/vrsky-alert-responder` — `alert not handled` gives the reason (handled within the hour, no key for the workspace, hourly cap); `responder run failed` gives the API error |
| `no API key configured for this workspace` | add the workspace to `alert-responder-tenant-keys` and restart the pod |
| Reports arrive but never mention an action | it is in `observe` mode |
| A tool says `HTTP 401` | the workspace API key was rotated; update the Secret |
