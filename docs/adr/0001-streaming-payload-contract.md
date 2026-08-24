# ADR 0001 — Streaming payload contract for unbounded (multi-GB) transfers

- **Status:** Accepted
- **Date:** 2026-08-24
- **Relates to:** #187 (large-payload handling), PR #189 (claim-check + streaming
  object I/O), PR #190 (Azure Copy streaming)

## Context

PR #189 lifted the *message-bus* ceiling: payloads over 256 KiB are offloaded to
object storage (claim-check) and only a `PayloadRef` rides NATS, so message size
no longer bounds payload size. But the connector contract is still `[]byte`:

```go
type Consumer interface  { Run(ctx, publish PublishFunc) error }            // publishes *envelope.Envelope
type Producer interface  { Deliver(ctx, env *envelope.Envelope) error }     // env.Payload []byte
type Filter interface    { Evaluate(ctx, env) (keep bool, out *Envelope, error) }
type Converter interface { Convert(ctx, env) (*Envelope, error) }
```

The SDK `rehydrate` does `io.ReadAll` before every deliver, and consumers build
`env.Payload` in memory before every publish. So the whole payload is
materialized **twice per hop** (connector buffer + envelope), plus parsing
overhead. Per-connection workers run with a **512 MiB memory limit**
(`orchestrator.MemoryLimit`), which puts the practical payload ceiling around
100–200 MB — far short of the multi-GB requirement, and a payload past that
doesn't fail cleanly: the worker OOM-kills mid-message and the message retries
into the same wall.

The principle from #187 stands: **pass by reference, move by streaming, never
hold the whole payload in bus or memory** — and never build our own
chunk/split-and-reassemble protocol (provider-native multipart inside
`PutStream` is fine; it's the storage layer's implementation detail).

## Decision drivers

1. Multi-GB files must flow source → destination with bounded worker memory.
2. ~20 existing connectors must keep working unchanged — most (retail/ERP APIs:
   Sitoo, Brightpearl, Business Central, Visma, SAP, Salesforce) deal in
   paginated JSON that is *correctly* small; forcing streams on them is pure
   churn.
3. Filters/converters are in-memory JSON transforms and **cannot** hold
   multi-GB payloads; whatever we do must give them a *predictable* behavior,
   not a silent OOM.
4. Today an oversized payload fails by OOM-kill; any outcome must be an
   explicit, observable error instead.

## Options considered

### A. Big-bang: change all four interfaces to `io.Reader`

Every connector rewritten; transforms gain a streaming signature they mostly
can't honor (a JSON-tree converter fundamentally needs the document). Massive
churn for a capability only edge connectors (file, SFTP, cloud-storage, HTTP)
can use. **Rejected.**

### B. Opt-in streaming capability, detected by type assertion  ← chosen

Keep the existing interfaces untouched. Add *optional* companion interfaces; the
SDK type-asserts at the chokepoints it already owns (the publish closure and
`subscribeDispatch`) and switches to the streaming path only when both the
connector opts in **and** a payload store is configured. Everything else runs
the existing inline/rehydrate path.

### C. No rehydrate: hand connectors the `PayloadRef` and let them fetch

Every connector grows objectstore plumbing, credentials leak into connector
code, and the SDK loses the single place where offload policy (thresholds,
checksums, cleanup) lives. **Rejected.**

## Decision

Option B, in three phases. Phase 1 is the substance; 2 and 3 are follow-ups
that become mechanical once 1 exists.

### Phase 1 — streaming edges (consumer + producer)

**Consumer side** — a streaming-capable consumer implements:

```go
// PublishStreamFunc streams a large payload into the pipeline. The SDK uploads
// body to the spill store (provider-native multipart; TeeReader-sha256 for the
// checksum), stamps PayloadRef/PayloadSize/Checksum, and publishes the small
// envelope. The payload never exists in worker memory beyond the copy buffer.
type PublishStreamFunc func(ctx context.Context, env *envelope.Envelope, body io.Reader) error

type StreamingConsumer interface {
    Consumer
    RunStream(ctx context.Context, publish PublishFunc, publishStream PublishStreamFunc) error
}
```

`RunConsumer` type-asserts: a `StreamingConsumer` gets `RunStream` (with the
plain `publish` still available for its small payloads); everyone else gets
`Run` exactly as today. `publishStream` on a worker with no payload store
configured returns a permanent error at first use — streaming is meaningless
inline, and this surfaces the misconfiguration instead of buffering.

**Producer side:**

```go
type StreamingProducer interface {
    Producer
    // DeliverStream is called instead of Deliver when the payload is offloaded.
    // body streams from the spill store; env.Payload is nil.
    DeliverStream(ctx context.Context, env *envelope.Envelope, body io.Reader) error
}
```

In `subscribeDispatch`: if `env.PayloadRef != ""` and the node implements
`StreamingProducer` → `GetStream` and hand over the reader (checksum-verifying
wrapper); no rehydrate. Otherwise rehydrate as today.

