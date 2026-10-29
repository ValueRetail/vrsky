# #526 Alert when the backup CronJob is gone

Backlog item 89. If `vrsky-mgmt-backup` is deleted (by hand, or a re-apply
that fails halfway), its kube-state-metrics series end. `MgmtBackupMissing`
and `MgmtBackupSuspended` both join on those series, so both match nothing:
no backups run and nobody is paged.

## Open questions

None.

## Acceptance criteria

1. A new alert `MgmtBackupCronJobAbsent` (warning) in the `backup` rule group
   of `infrastructure/prometheus-rules.yml`, after `MgmtBackupSuspended`.
   Labels `severity: warning`, `namespace: vrsky-database`,
   `cronjob: vrsky-mgmt-backup`.
2. It fires when the `vrsky-backup-config` Secret exists in `vrsky-database`
   and no `kube_cronjob_created` series for `vrsky-mgmt-backup` does, for
   30 minutes.
3. It stays quiet when:
   - the Secret doesn't exist (a fresh cluster before the human setup, or a
     restore that renamed it out of the way), CronJob or not;
   - the CronJob exists, suspended or not;
   - the cluster is parked (no samples of anything) and on the morning
     start, when the Secret's series can show up a scrape or two before the
     CronJob's;
   - kube-state-metrics is replaced and the new pod reports the Secret
     before the CronJob, or the old and new pod overlap.
4. It clears once the CronJob is back.
5. promtool unit tests for each case in
   `infrastructure/prometheus-rules-test.yml`, each new group listed in
   `infrastructure/prometheus-rules-test-legs.yml`. `check rules` and
   `test rules` pass with the CI docker command; a mutation run shows the
   tests fail without the rule and with the main parts of it broken.
6. `docs/operator/backup.md` and `docs/DR.md` describe the alert.
7. Backlog item 89 struck with (#526).

## Touches

- `infrastructure/prometheus-rules.yml`, `infrastructure/prometheus-rules-test.yml`,
  `infrastructure/prometheus-rules-test-legs.yml`
- `docs/operator/backup.md`, `docs/DR.md`
- `plans/backlog.md`

Not touched: `.github/workflows/` (since #507 the legs file lists the
groups), `infrastructure/kubernetes/**`. A change to the rules file makes
the next deploy run `install-monitoring.sh` (`affected.sh`: `monitoring`),
which is how every rule change ships.

## Approach

```
    max by (namespace) (kube_secret_created{namespace="vrsky-database", secret="vrsky-backup-config"})
  unless on (namespace)
    max by (namespace) (kube_cronjob_created{namespace="vrsky-database", cronjob="vrsky-mgmt-backup"})
for: 30m
```

- `unless` rather than a bare `absent()`. While Prometheus and
  kube-state-metrics have nothing yet (a morning start, before the first
  scrape), the Secret's series is absent too, so the left side is empty and
  the rule is quiet. A bare `absent(kube_cronjob_created{…})` would be
  true on every start; `absent(…) and on () (secret)` is the same thing
  written longer.
- `max by (namespace)` on both sides drops `pod`, `instance` and `job`.
  kube-state-metrics' series carry its pod and instance (review of #473),
  so a replaced pod starts new series. Matching on `pod` or `instance`
  would pair the new pod's Secret with the new pod's CronJob only; with
  `for: 30m` that only matters if the new pod lags by over 30 minutes.
  The real gain is that the alert's labels stay the same across a pod
  change, so a firing alert doesn't resolve and re-fire as a "new" one.
- Review findings left as they are: an evicted (not rolled)
  kube-state-metrics pod leaves a minute with no Secret series, so a
  firing alert resolves and fires again 30 minutes later.
  `keep_firing_for` would smooth that, but no neighbouring rule uses it and
  the page still comes back; the docs say so. A CronJob deleted just
  before a park pages the next morning about 07:30; not given its own test
  group, since 13a covers the firing path and 13b the morning start.
- `kube_cronjob_created`, the series `MgmtBackupMissing` already reads, so
  the metric is known to be scraped. It exists for as long as the object
  does, suspended or not.
- `for: 30m`, as on the neighbouring rules. Prometheus is parked every
  night; a pending state doesn't survive the night (the outage is far over
  the one-hour tolerance), so every morning starts from inactive. The only
  start-up gap is kube-state-metrics publishing the Secret a scrape or two
  before the CronJob (its informers sync separately), seconds to a minute.
  30 minutes covers that and a slow pod replacement with room to spare,
  and a deleted CronJob still pages on the same day.
- `cronjob` comes from the rule's `labels:` (the expression has only
  `namespace` left), so the alert carries the same labels as its two
  neighbours.

## Risks

- If the secrets collector is turned off, `kube_secret_created` goes away
  and this rule goes quiet, like `MgmtBackupSuspended`.
- If the cronjobs collector is turned off but the secrets one stays, this
  fires for good. That is wanted: `MgmtBackupMissing` and
  `MgmtBackupSuspended` would be blind then too.
- A CronJob deleted at 15:50 and a park at 16:00: the alert pages the next
  working morning, about 07:30.
- Renaming the Secret during a restore (docs/DR.md) keeps this quiet even
  if the CronJob is deleted too. That restore is supervised.
- Adds a little time to `promtool test rules`; the new groups are short.

## Verification

- `docker run … prom/prometheus:v3.5.0 check rules /cfg/prometheus-rules.yml`
- `docker run … prom/prometheus:v3.5.0 test rules /cfg/prometheus-rules-test.yml`
- `go test -count=1 ./pkg/managementapi/` from `src/` (legs guard).
- Mutations: rule removed, `unless` side removed (fires on any Secret),
  Secret clause removed (`absent()` alone), matching on `pod` too, any
  Secret in the namespace, `for: 0m` and `for: 5m`. Each must fail a test.
