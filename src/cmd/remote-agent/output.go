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
// or the service shuts down while a message is waiting for an agent, and the
// agent has not written it within the stop's grace (stopSubscribers). The
// message is NAK'd and redelivered to the next session. That costs one
// delivery attempt per stop, so a message held across five redeploys of an
// offline agent's pipeline lands in the dead-letter queue, where it can be
// retried from the UI. Targets of the message that had already written it are
// skipped by the redelivery (#587; see writtenSet).
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

// outputScope says which of a pipeline's output nodes a durable's handler
// serves: the pipeline's own durable takes the single-agent nodes; a group
// member's durable takes the group nodes that member is in and has the folder
// for — decided per message, since membership moves.
type outputScope struct {
	member string          // "" for the pipeline's own durable
	done   <-chan struct{} // ends a wait: the session, or this member's subscription
}

// outputHandler returns a durable's handler for one pipeline.
//
// It hands the message to each target agent and then waits — for the agents'
// acknowledgements, or for the scope to end. It never returns while an agent
// is merely offline: the dispatch loop keeps the message in progress with
// heartbeats, so it stays in the stream with its delivery count unchanged
// until the agent comes back. It returns nil (ack) only once every target
// confirmed the write. When it returns an error instead (NAK), the targets
// that did write are remembered in s.written, and the redelivery offers the
// file only to the others.
func (s *gateway) outputHandler(sess *connSession, scope outputScope) messaging.Handler {
	return func(_ context.Context, m *nats.Msg) error {
		var env envelope.Envelope
		if err := json.Unmarshal(m.Data, &env); err != nil {
			s.logger.Error("Dropping unreadable message", "connection_id", sess.connID, "error", err)
			return nil
		}
		last, _ := env.Metadata["_last_processed_by"].(string)
		type target struct {
			node    remoteNode
			agentID string
		}
		var targets []target
		for _, n := range sess.outputs {
			if !eligible(n, last) {
				continue
			}
			switch {
			case scope.member == "" && n.Target != targetGroup:
				targets = append(targets, target{n, n.AgentID})
			case scope.member != "" && n.Target == targetGroup && s.memberTakes(sess, scope.member, n, agentproto.ModeWrite):
				targets = append(targets, target{n, scope.member})
			}
		}
		if len(targets) == 0 {
			return nil // a message for another step, or not for this member
		}

		body, err := s.bodyFor(&env)
		if err != nil {
			return err // retriable: e.g. the object store is briefly unreachable
		}

		metaName, _ := env.Metadata["filename"].(string)
		_, converted := env.Metadata["_converted"]

		// A target that wrote this message in an earlier delivery, which was
		// then NAK'd because another target had not, is not offered it again
		// (#587; see writtenSet). A message without stream metadata (none in
		// production) is not tracked.
		meta, _ := m.Metadata()
		tracked := meta != nil
		now := s.now()

		var waiting []*delivery
		var keys []writtenKey    // keys[i] is waiting[i]'s write
		var skipped []writtenKey // written in an earlier delivery
		for _, t := range targets {
			n := t.node
			name := agentproto.GenerateFilename(n.FilenamePattern, env.ID, env.ContentType, env.Source, metaName, converted, env.CreatedAt)
			if err := agentproto.ValidFilename(name); err != nil {
				// Permanent for this node: retrying produces the same name.
				s.logger.Error("Not delivering: unusable filename", "connection_id", sess.connID,
					"node_id", n.NodeID, "envelope_id", env.ID, "error", err)
				s.events.emit(sess.connID, event{Type: "failed", Message: err.Error(), EnvelopeID: env.ID, Agent: s.agentName(t.agentID)})
				continue
			}
			var key writtenKey
			if tracked {
				key = writtenKey{
					connID: sess.connID, stream: meta.Stream, seq: meta.Sequence.Stream, envelopeID: env.ID,
					nodeID: n.NodeID, agentID: t.agentID, directory: n.Directory, filename: name,
				}
				if s.written.has(key, now) {
					skipped = append(skipped, key)
					continue
				}
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
				agentID:    t.agentID,
				tenantID:   sess.tenantID,
				inline:     body.inline,
				payloadRef: body.ref,
				result:     make(chan error, 1),
			}
			s.enqueue(d)
			waiting = append(waiting, d)
			keys = append(keys, key)
		}

		var firstErr error
		wrote := make([]bool, len(waiting)) // the agent confirmed waiting[i]'s write
		take := func(i int, err error) {
			wrote[i] = err == nil
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
		ended, unanswered := false, false
		for i, d := range waiting {
			if !ended {
				select {
				case err := <-d.result:
					take(i, err)
					continue
				case <-scope.done:
					s.dequeue(waiting[i:]...)
					ended = true
				}
			}
			// The scope has ended. An agent may have acked between the scope
			// ending and the dequeue: it got its 204 and wrote the file.
			// resolve hands the result over under s.mu, and the dequeue took
			// s.mu, so any such ack is in d.result by now; anything not there
			// never will be. Every remaining delivery is looked at, not only
			// those before the first without an answer: a write is
			// remembered below whichever target it was.
			select {
			case err := <-d.result:
				take(i, err)
			default:
				unanswered = true
			}
		}
		if unanswered {
			firstErr = errSessionEnded
		}
		if firstErr != nil {
			// NAK'd: remember who wrote it, so the redelivery skips them.
			if tracked {
				var done []writtenKey
				for i := range waiting {
					if wrote[i] {
						done = append(done, keys[i])
					}
				}
				if len(done) > 0 {
					s.written.add(s.now(), done...)
				}
			}
			return firstErr
		}
		if len(skipped) > 0 {
			s.written.forget(skipped...)
		}
		for _, d := range waiting {
			s.events.emit(sess.connID, event{Type: "delivered", Filename: d.wire.Filename, EnvelopeID: env.ID, Agent: s.agentName(d.agentID)})
		}
		// Written in an earlier delivery, and not reported then: the message
		// was not done.
		for _, k := range skipped {
			s.events.emit(sess.connID, event{Type: "delivered", Filename: k.filename, EnvelopeID: env.ID, Agent: s.agentName(k.agentID)})
		}
		return nil
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
//
// The result is handed over under s.mu, in the same critical section that
// takes the delivery out of pending. A handler whose scope ended dequeues
// under s.mu and then looks in d.result, so an ack is either seen there or
// refused here with a 404; it can't be accepted and then lost. The send
// can't block: result has room for one, and only the resolve that took the
// delivery out of pending sends on it.
func (s *gateway) resolve(agentID, deliveryID string, err error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[agentID]
	var d *delivery
	if a != nil {
		d = a.pending[deliveryID]
		delete(a.pending, deliveryID)
	}
	if d == nil {
		return false
	}
	if s.inResolve != nil {
		s.inResolve()
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
