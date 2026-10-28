# The File watcher panel's connected event shows the full volume path (#455)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

The `connected` event that opens the File watcher panel's stream
(`src/cmd/file-consumer/server.go`, `handleEvents`) names the folder the way
the workspace knows it, through `folderLabel` (the path relative to the
tenant root), as the error and recovery events from #409 already do. Today
it says `Watching /data/input/<tenant-id>/orders`, which gives away the
mount layout and the tenant id.

## Acceptance criteria

1. The `connected` event's message names the folder relative to the tenant
   root (`orders/in`, or `.` for the root).
2. It never contains the volume root, the tenant id or the absolute watch
   folder, for a nested path and for the default (root) path.
3. A test over the real handler (`handleEvents` through `httptest`) checks
   both and fails without the fix.
4. Mutation run: put back `ac.WatchDir` and the test fails.
5. From `src/`: build, vet, golangci-lint, `go test -race ./...` and
   `make lint-tenant` pass; the package passes with `-count=10 -race`.
6. Backlog item 60 struck with `(#455)`; `docs/connectors/file.md`
   mentions the panel's first line.

## Files

- `src/cmd/file-consumer/server.go` (one line)
- a new test file in `src/cmd/file-consumer`
- `docs/connectors/file.md`, `plans/backlog.md`

## Decisions

- The folder is quoted (`Watching "orders/in"`), the same as the #409
  messages, so the root reads `Watching "."` rather than a bare dot.
- The UI doesn't parse this message (`RemoteAgentPanel` handles its own
  `connected` type; the file panel only prints the text), so the wording
  can change.

## Risks

None worth noting: one message string built from fields of the connection
that don't change after the start.
