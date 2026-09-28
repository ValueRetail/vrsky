package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
	"github.com/ValueRetail/vrsky/pkg/messaging"
)

// Agent groups: one output node, many tills.
//
// A single-agent output is served by the pipeline's own durable, whose handler
// waits for that one agent. A group output cannot work that way — a handler
// waiting for 100 acks would let one till that is off starve the other 99 —
// so every member gets its own durable on the same subject
// (remote-agent-out-<connID>-<agentID>), and the message waits, retries and
// dead-letters per member. Members are re-read from the database every
// membershipRefresh and when an agent announces itself; a joining member gets
// a durable that starts at "now" (nats.DeliverNewPolicy), a revoked one loses
// its durable, one merely removed from the group keeps it until JetStream's
// inactive threshold reclaims it — so a till taken out for repair and put
// back catches up.
//
// The pipeline's tenant scopes the membership query, so a group name only
// ever means this workspace's agents.

// member is a live agent in one of a session's groups, as the database
// reports it.
type member struct {
	id, name string
	groups   map[string]bool
	dirs     map[string]string // folder name → mode
}

// takes reports whether this member serves a group node: it is in the node's
// group and has the node's folder in the needed mode.
func (m *member) takes(n remoteNode, mode string) bool {
	return m != nil && m.groups[n.Group] && m.dirs[n.Directory] == mode
}

func anyMemberTakes(members map[string]*member, n remoteNode, mode string) bool {
	for _, m := range members {
		if m.takes(n, mode) {
			return true
		}
	}
	return false
}

// memberSub is one member's output subscription; cancel ends its handler's
// wait without ending the session.
type memberSub struct {
	sub    *messaging.Subscriber
	cancel context.CancelFunc
}

func memberDurable(connID, agentID string) string { return outputDurable(connID) + "-" + agentID }

