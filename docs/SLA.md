# Service Level Agreement (template)

> This is a **template**. Numbers in brackets are placeholders to set per
> contract/plan; they are not a commitment until agreed in writing.

## 1. Service commitment

VRSky targets **[99.9%]** monthly uptime for the Platform Service (the
management API and data plane that accept and deliver pipeline messages).

| Plan | Monthly uptime target | Support response |
|------|-----------------------|------------------|
| Free | best-effort (no SLA)  | community         |
| Pro  | [99.5%]               | [next business day] |
| Enterprise | [99.9%]         | [1 hour, 24×7]    |

## 2. Definitions

- **Uptime** — the percentage of minutes in a calendar month during which the
  Platform Service is *Available*.
- **Available** — the management API answers `/readyz` with 200 **and** the data
  plane is processing messages (the platform `up` probes are 1). Measured from
  Prometheus probe data — the same source as the [status page](#5-status-page).
- **Downtime** — any minute that is not Available, excluding Exclusions below.
- **Monthly Uptime %** = `(total minutes − downtime minutes) / total minutes × 100`.

## 3. Exclusions

Downtime does **not** include unavailability caused by: scheduled maintenance
announced **[≥48h]** in advance; the customer's own misconfiguration,
credentials, or destination systems; force majeure; or factors outside VRSky's
control (e.g. a customer's network, a third-party API a pipeline targets).

## 4. Service credits

If Monthly Uptime % falls below the plan target, the customer may request a
credit against the next invoice:

| Monthly Uptime % | Credit |
|------------------|--------|
| < target but ≥ [99.0%] | [10%] |
| < [99.0%] but ≥ [95.0%] | [25%] |
| < [95.0%] | [50%] |

Credits are the sole remedy, must be requested within **[30 days]**, and are
capped at **[the monthly fee]**.

## 5. Status page

Real-time component status and rolling uptime are published at
**[status.vrsky.example]**, generated automatically from Prometheus probe data:

- **Human view:** `GET /status` (HTML).
- **Machine/automation view:** `GET /status.json` — overall status plus per
  component (`management-api`, data-plane workers, NATS, gateway, monitoring)
  with current up/down and 24h / 7d uptime.

!!! warning "Independence"
    The bundled status page is served by the management API, so it cannot
    report the management API's own hard-down state. For a customer-facing SLA,
    mirror `/status.json` to an **external** monitor (or a static page refreshed
    off-cluster) so an outage is still visible when the platform is down.

## 6. Measurement & reporting

Uptime is computed from Prometheus `up` series
(`avg_over_time(up{job=…}[30d])`), retained for at least **[the SLA window]**.
Monthly reports are available to Enterprise customers on request.
