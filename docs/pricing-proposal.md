# VRSky Pricing Proposal

> **Status: proposal for decision.** The *structure* below is grounded in what the
> platform already meters and enforces. The *price points* are placeholders that
> need competitor benchmarking + a target-segment decision before launch.

## Recommendation in one line

**Hybrid: a tiered monthly subscription (the predictable base) + usage overage on
message volume (captures heavy users).** This is the model the schema was already
built for — `tenant_quotas.plan_name` + per-plan ceilings, and `usage_daily`
metering of messages/deploys/storage.

## Why this model

- **Value metric = integrations × volume.** A customer gets more value the more
  retail systems they connect (breadth) and the more data flows through
  (depth). Those are exactly `max_integrations` and `messages_published`, so
  price tracks value.
- **Tiers give predictable revenue + low buying friction.** Customers pick a
  plan, not a calculator.
- **Overage captures upside without punishing spikes.** Heavy months cost more;
  light months don't. Retail is seasonal (Black Friday, holidays) — overage fits
  the traffic shape better than a hard cap that forces a plan upgrade.
- **It's already half-built.** Quotas are enforced inline (`max_msg_per_sec`,
  `max_integrations`) and storage via the hourly job (#74); daily usage is
  metered (#16). The missing pieces are billing + monthly-rollup + real per-plan
  numbers (see *Implementation gap*).

## Proposed tiers

Mapped 1:1 to the existing quota columns. Prices are **placeholders**.

| Plan | Base /mo | Integrations (`max_integrations`) | Throughput (`max_msg_per_sec`) | Included messages/mo | Storage (`max_storage_bytes`) | Overage | Support |
|---|---|---|---|---|---|---|---|
| **Free / Trial** | $0 | 2 | 25 | 100K | 1 GiB | — (hard cap) | Community |
| **Starter** | ~$99 | 5 | 50 | 1M | 10 GiB | $ per extra 1M msgs | Email |
| **Growth** | ~$499 | 20 | 200 | 10M | 100 GiB | $ per extra 1M msgs | Priority |
| **Enterprise** | Custom | Negotiated | Negotiated | Volume | Volume | Committed-use discount | SLA, SSO, dedicated |

Notes:
- **Free is a real trial, not the current dev default** (`free` today = 10
  integrations / 50 msg-s / 1 GiB, which is generous for a dev seed). Tighten
  the `free` row in `tenant_quotas` seed to the table above.
- **Enterprise** unlocks the things that don't fit a self-serve tier: private
  cluster / on-prem edge agents (e.g. the parked SuperPOS agent), custom SLAs,
  SSO, and volume message pricing.

## Overage mechanics

- Meter `usage_daily.messages_published`, roll up per calendar month per tenant.
- If `monthly_messages > plan.included_messages`, bill
  `ceil((monthly_messages − included) / 1,000,000) × overage_rate`.
- **Integrations are a tier lever, not metered overage** — hitting
  `max_integrations` prompts an upgrade (cleaner than per-connector metering and
  matches how the quota is enforced today).
- **Deploys** (`usage_daily.deploys`) stay unmetered for now — a fairness signal
  to watch, not a billed dimension.

## Implementation gap (what's built vs. needed)

**Already there**
- `tenant_quotas.plan_name` + enforced ceilings (`max_msg_per_sec`,
  `max_integrations`, `max_storage_bytes`) — inline enforcement + hourly storage
  job (#74).
- `usage_daily` metering of `messages_published`, `deploys`, `storage_bytes` (#16).

**Needed to launch**
1. **Per-plan quota values** — today only the `free` defaults exist; seed the
   Starter/Growth/Enterprise rows and a `plan_name → quotas` mapping.
2. **Billing integration** (Stripe or similar) — map `plan_name` to a
   subscription; a webhook keeps `tenant_quotas.plan_name` in sync with the
   subscription state.
3. **Monthly overage job** — roll up `usage_daily.messages_published` per month,
   compute overage, report it to the billing provider as metered usage.
4. **Included-messages quota** — `tenant_quotas` enforces rate + count of
   integrations + storage, but not a *monthly message allotment*; add
   `included_messages_per_month` (+ the overage rate) per plan.
5. **Plan-change UX** — upgrade/downgrade flow in the UI; the quota enforcement
   already reacts to `plan_name`.

## Open decisions (need your input)

- **Target segment first** — SMB retailers self-serve (Starter/Growth) vs.
  enterprise-led (bigger deals, slower). That choice sets the real price points.
- **Price anchors** — benchmark vs. iPaaS peers (Celigo, Workato, Patchworks,
  Make/Zapier for the low end) before fixing the numbers.
- **Annual discount** (typical 15–20%) and whether Free requires a card.
- **Message definition** — is one "message" one envelope, or one delivered
  record? Affects the included-message numbers materially.
