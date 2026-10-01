# Spilled payloads must outlive the messages that reference them

## Open questions

None.

## Goal

A large payload (> 256 KB, carried by reference through the claim-check) stays
in the object store for as long as any message can still ask for it:

- an offline till's delivery, held in the main stream for **72 h**;
- a dead-lettered message, replayable from the DLQ for **7 days**.

Today the `spill/` lifecycle rule deletes objects after **1 day**. Original-size
BC pictures are usually over 256 KB, so a till that is off for a long weekend
gets the catalogue CSV (inline, 72 h) but every picture arrives as a
"payload missing" failure. The remote-agent docs even promise "24 h for files
over 256 KB" — this plan removes that caveat.

## Facts (checked 2026-10-01)

| Where | Value |
|---|---|
| `infrastructure/kubernetes/minio/setup-job.yaml` | `DeleteSpillPayloads` → `Days: 1` (the only place the TTL is set; dev compose sets no lifecycle at all) |
| prod `vrsky-objects` (`mc ilm rule ls` via a one-off `mc` pod) | same two rules, 1 day; `spill/` holds 0 objects right now |
| `pkg/messaging/messaging.go` | `MainRetention = 72h`, `DLQRetention = 7×24h`, `MainMaxBytes = 512 MiB` |
| `pkg/claimcheck/claimcheck.go:197`, ADR 0001 (:140, :205), `docs/connectors/remote-agent.md:91,130`, `plans/remote-agent.md:418` | all state the 1-day TTL |

The 1-day value was chosen for the 15-minute envelope TTL of the original
design; the offline-agent hold (#266) and the DLQ outgrew it.

## Approach

### TTL = 8 days

`DLQRetention` (7 d) + 1 day margin, so anything the DLQ can replay still has
its body. S3 lifecycle expiry rounds forward to midnight UTC, so `Days: 8`
never deletes earlier than 8 × 24 h after the object was written.

Cost: `spill/` only holds hops' orphans (each hop re-offloads under a fresh
id), and nothing is duplicated per till — group fan-out serves 100 tills from
one object. A BC catalogue change ≈ 80 pictures × ~1 MB × 2–3 hops ≈ 250 MB,
kept 8 days instead of 1. MinIO's PVC has room; `temp/` stays at 1 day.

### Make the number follow the code

- `pkg/messaging` gains `SpillRetention = DLQRetention + 24*time.Hour` with a
  comment saying why (the object must outlive every message that can
  reference it), next to the two constants it derives from.
- A test in `pkg/messaging` parses `infrastructure/kubernetes/minio/setup-job.yaml`
  (same pattern as `TestAgentIngressTargetsRealServices`), finds the rule with
  prefix `spill/`, and asserts `Days ≥ ceil(SpillRetention / 24h)` and
  `Days ≥ ceil(MainRetention / 24h)`. Raising a retention without touching
  the manifest fails CI; so does lowering the manifest.
- `claimcheck.go` comment and ADR 0001 updated to reference `SpillRetention`
  instead of "1-day".

### Docs

- `docs/connectors/remote-agent.md`: the two "24 h for files over 256 KB"
  caveats become "72 h, whatever the size"; one sentence on why (spilled
  payloads are kept 8 days).
- `docs/adr/0001-streaming-payload-contract.md`: the two 1-day mentions.
- `plans/remote-agent.md:418`: mark the follow-up done.

## Files

| File | Change |
|---|---|
| `infrastructure/kubernetes/minio/setup-job.yaml` | `DeleteSpillPayloads` `Days: 1` → `8`, comment rewritten |
| `src/pkg/messaging/messaging.go` | `SpillRetention` |
| `src/pkg/messaging/spill_retention_test.go` (new) | manifest ↔ constant test |
| `src/pkg/claimcheck/claimcheck.go` | comment |
| `docs/connectors/remote-agent.md`, `docs/adr/0001-streaming-payload-contract.md`, `plans/remote-agent.md` | text |

No Go behaviour change, no UI, no schema.

## Tests

- `TestSpillLifecycleOutlivesRetention` as above.
  *Mutation: set the manifest back to `Days: 1` → fails naming both
  retentions; set `DLQRetention` to 30 d without touching the manifest → fails.*
- `gofmt`, `go vet`, `golangci-lint`, `go test -race ./pkg/messaging/... ./pkg/claimcheck/...`.

## Rollout (prod)

The setup Job ran once at install; re-applying the Job is awkward (Jobs are
immutable, and it also re-creates buckets). Apply the rule directly with a
one-off `mc` pod from the ACR copy, the same way the current value was read:

```bash
kubectl -n vrsky-storage run mc-ilm --rm -i --restart=Never \
  --image=vrskyprodacr.azurecr.io/minio/mc:RELEASE.2026-09-16T00-00-00Z \
  --overrides='{"spec":{"imagePullSecrets":[{"name":"acr-pull"}],"containers":[{"name":"mc","image":"vrskyprodacr.azurecr.io/minio/mc:RELEASE.2026-09-16T00-00-00Z","command":["sh","-c","mc alias set m http://minio.vrsky-storage.svc.cluster.local:9000 \"$AK\" \"$SK\" >/dev/null && mc ilm rule edit --id DeleteSpillPayloads --expire-days 8 m/vrsky-objects && mc ilm rule ls m/vrsky-objects"],"env":[{"name":"AK","valueFrom":{"secretKeyRef":{"name":"minio-credentials","key":"accesskey"}}},{"name":"SK","valueFrom":{"secretKeyRef":{"name":"minio-credentials","key":"secretkey"}}}]}]}}'
```

Verify: the listing shows `spill/ … 8`. A fresh install gets it from the
manifest. Nothing to restart.

## Risks

- **Storage growth** if a pipeline spills continuously: 8× today's steady
  state. Bounded by `MainMaxBytes` on the message side only indirectly;
  MinIO's PVC is the real cap — check `mc du` after a week.
- The one-off `mc` pod needs the ACR pull secret in `vrsky-storage`
  (present, verified while reading the rules).

## Non-goals

Per-tenant TTLs; tying the TTL to each message's own expiry (would need a
delete-on-ack protocol across hops); changing `MainRetention` or `DLQRetention`.
