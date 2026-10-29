# Front Systems unregisters its webhooks on stop (#503)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Backlog item 97. Since #429 the Front Systems consumer forgets a connection
on stop, but:

- it never removes the callback it registered at Front Systems, so Front
  Systems keeps posting to a stopped pipeline, and a stop then start (or a
  pod restart, which re-registers through boot restore) posts a second
  registration for the same event and URL;
- `POST /frontsystems/events/{id}` publishes for any connection that exists
  in the database, running or not.

## What Front Systems documents

From the API catalog behind developer.frontsystems.com (REST API V2, read
through the portal's `/developer/apis/...` endpoints):

- `POST /api/Webhooks` `{"event","url"}` creates one; `GET /api/Webhooks`
  lists them; `DELETE /api/Webhooks/{webhookId}` removes one. The model is
  `WebhookViewModel {id (uuid), storeId, event, url}`. The responses have no
  documented body, so the create response's `id` is read when present and
  the list is the fallback.
- The webhooks guide: any 2xx is delivered. There is **no automatic resend**:
  integrators find failed events with `GET /api/WebhooksEvents/{id}?success=false`
  and resend them. No status code is documented as "stop retrying", because
  nothing retries.

There is no live Front Systems account; all of this is tested against an
httptest fake.

## Acceptance criteria

1. A stop command for an active connection deletes every webhook id the
   start registered or found (`DELETE /api/webhooks/{id}` with both auth
   headers), with that connection's own credentials.
2. The unregister is best effort: it runs after the connection is forgotten,
   off the stop handler's goroutine; a failure (non-2xx other than 404,
   network error) is logged and the connection stays stopped.
3. A start lists existing webhooks first and reuses one whose event and URL
   match instead of posting a duplicate, so restarts and stop/start cycles
   don't stack registrations. A list failure falls back to posting, as today.
4. A start never lists before a previous stop's unregister for the same
   connection has finished, so it can't reuse an id that is about to be
   deleted.
5. A start abandoned by a stop that arrived mid-start deletes what it
   registered (before, there was "nothing to undo").
6. Inbound events are published only for a connection this process started,
   and only when the tenant that owns the connection row is the tenant the
   connection was started for. Otherwise nothing is published:
   - unknown connection: 404 (unchanged);
   - known but not running here, or tenant mismatch: 410 Gone;
   - boot restore still running, or shutting down: 503.
7. A stop naming another tenant neither forgets an active connection nor
   unregisters anything. (A start still in flight is abandoned by any stop
   for its connection id, because `StartGuard.StopLocked` takes no tenant;
   that is backlog item 95 and stays out of this change.)
   #498 adds tenant checks to every stop handler and may change
   `StopLocked`; whichever PR merges second rebases.
8. Process shutdown (`Stop`) does not unregister: the connections are still
   `running` in the database and boot restore picks them up again (and now
   reuses their registrations).
9. A start whose registration fails part way deletes the webhooks it
   created in that call (those whose id came back in the create response),
   so nothing is left posting to a stopped route.

## Design

- `active` becomes `map[string]*activeConn{tenantID, cfg, hookIDs}`.
- Per-connection `hookLock` (a mutex per connection id, never pruned, like
  StartGuard's `seq`) serialises all Front Systems webhook calls for one
  connection: a start holds it from after Reserve until it has activated or
  undone its work; a stop's unregister goroutine holds it while deleting.
- The stop handler moves the entry's ids to `pendingUnregister[conn]` in the
  same critical section that deletes it from `active`, then starts a
  goroutine that takes the hook lock and drains the pending list. A start,
  holding the hook lock, drains it too before listing. Whoever gets there
  first does the deletes; a start can never list before them.
- Unregister goroutines are tracked by a WaitGroup. They and every start's
  Front Systems calls run under a lifecycle context; `Stop` waits up to 5 s
  for the goroutines, cancels, then waits no longer than its own ctx.
- `restoring` is set in `Configure` (the runner starts the HTTP server right
  after it) and cleared after `RestoreRunning`, so the inbound route
  answers 503 rather than 410 for connections not restored yet.

## Review (round 1)

Fixed: 410 in the window between Configure and Run (now 503); Stop could
wait past its ctx; a partial registration failure left created webhooks
behind; comment on the AfterActivate hook under the hook lock. Recorded
instead of fixed: a failed start keeps refusing events (backlog 103, new);
a wrong-tenant stop abandons an in-flight start (backlog 95).

## Review (round 2)

Approved. Taken: the webhook list also accepts a `value`/`items`/`data`
wrapper and logs the body when it can't parse it; backlog 95 notes that an
abandoned start now deletes its webhooks. Left as is: a partial failure
can't undo a webhook whose create response had no id (the next start
reuses it); a hook lock is made for every start, Front Systems or not
(one small mutex per connection id); a start's Front Systems calls follow
the lifecycle context, not the restore ctx (as before, when they used
`context.Background()`).

## Files

- `src/cmd/front-systems-consumer/service.go`, `server.go`, `main.go`
  (doc comment) + tests
- `docs/connectors/front-systems.md`
- `plans/backlog.md`: strike 97 with #503

Not touched: `src/pkg/sdk`, `src/pkg/messaging`, other connectors,
`infrastructure/`, `.github/workflows`.

## Decisions

- 410 for a stopped connection. Front Systems documents no resend and no
  status that stops one, so the code doesn't change Front Systems'
  behaviour; any non-2xx marks the event failed, where it can be found and
  resent after the pipeline is started again. 410 says the endpoint is gone
  on purpose, and keeps it apart from 404 (no such connection) in their
  delivery log and ours.
- 503 while restoring or shutting down: the connection may still be
  running in the database, so "gone" would be wrong.
- Tenant mismatch answers 410 like "not running", so the answer says nothing
  about which tenant owns what.
- Look up before posting (the issue's "or"): without it every pod restart
  registers again, which is the same duplicate the stop path fixes.
  Duplicates already at Front Systems from earlier restarts are all recorded
  and all deleted on the next stop; a start does not delete them.
- The inbound route stays unauthenticated beyond the unguessable connection
  id in the URL, as before; Front Systems documents no signature. Tenant
  comes from the connection row as before, and is now also checked against
  the tenant the connection was started for.

## Risks

- An event that lands in the window between a pod's HTTP server starting
  and boot restore reaching its connection gets 503 instead of being
  published. Before, it was published. Front Systems doesn't resend
  automatically, so it shows up as failed in their event log. The window is
  the restore scan (one list plus at most one POST per event per
  connection).
- If boot restore fails to register (Front Systems down), the connection is
  not active and its events get 410 until the next start, where before they
  were published through the old registration.
- Response shapes for create and list are undocumented. If neither carries
  ids, nothing can be unregistered (logged), which is today's behaviour.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race ./...`, `make lint-tenant`; the package with `-count=20 -race`.
Mutation run: drop the unregister call, drop the inbound active check, drop
the tenant comparison; each must fail a test.
