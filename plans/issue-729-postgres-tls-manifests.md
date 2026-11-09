# #729 Postgres TLS: refuse plain connections in the manifests, alert on the certificate

Backlog item 156, the loose ends of #453 ([plan](issue-453-postgres-tls.md)).

1. Postgres refuses plain connections only because of a hand edit of
   `pg_hba.conf` on the volume (step 10 of #453, done on 2026-11-03). A
   snapshot restore, a new volume or a fresh cluster starts with the image's
   `host all all all scram-sha-256` again and takes plain connections.
2. Nothing alerts on the server certificate. cert-manager's metrics are not
   scraped, so a renewal that fails for 76 days goes unseen until every
   `verify-full` client fails at once.

## Open questions

None.

## Acceptance criteria

1. `statefulset.yaml` starts Postgres with `-c hba_file=/etc/postgresql/hba/pg_hba.conf`,
   read from a ConfigMap `postgres-hba` mounted as a directory (no
   `subPath`, so an edit of the ConfigMap reaches the pod and a
   `pg_reload_conf()` picks it up).
2. The ConfigMap is the first document of `statefulset.yaml`, so every
   `kubectl apply -f statefulset.yaml` (deploy-vrsky-platform.sh, which
   deploy-azure.sh wraps, and `postgres-tls.sh server`) creates it before the
   StatefulSet that needs it.
3. Its `pg_hba.conf` has the same effect as production's file after the hand
   edit: the image's `local` and loopback `trust` lines (entrypoint init,
   probes, `kubectl exec psql`, `kubectl port-forward`) and
   `hostssl all all all scram-sha-256` for everything else. No `host` or
   `hostnossl` rule for a non-loopback address, no `trust` beyond loopback.
4. Tested in Docker on `postgres:18-alpine` with the manifest's arguments, the
   ConfigMap's file and a throwaway certificate: a fresh volume initialises;
   the probe command passes; `psql` over the socket works; a TLS client
   (`verify-full`, and `require`) connects; a plain TCP client is refused
   (`no pg_hba.conf entry … no encryption`); a wrong password over TLS is
   refused; on an existing volume whose own `pg_hba.conf` says `host`, plain
   is still refused (the ConfigMap wins); without `hba_file` (the rollback)
   Postgres falls back to the volume's file.
5. The k3d dev strings (`secret.example.yaml` in `postgresql/` and
   `management-api/`) move from `sslmode=disable` to `sslmode=require`, so the
   local path keeps working (it has TLS but does not copy the CA).
6. `postgres-tls.sh`: `urls require` (TLS, no verification) is the client
   rollback that works against `hostssl`; `urls disable` refuses while the
   server turns plain connections away; `server` and `check` print
   `hba_file` and whether plain remote connections are refused.
7. A PodMonitor `cert-manager` in `podmonitors.yaml` scrapes the cert-manager
   controller (`app.kubernetes.io/name: cert-manager`,
   `app.kubernetes.io/component: controller`, port `http-metrics` 9402, as the
   v1.13.3 chart that `install-cert-manager.sh` pins renders them), with
   `honorLabels: true` so the metric keeps `namespace="vrsky-database"`
   instead of `exported_namespace`.
8. Alert rules in the `vrsky-platform` group on
   `certmanager_certificate_expiration_timestamp_seconds{namespace="vrsky-database", name="postgresql-tls"}`:
   `PostgresCertExpiring` warning under 14 days, critical under 3 (one at a
   time), and `PostgresCertMetricsAbsent` (warning) when the series has been
   missing for an hour, so a scrape that breaks again cannot silence the
   first one.
9. promtool unit tests for firing, the severity switch, quiet cases, a
   cert-manager pod replacement and the absent case; each group listed in
   `prometheus-rules-test-legs.yml`. `check rules` and `test rules` pass with
   the CI docker command.
10. Guard tests (`src/pkg/managementapi`) pin 1 to 3 and 7 to 8, and fail on
    main as it is.
11. README, `docs/operator/monitoring.md`, `docs/DR.md` (if it mentions the
    hba rule) and the #453 plan say what changed; backlog item 156 struck.

## Touches

- `infrastructure/kubernetes/postgresql/statefulset.yaml`, `README.md`,
  `secret.example.yaml`; `infrastructure/kubernetes/management-api/secret.example.yaml`
