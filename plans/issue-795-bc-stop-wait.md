# #795: Business Central consumer: Stop waits for the pollers

## Goal
`Stop` in `src/cmd/business-central-consumer/service.go` slept 2 s and returned, so a poller still storing its checkpoint could be cut off at shutdown and the next start resent what that poll had published. #789 (#800) gave each poller a `done` channel. `Stop` now waits on it.

## Acceptance criteria
- `Stop` waits for every active poller and every poller in `stopping`, bounded by its context, and no longer sleeps.
- It returns as soon as the last one is done; with no pollers it returns at once.
- A poller that never finishes holds `Stop` only until the context ends; `Stop` then returns the context's error (the SDK lifecycle only logs it).
- No goroutine is left behind once the pollers are released.

## Touched
`src/cmd/business-central-consumer/service.go` (`Stop`, new `waitExited`), new `stop_wait_test.go`. No docs change: the behaviour is internal to shutdown.

## Decisions
- Pollers are collected under `c.mu`, the wait happens after unlocking, so a poller exiting (`pollerExited` takes the lock) cannot deadlock against `Stop`.
- `stopping` is included: a stop command just before shutdown leaves a poller saving that is no longer in `active`.
- A poller with a nil `done` (built by hand in tests) is skipped, as `handleStopCommand` already does.
- Returning `ctx.Err()` on expiry (was always nil) so the SDK logs that the wait was cut short. The lifecycle gives Stop 5 s.
- Hung pollers are not abandoned silently: the error is the signal. Duplicates on the next start remain possible in that case; that is the bound the issue asks for.

## Shared helper for Sitoo (#790)?
`waitExited` is ten lines over `[]*poller`, and the poller type is local to each consumer. A shared helper would have to take `[]<-chan struct{}`, which is barely shorter than the loop. Not worth a package; Sitoo can copy the loop. If a third consumer needs it, move a `sdk.WaitAll(ctx, ...chan)` then.

## Verification
`go test -race -count=3` on the package: gated unit tests (done already, gated, stopping, nil done, ctx expiry), and two end-to-end tests on a real poller stopped mid-poll with a held checkpoint save (Stop returns only after the save; Stop returns on ctx expiry while the save hangs, then no poller goroutine remains). Mutation: replacing the wait with `return nil` fails four of them.
