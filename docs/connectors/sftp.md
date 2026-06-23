# SFTP

The SFTP connector (`config.type: "sftp"`) transfers files over SFTP. It works both as a source (consumer) and a destination (producer), sharing the same config shape.

## As a source (consumer)

A consumer node connects to a remote SFTP server, reads files from a directory, and emits them into the pipeline. The `after_action` setting controls what happens to a file once it has been read.

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
