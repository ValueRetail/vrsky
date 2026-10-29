package main

import (
	"context"
	"encoding/base64"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
)

// A message for several agents is acked only once every one of them has
// written it. When the session ends with some of them done and others not, or
// one of them fails, the message is NAK'd and comes back. Before #587 the
// redelivery went to every target again, so an agent that had already written
// the file wrote it a second time: the same name, replaced in place, or put
// back after whatever reads the folder had taken it away. Now the gateway
// remembers which agents wrote which message, and the redelivery goes only to
// the ones that did not.

const outWrite2 = `[{"name":"out","mode":"write"},{"name":"out2","mode":"write"}]`

// startTwoAgentPipeline starts a pipeline whose input goes to two single-agent
// outputs, in the order given: both on the pipeline's own durable, one handler
// waiting for both agents.
func (e *testEnv) startTwoAgentPipeline(t *testing.T, connID string, first, second string) {
	t.Helper()
	nodes := []any{outputNode("out-"+first, first, "out", ""), outputNode("out-"+second, second, "out", "")}
	e.expectStart(connID, tenant1, nodes, []any{}, map[string]string{agentA: outWrite, agentA2: outWrite}, "running")
	e.g.startConnection(context.Background(), connID, tenant1)
	if e.g.sessions[connID] == nil {
		t.Fatalf("pipeline %s did not start", connID)
	}
}

// startTwoFolderGroupPipeline starts a pipeline with two group outputs, to the
// folders "out" and "out2", and agent A as the group's only member: A's
// durable handles both nodes, one handler waiting for both of A's writes.
func (e *testEnv) startTwoFolderGroupPipeline(t *testing.T, connID string) {
	t.Helper()
	nodes := []any{groupOutputNode("g-out", "all-tills", "out", ""), groupOutputNode("g-out2", "all-tills", "out2", "")}
	e.expectStart(connID, tenant1, nodes, []any{}, nil, "running")
	e.expectMembers(tenant1, 2, memberRow(agentA, "till-a", "{all-tills}", outWrite2))
	e.g.startConnection(context.Background(), connID, tenant1)
	if e.g.sessions[connID] == nil {
		t.Fatalf("group pipeline %s did not start", connID)
	}
}

// ackAs answers one delivery as an agent and checks the gateway took it.
func (e *testEnv) ackAs(t *testing.T, cred, deliveryID, status string) {
	t.Helper()
	req := agentproto.AckRequest{Status: status}
	if status == agentproto.AckFailed {
		req.Error = "disk full"
	}
	resp := e.do(t, http.MethodPost, "/agent/v1/deliveries/"+deliveryID+"/ack", cred, mustJSON(req))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ack %s as %s: %d", status, cred, resp.StatusCode)
	}
}

// pollOne waits for exactly one delivery for an agent and returns it.
func (e *testEnv) pollOne(t *testing.T, cred string) agentproto.Delivery {
	t.Helper()
	var got []agentproto.Delivery
	waitFor(t, "a delivery for "+cred, func() bool {
		got = e.poll(t, cred, 1).Deliveries
		return len(got) > 0
	})
	if len(got) != 1 {
		t.Fatalf("%s: %d deliveries, want 1: %+v", cred, len(got), got)
	}
	return got[0]
}