// groupNames lists the distinct groups a session's nodes target.
func (sess *connSession) groupNames() []string {
	seen := map[string]bool{}
	for _, n := range append(append([]remoteNode{}, sess.inputs...), sess.outputs...) {
		if n.Target == targetGroup {
			seen[n.Group] = true
		}
	}
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// liveMembers reads the tenant's live agents in any of the groups.
func (s *gateway) liveMembers(ctx context.Context, tenantID string, groups []string) (map[string]*member, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, name, groups, directories FROM agents
		WHERE tenant_id::text = $1 AND revoked_at IS NULL AND groups && $2`,
		tenantID, pq.Array(groups))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*member{}
	for rows.Next() {
		var (
			m        = &member{groups: map[string]bool{}, dirs: map[string]string{}}
			gs       []string
			dirsJSON []byte
		)
		if err := rows.Scan(&m.id, &m.name, pq.Array(&gs), &dirsJSON); err != nil {
			return nil, err
		}
		for _, g := range gs {
			m.groups[g] = true
		}
		var dirs []agentproto.Directory
		_ = json.Unmarshal(dirsJSON, &dirs)
		for _, d := range dirs {
			m.dirs[d.Name] = d.Mode
		}
		out[m.id] = m
	}
	return out, rows.Err()
}

// memberTakes is takes under the gateway's lock, for the output handler.
func (s *gateway) memberTakes(sess *connSession, agentID string, n remoteNode, mode string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sess.members[agentID].takes(n, mode)
}

// reconcileGroups brings a session's members and per-member subscriptions in
// line with the database. Safe to call often and concurrently.
func (s *gateway) reconcileGroups(ctx context.Context, sess *connSession) {
	groups := sess.groupNames()
	if len(groups) == 0 {
		return
	}
	sess.reconcileMu.Lock()
	defer sess.reconcileMu.Unlock()

	members, err := s.liveMembers(ctx, sess.tenantID, groups)
	if err != nil {
		s.logger.Error("Could not refresh group members", "connection_id", sess.connID, "error", err)
		return
	}

	s.mu.Lock()
	if s.sessions[sess.connID] != sess {
		s.mu.Unlock()
		return // ended meanwhile
	}
	prev := sess.members
	sess.members = members
	desired := map[string]bool{}
	for id, m := range members {
		for _, n := range sess.outputs {
			if n.Target == targetGroup && m.takes(n, agentproto.ModeWrite) {
				desired[id] = true
				break
			}
		}
	}
	// Members in a node's group but without its folder: skipped, and said
	// once, so a misconfigured till is visible in the panel.
	var warnings []string
	check := func(n remoteNode, mode string) {
		if n.Target != targetGroup {
			return
		}
		for id, m := range members {
			key := id + "|" + n.NodeID
			if m.groups[n.Group] && !m.takes(n, mode) && !sess.warned[key] {
				sess.warned[key] = true
				warnings = append(warnings, fmt.Sprintf("%s is in group %s but has no %s folder named %s; skipped", m.name, n.Group, mode, n.Directory))
			}
		}
	}
	for _, n := range sess.inputs {
		check(n, agentproto.ModeRead)
	}
	for _, n := range sess.outputs {
		check(n, agentproto.ModeWrite)
	}
	var toStop []*memberSub
	var toDelete []string
	for id, ms := range sess.memberSubs {
		if !desired[id] {
			delete(sess.memberSubs, id)
			toStop = append(toStop, ms)
			if members[id] == nil {
				// Gone or revoked: nothing must keep waiting for it.
				toDelete = append(toDelete, memberDurable(sess.connID, id))
			} else {
				sess.dormant[id] = true // left the group: keep its place
			}
		}
	}
	// A member that left earlier and is now gone for good loses its durable
	// too; one that is back is resubscribed below (memberSubs has no entry).
	for id := range sess.dormant {
		if members[id] == nil {
			delete(sess.dormant, id)
			toDelete = append(toDelete, memberDurable(sess.connID, id))
		} else if desired[id] {
			delete(sess.dormant, id)
		}
	}
	var toStart []string
	for id := range desired {
		if sess.memberSubs[id] == nil {
			toStart = append(toStart, id)
		}
	}
	for id := range members {
		s.agentLocked(id).wake()
	}
	for id := range prev {
		s.agentLocked(id).wake()
	}
	s.mu.Unlock()

	sort.Strings(warnings)
	for _, w := range warnings {
		s.logger.Warn("Group member skipped", "connection_id", sess.connID, "reason", w)
		s.events.emit(sess.connID, event{Type: "warning", Message: w})
	}
	// Outside the lock: a stopping handler dequeues under it.
	for _, ms := range toStop {
		ms.cancel()
		ms.sub.Stop()
	}
	for _, d := range toDelete {
		if err := messaging.DeleteConsumer(s.js, d); err != nil {
			s.logger.Warn("Could not delete a departed member's consumer", "durable", d, "error", err)
		}
	}
	sort.Strings(toStart)
	for _, id := range toStart {
		mctx, mcancel := context.WithCancel(sess.ctx)
		sub, err := messaging.Subscribe(s.js, messaging.SubscriberOpts{
			DurableName:   memberDurable(sess.connID, id),
			FilterSubject: messaging.DataSubject(sess.tenantID, sess.connID),
			MaxAckPending: 1, // one held message per member; see startConnection
			AckWait:       s.outputAckWait,
			// A member joining a running pipeline starts at "now". Anything
			// else would hand it every catalogue message of the last 72 h.
			// An existing durable (a restart) keeps its position regardless.
			DeliverPolicy: nats.DeliverNewPolicy,
			Logger:        s.logger.With("connection_id", sess.connID, "agent_id", id),
		}, s.outputHandler(sess, outputScope{member: id, done: mctx.Done()}))
		if err != nil {
			mcancel()
			s.logger.Error("Could not subscribe a group member", "connection_id", sess.connID, "agent_id", id, "error", err)
			s.events.emit(sess.connID, event{Type: "failed", Message: "could not subscribe " + s.agentName(id) + ": " + err.Error(), Agent: s.agentName(id)})
			continue
		}
		s.mu.Lock()
		if s.sessions[sess.connID] != sess || sess.memberSubs[id] != nil {
			s.mu.Unlock()
			mcancel()
			sub.Stop()
			continue
		}
		sess.memberSubs[id] = &memberSub{sub: sub, cancel: mcancel}
		s.mu.Unlock()
	}
}

// reconcileAll refreshes every group session.
func (s *gateway) reconcileAll(ctx context.Context) {
	s.mu.Lock()
	all := make([]*connSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		all = append(all, sess)
	}
	s.mu.Unlock()
	for _, sess := range all {
		s.reconcileGroups(ctx, sess)
	}
}

// reconcileTenant refreshes a tenant's group sessions — after one of its
// agents registered or announced itself, so a new till starts receiving
// within seconds rather than at the next periodic refresh.
func (s *gateway) reconcileTenant(ctx context.Context, tenantID string) {
	s.mu.Lock()
	var mine []*connSession
	for _, sess := range s.sessions {
		if sess.tenantID == tenantID {
			mine = append(mine, sess)
		}
	}
	s.mu.Unlock()
	for _, sess := range mine {
		s.reconcileGroups(ctx, sess)
	}
}

func (s *gateway) membershipLoop(ctx context.Context) {
	t := time.NewTicker(s.membershipRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			s.reconcileAll(rctx)
			cancel()
		}
	}
}
