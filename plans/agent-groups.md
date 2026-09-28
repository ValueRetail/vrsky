# Remote agent groups: one output node, many tills

## Decisions (2026-09-28)

All three open questions answered yes: group **sources** too, a **Resend
everything** action, and **groups at install time**.

## Built (2026-09-28) — deviations from the plan below

- **Groups at install time come from the token.** Generating a token in
  Settings takes the groups; the agent that registers with it joins them, so
  the one-liner needs no extra flag. `--groups` / `-Groups` still exist and
  win over the token's.
- **Resend everything** is a management-API route (`POST
  /api/v1/connections/{id}/resend`, editor) that publishes
  `vrsky.commands.<tenant>.connection.resend`; the Business Central consumer
  honours it (next poll ignores the watermark and the seen pictures); other
  sources ignore it. A button in the Remote Agent tab.
- **Dormant members.** A member removed from a group keeps its durable but
  has no subscription; the gateway tracks those so that when the agent is
  later revoked the durable is deleted (first version forgot them — caught by
  the test).
- **Announce refreshes membership** immediately (`reconcileTenant`), besides
  the 30 s loop.
- **Events carry the agent's name** (`agent`), taken from the name the agent
  polls with.

## Goal

A Remote Agent output node can target a **group** instead of one agent. Every
agent in the group receives every message into the same folder name, each
with its own delivery queue, so one till being off or slow never holds up the
others. Adding a till = install it and put it in the group; no pipeline edits.
Removing/revoking a till stops its deliveries and nothing else.

VRSky owns this because it already owns what makes it hard at 100 tills:
per-till delivery tracking, holding data for an offline till (72 h, 24 h for
big pictures), retries and the dead-letter queue, and the tenant boundary.
Bifrost keeps doing the per-till part (watching `catalogue-in`, importing).

## How it works today (what the design has to fit)

- One JetStream durable per pipeline (`remote-agent-out-<connID>`,
  `MaxAckPending 1`). Its handler builds one delivery **per output node**,
  hands them to those agents' poll queues, and returns (acks) only when every
  target has confirmed the write; while an agent is offline the handler simply
  waits, so the message stays in the stream with no delivery attempt spent.
- Deliveries are keyed by the authenticated agent (`agentState.pending`), so
  an agent only ever sees its own. Uploads are accepted only from the agent
  named on the source node (`watchingSession`).
- Nodes name an agent by id (`remote_agent.agent_id`); the gateway and the
  management API both check the agent is in the pipeline's tenant, not
  revoked, and has the folder in the right mode.

With a group of 100 on that design, one handler would wait for 100 acks: one
till off = 99 tills starved. So:

## Design

### Membership: a `groups` column on agents

