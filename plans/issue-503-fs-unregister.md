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
   unregisters anything, nor abandons a start in flight or a start waiting
   to retry. Since the rebase onto #520 the stop handler calls
   `StartGuard.StopForTenantLocked` first, which covers the active entry and
   a start holding the claim; the handler checks a pending retry's tenant
   itself, because the guard knows nothing about it.
8. Process shutdown (`Stop`) does not unregister: the connections are still
   `running` in the database and boot restore picks them up again (and now
   reuses their registrations).
9. A start whose registration fails part way deletes the webhooks it
   created in that call (those whose id came back in the create response),
   so nothing is left posting to a stopped route.

Added by the owner's decision before merge (see "Retrying failed starts"):

10. A start that fails for a reason that passes is retried with backoff
    (5 s doubling to 5 min, with jitter) until it activates.
11. While a start is in progress or waiting to retry, the event route
    answers 503 for the connection's own tenant (410 for another), not 410.
12. Retries end on a stop for the connection's tenant, when the row is gone
    or no longer `running`, when any start activates the connection, after
    6 hours of failures, and on process shutdown. Nothing starts after a
    stop.
13. Every retry goes through the StartGuard, so two starts of one connection
    never run at once. The retry goroutine is counted by the consumer's
    WaitGroup and ends with the lifecycle context; `Stop` ends a waiting
    retry at once, not after its 5 s grace.
14. A permanent failure is not retried. The row goes to `error` with a
    reason in `last_error` (no response body, no secret), only if it still
    says `running`. Giving up after 6 hours does the same.

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

## Retrying failed starts (owner's decision, 2026-10-29)

After two review rounds the merge was held: since this change a start that
fails (a DB blip at boot restore, Front Systems down) leaves the connection
refusing events with 410 while its row says `running`, where before the
events were published through the old registration. Ludvik decided to fold a
bounded retry of failed starts (then backlog item 103) into this PR before
merging.

### Design (`retry.go`)

- `pending map[connID]*pendingStart{tenantID, ctx, cancel, looping,
  firstFailure, attempts}` under `mu`. A start command or boot restore
  attaches to the connection's pending start (or makes one) inside the
  `active` check that `ReserveOrRetry` runs with `mu` held, so the attach
  and the claim are one critical section and any stop after them finds
  both. A start that is refused drops the one it made. A retry whose
  pending start has been ended is refused in the same check.
- `attemptStart` is the old `startConnection` body. It returns an outcome:
  refused, activated, abandoned, not ours, transient, permanent.
  `startConnection` is now `attemptStart` plus `afterAttempt`.
- `afterAttempt`: transient starts the retry loop once per pending start
  (`bg.Add` under `mu`, refused once `closed`); permanent drops the pending
  start and writes `error`; anything else drops it. Activate drops it inside
  its own critical section.
- `retryLoop`: wait `retryDelay(n)` or until the pending start's ctx ends;
  check the row (`SELECT status ... WHERE id AND tenant_id`); stop if it is
  gone or not `running`; else `attemptStart` with the loop's own pending
  start and no queued retry. A failed row check counts as a transient
  failure. Everything that ends a pending start (activation, stop, giving
  up, a permanent failure, `Stop`) cancels its ctx, so the loop's next wait
  returns at once and the loop ends.
- The stop handler: `StopForTenantLocked` first (from #520), then the
  pending start's tenant, then the active entry as before.
- `Stop` cancels every pending start before waiting for the unregisters.
- Classification. Connection lookup: no row or no Front Systems node is
  "not ours" (every consumer sees every start command), a database that
  doesn't answer is transient, any other error permanent (round 3). The node's type is now read before its secrets, so another
  consumer's node never has its secrets resolved here. Secrets: a database
  that doesn't answer (net errors, EOF, `driver.ErrBadConn`, timeouts, pq
  classes 08, 40, 53, 57) is transient, anything else (not found, can't
  decrypt) permanent. Front Systems: 408, 429, 5xx and no answer at all are
  transient; any other status, a 401 or 403 on the first list, and a
  missing or broken base URL (no http(s) scheme or no host) are permanent.
- The `error` write is `UPDATE ... SET status = 'error', last_error = $1,
  updated_at = NOW() WHERE id = $2 AND tenant_id = $3 AND status = 'running'
  AND updated_at = $4`, with the version the start read (round 3). It keeps
  off a row a stop set to `stopped`, and off a new run after a stop and
  start.

### Decisions

- **503 while a start is in progress or waiting to retry.** The row says
  `running` and the pipeline is meant to take events; 410 would say it was
  stopped on purpose. Neither makes Front Systems resend, so the code is
  about what their log and ours say.
- **No replay of failed events on a successful (re)start.** Left to backlog
  136. The resend call and the response bodies aren't documented beyond the
  API catalog, there's no live account to check them against, and an event
  whose 2xx got lost would be published twice without an idempotency key.
  The connector page says so.
