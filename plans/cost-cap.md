# Cost cap: stop at 16:00 every day, and a budget that is enforced

Written 2026-10-20 from Azure Cost Management (month to date, 1–20 October,
NOK, the two VRSky resource groups) and the retail price list for Norway
East. All figures below are NOK.

## The numbers first

October so far, 20 days, VRSky's two resource groups: **5 414**. Projected
for the month at the same rate: about **8 100**. Where it went:

| Meter | 20 days | per 30-day month | Stops with the cluster? |
|---|---|---|---|
| E4ds v6 nodes, 2 × 4.46/h | 4 004 | ~6 000 at 24/7 | yes |
| Standard tier ("Uptime SLA"), ~0.9/h | 429 | ~640 at 24/7 | yes |
| E10 ZRS disks: NATS 3 × 100 GB + MinIO 100 GB | 373 | 560 | **no** |
| P10 OS disks, 2 × 128 GB Premium | 274 | 410 | **no** |
| Standard load balancer | 110 | 165 | **no** |
| Container registry (Basic) | 61 | 91 | **no** |
| ACR build minutes (every merge under `src/pkg` rebuilds 35 images) | 69 | ~100 | no |
| Public IPs (2) | 44 | 66 | **no** |
| Postgres 50 GB, Prometheus 20 GB, Grafana, Alertmanager, Azure Files 50 GB | ~107 | ~160 | **no** |

**A parked cluster costs about 1 450 a month** before a single node-hour.
The 500 target is a third of that. So the question is not only when to
stop the cluster but what shape it has.

What the shapes cost, with a stop at 16:00 every day and a start by hand
around 09:00 on working days (~154 node-hours a month):

| Shape | Nodes | Fixed | Total / month |
|---|---|---|---|
| **A. As is** + the 16:00 stop | 2 × E4ds v6: 1 373 + tier 140 | 1 450 + builds 100 | **~3 050** |
| **B. Trimmed**: Free tier, one node, ephemeral OS disks, NATS and MinIO on small LRS disks | 1 × E4ds v6: 687 | ~530 + builds 100 | **~1 300** |
| **C. 500**: not reachable on AKS. B's fixed costs alone exceed it at zero hours | — | — | — |

What 500 a month does buy: one Standard_B4as_v2 VM (1.81/h, 4 vCPU,
16 GB) running the repository's docker-compose or k3s path, stopped at
16:00: ~280 in hours plus ~80 for its disk and address, **~360**. Around
the clock it is ~1 300. That is a migration (no managed control plane, no
node pool, the 72-hour JetStream retention on one disk) and a separate
decision; it is not in this plan.

## Open questions

1. **Which budget is the real one.** Recommended: **shape B at 1 300**
   with the budget set to **1 500** (a working month with some slack),
   and the question of 500 reopened as "move off AKS", a plan of its own.
   Alternative: keep shape A and set the budget at 3 200; nothing changes
   but the stop. Setting a 500 budget on either shape means the
   enforcement below parks the cluster for good by about the 5th of each
   month, which is a decision, not a safeguard.
2. **The 16:00 stop, every day of the week, no automatic start.**
   Recommended: **yes**, 16:00 Europe/Oslo, Saturdays and Sundays too; a
   start stays `az aks start` by hand, and the Production check catches
   production up within the half hour. If someone is still working at
   16:00 they start it again. Alternative: an automatic 08:00 start on
   weekdays, which would spend ~8 hours a day whether anyone works or not.
3. **Enforcement, not only an e-mail.** Recommended: **the Production
   check reads the month-to-date cost** of the two resource groups and,
   at or above the budget, stops the cluster, keeps stopping it on every
   later run that month, and opens the issue (via the CI failure alert).
   Needs the deploy identity to hold *Cost Management Reader* on the two
   resource groups, one assignment by a subscription Owner, same as the
   roles it has. Alternative: an Azure budget with e-mail alerts at 50, 80
   and 100 percent and no action; Contributor can create it, nobody has
   to grant anything, and it stops nothing.
4. **Order of the trims in B.** Recommended: **tier and node count first**
   (two commands, reversible in minutes, ~1 000 a month between them);
   **ephemeral OS disks** next (a new node pool and a drain, ~400); the
   **NATS and MinIO disks** last, because Azure disks do not shrink: new
   claims and a restore through the DR drill, ~500. The last two are their
   own plan each; this one does tier, node count, the stop, the budget and
   the enforcement.

## What the build found (2026-10-20)

- The Park workflow does not repeat the budget check: the Production check
  runs every half hour and stops a cluster that was started over budget;
  one place for the rule.
- The Budget step is skipped while `MONTHLY_BUDGET_NOK` is unset, so the
  role grant can land first and the variable afterwards; a set variable
  with a failing query is a red run and no stop.
- No PodDisruptionBudget in the cluster allows zero disruptions, and no
  VRSky workload has a required anti-affinity, so one node can carry it;
  the cilium-operator's second replica stays Pending on a single node by
  design (AKS add-on).
- An Azure budget `vrsky-monthly` (1 500, 80/100 percent e-mails) was
  created with `az rest`; `az consumption budget create` has no
  notifications.

- **GitHub's scheduler runs this repository's crons hours late** (the
  first evening: 14:00 and 15:00 UTC fired at 18:10 and 18:53; the
  Production check runs every four to five hours, not every thirty
  minutes). An "is it 16:00" check therefore never matched and the cluster
  ran all night. Park now stops on any run landing between 16:00 and 07:00
  Oslo, with a cron every half hour across the window.

## Goal

