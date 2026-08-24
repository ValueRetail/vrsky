# ADR 0002 — Large payloads through pipeline transforms (filter / converter)

- **Status:** Accepted (phase A implemented in the same PR)
- **Date:** 2026-08-24
- **Relates to:** #187, [ADR 0001](0001-streaming-payload-contract.md) (phase 2),
  PRs #189–#197

## Context — and a correction to ADR 0001

ADR 0001 phase 1 gave the *edge* connectors streaming. For transforms it
claimed: *"a `PayloadRef` envelope reaching a plain filter/converter is
rehydrated under the 128 MiB cap → beyond it, permanent error → DLQ."*

**That claim assumed the transforms run on the SDK runner. They do not.**
`sdk.RunFilter` / `sdk.RunConverter` have **zero callers** — they are dead code.
The transforms that actually run are the standalone services `cmd/data-filter`
and `cmd/data-converter`, which predate the SDK runner: they hold their own
JetStream durables (`data-filter` / `data-converter`), **ack unconditionally**
("failures are deterministic; ack always"), `json.Unmarshal(env.Payload)`
directly, and publish outputs through a bare `messaging.Publisher`.

So with payload offload active (#189), today's real behavior is:

- **Input — silent data loss.** An offloaded envelope arrives with
  `Payload: nil` and `PayloadRef` set. `json.Unmarshal(nil)` fails, the service
  emits an "Invalid JSON payload" UI event, and **acks**. The message is gone:
  no retry, no DLQ, nothing downstream. Every pipeline
  `source → filter/converter → destination` **loses any payload over 256 KiB**.
- **Output — silent data loss.** A transform output larger than NATS
  `max_payload` (1 MB) fails at publish; ack-always drops it. This bites even
  *small* inputs: flatten multiplies rows, JSON→XML inflates.

These are correctness bugs at ordinary sizes (256 KiB – a few MB), independent
of any multi-GB ambition. Fixing them is phase A; streaming is phase B.

## What the transforms actually do (grounded)

| Operation | Scope |
|---|---|
| Filter rules (`equals`, `contains`, `gt`, …, `regex`) | **per record** |
| `extract_fields` (projection) | **per record** |
| Flatten (unroll `flatten_path` array into rows) | array streams; **parent-include fields need document context** |
| Converter `mappings` (rename/convert per field) | **per record** (`toRows` → row-wise) |
| Format conversion (JSON → CSV / XML / YAML / text) | **row-wise**; CSV header already comes from the **first row only** (`stableKeys`) |

Payloads are JSON: either a single object (one record) or an array of records.
There is **no** genuinely cross-record operation today — no sort, no aggregate,
no key-union CSV header. That makes record streaming a faithful reimplementation
of existing semantics, not a semantic change.

## Options

**A. Migrate the transforms onto `sdk.RunFilter`/`RunConverter`.** Cleanest
long-term — rehydrate, offload, cap, tracing all for free — but the standalone
services support multiple filter/converter entries per connection (branching),
which doesn't fit the single-`Evaluate` contract; they carry SSE event feeds and
preview endpoints; and their ack policy differs. A live-service rewrite is the
wrong vehicle for an urgent correctness fix. **Rejected for now**; recorded as
the preferred eventual direction.

**B. Extract the claim-check helpers into a shared package and wire them into
the standalone services at their boundaries.** Surgical; small diffs at the two
choke points each service already has (message entry, publish exit);
behavior-identical for small payloads. **← chosen for phase A.**

**C. Record streaming inside the transforms.** The multi-GB path. **← phase B,
built on B's plumbing.**

## Decision

### Phase A — close the correctness gap (small, do first)

1. **Extract** the claim-check machinery from `pkg/sdk/payloadstore.go` into a
   shared package (`pkg/claimcheck`): store-from-env, `OffloadIfLarge`,
   `Rehydrate` (with the cap), key/checksum helpers. The SDK delegates to it —
   no behavior change for connectors.
2. **data-filter / data-converter**: open the store from `PAYLOAD_STORE_*` at
   startup; `Rehydrate` on message entry; `OffloadIfLarge` before every publish.
3. **Error semantics change, deliberately.** Store/rehydrate errors (including
   over-cap) **return an error from the subscribe handler → NAK → retry → DLQ**.
   Transient store blips recover via redelivery; over-cap becomes an explicit
   DLQ entry instead of a vanished message. Transform-logic failures (bad JSON,
   bad mapping) keep today's ack + UI-event behavior — ack-always narrows to
   the deterministic failures it was meant for.
4. **Manifests**: add `PAYLOAD_STORE_*` to the platform filter/converter
   deployments (orchestrator-spawned workers already receive it from #189).

After phase A the pipeline is *correct* at every size: ≤256 KiB unchanged,
256 KiB–128 MiB transforms work via rehydrate, >128 MiB is an explicit DLQ
error pointing at the object.

### Phase B — record streaming (the multi-GB path)

When the input is offloaded **and** over the rehydrate cap, and the payload is a
**JSON array** (later: NDJSON), the transform streams instead of buffering:

```
GetStream(spill object)
  → json.Decoder token loop (one record at a time)
  → rules / extract / mappings per record
  → streaming encoder (JSON array | NDJSON | CSV | XML | YAML | text)
  → PutStream via io.Pipe to a NEW spill object (TeeReader → size + sha256)
  → publish the small ref envelope
```

Memory is bounded by the largest single record, not the payload. Notes:

- **CSV header from the first output row** — exactly today's `stableKeys`
  semantics, so streaming changes nothing.
- **Flatten** is B2: parent-include fields live outside the array, so the
  streamer must buffer the (small) non-array document context while streaming
  the (large) `flatten_path` array — a token-walking decoder. Ship B without it
  first; over-cap + flatten falls to the phase-C policy until B2 lands.
- **A single JSON object** over the cap has no records to stream — phase-C
  policy. This is the honest product boundary from ADR 0001: no architecture
  makes a whole-document transform of one multi-GB JSON object cheap.

### Phase C — explicit policy for the un-streamable remainder

Per-node config `"large_payloads": "error" | "passthrough"` (default `error`):

- **`error`** — over-cap, un-streamable input → NAK → DLQ with a message naming
  the node and the cap. Loud and inspectable.
- **`passthrough`** — forward the ref envelope untouched, skipping this
  transform. Explicit opt-in only: silently skipping a transform is a
  correctness decision the user must make, never a default.

## Consequences

**Positive.** The silent-loss bugs die in phase A — the most important outcome,
and it needs no streaming at all. Phase B extends the multi-GB path through
transforms for array data with bounded memory, completing
`source → filter/converter → destination` at any size for record-shaped
payloads. One shared claim-check package instead of SDK-private copies.

**Negative / accepted.** The transforms gain a payload-store dependency (nil
store degrades to phase-0 behavior, logged). Two processing paths (buffered /
streamed) in the transforms, mirroring the SDK's own two paths. Flatten and
single-object payloads stay bounded until B2 / phase C. The ack-policy change
means some messages that used to vanish now occupy DLQ space — that is the
point, but DLQ volume becomes a signal worth watching after rollout.

**Deployment note.** Phase A changes live services (filter/converter are
platform deployments, 2 replicas). Per the standing rule: validate in the TEST
(docker-compose) environment first, then roll prod images + manifests together.

## Test plan

- Unit: rehydrate-on-entry (offloaded → transformed → republished), over-cap →
  NAK not ack, output offload (large flatten result), store-error → NAK.
- Phase B: stream a synthetic multi-hundred-MB array through filter and
  converter asserting bounded memory and byte-identical output vs the buffered
  path on the same (smaller) input; CSV first-row header parity.
- E2E in TEST env: consumer → filter → converter → producer at 10 MB (phase A)
  and ≥1 GB (phase B) before any prod rollout.
