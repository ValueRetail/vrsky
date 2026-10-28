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

During a deploy the old file consumer stops before the new one starts, so only one ever watches a folder. For those few seconds nothing is watching: files that land then are published a little later, by the new one's first checks, and an upload from the editor fails and can be retried. The exception: if the new one can't read its list of published files when it starts (a database error, logged at Error in the file-consumer log), it counts everything in the folder as already there, so a file that landed in the gap is never published.

Move or delete files once they have been processed. The list of published files is kept for every file still in the folder, so it grows with the folder; a folder that only ever fills up makes every check slower.

Changing the folder in the pipeline's config counts as a first start for the new folder, so what is already in it is not ingested.

A file is told apart by its name, size and modification time. When a file that was already published (or was there at the first start) is overwritten, or a new file is renamed over it, its size or modification time changes and it counts as a new file: it is published once more, with the new content, after the same two unchanged checks. That holds across a restart too, so a file replaced while the pipeline was stopped is published when it starts. A file left alone is never sent again. Some things to know:

- Touching a file (`touch orders.csv`) or saving it again unchanged also changes its modification time, so it is sent again. That is a way to resend a file on purpose.
- Don't keep a file that is still growing, such as a log, in the folder: it is sent again every time it pauses for a check.
- A replacement with exactly the same size and modification time is not noticed. That takes a copy that keeps the original's timestamp (`cp -p`, `rsync -t`) of a file the same size, or a same-size rewrite within the same timestamp tick on a share that only keeps whole seconds.
- A file that failed to ingest is tried again when it is replaced, not before.

If the folder can't be read for a while (a network share reconnecting, say, or the folder deleted), the checks are skipped and logged, and nothing is republished when it comes back with the same files. Files copied back in fresh, with new modification times, count as replaced and are published again. A pipeline that can't read its folder when it starts still starts and shows as running, with a warning in the file-consumer log. It publishes nothing until a check can read the folder. That check counts as the start: files not yet published are picked up as they would be after a restart. On the pipeline's very first start, everything in the folder at that check counts as already there.

While the folder can't be read, the editor's File watcher panel shows a red ERROR line naming the folder and the reason (for example `permission denied`, or that the folder is missing): at once, then at most every 30 seconds while it lasts. When a check can read the folder again the panel says so. If a pipeline that started without its folder can read the folder but not the list of files it already published (a database hiccup), it keeps waiting and tries both again at the next check, rather than counting everything in the folder as already published.

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

- **Which folder on your machine (local stack).** In `docker compose`, the
  source reads `/data/input`, which is the repository's `data/input` unless
  `.env` sets `VRSKY_FILES_HOST_DIR` to another folder (any path style,
  `C:\Users\you\vrsky-files` included). A leading `~` in the source's path
  means that same folder. The destination writes to `/data/output`, the
  repository's `data/output`, and a leading `~` in its path means
  `/data/output`, not the source's folder. Either way your workspace's files
  sit in a `<workspace id>` subfolder. See [Windows](../operator/windows.md#windows-specific-things-that-bite).
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