// TestOutput_MultiTargetRedeliverySkipsAgentsThatWrote: one agent (or one of
// a member's folders) writes the file, the other does not, and the message
// comes back. The redelivery is offered only to the target that did not write
// it; the one that did is not offered the file again, and the message is acked
// once the remaining target has written it.
func TestOutput_MultiTargetRedeliverySkipsAgentsThatWrote(t *testing.T) {
	cases := []struct {
		name string
		// start starts the pipeline; durable is the durable that holds the
		// message.
		start   func(t *testing.T, e *testEnv, connID string)
		durable func(connID string) string
		// first returns the deliveries of the first attempt: the one that
		// is written, and the one that is not. Each with its agent's cred.
		first func(t *testing.T, e *testEnv) (wrote, other agentproto.Delivery, wroteCred, otherCred string)
		// stop ends the first attempt without the other target's write:
		// true stops the pipeline (and starts it again); false has the other
		// agent report a failed write.
		stop bool
	}{
		{
			name:    "the agent that wrote comes first; the session ends",
			start:   func(t *testing.T, e *testEnv, c string) { e.startTwoAgentPipeline(t, c, agentA, agentA2) },
			durable: outputDurable,
			first:   twoAgents,
			stop:    true,
		},
		{
			// The handler waits for the targets in node order. Here it is
			// still waiting for A2 when the session ends, and has not yet
			// looked at A's answer: it must take it anyway.
			name:    "the agent that wrote comes second; the session ends",
			start:   func(t *testing.T, e *testEnv, c string) { e.startTwoAgentPipeline(t, c, agentA2, agentA) },
			durable: outputDurable,
			first:   twoAgents,
			stop:    true,
		},
		{
			name:    "the other agent's write fails",
			start:   func(t *testing.T, e *testEnv, c string) { e.startTwoAgentPipeline(t, c, agentA, agentA2) },
			durable: outputDurable,
			first:   twoAgents,
			stop:    false,
		},
		{
			name:    "one member, two group folders; the session ends",
			start:   func(t *testing.T, e *testEnv, c string) { e.startTwoFolderGroupPipeline(t, c) },
			durable: func(c string) string { return memberDurable(c, agentA) },
			first:   twoFolders,
			stop:    true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.g.stopGrace = 100 * time.Millisecond
			const connID = "multi"
			c.start(t, e, connID)
			acks := e.watchAcks(t, c.durable(connID))
			events, unsub := e.g.events.subscribe(connID)
			defer unsub()
			e.publishTo(t, tenant1, connID, `{"order":1}`)

			wrote, other, wroteCred, otherCred := c.first(t, e)
			e.ackAs(t, wroteCred, wrote.ID, agentproto.AckOK)
			if c.stop {
				e.g.stopConnection(context.Background(), connID, tenant1)
				acks.waitUntil(t, "the NAK", func(evs []ackEvent) bool { return hasAck(evs, "-NAK", 1) })
				c.start(t, e, connID)
			} else {
				e.ackAs(t, otherCred, other.ID, agentproto.AckFailed)
				acks.waitUntil(t, "the NAK", func(evs []ackEvent) bool { return hasAck(evs, "-NAK", 1) })
			}

			// The redelivery: the target that did not write gets it again.
			again := e.pollOne(t, otherCred)
			if again.Directory != other.Directory || again.Filename != other.Filename {
				t.Fatalf("redelivered %s/%s, want %s/%s", again.Directory, again.Filename, other.Directory, other.Filename)
			}
			if body, _ := base64.StdEncoding.DecodeString(again.InlineBase64); string(body) != `{"order":1}` {
				t.Fatalf("redelivered body = %q", body)
			}
			// The handler enqueues every target before it waits, so anything
			// for the agent that wrote would be pending by now.
			if ds := e.poll(t, wroteCred, 0).Deliveries; len(ds) != 0 && !(wroteCred == otherCred && len(ds) == 1 && ds[0].ID == again.ID) {
				t.Fatalf("the agent that wrote %s/%s was offered it again: %+v", wrote.Directory, wrote.Filename, ds)
			}
			e.ackAs(t, otherCred, again.ID, agentproto.AckOK)
			acks.waitUntil(t, "the ack on the second delivery", func(evs []ackEvent) bool { return hasAck(evs, "+ACK", 2) })
			if ds := e.poll(t, wroteCred, 1).Deliveries; len(ds) != 0 {
				t.Fatalf("after the ack, the agent that wrote was offered %+v", ds)
			}
			// Both writes are reported once the message is done, the earlier
			// one included; nothing is reported for the attempt that was not.
			delivered := map[string]int{}
			for len(events) > 0 {
				if ev := <-events; ev.Type == "delivered" {
					delivered[ev.Agent+"|"+ev.Filename]++
				}
			}
			want := map[string]int{}
			want[e.g.agentName(agentIDOf(wroteCred))+"|"+wrote.Filename]++
			want[e.g.agentName(agentIDOf(otherCred))+"|"+other.Filename]++
			if !maps.Equal(delivered, want) {
				t.Errorf("delivered events = %v, want %v", delivered, want)
			}
			if n := e.g.written.size(); n != 0 {
				t.Errorf("%d writes still remembered after the message was acked, want 0", n)
			}
		})
	}
}

