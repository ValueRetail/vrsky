// Package messaging wraps the NATS JetStream client with the semantics
// VRSky needs for at-least-once data-flow delivery (issue #70 / Phase 1E).
//
// Architecture (one stream, many durable consumers):
//
//   - A single stream VRSKY_DATA captures every data-flow message under the
//     subject pattern "vrsky.data.>".
//   - A single stream VRSKY_DLQ captures dead-lettered messages under
//     "vrsky.dlq.>".
//   - Each worker (data-filter, data-converter, http-producer, db-producer,
//     file-producer, tenant-consumer/bridge) creates ONE durable consumer on
//     VRSKY_DATA. Workers see every message but only act on those whose
//     pipeline config names their node type. Each consumer acks independently,
//     so a failure in one worker does not block delivery to another.
//
// Subject patterns:
//
//	vrsky.data.<tenant>.pipeline.<connectionID>   - main data flow
//	vrsky.dlq.<tenant>.pipeline.<connectionID>    - dead-letter
//
// Control-plane subjects (vrsky.commands.*) intentionally stay on core NATS;
// command loss is acceptable because deploy is idempotent.
package messaging

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	// MaxDeliveryAttempts is the default cap on redelivery before a message
	// moves to the DLQ. Set via the issue: "5 consecutive failures → DLQ".
	MaxDeliveryAttempts = 5

	// DLQRetention is how long DLQ messages remain available for inspection.
	DLQRetention = 7 * 24 * time.Hour

	// MainRetention is the upper bound on age for messages in the main
	// stream. WorkQueue retention removes acked messages immediately; this
	// is the backstop in case a consumer is offline for a long time.
	MainRetention = 72 * time.Hour

	// SpillRetention is how long an offloaded payload (claim-check, > 256 KiB)
	// must stay in the object store: as long as any message that references
	// it can still be delivered or replayed. A dead-lettered message can be
	// replayed for DLQRetention, and a delivery for an offline remote agent
	// waits up to MainRetention, so the object outlives both with a day's
	// margin. The bucket lifecycle rule for spill/ in
	// infrastructure/kubernetes/minio/setup-job.yaml is held to this value by
	// TestSpillLifecycleOutlivesRetention.
	SpillRetention = DLQRetention + 24*time.Hour

	// MainMaxBytes / MainMaxMsgs bound the *size* of the main stream. The data
	// stream is an ephemeral transport (docs/NATS_ARCHITECTURE.md) — messages
	// should be consumed in seconds. Age alone (72h) doesn't stop a runaway
	// producer or a stuck/absent consumer from piling up gigabytes within the
	// window and OOM-killing NATS. With DiscardOld these caps shed the oldest
	// (already-stale) messages instead, keeping the broker alive — the right
	// trade-off for a transit stream. ~512 MiB sits well under the ~830 MiB
	// that OOM-killed the dev broker.
	MainMaxBytes = 512 * 1024 * 1024 // 512 MiB
	MainMaxMsgs  = 1_000_000

	// MainStreamName is the singleton data-flow stream.
	MainStreamName = "VRSKY_DATA"

	// DLQStreamName is the singleton dead-letter stream.
	DLQStreamName = "VRSKY_DLQ"

	// MainSubjectAll is the wildcard pattern the main stream binds to.
	MainSubjectAll = "vrsky.data.>"

	// DLQSubjectAll is the wildcard pattern the DLQ stream binds to.
	DLQSubjectAll = "vrsky.dlq.>"
)

// DataSubject returns the NATS subject a producer publishes to.
func DataSubject(tenantID, connectionID string) string {
	return fmt.Sprintf("vrsky.data.%s.pipeline.%s", tenantID, connectionID)
}

// DLQSubject returns the DLQ subject for a pipeline.
func DLQSubject(tenantID, connectionID string) string {
	return fmt.Sprintf("vrsky.dlq.%s.pipeline.%s", tenantID, connectionID)
}

