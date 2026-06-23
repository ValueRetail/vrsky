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
- [ ] **Use TLS to the databases** in production (`sslmode=require` or better).
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
