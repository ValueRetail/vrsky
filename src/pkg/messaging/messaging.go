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
	_, err := js.StreamInfo(cfg.Name)
	if errors.Is(err, nats.ErrStreamNotFound) {
		_, err = js.AddStream(cfg)
		return err
	}
	if err != nil {
		return err
	}
	// Stream exists. Try a best-effort reconciliation; JS rejects changes
	// that would silently re-deliver, which is fine — log + carry on at
	// the caller's discretion.
	_, _ = js.UpdateStream(cfg)
	return nil
}