// EnsureStreams creates the singleton main and DLQ streams if they don't
// already exist. Safe to call from many services at boot time.
//
// The main stream uses WorkQueue retention: acked messages are deleted
// straight away. **Important consequence**: every durable consumer must
// independently ack a message before JetStream considers it processed.
// We deliberately want this — each worker is its own consumer and the
// stream stays small.
//
// Wait — WorkQueue retention requires that **all** consumers are bound to
// the SAME subject so that a single ack deletes the message. With multiple
// independent workers we instead use Limits policy with a TTL, so each
// consumer maintains its own ack state.
func EnsureStreams(js nats.JetStreamContext) error {
	if err := ensure(js, &nats.StreamConfig{
		Name:      MainStreamName,
		Subjects:  []string{MainSubjectAll},
		Retention: nats.LimitsPolicy,
		Storage:   nats.FileStorage,
		MaxAge:    MainRetention,
		// Size caps + DiscardOld: shed the oldest stale messages rather than
		// grow unbounded and OOM NATS (see MainMaxBytes).
		MaxBytes:   MainMaxBytes,
		MaxMsgs:    MainMaxMsgs,
		Discard:    nats.DiscardOld,
		Duplicates: 5 * time.Minute,
	}); err != nil {
		return fmt.Errorf("ensure %s: %w", MainStreamName, err)
	}
	if err := ensure(js, &nats.StreamConfig{
		Name:      DLQStreamName,
		Subjects:  []string{DLQSubjectAll},
		Retention: nats.LimitsPolicy,
		Storage:   nats.FileStorage,
		MaxAge:    DLQRetention,
	}); err != nil {
		return fmt.Errorf("ensure %s: %w", DLQStreamName, err)
	}
	return nil
}

func ensure(js nats.JetStreamContext, cfg *nats.StreamConfig) error {
	info, err := js.StreamInfo(cfg.Name)
	if errors.Is(err, nats.ErrStreamNotFound) {
		_, err = js.AddStream(cfg)
		return err
	}
	if err != nil {
		return err
	}
	// Every connector and core service calls this at start. Updating a stream
	// that already matches is noise, and a fleet restart's dozens of
	// concurrent updates were what triggered the nats-server race fixed in
	// #339 (plans/nats-server-race.md). So update only on drift.
	if len(streamDrift(info.Config, *cfg)) == 0 {
		return nil
	}
	// Stream exists and differs. Try a best-effort reconciliation; JS rejects
	// changes that would silently re-deliver, which is fine — log + carry on
	// at the caller's discretion.
	_, _ = js.UpdateStream(cfg)
	return nil
}

// streamDrift names the fields on which an existing stream differs from what
// EnsureStreams wants. It compares exactly the fields EnsureStreams sets; a
// field newly set there must be added here, or a change to it is never
// applied to an existing stream.
//
// An unset field on the wanted side is compared against the value
// nats-server fills in for it (checkStreamCfg in server/stream.go). Where
// that guess could be wrong, say an account limit lowering the duplicates
// window, the field reports as drifted and the stream is updated as it was
// before this check existed: a wrong guess costs an update, never a skipped
// one.
func streamDrift(have, want nats.StreamConfig) []string {
	var out []string
	if !slices.Equal(have.Subjects, want.Subjects) {
		out = append(out, "subjects")
	}
	if have.Retention != want.Retention {
		out = append(out, "retention")
	}
	if have.Storage != want.Storage {
		out = append(out, "storage")
	}
	if have.MaxAge != want.MaxAge {
		out = append(out, "max_age")
	}
	if have.MaxBytes != unlimitedIfUnset(want.MaxBytes) {
		out = append(out, "max_bytes")
	}
	if have.MaxMsgs != unlimitedIfUnset(want.MaxMsgs) {
		out = append(out, "max_msgs")
	}
	if have.Discard != want.Discard {
		out = append(out, "discard")
	}
	if have.Duplicates != duplicatesWindow(want) {
		out = append(out, "duplicates")
	}
	return out
}

// unlimitedIfUnset mirrors the server: 0 or anything below -1 means -1,
// unlimited.
func unlimitedIfUnset(v int64) int64 {
	if v == 0 || v < -1 {
		return -1
	}
	return v
}

// duplicatesWindow mirrors the server's default for an unset window: two
// minutes, or the max age when that is set and shorter.
func duplicatesWindow(cfg nats.StreamConfig) time.Duration {
	if cfg.Duplicates != 0 {
		return cfg.Duplicates
	}
	const serverDefault = 2 * time.Minute
	if cfg.MaxAge != 0 && cfg.MaxAge < serverDefault {
		return cfg.MaxAge
	}
	return serverDefault
}