**Per-message escape hatch (`ErrStreamUnsupported`) — added during
implementation.** Building the first adopter surfaced a gap: the cloud-storage
producer can fan out to several targets, and a stream can only be read once.
Buffering to serve N targets would reintroduce the OOM this ADR exists to
prevent, while failing outright would regress configurations that work today
(multi-target + a 1–128 MB payload). So `DeliverStream` may return
`ErrStreamUnsupported` for a message it cannot stream; the SDK then falls back to
rehydrate + `Deliver`, giving behaviour identical to a non-streaming connector.
Returning it is free — nothing has been read yet. Capability is therefore
declared statically (type assertion) but *applied* per message.

**Rehydrate guard (independent hardening, do first — shipped):** cap `rehydrate`
at `PAYLOAD_REHYDRATE_MAX_BYTES` (default 128 MiB — comfortable inside the
512 MiB limit). The check runs on `PayloadSize` *before* any download, with an
`io.LimitReader` bound on the read itself in case the declared size understates
the object. This replaces today's OOM-kill loop with an explicit error, and is
worth shipping before any connector opts in.

*Error classification:* the over-cap error is returned bare, so it rides the
SDK's **default Retriable** classification, and the envelope reaches the **DLQ**
after the retry budget. This is deliberate and contradicts an earlier draft of
this ADR that said "permanent": `sdk.Permanent` **acks and drops** the message
(see `pkg/sdk/errors.go`), and silently discarding a customer payload is
unacceptable for an integration platform. Retries are cheap here because the
rejection happens without downloading, and a DLQ'd envelope keeps its
`PayloadRef` so an operator can inspect it and replay once a streaming-capable
connector is in place (within the spill object's 1-day TTL).

**Integrity (shipped with the guard):** `Checksum string` (`sha256:<hex>`) on the
envelope, computed during offload and verified on rehydrate. Empty checksums are
skipped so envelopes in flight across a rollout still deliver. When streaming
lands, `DeliverStream` verifies via a wrapping reader that errors on Close if the
digest mismatches, and offload computes the digest with a `TeeReader` rather than
over a buffer.

**First adopters** (they already hold an `io.Reader` from disk or network, so
this is deleting buffering, not adding machinery): cloud-storage consumer +
producer, file consumer + producer, SFTP consumer, http-producer. Retail/ERP
API connectors don't change.

### Phase 2 — record-streaming transforms

Filters/converters get a predictable policy for offloaded payloads **now**, and
a streaming capability **later**:

- **Default (phase 1):** a `PayloadRef` envelope reaching a plain
  filter/converter is rehydrated under the same 128 MiB cap → beyond it,
  permanent error → DLQ ("transform does not support payloads this large").
  Predictable, observable, no silent skip.
- **Explicit bypass:** per-node config `"large_payloads": "passthrough"`
  forwards over-cap envelopes untouched (ref and all) for pipelines where the
  transform is only meant for the small records. Opt-in only — silently
  skipping a transform is a correctness decision the user must make.
- **Phase 2 proper:** `StreamingTransformer` for *record-oriented* formats
  (NDJSON, CSV): SDK streams spill-object → record iterator → per-record
  Evaluate/Convert → streamed new spill object, memory bounded by record size.
  Only per-record transforms qualify; anything cross-record (sort, aggregate,
  whole-document JSONPath) stays small-payload-only by nature — that's a
  documented product boundary, not a TODO.

### Phase 3 — presigned URLs (zero-copy edges)

`PresignGet`/`PresignPut` on `ObjectStore` (S3 presign / Azure SAS / GCS signed
URLs), so an external system uploads or downloads **directly** against the
store and even the edge worker never carries the bytes. Valuable but orthogonal
— it changes who moves the bytes, not the contract — hence last.

## Consequences

**Positive**
- Multi-GB transfers with worker memory bounded by the multipart buffer
  (~5–16 MiB), on 512 MiB pods, no interface breakage, no connector churn
  outside the edge connectors that benefit.
- Oversized payloads become explicit DLQ errors instead of OOM-kill loops —
  strictly better operability even for pipelines that never opt in.
- Checksums close the integrity gap flagged in #187.

**Negative / accepted**
- Two delivery paths (inline vs streaming) live in the SDK forever; the
  chokepoints are already centralized, which contains the cost.
- Transforms on huge payloads are constrained by design (per-record streaming
  or explicit passthrough). This is honest: no architecture makes a
  whole-document JSON transform of a 5 GB payload cheap.
- The 1-day `spill/` TTL bounds end-to-end pipeline latency for offloaded
  payloads; fine against the 15-minute envelope TTL, but a future "park large
  files for a day" feature would need its own prefix and rule.

## Test plan

- Unit: type-assertion routing (streaming vs plain, both sides), rehydrate cap
  → permanent error, checksum mismatch → error, passthrough config.
- Integration (`-tags=integration`, MinIO): consumer→spill→producer round-trip
  of a synthetic ~100 MB payload asserting the envelope on the bus stays under
  1 KB; multi-GB verified manually against the TEST env (per the standing rule:
  validate in TEST before prod).
