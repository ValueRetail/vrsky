# Every file-consumer poll stats every file in the folder (#537)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Backlog item 77. Since #420 each poll stats every file in the folder to see
whether a done file was replaced (overwritten, or renamed over). On the
production share (`azurefile-csi`, an SMB mount) that is a round trip per
file per poll. Make a poll of a large folder cheaper without losing #420's
replaced-file detection.

## Options considered

1. Directory mtime as the change signal. A rename into or over a name, a
   create or a delete changes the directory's mtime; an in-place overwrite
   (`open(O_TRUNC)`, write) does not. #420 must catch both, so a quiet
   directory mtime can't skip the stats. SMB servers are also loose about
   directory timestamps. Not safe.
2. Stat the names from the listing. `os.ReadDir` on Linux reads names and
   types only; `DirEntry.Info` is an `lstat` per name, the same round trip.
   The SMB listing does carry size and mtime, and the Linux cifs client may
   prime its attribute cache from it (`actimeo`, 1 s by default), so some of
   today's stats may already be served from cache. I can't check that
   without a share, and with 10 000 files the loop outlasts a 1 s cache
   anyway. Nothing to change in code here.
3. Stat done files less often (chosen). Every poll still stats every name
   that is new, pending, or done with no stamp yet. Done files with a stamp
   are stat'ed in turns once there are more of them than a per-poll budget
   (1 000): each name in the turn its FNV hash falls in, so each is looked
   at once every `ceil(done / 1000)` polls. A folder at or under the budget
   behaves exactly as before.

## Approach

- `statTurns` (per poll) splits the done set into `ceil(len(known)/budget)`
  turns and picks this poll's turn from a counter on the connection
  (`statRound`), which only counts polls that read the folder.
- `statThisPoll` skips the stat only for a done, stamped name that isn't
  pending and isn't in this turn. Once a name is seen changed it is pending
  and is stat'ed every poll until it settles and is published, as before.
- `fileConsumer.stat` (default `os.Stat`) and `knownStatsPerPoll` (default
  1 000) are fields so tests can count stats and lower the budget, like
  `readDir` and `pollInterval`.
- Nothing else changes: the listing, deletions, new files, the stability
  check, `mergeSaved`, `savedStampFor`, the checkpoint rows.

## Trade-off

A replacement in a folder with more than 1 000 done files is noticed up to
`ceil(done/1000)` polls later: within 10 polls (50 s at the default 5 s
interval) at 10 000 files, 100 polls (about 8 minutes) at 100 000. It is then
published once, a poll later, as before. When the done set crosses a
multiple of the budget the number of turns changes and every name's turn is
drawn again, so once in a while a name waits up to about twice that. New files are not delayed at all. In exchange an
idle poll costs about 1 000 stats instead of one per file. With a
millisecond per round trip that is 1.3 s per poll instead of 12.8 s, so a
poll of a 10 000-file folder fits its 5 s interval again; before, every poll
overran it.

A file overwritten twice before its turn comes is published once, in its
last version. That already happened for two overwrites within one poll
interval; the window is now longer for large folders.

## Acceptance criteria

1. A folder with more done files than the budget: over `turns` consecutive
   idle polls each done file is stat'ed exactly once; each poll stats about
   `done/turns` of them. Fails without the change (each is stat'ed every
   poll).
2. A folder at or under the budget stats every file every poll, as before.
3. A new file, a pending (changing) file and a done file with no stamp are
   stat'ed every poll, whatever the turn. A new file in a large folder is
   published on the same poll it would have been before.
4. A done file replaced in a large folder is published exactly once, within
   `turns + 1` polls of the replacement, with the new content.
5. Deletions in a large folder are noticed on the next poll, as before.
6. Measured on a synthetic 10 000-file folder, before and after (below).
7. Mutation run: stat'ing every done file, skipping pending or stampless
   names, and a turn that never changes each make a test fail.
8. From `src/`: build, vet, golangci-lint, `go test -race ./...`,
   `make lint-tenant`; the package with `-count=10 -race`.
9. Backlog item 77 struck with `(#537)`.

## Files

- `src/cmd/file-consumer/service.go` (`pollOnce`, `statTurns`,
  `statThisPoll`, `statTurn`, `statFile`, two fields on `fileConsumer`, one on
  `ActiveConnection`)
- tests in `src/cmd/file-consumer`; `stat_measure_test.go` is the
  measurement, skipped unless `VRSKY_MEASURE_STATS` is set
- `known_changes_test.go`: `TestKnownChanges_CompactsPastTheLimit` sets the
  budget to its folder size (see Decisions)
- `plans/backlog.md`

## Measurements

`TestMeasurePollStats`: 10 000 files named like
`order-2026-10-29-NNNNNNN.csv`, local APFS on a Mac, no checkpoint store,
20 idle polls, then one file replaced and polled until it is published.
"Before" is the same code with the budget above the folder size, which is
one turn: the path `main` takes. The 1 ms row adds a 1 ms sleep to each stat
to stand in for an SMB round trip.

| | before | after |
|---|---|---|
| stats per idle poll (mean, most) | 10 000, 10 000 | 1 000, 1 050 |
| idle poll, local disk | 22 ms | 6.6 ms |
| idle poll, 1 ms per stat | 12.8 s | 1.26 s |
| polls until a replacement is published (one run) | 2 | 11 |

The 11 is one run, and the worst case at 10 000 files: it depends on where
the replaced name's turn falls, anywhere from 2 to 11 polls, about 6 on
average.

```
VRSKY_MEASURE_STATS=1 VRSKY_MEASURE_STAT_DELAY=1ms go test -timeout 20m -run TestMeasurePollStats -v ./cmd/file-consumer/
```

## Decisions

- Option 3, budget 1 000 per poll: about a second of round trips per poll
  on a share at 1 ms, well under the 5 s interval, and every folder up to
  1 000 files keeps today's behaviour exactly.
- Turns by a hash of the name, not a full check every Nth poll: the cost is
  spread evenly instead of one expensive poll in N.
- `TestKnownChanges_CompactsPastTheLimit` replaces batches of names in a
  2 000-file folder and expects them published two polls later. It tests
  compaction of the changes row, not stat timing, so it now sets the budget
  to its folder size. Turn timing is tested in its own file.
- `docs/connectors/file.md` says each check looks at every file and that a
  replaced file goes out after two checks. Another worker owns `docs/`
  right now, so the doc change is backlog item 107 instead of part of this
  PR.

## Risks

- Slower replacement detection in large folders, as above. Nothing is lost
  or published twice because of it.
- The budget is a guess at SMB latency; I couldn't measure the production
  share from here. If the cifs client already serves most stats from the
  cache primed by the listing, the gain in production is smaller than the
  1 ms row suggests, and the cost (later detection) is the same.
