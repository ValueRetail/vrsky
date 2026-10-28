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

### Files dropped into the watched folder

The folder is checked every 5 seconds. A new file is published once its size and modification time have not changed between two checks, so it goes into the pipeline 5–10 seconds after it lands. A file that is still being written waits until the writer stops.

Names that start with `.` or `~`, or end in `.tmp` or `.part` (in any case), are never ingested. Upload under such a name and rename the file to its final name when the upload is done; the final name is then published once, complete. A writer that pauses for longer than a check partway through a file can still have it published early, so renaming into place is the safe way to deliver large files.

Files already in the folder the first time the pipeline starts are not ingested. After that the file consumer remembers, per pipeline, which files it has published (in the database, so a restart or deploy keeps it). When the pipeline starts again, any file in the folder that was not published yet is published once: a file that landed in the last seconds before a stop, restart or deploy, a file still being written at the time, and files that arrived while the pipeline was stopped. Files published before the stop are not sent again.

Stopping a pipeline pauses it: files that arrive while it is stopped are published when it starts. To skip them, move them out of the folder before starting.

Move or delete files once they have been processed. The list of published files is kept for every file still in the folder, so it grows with the folder; a folder that only ever fills up makes every check slower.

Changing the folder in the pipeline's config counts as a first start for the new folder, so what is already in it is not ingested. A file overwritten under a name that was already published is not picked up, while running or across a restart; copy it in under a new name.

If the folder can't be read for a while (a network share reconnecting, say, or the folder deleted), the checks are skipped and logged, and nothing is republished when it comes back. A pipeline that can't read its folder when it starts still starts and shows as running, with a warning in the file-consumer log. It publishes nothing until a check can read the folder. That check counts as the start: files not yet published are picked up as they would be after a restart. On the pipeline's very first start, everything in the folder at that check counts as already there.

## As a destination (producer)

A producer node writes one file per incoming message to the configured directory.

Config reference:

- `type` — `"file"`.
- `file.path` — output directory, relative to your workspace's own area of the
  mounted volume. If empty, files go to the top of that area.

File naming and serialization options are configured via the in-app pipeline editor (Property panel). A media file (image, audio, video, PDF) that carries a `filename` in its
metadata is always written under that name, even when a filename pattern is
set — the pattern names the records, and pictures travelling with them keep
their own names (see [Business Central → Pictures](business-central.md#pictures)).

```json
{
  "type": "file",
  "file": {
    "path": "/data/output"
  }
}
```

## Notes

- **Uploads.** The editor's upload panel posts to the management API, which checks your workspace owns the connection before handing the file to the file-consumer.
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
- **Browsing output.** The editor's **File Output** panel lists and deletes what
  a pipeline produced. It goes through the management API
  (`/api/v1/connections/{id}/files`), which checks the session owns the
  connection; the file-producer then resolves the directory from that
  connection's workspace. So the panel only ever shows your own files, and the
  path you typed is understood as your workspace's copy of it — the same rule as
  writing.

    !!! danger "The worker ports must not be published"

        `file-producer:9900` and `file-consumer:9200` serve `/files` and
        `/upload`. Neither has a tenant check reachable without a connection ID,
        and both treat an unset `FILE_PRODUCER_AUTH_TOKEN` /
        `FILE_CONSUMER_AUTH_TOKEN` as "allow any caller". The management API's
        proxy is the security boundary, and the tokens are what keep anything
        else on the cluster network off them.

        Giving either port an ingress — the obvious-looking fix when a panel
        cannot reach a worker — puts a cross-workspace file browser, delete
        endpoint and pipeline-injection endpoint on the internet. Route the new
        verb through the management API instead, as `/files` and `/upload` are.

- **No SSE panel.** The file-producer has no live SSE panel in the editor. To watch output, tail the worker logs:

    ```bash
    docker compose logs -f file-producer
    ```
