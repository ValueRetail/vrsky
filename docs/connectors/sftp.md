# SFTP

The SFTP connector (`config.type: "sftp"`) transfers files over SFTP. It works both as a source (consumer) and a destination (producer), sharing the same config shape.

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

The same `sftp` config block applies to both roles:

- `type` — `"sftp"`.
- `sftp.host` — server hostname.
- `sftp.port` — server port (e.g. `22`).
- `sftp.username` — login username.
- `sftp.password_secret_id` — reference to the password secret. Use this **or** the private key.
- `sftp.private_key_secret_id` — reference to the private key secret. Use this **or** the password.
- `sftp.path` — remote directory.
- `sftp.after_action` — what to do with a source file after processing: `"none"`, `"delete"`, or `"move"`.

```json
{
  "type": "sftp",
  "sftp": {
    "host": "sftp.example.com",
    "port": 22,
    "username": "vrsky",
    "private_key_secret_id": "sec_sftpkey_01",
    "path": "/remote/dir",
    "after_action": "move"
  }
}
```

## Notes

- **Authentication.** Provide **either** a password (`password_secret_id`) **or** a private key (`private_key_secret_id`), not both.
- **Secrets.** `password` and `private_key` are secret fields. You enter them as plaintext in the editor; at deploy they are minted into encrypted tenant secrets and replaced with `<field>_secret_id` references. Stored and example JSON therefore shows only the `_secret_id` form.
- **Test connection.** Use the **Test connection** button in the editor, backed by the `/test-connection` endpoint (#82), to validate host, credentials, and path before deploying.
