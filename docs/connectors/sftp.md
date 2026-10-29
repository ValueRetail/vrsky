# SFTP

The SFTP connector (`config.type: "sftp"`) transfers files over SFTP. It works both as a source (consumer) and a destination (producer). Both use the same connection fields; each role adds a few of its own.

## As a source (consumer)

A consumer node connects to a remote SFTP server, reads files from a directory, and emits them into the pipeline. The `after_action` setting controls what happens to a file once it has been read.

### What gets published, and when again

The consumer remembers which files it published, with the size and modification time each had, in the connection's checkpoint (one per tenant, connection and node). It survives restarts, deploys and the nightly park, so with `after_action: "none"` a file left in the folder is published once, not again at every start (#538).

- **First start.** Every file already in the folder is published. A start after the host, port, username or folder was edited counts as a first start too.
- **Replaced file.** A file overwritten under the same name (its size or modification time changed) is published again, once.
- **Deleted, then added again.** A file that a poll finds gone from the folder is forgotten; if it comes back it is published again. One deleted and put back between two polls with the same size and modification time (`scp -p`, `rsync -t`) looks unchanged and is not.
- **Failed after-action.** If `delete` or `move` fails, the file is still counted as published and stays in the folder; it is not published or moved again, also after a restart. The failure is in the log (`after-action failed`).
- **Checkpoint unreadable.** If the database can't be read at start, the consumer publishes nothing and logs `Could not read which files were already published`. It tries again at every poll and carries on as normal once the read works. A one-shot connection (`poll_interval_seconds: 0`) ends with status `error` instead.
- **Crash.** The list is saved every 32 files and at the end of each poll, so a crash in the middle of a long poll publishes at most the files since the last save again.

Two limits: a file replaced with the same size within the same second (most servers keep modification times in whole seconds) looks unchanged and is not published again. And there is no stability check yet, so a file listed while it is still being uploaded is published half-written, then again once the upload finishes and its size changes.

After the release that added this, each running SFTP connection's first start has no saved list yet, so it publishes what is in its folder once more, as every restart did before.

## As a destination (producer)

A producer node connects to a remote SFTP server and writes files into the configured directory.

## Config reference

Both roles use an `sftp` block. The connection fields are the same for both:

- `type` — `"sftp"`.
- `sftp.host` — server hostname. Required.
- `sftp.port` — server port. Default `22`.
- `sftp.username` — login username. Required.
- `sftp.password_secret_id` — reference to the password secret.
- `sftp.private_key_secret_id` — reference to the private key secret (PEM).
- `sftp.host_key` — the server's public key as one line, e.g. `ssh-ed25519 AAAA…` (the key part of its `known_hosts` line). When set, a server that presents another key is refused. When empty, any key is accepted and the worker logs a warning; set it in production.
- `sftp.remote_dir` — remote directory to read from or write to. Required.

Source only:

- `sftp.file_pattern` — glob matched against the file name, e.g. `*.csv`. Empty means every file. Subdirectories are never read.
- `sftp.poll_interval_seconds` — seconds between polls. `0` or unset means one poll, after which the connection shows as stopped. The editor shows `60` in this field until you change it, but saves nothing until you do, so type the interval you want.
- `sftp.after_action` — what to do with a file once it is published: `"none"` (default, leave it), `"delete"`, or `"move"`.
- `sftp.move_dir` — directory to move files into when `after_action` is `"move"`; created if missing. With `"move"` and no `move_dir`, every move fails and the files stay where they are.

Destination only:

- `sftp.filename_template` — Go template for the file name, rendered against the record when it is a JSON object, plus `{{.timestamp}}` (UTC, `20060102T150405Z`) and `{{.uuid}}` (the message id). Default `{{.uuid}}.json`. A record field named `timestamp` or `uuid` takes the place of the built-in. A template that names a field the record doesn't have drops the message, with an error in the producer's log (`invalid filename template; dropping message`). The result is cut to a single file name, so it can't point outside `remote_dir`. `remote_dir` is created if missing.

```json
{
  "type": "sftp",
  "sftp": {
    "host": "sftp.example.com",
    "port": 22,
    "username": "vrsky",
    "private_key_secret_id": "sec_sftpkey_01",
    "host_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA...",
    "remote_dir": "/remote/in",
    "file_pattern": "*.csv",
    "poll_interval_seconds": 60,
    "after_action": "move",
    "move_dir": "/remote/in/processed"
  }
}
```

## Notes

- **Authentication.** Set a password (`password_secret_id`) or a private key (`private_key_secret_id`). With neither, a source doesn't start and a destination drops each message with an error in its log. With both, the key is tried first, then the password.
- **Secrets.** `password` and `private_key` are secret fields. You enter them as plaintext in the editor; at deploy they are minted into encrypted tenant secrets and replaced with `<field>_secret_id` references. Stored and example JSON therefore shows only the `_secret_id` form.
- **Test connection.** Use the **Test connection** button in the editor, backed by the `/test-connection` endpoint (#82), to validate host, credentials, and remote directory before deploying.
