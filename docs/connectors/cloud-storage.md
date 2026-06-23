# Cloud storage (S3 / Azure / GCS)

The cloud storage connector reads from and writes to object stores. A single
connector kind (`cloud_storage`) covers Amazon S3, Azure Blob Storage, and
Google Cloud Storage; the `provider` field selects the backend and which
credential fields apply.

| Provider | Credential fields |
| -------- | ----------------- |
| `s3`     | `access_key_id` + `secret_access_key` |
| `azure`  | `account_name` + `account_key` |
| `gcs`    | `credentials_json` |

## As a source (consumer)

A consumer node lists objects under `prefix` in the configured `bucket` and
downloads them as messages. By default it polls; set `event_mode` to `event`
to receive object-created notifications instead (S3 → SQS, Azure → Storage
Queue, GCS → Pub/Sub) for low-latency, event-driven ingestion (#106).

Config bullets:

- `provider` — `s3`, `azure`, or `gcs`.
- `bucket` — the bucket / container to read from.
- `prefix` — key prefix to scope the listing (e.g. `incoming/`).
- `region` — provider region (S3).
- Credentials — per-provider, as in the table above.
- `event_mode` — `poll` (default) or `event` for notification-driven reads.

```json
{
  "type": "cloud_storage",
  "cloud_storage": {
    "provider": "s3",
    "bucket": "my-bucket",
    "prefix": "incoming/",
    "region": "us-east-1",
    "access_key_id": "AKIA...",
    "secret_access_key_secret_id": "<field>_secret_id",
    "event_mode": "event"
  }
}
```

## As a destination (producer)

A producer node writes each incoming message as an object under `prefix` in
`bucket`. Server-side encryption can be enabled per bucket (#80) via the
`encryption` block: `none`, `sse-s3` (S3-managed keys), or `sse-kms` (supply
`kms_key_id`).

Config bullets:

- `provider`, `bucket`, `prefix`, `region` — as above.
- Credentials — per-provider.
- `encryption.mode` — `none`, `sse-s3`, or `sse-kms`.
- `encryption.kms_key_id` — KMS key when `mode` is `sse-kms`.

```json
{
  "type": "cloud_storage",
  "cloud_storage": {
    "provider": "azure",
    "bucket": "my-container",
    "prefix": "outgoing/",
    "account_name": "mystorageacct",
    "account_key_secret_id": "<field>_secret_id",
    "encryption": { "mode": "none" }
  }
}
```

## Notes

- **Secrets.** Credentials typed in the UI (`secret_access_key`,
  `account_key`, `credentials_json`) are minted into encrypted tenant secrets
  at deploy and replaced with `<field>_secret_id` references — they never
  appear in plaintext in the stored connection. Non-secret fields like
  `access_key_id` and `account_name` are stored as-is.
- **Test connection.** The cloud-storage worker exposes a `/test-connection`
  endpoint; use the **Test connection** button in the editor to verify bucket
  access before deploying.
- Object-format handling and any provider-specific options not listed here are
  configured via the in-app pipeline editor (Property panel).
