package messaging

import (
	"context"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"
)

// Publisher is a thin wrapper around nats.JetStreamContext.Publish. The two
// singleton streams (VRSKY_DATA and VRSKY_DLQ) are ensured exactly once per
// process; subsequent Publish calls hit JS directly with no extra round
// trips.
type Publisher struct {
	js nats.JetStreamContext

	ensureOnce sync.Once
	ensureErr  error
}

// NewPublisher binds a Publisher to an existing JetStream context.
func NewPublisher(js nats.JetStreamContext) *Publisher {
	return &Publisher{js: js}
}

// Publish writes a message to the main data stream and waits for the
// JetStream ack from the server. Returning nil means the broker has
// persisted the message; producers can drop their copy after this point.
//
// msgID (envelope ID is the natural choice) deduplicates re-publishes
// inside the stream's 5-minute window.
func (p *Publisher) Publish(ctx context.Context, tenantID, connectionID, msgID string, body []byte) error {
	if err := p.ensure(); err != nil {
		return err
	}
	subj := DataSubject(tenantID, connectionID)
	opts := []nats.PubOpt{nats.Context(ctx)}
	if msgID != "" {
		opts = append(opts, nats.MsgId(msgID))
	}
	if _, err := p.js.Publish(subj, body, opts...); err != nil {
		return fmt.Errorf("js.Publish %s: %w", subj, err)
	}
	publishCount.WithLabelValues(tenantID).Inc()
	return nil
}

// PublishToDLQ writes a payload into the DLQ stream. Used by Subscriber
// when MaxDeliver is exhausted, and by the Management API retry/discard
// handlers.
func (p *Publisher) PublishToDLQ(ctx context.Context, tenantID, connectionID, msgID string, body []byte, headers nats.Header) error {
	if err := p.ensure(); err != nil {
		return err
	}
	subj := DLQSubject(tenantID, connectionID)
	msg := &nats.Msg{Subject: subj, Data: body, Header: headers}
	if msgID != "" {
		if msg.Header == nil {
			msg.Header = nats.Header{}
		}
		msg.Header.Set(nats.MsgIdHdr, msgID)
	}
	if _, err := p.js.PublishMsg(msg); err != nil {
		return fmt.Errorf("js.PublishMsg dlq %s: %w", subj, err)
	}
	return nil
}

func (p *Publisher) ensure() error {
	p.ensureOnce.Do(func() {
		p.ensureErr = EnsureStreams(p.js)
	})
	return p.ensureErr
}