- `infrastructure/azure/postgres-tls.sh`
- `infrastructure/kubernetes/monitoring/podmonitors.yaml`, `README.md` if it lists the monitors
- `infrastructure/prometheus-rules.yml`, `prometheus-rules-test.yml`, `prometheus-rules-test-legs.yml`
- `src/pkg/managementapi/postgres_tls_guard_test.go`, `postgres_tls_script_test.go`, `monitoring_config_test.go`
- `docs/operator/monitoring.md`, `plans/backlog.md`, `plans/issue-453-postgres-tls.md`

## Risks

- **Locking a client out.** Every client was on TLS on 2026-11-03 (39 of 39)
  and production already refuses plain connections through the hand edit, so
  the manifest changes nothing for clients. The pre-check is
  `postgres-tls.sh check` showing no `ssl=false` line.
- **Probes and init.** `pg_isready` and the entrypoint's temporary server use
  the Unix socket (`local … trust`); `pg_isready` reports the server up even
  when authentication would fail. Tested in Docker, fresh and existing volume.
- **A broken hba file stops Postgres at start.** The guard test parses the
  file; the Docker run starts with it. On a reload Postgres keeps the old
  rules if the new file is broken.
- **Applying the StatefulSet restarts Postgres.** A minute or more without a
  database while the disk moves, same as `postgres-tls.sh server` in #453.
  No data is touched: the data volume and `$PGDATA/pg_hba.conf` stay as they
  are.
- **The PodMonitor and rules deploy on merge** (`install-monitoring.sh`
  through `affected.sh`). If the labels or port differ in production the
  absent alert fires after an hour, which is the point; it is a `vrsky-*`
  warning, so it would also fail later deploys' smoke checks until fixed.
- **The alert sees cert-manager's Secret, not the copy Postgres serves.** A
  renewed Secret with a Postgres that has not restarted for 90 days is
  `postgres-tls.sh check`'s "OLDER certificate" line, not this alert. Noted
  as a follow-up.

## Rollout (Ludvik, after merge)

1. `infrastructure/azure/postgres-tls.sh check`: no `ssl=false` line.
2. `DRY_RUN=1 infrastructure/azure/postgres-tls.sh server`: the diff shows the
   new ConfigMap `postgres-hba` and, in the StatefulSet, the `hba_file`
   argument and the `hba` volume, nothing else.
3. `CONFIRM=1 infrastructure/azure/postgres-tls.sh server`: restarts
   `postgresql-0`; it prints `ssl`, `hba_file` and the refused-plain check.
4. `postgres-tls.sh check` again; a plain connection from a throwaway pod is
   refused (README has the command, no password needed).
5. Monitoring: after the merge's deploy, Prometheus lists the
   `podMonitor/vrsky-monitoring/cert-manager` target as up, and
   `certmanager_certificate_expiration_timestamp_seconds{name="postgresql-tls"}`
   returns one series with `namespace="vrsky-database"`.

## Rollback

- A client must use plain TCP again (emergency): edit the ConfigMap
  (`hostssl` to `host` on the last line), wait for the file in the pod to
  change (a minute or two), `SELECT pg_reload_conf()`. No restart.
- A client has a CA or verification problem: `CONFIRM=1 postgres-tls.sh urls require`
  and `restart` (TLS without verification). `urls disable` refuses while the
  server is `hostssl`.
- Postgres does not start: `kubectl -n vrsky-database rollout undo statefulset/postgresql`,
  then `kubectl -n vrsky-database delete pod postgresql-0`. Without
  `hba_file` it reads `$PGDATA/pg_hba.conf`, which in production has said
  `hostssl` since 2026-11-03, so the effect is the same.
- The alerts: revert the PR; the deploy runs `install-monitoring.sh` again.

## Verification

- Docker rehearsal (acceptance 4), output in the PR.
- `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -race -count=1 ./pkg/managementapi/...` with
  `VRSKY_TEST_POSTGRES_URL`, `make lint-tenant`.
- promtool `check rules` and `test rules` with the CI image.
- Mutation: the new guard tests fail with `hba_file` removed, with `host`
  instead of `hostssl`, with `subPath`, without the PodMonitor or
  `honorLabels`; the rule tests fail with a threshold or the name matcher
  changed.
- Not checked: nothing ran against cert-manager or the cluster. No
  kubeconform here, so the manifests are checked by YAML parse and the guard
  tests only.
