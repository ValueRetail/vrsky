# Plans and billing

VRSky is open to sign up and not free to keep using. This page is how that
works day to day. The design is `plans/paid-plans.md`.

## The flow

1. **Sign-up** creates a workspace on the **trial** plan with a 14-day clock
   (`tenants.trial_ends_at`). A banner counts down.
2. **The trial ends.** The billing sweep (hourly, one API replica at a time)
   marks the workspace **suspended**, stops its running pipelines and
   remembers which, writes a connection event per pipeline, and raises a
   `TrialExpired` alert to the platform notification targets — Teams, if one
   is flagged *platform*. The banner turns red. Configuration, secrets and
   history are untouched; reading and editing keep working. Starting or
   creating a pipeline, data ingest and connection requests answer
   `402 PlanRequired`.
3. **The customer asks.** Settings → Plan → *Request the Paid plan* (or
   Enterprise), with a message. One open request per workspace. You get a
   `PlanRequested` alert with the workspace, owner email and message.
4. **You invoice, then activate** under Platform → Workspaces: pick the plan,
   Save. That copies the tier's limits into the workspace's quotas, sets the
   billing status, closes the request, and starts the pipelines the
   suspension stopped. Pipelines the customer had stopped themselves stay
   stopped.

Payments are outside the product; there is no card entry and no Stripe.

## The tiers

`plan_limits` holds them; 0 means unlimited. Change a tier with one UPDATE —
workspaces already on it keep their copied limits until the plan is set again.

| Plan | Integrations | Throughput | Storage | Messages/month (shown, not enforced) |
|---|---|---|---|---|
| trial | 2 | 25 msg/s | 1 GiB | 100 k |
| paid | 20 | 200 msg/s | 100 GiB | 10 M |
| enterprise | unlimited | unlimited | unlimited | — |

Workspaces that existed before paid plans shipped were grandfathered as
**paid** by the migration.

## Being the operator

`PLATFORM_OPERATORS` on the management API is a comma-separated list of user
emails. Those users see Platform → Workspaces and may call the `/api/v1/platform/*`
routes and the two legacy writes (`PUT …/quotas`, `PUT …/plan`). Nobody else
can, whatever their workspace role — an owner can no longer raise their own
limits. An empty list means nobody can.

```bash
kubectl set env -n vrsky-platform deploy/vrsky-management-api PLATFORM_OPERATORS=you@example.com
```

`deploy-core-azure.sh` sets images only; the variable has to be set on the
deployment once (and is in `infrastructure/kubernetes/management-api/deployment.yaml`).

### Extending a trial

Platform → Workspaces → plan **Trial**, pick a new end date, Save. A
suspended workspace comes back the same way.

### Overriding one workspace's limits

Settings → Usage & quotas shows the quota form to operators only. Changes are
audited (`quota.update`). Setting the plan again resets them to the tier.

## Where to look

- `kubectl logs deploy/vrsky-management-api | grep "billing"` — suspensions
  ("workspace suspended", with the pipeline count) and failures.
- Audit log: `plan.request`, `platform.plan.set`.
- Connection events: `stopped` with `reason: trial ended`; `started` with
  `reason: plan activated`.
- Alerts `PlanRequested` and `TrialExpired` go to platform targets only.
