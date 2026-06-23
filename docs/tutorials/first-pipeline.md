# Build your first pipeline

**Goal:** receive a webhook and forward it somewhere — running in under 10
minutes, no code. You'll use the onboarding wizard.

## Before you start

- VRSky is running and you can reach the UI (`http://localhost:5173` locally).
- That's it — no credentials needed for this tutorial.

## 1. Sign in

Register or log in. On first login (a workspace with no pipelines yet) you land
on the **Get started** wizard automatically. You can also reach it any time at
`/welcome`.

## 2. Pick a template

Choose **Webhook → HTTP endpoint**. Templates pre-fill ~80% of the pipeline; you
only fill what's yours.

> Other starter templates: **Webhook → Slack** (paste a Slack incoming-webhook
> URL), **CSV → Database**, **API poll → File**.

## 3. Fill the one field

For Webhook → HTTP, enter a **Destination URL** — any endpoint that accepts a
POST. For a no-setup target, use a request-bin style service, or
`https://httpbin.org/post`. Click **Deploy pipeline**.

## 4. See it work

The final step shows your **webhook URL** and a **Send a sample event** button.
Click it — VRSky POSTs a sample body to your webhook, runs it through the
pipeline, and forwards it to your destination. A green ✓ with an HTTP status
means it worked end-to-end.

## 5. Send your own data

POST anything to the webhook URL shown:

```bash
curl -X POST <your-webhook-url> \
  -H 'Content-Type: application/json' \
  -d '{"hello":"world"}'
```

Watch the producer node's live event panel light up with each delivery.

## What you just built

A running connection: **webhook consumer → HTTP producer**, with delivery
flowing through NATS. From here:

- Add a **filter** or **converter** between the nodes in the visual builder.
- Try a [different connector](../connectors/index.md) as the source or
  destination.
- Watch traffic on **Settings → Usage** and in Grafana
  ([monitoring](../operator/monitoring.md)).
