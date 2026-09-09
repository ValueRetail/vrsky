# Files

The file connector (`config.type: "file"`) reads uploaded files as a source and writes one file per message as a destination.

## As a source (consumer)

A consumer node reads and watches files that arrive via the file-consumer ingress (locally port `9200`). CSV is supported: the header row maps to columns (#81), and each data row is emitted as a message.

Config reference:

- `type` — `"file"`.
- `file.format` — input format (e.g. `"csv"`).

Other parsing options are configured via the in-app pipeline editor (Property panel).

```json
{
  "type": "file",
  "file": {
    "format": "csv"
  }
}
```

## As a destination (producer)

A producer node writes one file per incoming message to the configured directory.

Config reference:

- `type` — `"file"`.
- `file.path` — output directory, relative to your workspace's own area of the
  mounted volume. If empty, files go to the top of that area.

File naming and serialization options are configured via the in-app pipeline editor (Property panel).

```json
{
  "type": "file",
  "file": {
    "path": "/data/output"
  }
}
```

## Notes

- **Uploads.** Source files arrive through the file-consumer ingress (locally port `9200`).
- **CSV columns.** For `format: "csv"`, the header row defines the column names (#81).
- **Default path.** An empty `file.path` on the producer writes to the top of your workspace's own directory.
- **Paths are confined to your workspace.** Both the source's watch directory and
  the destination's output directory resolve inside a per-workspace subtree of the
  mounted volume (`<mount>/<workspace-id>/…`). Writing a path you can see, like
  `/data/output`, is understood as your workspace's copy of it — the config keeps
  working and the files land in your own area. A path genuinely outside the
  mounted volume is refused and the connection will not start, rather than being
  quietly relocated.

    This matters because the source and destination connectors share one volume
    across all workspaces. Before this, two workspaces writing a file with the
    same name overwrote each other, and a source pointed at the shared output
    root would ingest other workspaces' files.
- **No SSE panel.** The file-producer has no live SSE panel in the editor. To watch output, tail the worker logs:

    ```bash
    docker compose logs -f file-producer
    ```
