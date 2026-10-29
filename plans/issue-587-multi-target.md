# #587: a redelivery skips the targets that already wrote the message

## Problem

The remote-agent output handler sends a message to every target it serves
(one per eligible output node: an agent, or a group member's folder) and
acks it only once all of them confirmed the write. When the session ends
while some targets have acked and others have not, the handler returns
`errSessionEnded` and the message is NAK'd. A failed write on one target
does the same. The redelivery went to every target again, so an agent that
had already written the file was offered it a second time.

Which pipelines have more than one target in one handler: two or more
single-agent output nodes (all on the pipeline's own durable), or two group
nodes whose folders the same member has (both on that member's durable).
Group members otherwise have a durable each, so they never wait for each
other.

## What a duplicate costs on the agent side

`pkg/agent/writer.go` writes to `~vrsky-<deliveryID>.part` in the target
folder, checks the checksum, then renames onto the final name, replacing an
existing file. The name comes from `agentproto.GenerateFilename`, which is
deterministic for a message (envelope ID, content type, source, metadata
filename, created-at). So a duplicate write is the same file under the same
name with the same content:

- harmless while the first copy is still in the folder (it is replaced by
  an identical one);
- a second copy for whatever reads the folder once it has taken the first
  away (SuperPOS imports and moves or deletes the file). That is the real
  cost: the importer sees the file twice.

The agent keeps no record of what it wrote, and adding one would change the
agent's local state and behaviour. The agent side stays as it is.

## Decision: (a) per-target tracking in the gateway, in memory

The gateway remembers, for a message it NAK's, which targets wrote it, and
the redelivery skips them.

- Key (`writtenKey`): connection ID, stream name, stream sequence, envelope
  ID, node ID, agent ID, directory, filename. Stream and sequence identify
  the message in JetStream; the envelope ID keeps a sequence reused by a
  recreated stream from matching a different message (a wrong match would
  lose a write, which is worse than a duplicate); node, directory and
  filename make a redeploy that changes the node's folder or pattern write
  the new file.
- Entries are added only when the handler returns an error and only for
  targets whose agent acked `ok`. A single-target message adds nothing: if
  its one target wrote it, the handler acks. Most of the time the set is
  empty.
- The redelivery that completes the message forgets the entries it skipped.
- Bounded: an entry expires after `messaging.MainRetention` (72 h; a message
  can't wait longer than that in the stream), and the set holds at most
  10 000 entries. When full it drops the expired ones and then the oldest.
  Dropping an entry early costs a second write, never a missed one.
- Delivered events: the panel gets a "delivered" event for every target once
  the message is done, including those written in an earlier attempt (they
  were not reported then, since the message was not done).

Also fixed in passing: after the scope ended, the handler returned at the
first target without an answer, so the answers of targets after it were
never read. It now reads all of them; an answered `ok` is remembered whatever
its position.

### Why not the alternatives

- (b) at-least-once per target with idempotent writes on the agent: the
  agent already writes idempotently by name, and that does not help the
  importer that took the first copy away. It would also need the agent to
  keep state, which is a change to the agent.
- A JetStream KV bucket (survives restarts): new stream configuration, so a
  human decision. Filed as backlog item 138.

### What remains

- The memory is per process. A redelivery handled by another process (the
  gateway restarted, or the new pod of a rolling deploy took over) writes to
  every target again. The deployment runs one replica, so that is a restart
  or deploy between the NAK and the redelivery.
- A DLQ retry from the UI republishes the message: a new stream sequence,
  so every target writes it again. That is a retry the user asked for.
- A single target whose ack reaches the gateway but whose JetStream ack is
  lost (pod killed in between) is redelivered after AckWait and written
  again. Unchanged, out of scope.

Documented in `docs/connectors/remote-agent.md` ("Several destinations").

## Acceptance criteria

- Two targets, one acks, the session ends: the redelivery is offered only to
  the target that did not write; the message is acked once it has.
- Same when the target that wrote comes second in node order.
- Same when the other target's write fails instead of the session ending.
- Same for one member with two group folders (key includes node and folder).
- A single target: no entry is ever added; a failed write and a session end
  redeliver to it as before; a write acks on delivery 1.
- The set is empty after the message completes; TTL, cap and eviction
  tested directly.
- No change to stream or consumer configuration, the agent, its allowed
  operations or permission model, tenancy or auth.

## Files

- `src/cmd/remote-agent/written.go`: new, `writtenSet` and `writtenKey`
- `src/cmd/remote-agent/output.go`: the handler skips, remembers, forgets;
  reads every answer after the scope ends
- `src/cmd/remote-agent/service.go`: the `written` field
- `src/cmd/remote-agent/multi_target_test.go`: new
- `docs/connectors/remote-agent.md`: "Several destinations"
- `plans/backlog.md`: item 138

## Tests

`multi_target_test.go`, against the embedded JetStream server with agents
polling the real HTTP API:

- `TestOutput_MultiTargetRedeliverySkipsAgentsThatWrote`: four cases (writer
  first / writer second with a stop, failed write, one member with two group
  folders). Checks the redelivery goes only to the other target, the target
  that wrote is not offered it, the message is acked on delivery 2, one
  "delivered" event per target, the set empty afterwards. All four fail on
  main.
- `TestOutput_SingleTargetRedeliveryUnchanged`: write, failed write,
  session end; nothing remembered.
- `TestWrittenSet`: TTL boundary, every key field, forget, cap (expired
  first, then oldest), re-adding a held key.

Mutation run over the key conditions: see the PR.
