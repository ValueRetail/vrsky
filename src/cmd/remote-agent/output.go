package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
	"github.com/ValueRetail/vrsky/pkg/claimcheck"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// errSessionEnded is returned from the output handler when its pipeline stops
// or the service shuts down while a message is waiting for an agent. The
// message is NAK'd and redelivered to the next session. That costs one
// delivery attempt per stop, so a message held across five redeploys of an
// offline agent's pipeline lands in the dead-letter queue, where it can be
// retried from the UI.
var errSessionEnded = errors.New("pipeline stopped while the message was waiting for its agent")

// delivery is one file waiting to be written by one agent.
type delivery struct {
	wire     agentproto.Delivery
	agentID  string
	tenantID string

	inline     []byte // the body when it travelled inline
	payloadRef string // the object-store key when it was offloaded

	leasedAt time.Time  // zero until handed to the agent in a poll
	result   chan error // capacity 1; the agent's ack
}

// outputHandler returns the durable's handler for one pipeline.
//
// It hands the message to each target agent and then waits — for the agents'
// acknowledgements, or for the session to end. It never returns while an agent
// is merely offline: the dispatch loop keeps the message in progress with
// heartbeats, so it stays in the stream with its delivery count unchanged
// until the agent comes back. It returns nil (ack) only once every target
// confirmed the write.
func (s *gateway) outputHandler(sess *connSession) messaging.Handler {
	return func(_ context.Context, m *nats.Msg) error {
		var env envelope.Envelope
		if err := json.Unmarshal(m.Data, &env); err != nil {
			s.logger.Error("Dropping unreadable message", "connection_id", sess.connID, "error", err)
			return nil
		}
		last, _ := env.Metadata["_last_processed_by"].(string)
		var targets []remoteNode
		for _, n := range sess.outputs {
			if eligible(n, last) {
				targets = append(targets, n)
			}
		}
		if len(targets) == 0 {
			return nil // a message for another step of this pipeline
		}

		body, err := s.bodyFor(&env)
		if err != nil {
			return err // retriable: e.g. the object store is briefly unreachable
		}

		metaName, _ := env.Metadata["filename"].(string)
		_, converted := env.Metadata["_converted"]

		var waiting []*delivery
		for _, n := range targets {
			name := agentproto.GenerateFilename(n.FilenamePattern, env.ID, env.ContentType, env.Source, metaName, converted, env.CreatedAt)
			if err := agentproto.ValidFilename(name); err != nil {
				// Permanent for this node: retrying produces the same name.
				s.logger.Error("Not delivering: unusable filename", "connection_id", sess.connID,
					"node_id", n.NodeID, "envelope_id", env.ID, "error", err)
				s.events.emit(sess.connID, event{Type: "failed", Message: err.Error(), EnvelopeID: env.ID})
				continue
			}
			d := &delivery{
				wire: agentproto.Delivery{
					Op:           agentproto.OpWriteFile,
					ID:           uuid.NewString(),
					ConnectionID: sess.connID,
					Directory:    n.Directory,
					Filename:     name,
					ContentType:  env.ContentType,
					Size:         body.size,
					Checksum:     body.checksum,
				},
				agentID:    n.AgentID,
				tenantID:   sess.tenantID,
				inline:     body.inline,
				payloadRef: body.ref,
				result:     make(chan error, 1),
			}
			s.enqueue(d)
			waiting = append(waiting, d)
		}

		var firstErr error
		for i, d := range waiting {
			select {
			case err := <-d.result:
				if err != nil && firstErr == nil {
					firstErr = err
				}
			case <-sess.ctx.Done():
				s.dequeue(waiting[i:]...)
				return errSessionEnded
			}
		}
		if firstErr == nil {
			for _, d := range waiting {
				s.events.emit(sess.connID, event{Type: "delivered", Filename: d.wire.Filename, EnvelopeID: env.ID})
			}
		}
		return firstErr
	}
}

// eligible reports whether an output node takes a message at this step: from
// the input directly, from its predecessor, or — with no incoming edge — any.
// The same rule as file-producer's eligibleConfigs.
func eligible(n remoteNode, lastProcessedBy string) bool {
	if n.PredIsConsumer {
		return lastProcessedBy == ""
	}
	if n.PredecessorID != "" {
		return lastProcessedBy == n.PredecessorID
	}
	return true
}

type outputBody struct {
	inline   []byte
	ref      string
	size     int64
	checksum string
}

func (s *gateway) bodyFor(env *envelope.Envelope) (outputBody, error) {
	if env.PayloadRef == "" {
		return outputBody{inline: env.Payload, size: int64(len(env.Payload)), checksum: claimcheck.Checksum(env.Payload)}, nil
	}
	if s.store == nil {
		return outputBody{}, fmt.Errorf("envelope %s is offloaded but no payload store is configured", env.ID)
	}
	return outputBody{ref: env.PayloadRef, size: env.PayloadSize, checksum: env.Checksum}, nil
}

// enqueue makes a delivery visible to its agent's next poll.
func (s *gateway) enqueue(d *delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agentLocked(d.agentID)
	a.pending[d.wire.ID] = d
	a.wake()
}

// dequeue withdraws deliveries that nobody is waiting for any more.
func (s *gateway) dequeue(ds ...*delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range ds {
		if a := s.agents[d.agentID]; a != nil {
			delete(a.pending, d.wire.ID)
		}
	}
}

// resolve records an agent's ack for one of ITS deliveries. The lookup starts
// from the authenticated agent, so an ID belonging to any other agent is simply
// not found. Returns false when there is no such pending delivery.
func (s *gateway) resolve(agentID, deliveryID string, err error) bool {
	s.mu.Lock()
	a := s.agents[agentID]
	var d *delivery
	if a != nil {
		d = a.pending[deliveryID]
		delete(a.pending, deliveryID)
	}
	s.mu.Unlock()
	if d == nil {
		return false
	}
	d.result <- err
	return true
}

// pendingFor returns one of an agent's own deliveries, or nil.
func (s *gateway) pendingFor(agentID, deliveryID string) *delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.agents[agentID]; a != nil {
		return a.pending[deliveryID]
	}
	return nil
}
