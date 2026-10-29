# Connector pages name fields the code doesn't read (#564)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Backlog items 108 and 113 (the file-page item, numbered 107 until #560). The SFTP and
cloud-storage pages document keys the workers don't read, and leave out most
of the ones they do. The file page still says every check looks at every
file, which stopped being true with #537.

## What the code reads

SFTP, `config.sftp` (`SFTPConfig` in `src/cmd/sftp-consumer/service.go` and
`src/cmd/sftp-producer/service.go`):

- both roles: `host`, `port` (0 means 22), `username`, `password`,
  `private_key`, `host_key` (empty: any host key accepted, warning logged),
  `remote_dir` (required; the producer creates it).
- consumer: `file_pattern` (glob on the file name), `poll_interval_seconds`
  (0 or unset: one poll, then stopped), `after_action` (`none` default,
  `delete`, `move`), `move_dir` (created if missing; `move` without it fails
  per file).
- producer: `filename_template` (default `{{.uuid}}.json`).
- There is no `sftp.path`.

Cloud storage, `config.cloud_storage` (`objectstore.Config` in
`src/pkg/objectstore/store.go`, `cloudConfig` in both workers):

- both roles: `provider` (empty means `s3`), `bucket`, `prefix`, `region`,
  `endpoint`, `access_key_id` + `secret_access_key` (neither: AWS default
  chain), `connection_string` or `account_name` + `account_key` (Azure),
  `credentials_json` (GCS).
- consumer: `mode` (`poll` default, `event`), `event_queue_url`,
  `event_queue_name`, `event_subscription`, `event_project`,
  `event_endpoint`, `file_pattern` (glob on the base name, poll mode only),
  `poll_interval_seconds` (0 or unset: once), `after_action`, `move_prefix`
  (required with `move`, the consumer doesn't start without it).
- producer: `key_template` (default `{{.uuid}}`), `sse.mode`,
  `sse.kms_key_id`. The page says `encryption.*`; the code reads `sse`.
- There is no `event_mode`.

File consumer (`statTurns`, `statThisPoll` in
`src/cmd/file-consumer/service.go`): with more than 1 000 known files a poll
stats about 1 000 of them, each once every `ceil(known/1000)` polls. New,
pending and stampless names are stat'ed every poll.

## Acceptance criteria

1. Every key on `sftp.md` and `cloud-storage.md` is one the code reads, with
   its default from the code; every key the consumers and producers read is
   on the page.
2. `file.md`'s paragraphs on moving files out and on replaced files describe
   the in-turns check from #537 (1 000 per check, `ceil(files/1000)` checks,
   about 50 s at 10 000 files).
3. `mkdocs build --strict` passes (what `docs.yml` runs).
4. Backlog items 108 and 113 struck with `(#564)`. Mismatches
   between code and UI found on the way go in as new backlog items.

## Files

- `docs/connectors/sftp.md` (config reference and notes only; another
  worker adds a stability paragraph to the source section)
- `docs/connectors/cloud-storage.md`
- `docs/connectors/file.md`
- `plans/backlog.md`

## Decisions

- Document what the code reads where the UI differs: the editor shows a
  poll interval of 60 when none is saved, the consumers treat unset as
  "poll once". The page says so; the mismatch is a backlog item.
- Fix the cloud-storage producer's `encryption` block to `sse` too. Not in
  the issue, but it is the same kind of error on the same page.
- SFTP with both a password and a key: the code offers both (key first), so
  the page no longer says "not both".
