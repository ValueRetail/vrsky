# Encryption-at-rest checklist

VRSky encrypts tenant secrets (and backups) with AES-256-GCM. The master key is
the `ENCRYPTION_KEY` environment variable. This checklist keeps that safe.

## Before go-live

- [ ] **Generate a strong key** — 32 random bytes as 64 hex chars:
      `openssl rand -hex 32`.
- [ ] **Store it in a real secret manager** (Vault, AWS/GCP/Azure secret
      manager, or a sealed K8s secret) — never in git, never in a plain `.env`
      committed anywhere.
- [ ] **Set the same value** on the management-API and every worker that
      resolves secrets (HTTP/DB/cloud-storage/SFTP/etc.). A mismatch means
      workers can't decrypt and pipelines fail at runtime.
- [ ] **Confirm ciphertext, not plaintext, is stored.** Create a secret, then
      check the DB: the value column is `aes256:<base64…>`, never the raw value.
- [ ] **Use TLS to the databases** where you can (`sslmode=require` or better).
      Production on AKS is prepared for it (#453): the repo has the cert-manager
      certificate for the in-cluster Postgres, and the services are ready to
      connect with `sslmode=verify-full` against its private CA
      (`infrastructure/azure/rotate-secrets.sh` builds those strings). TLS is
      enabled after the operator steps in the plan have run;
      `infrastructure/azure/postgres-tls.sh check` shows whether it is on.
      Production has run on it since 2026-11-02, and Postgres refuses plain
      connections from other pods: by hand since 2026-11-03, and through the
      `pg_hba.conf` in its manifest since #729 (see
      [the Postgres README](https://github.com/ValueRetail/vrsky/blob/main/infrastructure/kubernetes/postgresql/README.md#tls-453)).
      The rollout record is in
      [plans/issue-453-postgres-tls.md](https://github.com/ValueRetail/vrsky/blob/main/plans/issue-453-postgres-tls.md).
      Local Docker Compose stays on plain connections; the k3d dev Secrets
      use `sslmode=require`, since k3d does not copy the CA.
      Connections that tenants configure to their own databases choose their
      `sslmode` themselves.
- [ ] **Back up the key** alongside (but stored separately from) the database
      backup — a DB restore is useless without the key. See
      [Backup & restore](backup.md).

## Key rotation

The master key can be rotated without downtime by re-wrapping secrets. See the
rotation procedure in the [security reference](../SECURITY.md) (re-wrap a single
secret, or rotate the master key itself). Coordinate with an upgrade window and
back up first.

## Defense in depth

- Secrets are referenced as `<field>_secret_id` in connection configs — the
  plaintext is never persisted server-side and never returned by the API.
- Per-tenant isolation is linted in CI (`lint-tenant-filter`): every
  tenant-scoped query must filter by `tenant_id`.
- See the [security whitepaper](../security/whitepaper.md) for the full model.
