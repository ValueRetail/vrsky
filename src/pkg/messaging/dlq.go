package messaging

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// DLQEntry is the projection the Management API surfaces to the UI.
type DLQEntry struct {
	Sequence     uint64            `json:"sequence"` // JetStream sequence in the DLQ stream
	Subject      string            `json:"subject"`  // vrsky.dlq.<tenant>.pipeline.<conn>
	TenantID     string            `json:"tenant_id"`
	ConnectionID string            `json:"connection_id"`
	Worker       string            `json:"worker"` // service name that gave up on it
	LastError    string            `json:"last_error"`
	Delivered    int               `json:"delivered"` // delivery attempts before DLQ
	ReceivedAt   time.Time         `json:"received_at"`
	PayloadSize  int               `json:"payload_size"`
	PayloadJSON  any               `json:"payload,omitempty"` // parsed envelope, optional
	Headers      map[string]string `json:"headers,omitempty"`
}

// ListDLQ returns a paginated view of DLQ messages for one pipeline.
//
// JetStream doesn't expose a "browse messages" API per se; the trick is to
// open a short-lived ephemeral consumer with DeliverAll + filter on the
// pipeline's DLQ subject, fetch up to `limit` messages, peek at them without
// acking, then drain the consumer.
func ListDLQ(js nats.JetStreamContext, tenantID, connectionID string, limit, offset int) ([]*DLQEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	subj := DLQSubject(tenantID, connectionID)
	// OptStartSeq is 1-based; using DeliverAll + skipping `offset` items is
	// simpler and correct for the small page sizes we ship in the UI.
	sub, err := js.PullSubscribe(subj, "",
		nats.BindStream(DLQStreamName),
		nats.DeliverAll(),
		nats.AckExplicit(),
		nats.InactiveThreshold(30*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("dlq subscribe: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// Pull messages, peeking only — every message is immediately NAK'd so
	// it stays in the DLQ. Because NAK'd messages are eligible for
	// immediate redelivery, a subsequent Fetch in the same loop will
	// return the SAME messages again; we therefore track seen sequences
	// to dedup. Stops when one Fetch yields zero NEW messages or when
	// the page is full.
	out := make([]*DLQEntry, 0, limit)
	seen := make(map[uint64]struct{})
	fetched := 0
	for len(out) < limit {
		batch, err := sub.Fetch(limit, nats.MaxWait(500*time.Millisecond))
		if err != nil {
			break // ErrTimeout = stream drained for our purposes
		}
		newInBatch := 0
		for _, m := range batch {
			meta, _ := m.Metadata()
			var seq uint64
			if meta != nil {
				seq = meta.Sequence.Stream
			}
			if _, dup := seen[seq]; dup {
				_ = m.Nak()
				continue
			}
			seen[seq] = struct{}{}
			newInBatch++
			if fetched < offset {
				_ = m.Nak()
				fetched++
				continue
			}
			out = append(out, toDLQEntry(m))
			_ = m.Nak()
			fetched++
			if len(out) >= limit {
				return out, nil
			}
		}
		if newInBatch == 0 {
			// Same set as last loop — we've seen everything available.
			break
		}
	}
	return out, nil
}

func toDLQEntry(m *nats.Msg) *DLQEntry {
	tenantID, connID := parseSubject(strings.Replace(m.Subject, "vrsky.dlq.", "vrsky.data.", 1))
	headers := map[string]string{}
	for k, v := range m.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	meta, _ := m.Metadata()
	entry := &DLQEntry{
		Subject:      m.Subject,
		TenantID:     tenantID,
		ConnectionID: connID,
		Worker:       m.Header.Get("X-Vrsky-Worker"),
		LastError:    m.Header.Get("X-Vrsky-Last-Error"),
		PayloadSize:  len(m.Data),
		Headers:      headers,
	}
	if meta != nil {
		entry.Sequence = meta.Sequence.Stream
		entry.ReceivedAt = meta.Timestamp
	}
	if d := m.Header.Get("X-Vrsky-Delivered"); d != "" {
		_, _ = fmt.Sscanf(d, "%d", &entry.Delivered)
	}
	return entry
}

// GetDLQRaw fetches a single DLQ message by sequence so the user can see
// the full payload.
func GetDLQRaw(js nats.JetStreamContext, tenantID, connectionID string, seq uint64) (*DLQEntry, []byte, error) {
	subj := DLQSubject(tenantID, connectionID)
	sub, err := js.PullSubscribe(subj, "",
		nats.BindStream(DLQStreamName),
		nats.StartSequence(seq),
		nats.AckExplicit(),
		nats.InactiveThreshold(30*time.Second),
	)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = sub.Unsubscribe() }()

	batch, err := sub.Fetch(1, nats.MaxWait(2*time.Second))
	if err != nil || len(batch) == 0 {
		return nil, nil, fmt.Errorf("dlq message %d not found", seq)
	}
	m := batch[0]
	defer func() { _ = m.Nak() }()
	if m.Subject != subj {
		return nil, nil, fmt.Errorf("sequence %d does not belong to %s", seq, subj)
	}
	return toDLQEntry(m), append([]byte(nil), m.Data...), nil
}

// RetryDLQ moves a DLQ message back to the main data stream (so workers
// pick it up again) and removes it from the DLQ.
func RetryDLQ(js nats.JetStreamContext, tenantID, connectionID string, seq uint64) error {
	entry, payload, err := GetDLQRaw(js, tenantID, connectionID, seq)
	if err != nil {
		return err
	}
	if entry == nil {
		return fmt.Errorf("dlq message %d not found", seq)
	}
	pub := NewPublisher(js)
	if err := pub.Publish(context.Background(), tenantID, connectionID,
		fmt.Sprintf("retry-%d", seq), payload); err != nil {
		return fmt.Errorf("re-publish: %w", err)
	}
	if err := js.DeleteMsg(DLQStreamName, seq); err != nil {
		return fmt.Errorf("delete from dlq: %w", err)
	}
	return nil
}

// DiscardDLQ removes a DLQ message without retrying.
func DiscardDLQ(js nats.JetStreamContext, seq uint64) error {
	return js.DeleteMsg(DLQStreamName, seq)
}
