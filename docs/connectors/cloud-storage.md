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

### What gets published, and when again (poll mode)

In poll mode the consumer remembers which objects it published, with the
size and last-modified time each had, in the connection's checkpoint (one
per tenant, connection and node). It survives restarts, deploys and the
nightly park, so with `after_action: "none"` an object left in the bucket is
published once, not again at every start (#538).

- **First start.** Every object already under the prefix is published. A
  start after the provider, endpoint, account, bucket or prefix was edited
  counts as a first start too.
- **Replaced object.** An object overwritten under the same key (its size or
  last-modified time changed) is published again, once.
- **Deleted, then added again.** An object that a poll finds gone from the
  listing is forgotten; if it comes back it is published again.
- **Failed after-action.** If `delete` or `move` fails, the object is still
  counted as published and stays where it is; it is not published or moved
  again, also after a restart. The failure is in the log (`after-action
  failed`).
- **Checkpoint unreadable.** If the database can't be read at start, the
  consumer publishes nothing and logs `Could not read which objects were
  already published`. It tries again at every poll and carries on as normal
  once the read works. A one-shot connection (`poll_interval_seconds: 0`)
  ends with status `error` instead.
- **Crash.** The list is saved every 32 objects and at the end of each poll,
  so a crash in the middle of a long poll publishes at most the objects since
  the last save again.

An object replaced with the same size within the same second (S3 keeps
last-modified times in whole seconds) looks unchanged and is not published
again. Event mode doesn't use this list: there the queue's acknowledgement
decides what was handled.

After the release that added this, each running poll-mode connection's first
start has no saved list yet, so it publishes what is under its prefix once
more, as every restart did before.

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
- `key_template` — Go template for the object key, rendered against the
  record when it is a JSON object plus `{{.timestamp}}` and `{{.uuid}}`
  (default `{{.uuid}}`). **Pictures and other media** (images, audio,
  video, PDF) that carry a `filename` ignore the template and are written
  as `<prefix>/<filename>` — the same rule as the file and remote-agent
  destinations, so `1896-S.jpg` stays `1896-S.jpg` next to the records.

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