// twoAgents is the first attempt of a two-agent pipeline: one delivery each.
// A writes it; A2 does not.
func twoAgents(t *testing.T, e *testEnv) (wrote, other agentproto.Delivery, wroteCred, otherCred string) {
	t.Helper()
	return e.pollOne(t, credA), e.pollOne(t, credA2), credA, credA2
}

// twoFolders is the first attempt of the two-folder group pipeline: A gets
// both deliveries, and writes the one into "out" only.
func twoFolders(t *testing.T, e *testEnv) (wrote, other agentproto.Delivery, wroteCred, otherCred string) {
	t.Helper()
	var got []agentproto.Delivery
	waitFor(t, "both of A's deliveries", func() bool {
		got = append(got, e.poll(t, credA, 1).Deliveries...)
		return len(got) >= 2
	})
	if len(got) != 2 || got[0].Directory == got[1].Directory {
		t.Fatalf("want one delivery per folder, got %+v", got)
	}
	if got[0].Directory == "out" {
		return got[0], got[1], credA, credA
	}
	return got[1], got[0], credA, credA
}

func agentIDOf(cred string) string {
	if cred == credA2 {
		return agentA2
	}
	return agentA
}

func hasAck(evs []ackEvent, kind string, delivered uint64) bool {
	for _, ev := range evs {
		if ev.kind == kind && ev.delivered == delivered {
			return true
		}
	}
	return false
}

// TestOutput_SingleTargetRedeliveryUnchanged: with one target there is no
// other write to wait for, so nothing is remembered. A message the agent did
// not write (the session ended, or the write failed) comes back to it as
// before, and one it wrote is acked on its first delivery.
func TestOutput_SingleTargetRedeliveryUnchanged(t *testing.T) {
	cases := []struct {
		name string
		// end ends the first attempt; it returns whether the message comes
		// back.
		end func(t *testing.T, e *testEnv, connID string, d agentproto.Delivery) bool
	}{
		{"the agent writes it", func(t *testing.T, e *testEnv, _ string, d agentproto.Delivery) bool {
			e.ackAs(t, credA, d.ID, agentproto.AckOK)
			return false
		}},
		{"the write fails", func(t *testing.T, e *testEnv, _ string, d agentproto.Delivery) bool {
			e.ackAs(t, credA, d.ID, agentproto.AckFailed)
			return true
		}},
		{"the session ends", func(t *testing.T, e *testEnv, connID string, _ agentproto.Delivery) bool {
			e.g.stopConnection(context.Background(), connID, tenant1)
			e.startOutputPipeline(t, connID)
			return true
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.g.stopGrace = 100 * time.Millisecond
			const connID = "single"
			e.startOutputPipeline(t, connID)
			acks := e.watchAcks(t, outputDurable(connID))
			e.publishTo(t, tenant1, connID, `{"order":1}`)
			first := e.pollOne(t, credA)
			if !c.end(t, e, connID, first) {
				acks.settledOnFirstDelivery(t)
			} else {
				again := e.pollOne(t, credA)
				if again.Directory != first.Directory || again.Filename != first.Filename {
					t.Fatalf("redelivered %s/%s, first %s/%s", again.Directory, again.Filename, first.Directory, first.Filename)
				}
				if n := e.g.written.size(); n != 0 {
					t.Errorf("%d writes remembered for a message nobody wrote, want 0", n)
				}
				e.ackAs(t, credA, again.ID, agentproto.AckOK)
				acks.waitUntil(t, "the ack on the second delivery", func(evs []ackEvent) bool { return hasAck(evs, "+ACK", 2) })
			}
			if n := e.g.written.size(); n != 0 {
				t.Errorf("%d writes remembered, want 0", n)
			}
		})
	}
}

