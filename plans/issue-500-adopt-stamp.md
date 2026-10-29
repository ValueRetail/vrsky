# A file-consumer pod can adopt a replacement without publishing it (#500)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Backlog item 76. A done name with no stamp in a watcher's memory gets one at
the next poll that can stat it: the file's stamp as it is then, and nothing
is published (`pollOnce`, `src/cmd/file-consumer/service.go`). A name has no
stamp when its stat failed as the set was loaded (`loadKnown`: first start,
a failed checkpoint read, or a name saved before #420), or when it was saved
by a pod from before #420.

That goes wrong with two watchers on one connection:

1. Pod A loads the set; its stat of `x.csv` fails, so `x.csv` is done with
   no stamp.
2. Pod B publishes `x.csv` at stamp S and saves S.
3. `x.csv` is replaced: it is now S'.
4. A's next poll adopts S' as done. `mergeSaved` doesn't help: it leaves a
   saved stamp alone when the file no longer has it, and it runs only
   before a publish anyway.
5. A saves S'. B's next poll reads it before publishing, finds S' is the
   file's stamp, and skips it too. S' is never published.

## Approach

Before adopting the current stamp for a done name with no stamp, `pollOnce`
looks at the saved set (`savedStampFor` in `known.go`):

- the saved row has no stamp for the name (no checkpoint, a name saved
  without one, or no checkpoint store): adopt as before;
- it has the same stamp as the file now: adopt, nothing to publish;
- it has a different stamp: take the saved stamp instead. The name is then
  a done file whose stamp differs from the file's, which is the #420
  replaced-file path: it goes through the stability check and is published
  once. `mergeSaved` before the publish still skips it if the other pod has
  published S' since;
- the saved set can't be read: leave the name without a stamp and look
  again next poll. Nothing is published or adopted on a guess.

The saved set is read with `loadSaved`: the snapshot plus the changes row
when it is on that snapshot, as #426 laid out. It is read at most once per
poll, and only by a poll that meets a done name with no stamp. Every such
name leaves that poll with a stamp (or the read failed), so this is a
one-off read after a start in practice, not a cost per poll.

The test author found a second way to the same loss: a snapshot written
while the name still has no stamp replaces the saved set, and with it the
other pod's stamp. Then the next poll finds no saved stamp and adopts the
replacement. That happens when a poll whose adoption read failed still
saves (it published another file, or saw one deleted, and the read in
`saveKnown` failed too, so the snapshot went out blind), or when A's first
save comes before it can stat the file. So `saveSnapshot` now starts with
`keepSavedStamps`: if any done name has no stamp, it reads the saved set and
gives each such name the stamp saved for it, if there is one. If that read
fails, the snapshot is not written; the set stays dirty and the next poll
tries again. It reads nothing when every done name has a stamp.

No change to the checkpoint format, `mergeSaved` or the changes row. Taking the saved stamp marks the name changed (`touch`), so the
next save records it under the usual #426 rules (changes row while this
watcher owns the snapshot, snapshot otherwise).

## Acceptance criteria

1. Two watchers on one connection over one checkpoint store: A loads with
   the stat of `x.csv` failing; B publishes `x.csv` and saves its stamp; the
   file is replaced; A polls. The replacement is published exactly once
   (by A or B), and the original is not published a second time.
2. The test for 1 fails without the fix.
3. A done name with no stamp whose saved row has the same stamp, or no
   stamp, is still adopted without publishing (no regression for the
   pre-#420 checkpoint and first-start cases).
4. A failed saved-set read leaves the name without a stamp, publishes
   nothing, and the next poll that can read adopts or publishes correctly.
5. The saved set is read at most once per poll for adoption, and not at
   all by a poll with no stampless done names.
6. A snapshot written while a done name has no stamp keeps the stamp
   another watcher saved for it; if the saved set can't be read, no
   snapshot is written. A poll whose reads fail but which publishes another
   file does not lose the replacement (the test author's probe).
7. Against Postgres (`src/pkg/testdb`): the two-pod case of 1.
8. Mutation run: adopting the current stamp unconditionally, and ignoring a
   failed read, each make a test fail.
9. From `src/`: build, vet, golangci-lint, `go test -race ./...` and
   `make lint-tenant` pass; the package passes with `-count=10 -race`.
10. Backlog item 76 struck with `(#500)`.

## Files

- `src/cmd/file-consumer/service.go` (`pollOnce`)
- `src/cmd/file-consumer/known.go` (`adoptCheck`, `savedStampFor`,
  `keepSavedStamps` at the start of `saveSnapshot`)
- new tests in `src/cmd/file-consumer`
- `plans/backlog.md`

## Decisions

- The fix goes in `pollOnce`, as the issue says, and not in `mergeSaved`.
  `mergeSaved` runs only before a publish, and an adoption publishes
  nothing, so it never gets the chance.
- The watcher takes the saved stamp, rather than marking the name "not
  done". The replaced-file path from #420 then does the rest: stability
  check, refresh, publish, restore on a stop. Nothing new to get wrong.
- A failed read waits a poll instead of adopting. Adopting is the bug; a
  poll's delay for a name that is already done costs nothing.
- `docs/connectors/file.md` already says a replaced file is published once;
  this makes that true in one more case, so the docs don't change.

## Risks

- One extra checkpoint read (the whole snapshot) in a poll that meets
  stampless names. With 100 000 names that is about 12 MB once (#426's
  numbers), only after a start where stats failed or a pre-#420 row was
  loaded.
- A snapshot that can't read the saved set while a done name has no stamp
  is held back until it can. If the pod crashes in that window, the files
  it published since its last save are published again at the next start.
  That needs a checkpoint read failure and a stampless name at once, and a
  duplicate beats a file never published.
- A saved row that can never be decoded, together with a stampless name,
  stops snapshots until the row is fixed (the error is logged every poll).
  That is the same class as the deferred baseline from #409, which also
  waits for good on such a row. A name whose stat always fails (a dangling
  symlink) costs one extra read per snapshot, not per poll.
- Taking another pod's stamp can turn the old loss into a duplicate: if B
  published S' but its save hasn't landed, A's `mergeSaved` still sees S
  and A publishes S' too. The stability check leaves at least a poll
  interval for B's save; it's the window #420 and #426 already have.
- Review found one more route to a lost replacement that this doesn't
  close: `loadKnown`'s fallback after a failed checkpoint read is saved by
  the first change after all, blind. Filed as backlog item 102.
- Two watchers of this version still have the last-writer-wins window
  #426 lists under Risks. This only closes it for stampless names. Since
  #425 the Deployment is `Recreate`, so two pods overlap only by accident.
