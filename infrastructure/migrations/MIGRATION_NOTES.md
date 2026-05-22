# Migration notes

## 000009 — secrets table (#66, Phase 1A)

Adds `secrets` and the `_secret_id` reference convention used by connector
configs. No data is migrated by the schema migration itself; existing
connections continue to hold plaintext until you also run
`migrate-secrets`.

### Steps

1. Apply schema migration as part of normal management-api startup
   (`entrypoint.sh` calls `migrate up`).
2. Set `ENCRYPTION_KEY` (64 hex chars). See `docs/SECURITY.md`.
3. From the repo root run the one-shot data migration:

   ```bash
   # Dry-run first — prints the connections that would be modified.
   ENCRYPTION_KEY=$KEY MGMT_API_DB_URL=$DB_URL \
     go run ./src/cmd/migrate-secrets --dry-run

   # Commit:
   ENCRYPTION_KEY=$KEY MGMT_API_DB_URL=$DB_URL \
     go run ./src/cmd/migrate-secrets
   ```

4. Verify no cleartext credential strings remain:

   ```sql
   -- Should return 0:
   SELECT count(*)
   FROM connections
   WHERE nodes::text ~* '"password"\s*:\s*"[^{]'
      OR nodes::text ~* '"token"\s*:\s*"[^{]'
      OR nodes::text ~* '"auth_value"\s*:\s*"[^{]'
      OR (nodes::text ~ 'postgres://[^/]*:[^@{][^@]*@'
          AND nodes::text NOT LIKE '%{secret:%');
   ```

5. Restart all workers so they pick up the resolver path (`db-consumer`,
   `webhook-consumer` use it today; api-consumer reads via its existing
   `DecryptToken` wrapper).

### Idempotency

The data migration is safe to re-run. Fields already showing the
`_secret_id` suffix or the `{secret:<uuid>}` DSN placeholder are skipped.
If a (tenant, name) row already exists in `secrets`, its ciphertext is
overwritten via `ON CONFLICT` so the script always produces a coherent
state.

### Rolling back

Schema rollback (`000009_create_secrets.down.sql`) drops the `secrets`
table. **Do not roll back without first reversing the data migration**,
otherwise references in `connections.nodes` will become dangling.

To reverse the data migration manually, restore the connection rows from a
pre-migration backup — there is no automated downgrade script (Phase 1A
scope decision).

### Worker coverage

As of issue #66 the resolver is wired in:

- `src/cmd/db-consumer` — DB password.
- `src/cmd/webhook-consumer` — used today for path resolution; HMAC
  signing secret arrives in #67.
- `src/cmd/api-consumer` — already encrypts `auth_value` via the legacy
  `DecryptToken` wrapper, which now delegates to `pkg/crypto.Decrypt`.

The following workers are **not yet** wired and will need a follow-up when
their respective issues add credential-bearing fields:

- `src/cmd/http-producer` — needs tenant_id added to its NATS deploy
  message (currently queries `connections` by `connection_id` alone).
- `src/cmd/db-producer`, `postgres-consumer`, `postgres-producer` — same.

This is safe because today's plaintext path still works; the migration
script extracts secrets only where a worker can resolve them. Run the
worker upgrades and re-run `migrate-secrets` per worker as they land.