The cluster is never left running overnight by mistake, the monthly cost
has a number that is enforced rather than hoped for, and the number is
one the shape can meet.

## What exists (checked)

- `cost-parking.md`: stop and start by hand; what survives a stop.
- The deploy identity ("VRSky deploy") holds Contributor on the cluster,
  which covers `az aks stop`; it holds nothing at subscription scope and
  cannot read costs today.
- `drift-check.yml` (Production check): every 30 minutes, gate on power
  state, in-cluster steps, `production` concurrency group.
- Cost Management query API works from `az rest` at subscription scope
  with Ludvik's account (`Microsoft.CostManagement/query`, ActualCost,
  MonthToDate); the legacy `az consumption` API returns no costs on this
  subscription.
- The cluster is on the **Standard** tier (`sku.tier`), two
  Standard_E4ds_v6 in one system pool, Premium 128 GB OS disks. CPU use
  2 %, memory ~14 GB of 64 (`plans/cpu-request-headroom.md`): one node
  carries it. NATS runs three replicas with a 512 MiB stream limit on three
  100 GB disks.

## Approach

### 1. Trims (shape B, part one), on your word

```bash
az aks update -g vrsky-prod -n vrsky-prod --tier free
az aks nodepool scale -g vrsky-prod --cluster-name vrsky-prod -n fpool --node-count 1
```

Before the scale: the three NATS replicas and the two-replica core
services land on one node; pod anti-affinity is `preferred`, not
`required` (checked in the manifests before running). The Free tier has
no uptime SLA and a lower API-server rate limit; neither matters at 100
messages a day. `TestCPURequestsStayWithinBudget` stays as is (requests
are 1 570 m of one node's 3 860 m).

### 2. The 16:00 stop: `.github/workflows/park.yml`

`schedule` at `0 14 * * *` and `0 15 * * *` UTC (16:00 Oslo in winter and
summer); the job checks `TZ=Europe/Oslo date +%H` is `16` and exits
otherwise, so exactly one of the two fires. `production` environment and
concurrency group (queues behind a deploy, never interrupts one), OIDC
login, `az aks stop` when the state is Running, a summary line, nothing
else. `workflow_dispatch` too, for a stop from the phone.

### 3. The budget and its enforcement

- An Azure budget on each VRSky resource group (`az consumption budget
  create`, monthly, amount from question 1 split by group) with e-mail
  notifications at 80 and 100 percent to you. Created once, by hand.
- A step *Budget* in the Production check, before the heartbeat: the
  month-to-date cost of the two groups through the Cost Management query
  (`az rest`, the identity needs Cost Management Reader), compared with
  the budget held in a repository variable `MONTHLY_BUDGET_NOK`. At or
  above it: `az aks stop`, an error annotation, exit 1 (the issue opens
  and names the number). Below it: the number in the job summary, so every
  run shows the month's spend. The stop-at-16:00 workflow does the same
  check so a start after the budget is hit lasts at most until the next
  Production check, half an hour.

The comparison lives in `infrastructure/azure/budget.sh` (month-to-date
and budget in, verdict out), table-tested like the other scripts.

### 4. Docs

`cost-parking.md`: the daily stop, the budget, what the Production check
does at the limit, the role grant. `deploy.md`: one line.

## Files

| File | Change |
|---|---|
| `.github/workflows/park.yml` (new) | the 16:00 stop |
| `.github/workflows/drift-check.yml` | *Budget* step |
| `.github/workflows/ci-failure-alert.yml` | "Park" in the list |
| `infrastructure/azure/budget.sh` (new) | the verdict |
| `src/pkg/managementapi/nodeconfig_test.go` | `TestBudgetVerdict`, `TestParkWorkflow`, `TestDriftCheckWorkflow` extended |
| `docs/operator/cost-parking.md`, `docs/operator/deploy.md` | as above |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestBudgetVerdict` | below → ok with the numbers; at → stop; above → stop; no cost data → stop is NOT taken, the run fails loudly (a broken query must not park production) | treat missing data as zero |
| `TestParkWorkflow` | both crons, the Oslo-hour check, production environment and group, `az aks stop` only when Running, no deploy script, no kubectl | drop the hour check |
| `TestDriftCheckWorkflow` | the budget step precedes the heartbeat and is gated | remove it |
| `TestCIFailureAlertWatchesEveryMainWorkflow` | the park workflow is watched | — |

## Verify

1. Tier and node count: `az aks show` says Free and one node; all pods
   Running; smoke ok; the next day's cost line in the Production check
   summary is about half.
2. The park workflow by dispatch at any hour: "not 16:00 in Oslo, nothing
   to do"; then wait for 16:00 once: the cluster is Stopped at 16:01.
3. Budget: set `MONTHLY_BUDGET_NOK` to a number below month-to-date for
   one run by dispatch: the cluster stops and the issue opens; set it
   back, start the cluster, the next run closes the issue.

## Risks

- **A stop at 16:00 while someone is mid-task** loses nothing (the
  connectors restore their pipelines; webhooks retry from Bifrost's
  outbox; the remote agent holds 72 h), it just needs `az aks start`.
- **One node is one node**: a node failure takes everything down until
  AKS replaces it, minutes. At this stage acceptable; the second node is
  one command away.
- **The enforcement stops production in the middle of a working day** by
  design. The 80 percent e-mail is the warning.
- **Cost data lags by several hours**; the check can overshoot the budget
  by at most a day's spend.

## Non-goals

Ephemeral OS disks and the disk shrink (follow-ups, each its own plan);
leaving AKS for a VM; an automatic morning start.
