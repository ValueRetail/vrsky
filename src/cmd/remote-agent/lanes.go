package main

import "sync"

// commandLanes runs each connection's start and stop commands one at a time,
// in the order they were queued (#429). It is this connector's counterpart of
// sdk.StartGuard, which it cannot use as is: a start here may replace a
// running session whose configuration changed (the fingerprint), so a second
// start must not simply be refused while the first is in flight, and a start
// refused because the first activated would leave the older configuration
// running.
//
// Before this, each command ran on its own goroutine with nothing between
// them. Two starts could both find no session, both subscribe the output
// durable, and the second insert overwrote the first session, whose
// subscriber nobody halted or stopped again. A stop that ran while a start was
// still loading found no session and was lost, and the start then wrote
// "running" for a pipeline that had been stopped.
//
// A command takes its turn when it arrives (on the NATS callback goroutine,
// or in boot restore), then waits for the commands before it on the same
// connection to finish. A start therefore sees what an earlier start or stop
// left behind: the same configuration is a no-op, a changed one replaces the
// session, and a start that gave up leaves the next one to try. A stop queued
// while a start is waiting or loading makes that start give up before it
// subscribes (stoppedSince), and a stop queued after a start has subscribed
// ends the session once the start's turn is over. Status writes happen inside
// the turns, so they land in command order too.
//
// Lanes hold no lock while a command runs: a turn is a channel the next turn
// waits on. The lane's own mutex is a leaf: it is held only to queue, check
// and finish turns, takes no other lock and does no I/O. runStart checks
// stoppedSince with the gateway's mu held, so the order is mu, then the lane
// mutex, never the other way round. A turn is never waited on with mu or a
// session's reconcileMu held, so the lock rules of endSession and
// reconcileGroups (#444, #461, #467) are unchanged. A lane is dropped once no
// turn uses it.
//
// The zero value is ready to use.
type commandLanes struct {
	mu    sync.Mutex
	lanes map[string]*commandLane // by connection ID, like the gateway's sessions
}

// commandLane is one connection's queue of commands.
type commandLane struct {
	tail  chan struct{}     // closed when the last command queued so far has finished
	users int               // turns queued and not yet done
	stops map[string]uint64 // stop commands queued so far, by the tenant they name
}

// commandTurn is one command's place in its connection's lane.
type commandTurn struct {
	lanes    *commandLanes
	lane     *commandLane
	connID   string
	tenantID string
	prev     chan struct{} // closed when the command before this one finished; nil if none
	mine     chan struct{} // closed by done
	stops    uint64        // lane.stops[tenantID] when this turn was queued
}

// queue takes the next turn on the connection. It does not block. stop marks
// a stop command, which makes every start queued before it for the same
// tenant give up (stoppedSince). Every turn must be finished with done.
func (c *commandLanes) queue(connID, tenantID string, stop bool) *commandTurn {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lanes == nil {
		c.lanes = make(map[string]*commandLane)
	}
	l := c.lanes[connID]
	if l == nil {
		l = &commandLane{stops: make(map[string]uint64)}
		c.lanes[connID] = l
	}
	if stop {
		l.stops[tenantID]++
	}
	t := &commandTurn{
		lanes: c, lane: l, connID: connID, tenantID: tenantID,
		prev: l.tail, mine: make(chan struct{}), stops: l.stops[tenantID],
	}
	l.tail = t.mine
	l.users++
	return t
}

// wait blocks until every command queued before this one on the connection
// has finished.
func (t *commandTurn) wait() {
	if t.prev != nil {
		<-t.prev
	}
}

// done ends the turn and lets the next command on the connection run.
func (t *commandTurn) done() {
	close(t.mine)
	t.lanes.mu.Lock()
	defer t.lanes.mu.Unlock()
	t.lane.users--
	if t.lane.users == 0 && t.lanes.lanes[t.connID] == t.lane {
		delete(t.lanes.lanes, t.connID)
	}
}

// stoppedSince reports whether a stop naming the same tenant and connection
// was queued after this turn. A start that sees one gives up: the stop is
// newer and wins. A stop naming another tenant does not count; it stops
// nothing of this tenant's (stopConnection checks the tenant).
func (t *commandTurn) stoppedSince() bool {
	t.lanes.mu.Lock()
	defer t.lanes.mu.Unlock()
	return t.lane.stops[t.tenantID] != t.stops
}
