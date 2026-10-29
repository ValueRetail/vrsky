# SFTP and cloud-storage source panels save the poll interval they show (#589)

Backlog item 119, found in #564. UI only.

## What is wrong

`SFTPConsumerConfig` and `CloudStorageConfigEditor`
(`ui/src/components/Pipeline/PropertyEditor.tsx`) show
`poll_interval_seconds ?? 60` but write the field only from `onChange`.
Both consumers decode a missing field as 0 and treat `<= 0` as "run once"
(`src/cmd/sftp-consumer/service.go:358`,
`src/cmd/cloud-storage-consumer/service.go:363`). A source saved without
touching the field shows 60 and polls once, then the connection shows as
stopped.

## Decision

Write the default into the node config, as `PollOrMethod` does for the
retail connectors' 300, instead of changing the shown value to 0. 60 is
what the panel has always promised, polling is what almost everyone wants
from a source, and the docs and the label ("0 = once") stay true.

Two differences from `PollOrMethod`:

- The effect is keyed on the field being unset, not on an empty dependency
  list. `PropertyEditor` is not remounted when the selection moves from one
  SFTP node to another (`useNodeEditorState` swaps the config under the same
  component), so a mount-only effect would miss the second node.
- Cloud storage writes it only for the consumer role in poll mode. Event
  mode has no interval and a destination has no poll. Switching an event
  node back to poll writes it then.

0 stays a legitimate choice (one poll, a documented mode). An explicit 0 is
`!== undefined`, so it is never overwritten. Typing a non-number still
turns into 0 as before; that is the existing `parseInt(v) || 0`, not
changed here.

## Moving the two panels out of PropertyEditor.tsx

The tests render the panels directly. Importing `PropertyEditor.tsx` into a
test pulls a 4000-line file with almost no test coverage into the coverage
denominator and takes the suite from 89% to 58% statements, under the 80%
gate. `RemoteAgentConfigEditor` is already a file of its own for the same
reason. So `SFTPConsumerConfig` and `CloudStorageConfigEditor` move, as they
are, into `SFTPConsumerConfig.tsx` and `CloudStorageConfigEditor.tsx` (the
latter still serves both roles), apart from the added effect. The 60 lives in
`pollInterval.ts`. Use `git diff --color-moved` to review.

## Existing nodes

Nodes saved before this have no value and still poll once. No migration
(out of scope). The editor writes the 60 into its local config when the
node is opened, so opening the node and pressing Save Configuration fixes
it; the node is redeployed as usual. The two connector pages now say so.

## Acceptance criteria

- A new SFTP source or poll-mode cloud-storage source saves
  `poll_interval_seconds: 60` without the field being touched.
- An explicit value, including 0, is not overwritten.
- Event-mode cloud storage and cloud-storage destinations get no interval.
- vitest tests fail before the change and pass after.

## Touches

- `ui/src/components/Pipeline/PropertyEditor.tsx` (the two panels leave it)
- `ui/src/components/Pipeline/SFTPConsumerConfig.tsx`, `CloudStorageConfigEditor.tsx`, `pollInterval.ts` (new)
- `ui/src/components/Pipeline/PollIntervalDefault.test.tsx` (new)
- `docs/connectors/sftp.md`, `docs/connectors/cloud-storage.md`
- `plans/backlog.md` (item 119 struck)
