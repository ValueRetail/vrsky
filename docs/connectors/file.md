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
- `file.path` — output directory. If empty, defaults to `/data/output`.

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
- **Default path.** An empty `file.path` on the producer falls back to `/data/output`.
- **No SSE panel.** The file-producer has no live SSE panel in the editor. To watch output, tail the worker logs:

    ```bash
    docker compose logs -f file-producer
    ```
