# Deploying & monitoring

Once a pipeline is deployed it runs continuously. This page covers how to watch
it, control it, and test it.

## Live event panels (in the builder)

Right after you deploy, the panels at the bottom of the builder stream what's
happening — e.g. **HTTP Output** shows each delivery with its status code, file
panels show files written, the converter/filter panels show transforms and
pass/drop decisions. This is the fastest way to confirm data is flowing.

![Screenshot: the builder's HTTP Output event panel showing SENT POST ... 200 lines](../img/event-panel.png)

!!! note
    The **file producer** has no live panel — check its output folder or ask an
    operator to tail its logs.

## Connections list

**Connections** (sidebar) lists every deployed pipeline in the workspace with
its status (**running**, **stopped**, **error**). Filter by status and page
through them. From here you open a connection or start/stop/delete it.

![Screenshot: the Connections list with status badges and start/stop actions](../img/connections-list.png)

## Connection detail

Click a connection to open its dashboard:

- **Metrics** — throughput, latency, and error counts, with a live chart and a
  visual of the pipeline flow.
- **Controls** — **Start**, **Stop**, **Delete**.
- A link to **Test data** (below).

## Testing a running pipeline

From a connection's **Test data** page (`/connections/{id}/test-data`) you can:

- **Send a test message** — paste/craft a JSON payload and inject it.
- **Auto-generate** — produce a stream of synthetic messages at a chosen rate to
  watch throughput.
- **Message log** — a timestamped log of what you sent and what happened.

For a **webhook** source you can also just `POST` to its webhook URL (shown on
the done step of the wizard and in the builder) and watch the output panel.

## The dead-letter queue (DLQ)

When a message can't be delivered (e.g. the destination rejects it), it isn't
lost — it goes to the connection's **DLQ**. Open the **DLQ** panel to see each
failed message with its error and payload, then **Retry** it (after fixing the
cause) or **Discard** it.

![Screenshot: the DLQ panel listing a failed message with its error and Retry/Discard buttons](../img/dlq.png)

!!! tip "Where to look when nothing happens"
    1. Is the connection **running**? 2. Did you send/receive anything (event
    panel)? 3. Anything in the **DLQ**? See
    [Troubleshooting](troubleshooting.md) for the full checklist.

## Usage

Per-workspace message, deploy, and storage totals are on **Settings → Usage &
quotas**, with a CSV export — see [Settings](settings.md).
