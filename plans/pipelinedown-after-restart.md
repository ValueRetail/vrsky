# PipelineDown after a parked-cluster restart

Seen 2026-10-19: the cluster was stopped at about 10:00 UTC and started at
12:26 UTC; `PipelineDown` fired at 12:29 UTC for the Business Central
connection and sent a Teams card. Nothing was wrong with the pipeline.

## Open questions

1. **What "scraped for the whole hour" means.** Recommended: the counter
   has **at least 100 samples in the last hour** (`count_over_time(...[1h])
   >= 100`; the scrape interval is 30 s in `podmonitors.yaml`, so a full
   hour is 120). A gap of ten minutes or more, which only a stop produces
   (a rolling restart overlaps old and new pod by seconds), makes the alert
   wait until the counter has a full hour of history. Alternative: require
   a sample 50–60 minutes ago (`count_over_time(...[10m] offset 50m) > 0`);
   scrape-interval independent but it still fires on a stop of 50–59
   minutes, where Prometheus has samples at both ends of the gap.
2. **Only the last-hour window, not the comparison window.** Recommended:
   **yes.** Requiring the `6h offset 1h` window to be fully scraped too
   would silence the alert for seven hours after any restart, including a
   pipeline that really dies right after one. With the recommended rule a
   source that stays quiet for the hour *after* the first hour post-restart
   still fires; that is the same blind spot a quiet source has while
   running continuously, and this metric cannot tell the two apart.
3. **The unit tests at a 30 s interval.** The promtool file evaluates
   every rule's test at `interval: 1m`, which gives 60 samples an hour and
   can never satisfy "≥ 100". Recommended: the `PipelineDown` test group
   moves to `interval: 30s` with its series doubled; the other groups stay
   at 1 m. Alternative: lower the threshold to fit 1-minute samples, which
   would make a real 30 s-scraped series pass the check after 25 minutes.

## Goal

A start that follows a stop of more than an hour does not page. A pipeline
that goes quiet while the cluster runs still pages within the same hour
plus five minutes it does today.

## What exists (checked)

- `infrastructure/prometheus-rules.yml` `PipelineDown`: per (tenant,
  connection), `increase[1h] == 0 and increase[6h] offset 1h > 0`, `for:
  5m`, severity critical (#309).
- During a stop Prometheus has no samples at all, so after a start the
  last-hour increase is 0 while the offset window still holds the pre-stop
  publishes: measured today, 0 in the last hour and 17 in the offset
  window, with 6 + 29 samples (two pod generations) in the last hour
  against 120 in a full hour before the stop.
- Tests: `infrastructure/prometheus-rules-test.yml`, group 1, run by CI
  through `docker run … prom/prometheus:v3.5.0 … test rules`.
- Go guard `TestNodeConfigRulesAreDeployed` reads the rules file for
  deployment coverage only; no expression is asserted there.

## Approach

### 1. The rule

```yaml
expr: |
  sum by (tenant_id, connection_id) (increase(…[1h])) == 0
  and
  sum by (tenant_id, connection_id) (increase(…[6h] offset 1h)) > 0
  and
  sum by (tenant_id, connection_id) (count_over_time(…[1h])) >= 100
```

With a comment: 120 samples an hour at the 30 s scrape interval; fewer
than 100 means the cluster was parked within the hour, and the alert waits
for a full hour of history. The `sum` folds the old and new pod of a
rolling restart together, so a deploy never trips it.

### 2. Tests (`prometheus-rules-test.yml`, group 1 at `interval: 30s`)

| Case | Series | Expect |
|---|---|---|
| a. still polling | as today, doubled | no alert at 200 m and 359 m |
| b. dead pipeline | silence from minute 360 | fires at 440 m, same labels and annotations |
| c. parked restart | 3 h of polling, a 3 h gap (`_`), then the counter restarts at 0 and stays flat | no alert 10 m and 50 m after the restart |
| d. restart, then real death | case c, then still flat at 70 m after the restart | fires (the comparison window still holds the pre-stop publishes and the hour of history is complete): documents the blind spot in question 2 rather than hiding it |

Mutation: remove the new `and` clause; case c must fail (alert at
10 m after the restart). Run once and the output goes in the PR.

### 3. Docs

- `docs/OBSERVABILITY.md` `PipelineDown` row: "… and the counter was
  scraped for the whole hour (a parked cluster does not page on start)".
- `docs/operator/cost-parking.md` *Start*: no `PipelineDown` card after a
  start; a source that is really dead pages an hour later.
- `plans/deploy-while-parked.md`: the "separate change" line points here.

## Files

| File | Change |
|---|---|
| `infrastructure/prometheus-rules.yml` | the third clause and its comment |
| `infrastructure/prometheus-rules-test.yml` | group 1 at 30 s, cases c and d |
| `docs/OBSERVABILITY.md`, `docs/operator/cost-parking.md`, `plans/deploy-while-parked.md` | as above |

## Verify

`docker run … promtool check rules` and `test rules` locally (the CI
command), the mutation above, `go test ./pkg/managementapi/ -run Rules`.
In prod, on your word: `PROFILE=azure install-monitoring.sh` (it also does
the Grafana `Recreate` switch pending from today), then the alert resolves
on the next evaluation. Rules are auto-deployed by the `monitoring` stage
on merge when the cluster runs.

## Risks

- **A scrape interval change** (podmonitors.yaml) silently changes what
  "≥ 100" means: a slower interval would suppress the alert for good. The
  comment on the rule names the file; a Go guard is not worth it for one
  number.
- **Prometheus itself restarting** for more than ten minutes delays the
  alert by up to an hour. Acceptable.

## Non-goals

Telling a quiet source from a dead one; a dead man's switch for
Prometheus; any change to the other rules.
