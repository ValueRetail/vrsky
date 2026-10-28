# #432 Alert when the backup CronJob is left suspended

Backlog item 57. `MgmtBackupMissing` is silent while `vrsky-mgmt-backup` is
suspended, on purpose. So a CronJob suspended by hand, or never turned back
on after a restore, stops backups and pages nobody.

## Open questions

None.

## Acceptance criteria

1. A new alert `MgmtBackupSuspended` (warning) in the `backup` rule group of
   `infrastructure/prometheus-rules.yml`, next to `MgmtBackupMissing`.
2. It fires when the CronJob has been suspended for over 7 days while
   `vrsky-backup-config` exists and has existed for over 7 days.
3. It stays quiet when:
   - the Secret doesn't exist (the CronJob ships suspended until the human
     setup),
   - the CronJob has been suspended for under 7 days, a few hours during a
     restore included,
   - the CronJob isn't suspended,
   - the Secret is under 7 days old,
   - the CronJob is gone.
4. Nights the cluster is parked count as suspended time.
5. promtool unit tests for each case in `infrastructure/prometheus-rules-test.yml`;
   `promtool check rules` and `promtool test rules` pass with the CI command.
   Breaking the expression makes a test fail.
6. `docs/operator/backup.md` and `docs/DR.md` describe the alert.
7. Backlog item 57 struck with (#432).

## Touches

- `infrastructure/prometheus-rules.yml`, `infrastructure/prometheus-rules-test.yml`
- `docs/operator/backup.md`, `docs/DR.md`
- `plans/backlog.md`
- `.github/workflows/build-push.yml`: the three new test groups go into
  legs of the `alert-rules` matrix (since #468 every group must be in
  exactly one, `TestRuleTestGroupsRunOnce`). `backup-suspended-parked` and
  `backup-suspended-pod-replaced` in 1/4, `backup-suspended-late-secret`
  in 4/4.

Not touched: `infrastructure/kubernetes/backup/**`. A change there, even to
the README, makes the next deploy run `deploy-backup-azure.sh`
(`affected.sh`), which re-derives `suspend` and would undo a hand suspend.

## Approach

Metrics, all from kube-state-metrics:

- `kube_cronjob_spec_suspend{namespace, cronjob}`, already used by
  `MgmtBackupMissing`.
- `kube_secret_created{namespace, secret}` from the secrets collector. The
  repo doesn't override the chart's `collectors` list
  (`infrastructure/kubernetes/monitoring/prometheus-values*.yaml`), and the
  kube-state-metrics chart default includes `secrets`; kube-prometheus-stack
  adds no metric relabelling that drops it. The series exists only while
  the Secret does, and its value is the creation time, so one metric gives
  both "exists" and "older than 7 days". `kube_secret_info` would only say
  it exists.

Not `for: 7d`. Prometheus runs in the cluster, which is parked every night,
and a pending alert's `for` state is lost after an outage longer than the
default one-hour tolerance. It would never reach 7 days. Instead:

```
(
    min by (namespace, cronjob) (min_over_time(kube_cronjob_spec_suspend{…}[7d])) == 1
  and on (namespace, cronjob)
    min by (namespace, cronjob) (kube_cronjob_spec_suspend{…}) == 1
)
and on (namespace)
  (time() - max by (namespace) (kube_secret_created{…, secret="vrsky-backup-config"}) > 604800)
for: 30m
```

- `min_over_time(...[7d]) == 1`: every sample in the last 7 days is
  suspended. One active sample keeps it quiet for 7 days, so a short
  suspend never pages. Parked time has no samples and counts as suspended,
  which is right: nothing can unsuspend it while parked.
- The instant `== 1` makes it clear as soon as the CronJob is unsuspended
  or deleted, instead of riding the 7-day window.
- `min by`, not `max by` (review of #473). The series carry
  kube-state-metrics' `pod` and `instance`, so a replaced pod starts a new
  series holding only recent samples. With `max by` a new series that is
  all 1 after a few hours of suspend decided alone and paged. With `min by`
  the old pod's active samples still count. The instant check uses `min`
  too for consistency. In practice it makes no difference there: a vanished
  target's series goes stale, and two live replicas report the same value.
- `for: 30m` as on the neighbouring rule. No `keep_firing_for`; the
  neighbouring rules don't use it.

## Risks

- If the secrets collector is ever turned off, `kube_secret_created`
  disappears and the alert goes quiet (as today). It can't be checked
  against production from here.
- Recreating the Secret (rename during a restore, delete and recreate on a
  rotation) restarts its 7 days.
- If Prometheus loses its history, a CronJob suspended only recently could
  page at once if the Secret is old. Rare, and it would still be a real
  suspend.
- A Secret that exists but lacks a key keeps the CronJob suspended at every
  deploy; this alert fires for that too after 7 days. Intended: no backups
  are being taken either way. The description points at the deploy log.
- Adds time to `promtool test rules` (its own CI job since #446): every
  rule is evaluated each minute of the new 5–9 day groups. Three long
  groups instead of one per case; locally the run went from 92 s to about
  135 s (a first draft with seven groups took 195 s).
- A deleted CronJob is silent here and in `MgmtBackupMissing`; backlog
  item 89.

## Verification

- `docker run … prom/prometheus:v3.5.0 check rules /cfg/prometheus-rules.yml`
- `docker run … prom/prometheus:v3.5.0 test rules /cfg/prometheus-rules-test.yml`
- Mutations against the new test groups: rule removed, Secret clause
  removed, any Secret in the namespace, 1-day window, instant check
  removed, Secret age 0, `max_over_time` for `min_over_time`, `max by` for
  `min by` over the window. Each must fail a test.