func TestWrittenSet(t *testing.T) {
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	key := func(seq uint64) writtenKey {
		return writtenKey{connID: "c", stream: "s", seq: seq, envelopeID: "e", nodeID: "n", agentID: "a", directory: "d", filename: "f"}
	}
	t.Run("an entry lasts its TTL", func(t *testing.T) {
		w := newWrittenSet(time.Hour, 10)
		w.add(t0, key(1))
		if !w.has(key(1), t0.Add(time.Hour-time.Nanosecond)) {
			t.Error("gone before its TTL")
		}
		if w.has(key(2), t0) {
			t.Error("another message's key matched")
		}
		if w.has(key(1), t0.Add(time.Hour)) {
			t.Error("still there at its TTL")
		}
		if w.size() != 0 {
			t.Errorf("an expired entry was looked up and kept: size %d", w.size())
		}
	})
	t.Run("every field is part of the key", func(t *testing.T) {
		w := newWrittenSet(time.Hour, 10)
		w.add(t0, key(1))
		for name, mutate := range map[string]func(*writtenKey){
			"connID":     func(k *writtenKey) { k.connID = "x" },
			"stream":     func(k *writtenKey) { k.stream = "x" },
			"seq":        func(k *writtenKey) { k.seq = 9 },
			"envelopeID": func(k *writtenKey) { k.envelopeID = "x" },
			"nodeID":     func(k *writtenKey) { k.nodeID = "x" },
			"agentID":    func(k *writtenKey) { k.agentID = "x" },
			"directory":  func(k *writtenKey) { k.directory = "x" },
			"filename":   func(k *writtenKey) { k.filename = "x" },
		} {
			k := key(1)
			mutate(&k)
			if w.has(k, t0) {
				t.Errorf("a key with another %s matched", name)
			}
		}
	})
	t.Run("forget", func(t *testing.T) {
		w := newWrittenSet(time.Hour, 10)
		w.add(t0, key(1), key(2))
		w.forget(key(1))
		if w.has(key(1), t0) || !w.has(key(2), t0) {
			t.Error("forget removed the wrong entry")
		}
	})
	t.Run("a full set drops everything expired", func(t *testing.T) {
		w := newWrittenSet(time.Hour, 3)
		w.add(t0, key(1), key(2))
		w.add(t0.Add(30*time.Minute), key(3))
		now := t0.Add(61 * time.Minute) // keys 1 and 2 have expired
		w.add(now, key(4))
		if w.size() != 2 || !w.has(key(3), now) || !w.has(key(4), now) {
			t.Errorf("after adding to a full set with two expired entries: size %d, want 2 (keys 3 and 4)", w.size())
		}
	})
	t.Run("a full set drops the oldest", func(t *testing.T) {
		w := newWrittenSet(time.Hour, 3)
		w.add(t0.Add(2*time.Minute), key(1))
		w.add(t0, key(2))
		w.add(t0.Add(time.Minute), key(3))
		now := t0.Add(3 * time.Minute)
		w.add(now, key(4))
		if w.size() != 3 || w.has(key(2), now) || !w.has(key(1), now) || !w.has(key(3), now) || !w.has(key(4), now) {
			t.Errorf("want the oldest (key 2) dropped; size %d", w.size())
		}
	})
	t.Run("adding a key it holds drops nothing", func(t *testing.T) {
		w := newWrittenSet(time.Hour, 2)
		w.add(t0, key(1), key(2))
		w.add(t0.Add(time.Minute), key(1))
		if w.size() != 2 || !w.has(key(2), t0.Add(time.Minute)) {
			t.Errorf("re-adding a held key evicted another; size %d", w.size())
		}
	})
}