- **Permanent failures go to `error` and are not retried.** Retrying a 401
  every 5 minutes won't fix the keys and may lock out or rate-limit the
  integration user at Front Systems. `error` makes the UI show the cause,
  and the management API lets a connection in `error` be started again
  directly, while a `running` one must be stopped first.
- **Bounded at 6 hours, not by attempt count.** An attempt cap would bring
  back the bug after any outage longer than the cap. At 5 minutes per
  attempt the cost is one indexed SELECT and one list plus a few posts. The
  cluster is parked every night, which restarts the count anyway. After 6
  hours the row goes to `error` with the last cause, so a human sees it.
- **Retry in the consumer, not the SDK.** Moving the loop next to
  `StartGuard` would touch all 16 consumers and the SDK, which runs alone.
  Backlog 135 covers that, and `RestoreRunning` giving up on a failed list.
- **The Front Systems calls of a retry still run under the lifecycle
  context**, not the pending start's. A stop then makes the attempt give up
  at Activate and undo what it registered; cancelling the calls instead
  would leave half-made registrations behind.

### Not covered

- A failed `RestoreRunning` list query (backlog 135).
- An event that arrives while a start command's first attempt runs gets 503
  now; before this PR it was published, before the retry it got 410.

## Review of the retry commit (round 3)

A fresh reviewer asked for one fix and four smaller changes; all taken.

- Required: a `base_url` without a scheme (`fs-apim.example.net`, likely
  since the docs call it the APIM host) failed in `http.Client.Do` with no
  status and was retried for 6 hours. `do()` now parses it and requires
  http or https and a host, else `errBadBaseURL` (permanent). The docs say
  to write `https://`.
- A start command naming the wrong tenant that held the claim made the
  right tenant's queued start run untracked: `Release` ran it before
  `afterAttempt` had dropped the wrong tenant's pending start, so its
  transient failure was never retried. `attemptStart` now hands the claim
  back still held, and `startConnection` and the retry loop release it
  after `afterAttempt`.
- A start queued behind one that fails for good is dropped: a
  per-connection count of permanent failures, read when the start is asked
  for and checked when the queue runs it. Without that, boot restore and a
  start command would both try the refused keys. Reordering the release
  alone doesn't stop it, and dropping the claim's queue would need an SDK
  change.
- The `error` write is guarded by the row version: `getConfig` and the
  retry loop's status check read `updated_at`, the pending start keeps the
  latest, and the UPDATE adds `AND updated_at = $4`. The management API
  bumps `updated_at` on every start and stop, so a stop and a new start
  that landed meanwhile keep the new run. Without a version (the row was
  never read) nothing is written.
- The connection lookup uses `transientDBError` like the secrets: other
  errors (an id that isn't a UUID, 22P02) are permanent, not retried for 6
  hours. A 401 or 403 on the first `GET /api/webhooks` is permanent too (a
  bad base URL already fails the create permanently, before any request).
  DNS and x509 errors stay transient.

## Review (round 1)

Fixed: 410 in the window between Configure and Run (now 503); Stop could
wait past its ctx; a partial registration failure left created webhooks
behind; comment on the AfterActivate hook under the hook lock. Recorded
instead of fixed at the time: a failed start keeps refusing events (now
fixed, see above); a wrong-tenant stop abandons an in-flight start (fixed on
main by #520).

## Review (round 2)

Approved. Taken: the webhook list also accepts a `value`/`items`/`data`
wrapper and logs the body when it can't parse it. Left as is: a partial failure
can't undo a webhook whose create response had no id (the next start
reuses it); a hook lock is made for every start, Front Systems or not
(one small mutex per connection id); a start's Front Systems calls follow
the lifecycle context, not the restore ctx (as before, when they used
`context.Background()`).

## Files

- `src/cmd/front-systems-consumer/service.go`, `retry.go`, `server.go`,
  `main.go` (doc comment) + tests (`unregister_test.go`, `retry_test.go`,
  `retry_pg_test.go`)
- `docs/connectors/front-systems.md`
- `plans/backlog.md`: strike 97 with #503; new items 135 and 136 for what
  the retry leaves out

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
  not active until a retry gets through; its events get 503 meanwhile, where
  before this PR they were published through the old registration. Front
  Systems keeps them as failed (backlog 136).
- Response shapes for create and list are undocumented. If neither carries
  ids, nothing can be unregistered (logged), which is today's behaviour.
- A row edited (anything that bumps `updated_at`) while a start fails for
  good is left as it is rather than marked `error`; the failure is logged.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race ./...`, `make lint-tenant`; the package with `-count=20 -race`.
Mutation run: drop the unregister call, drop the inbound active check, drop
the tenant comparison; each must fail a test. For the retry, see the PR's
mutation table. Postgres tests: `VRSKY_TEST_POSTGRES_URL` set.
