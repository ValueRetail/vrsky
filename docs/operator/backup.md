# Backup & restore

The durable state you must protect is the **management database** (connections,
secrets, tenants, audit log, OAuth grants) and the **`ENCRYPTION_KEY`**. NATS
JetStream is in-flight data and is bounded/replayable, not a backup target.

## What to back up

| Item | Why |
|------|-----|
| Management Postgres | All connection/tenant/secret/audit state |
| `ENCRYPTION_KEY` | Without it, encrypted secrets in the DB are unrecoverable |
| Per-tenant source/target DBs | If VRSky manages them; otherwise the customer's concern |

## Tooling

`vrsky-cli` provides `backup` / `restore` / `list` and is packaged as a container
and a Kubernetes CronJob (see `infrastructure/kubernetes/`). Backups are
encrypted (`pkg/crypto` `EncryptBytes`/`DecryptBytes`).

```bash
vrsky-cli backup  --out s3://backups/vrsky/$(date +%F).enc
vrsky-cli list    --bucket s3://backups/vrsky/
vrsky-cli restore --in  s3://backups/vrsky/2026-06-23.enc
```

## Restore drill

A restore is only real if it's been tested. CI runs a **restore drill**
(`.github/workflows/dr-drill.yml`) that backs up, tears down, restores into a
fresh database, and asserts the data matches. Run it before you rely on a new
backup target.

Full procedures, RPO/RTO targets, and the K8s CronJob schedule are in the
[disaster-recovery guide](../DR.md).