`agents.groups TEXT[] NOT NULL DEFAULT '{}'` + GIN index (migration 000023).
A group is just a name an agent carries (`all-tills`, `store-oslo`); an agent
can be in several. Names follow the folder-name rule
(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`). No groups table: nothing to keep in sync,
no new tenant-scoped table for the lint, and "the group's members" is
`WHERE tenant_id = $1 AND $2 = ANY(groups) AND revoked_at IS NULL`. Renaming a
group (rare) means updating its members — a follow-up if ever needed.

Set from Settings → Remote agents (a Groups cell per agent, editor role, same
inline pattern as rename), at registration (`register --groups a,b` → the
installer's `-Groups`), and by API (`PATCH /api/v1/agents/{id}` gains
`groups`). `GET /api/v1/agents/groups` lists `{name, members, online}` for the
node editor.

### Node config

```json
{"type":"remote_agent","remote_agent":{"target":"group","group":"all-tills","directory":"catalogue-in","filename_pattern":"…"}}
```

`target` defaults to `agent` (existing pipelines unchanged). Exactly one of
`agent_id` / `group` is required (`nodeconfig.go` gets a small custom check;
`configRequirement` is presence-only).

### Gateway: one durable per member

- **Group outputs get a durable per member:** `remote-agent-out-<connID>-<agentID>`,
  same filter subject, `MaxAckPending 1`, the existing handler logic but with
  **that one agent** as the only target. Each member's message therefore waits,
  retries and dead-letters on its own. 100 tills = 100 durables on the same
  subject, which JetStream is built for.
- **Single-agent outputs keep the existing durable and code path**
  (`remote-agent-out-<connID>`), so upgrading changes nothing for running
  pipelines: no durable is renamed, no position is lost, nothing replays.
  A pipeline with both kinds runs both.
- **Membership is live.** At session start the gateway resolves the members.
  Then every 30 s, and immediately when any agent announces (registers /
  restarts), it re-resolves each group session and diffs: a **new member** gets
  a durable and subscription; a member that is **revoked** gets its
  subscription stopped and its durable deleted (nothing should keep waiting
  for it); a member merely **removed from the group** gets its subscription
  stopped and its durable left (JetStream's inactive threshold cleans it up;
  if it is added back soon it resumes where it was).
- **Members without the folder** (or wrong mode) are skipped and named in a
  warning event + the pipeline log; the pipeline starts if at least one member
  is usable, and refuses with a clear reason if none is.
- **Late joiners start at "now":** `SubscriberOpts` gains `DeliverPolicy`
  (default unchanged, `DeliverAll`); a durable created for a member joining a
  running session uses `DeliverNew`. Otherwise a till added a day later would
  receive every catalogue message of the last 72 h (≈100 CSVs). Seeding a new
  till = redeploy the BC pipeline (a fresh poller sends all records and, with
  an empty seen-map, all pictures) — documented; a "Resend catalogue" action is
  the follow-up (open question 2).
- **Isolation:** membership is resolved with the session's tenant in the
  query; a group name is only meaningful inside a workspace, so `all-tills` in
  two workspaces are unrelated. Deliveries stay keyed by authenticated agent.
- **Events** gain `agent` (name) on `delivered`/`failed`, so the panel can say
  which till.
- `fingerprint` includes `target` + `group`.

### Sources from a group (open question 1)

A source node with `target: group`: the watch is sent to every member's poll
(`collectWork` matches group membership as well as `AgentID`), and an upload
is accepted from any current member (`watchingSession` checks membership).
Each upload becomes its own message with `agent_id` in the metadata, as now.

### Management API early check

`checkRemoteAgentNodes` for a group: at least one live member with the folder
in the right mode, else a deploy-time error naming the group and folder; the
members that lack it are listed as a warning in the same response.

### UI

- Node editor: **Send to** — *One agent* / *A group of agents*. Group select
  lists existing groups with "n agents · m online"; folder select shows the
  folder names the members report (union), marking ones not every member has.
- Settings → Remote agents: a **Groups** column, editable chips.
- Builder's Remote Agent tab: a group end shows `all-tills · 3/4 online`, and
  the offline note lists the offline tills by name.

## Files

| Area | Files |
|---|---|
| Schema | `infrastructure/migrations/000023_agent_groups.{up,down}.sql` |
| Management API | `repo_agents.go` (groups in Agent/List/Get, `SetAgentGroups`, `ListAgentGroups`), `agents_handler.go` (PATCH groups, GET groups, group-aware `checkRemoteAgentNodes`), `openapi_registry.go`, `nodeconfig.go` (agent_id xor group) |
| Protocol / agent | `pkg/agentproto/proto.go` (`RegisterRequest.Groups`), `cmd/vrsky-agent/main.go` (`--groups`), `pkg/managementapi/agent_install.ps1` (`-Groups`), `agentService.installCommand` (optional groups arg) |
| Gateway | `cmd/remote-agent/service.go` (remoteNode.Target/Group, member sessions, refresh loop, per-member durables), `output.go` (single-target handler), `work.go`/`input.go` (group watches/uploads), `events.go` (agent name), `register.go` (groups) |
| Messaging | `pkg/messaging/subscriber.go` (`DeliverPolicy` opt, `DeleteConsumer`) |
| UI | `RemoteAgentConfigEditor.tsx` + test, `AgentsPage.tsx` + test, `agentService.ts`, `remoteAgentEnds.ts`, `RemoteAgentPanel.tsx` + test |
| Docs | `docs/connectors/remote-agent.md` (Groups), `docs/operator/remote-agent.md` (`--groups`, Settings) |

## Tests

Gateway, on embedded JetStream with fake agents (the existing harness):
- `TestGroup_EachMemberGetsItsOwnCopy` — two members, both receive the same
  file; each ack settles only that member's durable.
- `TestGroup_OfflineMemberDoesNotBlockOthers` — B never polls; A receives,
  acks, and receives the next message too; B's durable holds with
  `NumRedelivered == 0`. **This is the point of the feature.**
- `TestGroup_LateJoinerStartsAtNow` — publish, add C to the group, C gets
  nothing old; publish again, C gets it.
- `TestGroup_MemberWithoutTheFolderIsSkippedAndNamed`.
- `TestGroup_RevokedMemberStopsReceiving` (durable gone); removed-from-group
  member stops receiving (durable kept).
- `TestGroup_NoUsableMemberIsRefused`.
- `TestIsolation_GroupMembershipIsPerTenant` — same group name in another
  tenant; its agent never receives, never appears in the member list.
  *Mutation: drop the tenant condition from the membership query → fails.*
- `TestGroup_UploadAcceptedFromAnyMemberOnly` (if sources are included).
- `TestParseRemoteNodes_GroupTarget`, fingerprint changes with group.

Messaging: `TestSubscribe_DeliverNewSkipsExistingMessages`.

Management API: PATCH groups (validation, isolation — tenant B cannot set A's
groups), groups listing, `checkRemoteAgentNodes` for a group with/without a
usable member.

UI: editor toggle + group select; Agents page groups editing; panel group
status. Mutations as usual, pasted into the PR.

## Rollout

Migration runs on management-api start. Then `deploy-core-azure.sh
management-api ui`; `kubectl rollout restart deploy/vrsky-remote-agent`
(it restores running pipelines itself; running single-agent pipelines keep
their durable and position). New agent binary only for `--groups`; existing
tills need no upgrade. Then on the till: put POS-PC in `all-tills`, switch the
catalogue pipeline's output to the group, redeploy, drop a file — same result
as today. Add a second machine (or a second agent on the same PC with another
folder) to see the fan-out and the offline hold.

## Risks

- **Late-join semantics** surprise: a new till gets nothing until the next
  poll. Documented, and the BC pipeline polls every 15 min anyway.
- **A till that is gone for good** keeps a durable holding messages until
  someone revokes it (retention bounds the cost). Settings shows it offline;
  revoke it.
- **Group fan-out × pictures**: 100 tills × 80 pictures = 8 000 deliveries per
  catalogue change. Each is a small pull by the till; the gateway serves them
  from the object store by reference, nothing is duplicated in storage.
- **Durable churn** from repeated add/remove is bounded by the inactive
  threshold cleanup.

## Non-goals

Per-store data differences (separate pipelines/groups), group rename, a
groups table with its own permissions, agent-side awareness of groups (an
agent never needs to know what group it is in).
