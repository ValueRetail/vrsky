package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
	"github.com/ValueRetail/vrsky/pkg/claimcheck"
	"github.com/ValueRetail/vrsky/pkg/failurelimit"
	// lint:connector-ok — the output direction owns one durable per pipeline
	// (remote-agent-out-<connID>) so a message can wait in the stream while
	// its agent is offline. The SDK's producer path cannot: it NAKs on error
	// and dead-letters after five attempts, about three minutes. Same shape as
	// tenant-consumer's per-bridge durable; see output.go.
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/nats-io/nats.go"
)

// nodeType is the config.type both directions claim.
const nodeType = "remote_agent"

// A node targets one agent (the default) or a group of agents.
const (
	targetAgent = "agent"
	targetGroup = "group"
)

// defaultMembershipRefresh is how often a group session re-reads its members
// from the database, besides doing so whenever an agent announces itself.
const defaultMembershipRefresh = 30 * time.Second

// defaultUploadMax bounds one upload. The ingress passes bodies unbuffered and
// unlimited so large files can stream; this is the limit that actually holds.
const defaultUploadMax int64 = 2 << 30

// lastSeenWriteEvery throttles the last_seen_at write. A poll arrives at least
// every 25 s per agent; one write per 30 s keeps the online window (90 s) honest
// without a database write per poll.
const lastSeenWriteEvery = 30 * time.Second

// defaultStopGrace is how long a stop lets a halted subscriber finish its last
// poll, with its handlers' scope still alive, before it ends that scope (#576).
const defaultStopGrace = time.Second

// shutdownMargin is what a session's stop needs after its grace has ended:
// the poll in progress (150 ms at most) and the drain. A shutdown whose
// context has a deadline shortens the grace to leave this much.
const shutdownMargin = 250 * time.Millisecond

// gateway is the remote-agent connector. See main.go for the shape.
type gateway struct {
	sdk.BaseConsumer

	db     *sql.DB
	nc     *nats.Conn
	js     nats.JetStreamContext
	logger *slog.Logger

	// store reads offloaded output bodies. Opened here rather than taken from
	// the SDK, whose store is unexported; same PAYLOAD_STORE_* configuration.
	store     objectstore.ObjectStore
	inlineMax int
	uploadMax int64

	publish       sdk.PublishFunc
	publishStream sdk.PublishStreamFunc // nil when no payload store is configured

	mu       sync.Mutex
	sessions map[string]*connSession // connectionID → running pipeline
	agents   map[string]*agentState  // agentID → pending work
	events   *eventHub
	// closed is set by Stop, under mu. A start that finishes after it ends
	// the session it built instead of registering it, and a command that
	// arrives after it is ignored.
	closed bool
	// commandsRunning counts the command goroutines onCommand started. Stop
	// waits for them: a stop command may still be in its grace, and closing
	// the connection under it would lose the ack of a message the agent
	// wrote. Add only under mu while !closed, so every Add happens before
	// Stop's Wait.
	commandsRunning sync.WaitGroup

	// commands runs each connection's starts and stops one at a time, in
	// arrival order (#429); see lanes.go.
	commands commandLanes

	startSub, stopSub *nats.Subscription

	// lookupAgent resolves a credential hash to its agent. The database
	// lookup in production; replaced in tests so the HTTP paths can be driven
	// without a database round trip per request. Its SQL is tested directly.
	lookupAgent func(ctx context.Context, credentialHash string) (agentIdentity, bool, error)
	// outputAckWait is the output durable's AckWait: the crash-recovery
	// window, not a delivery deadline — the dispatch loop heartbeats a
	// message for as long as it waits for its agent.
	outputAckWait time.Duration
	// membershipRefresh is how often group sessions re-read their members.
	membershipRefresh time.Duration
	// stopGrace bounds how long a stop keeps a session's handlers' scope
	// alive after halting its subscribers; see stopSubscribers.
	stopGrace time.Duration
	// registerLimit bounds failed registration attempts per client address.
	registerLimit *failurelimit.Limiter

	now func() time.Time

	// afterCancel is a test seam, nil in production. It is folded into each
	// session's cancel func (see newSessionContext), so it runs right after
	// the cancel wherever that cancel sits: a test that holds this window
	// open fails if endSession ever cancels before it halts (#444, #461).
	// It is captured when a session is created, so set it before the
	// pipeline starts; setting it later has no effect on that session.
	afterCancel func()
	// afterMemberSubscribe is a test seam, nil in production. It runs in
	// reconcileGroups after a member's subscriber started and before it is
	// registered in memberSubs: the window in which an endSession could not
	// see it to halt it (#461).
	afterMemberSubscribe func(agentID string)
	// afterPipelineLoad is a test seam, nil in production. It runs in a start
	// once the pipeline's nodes are read, during the start's turn: the window
	// in which a second start or a stop for the connection arrives while the
	// first is still loading (#429).
	afterPipelineLoad func(connID string)
	// afterHalt is a test seam, nil in production. It runs each time a stop
	// has halted subscribers and before it ends their handlers' scope: a
	// message published here lands in the last poll of a halted subscriber
	// (#576).
	afterHalt func()
	// inResolve is a test seam, nil in production, read and run under mu.
	// It runs in resolve once an ack's delivery has been taken out of
	// pending and before its result is handed to the waiting handler: the
	// window in which a stop's grace can end under an ack being accepted.
	inResolve func()
}

func newGateway() *gateway {
	g := &gateway{
		sessions:          map[string]*connSession{},
		agents:            map[string]*agentState{},
		events:            newEventHub(),
		uploadMax:         defaultUploadMax,
		outputAckWait:     30 * time.Second,
		membershipRefresh: defaultMembershipRefresh,
		stopGrace:         defaultStopGrace,
		registerLimit:     newRegisterLimiter(defaultRegisterMaxFailures),
		now:               time.Now,
	}
	g.lookupAgent = g.dbLookupAgent
	return g
}

// remoteNode is one remote_agent node of a pipeline.
type remoteNode struct {
	NodeID    string
	Target    string // targetAgent | targetGroup
	AgentID   string // when Target == targetAgent
	Group     string // when Target == targetGroup
	Directory string

	// Input only.
	After string

	// Output only.
	FilenamePattern string
	PredecessorID   string
	PredIsConsumer  bool
}

// connSession is one running pipeline that has remote_agent nodes.
type connSession struct {
	connID   string
	tenantID string
	// fingerprint identifies the node configuration, so a redeploy with an
	// unchanged config is a no-op and does not disturb an in-flight message.
	fingerprint string
	inputs      []remoteNode
	outputs     []remoteNode

	ctx    context.Context
	cancel context.CancelFunc
	sub    *messaging.Subscriber // nil when the pipeline has no single-agent output

	// Group nodes: the live members of this session's groups, and the
	// per-member output subscriptions (see groups.go). Both guarded by the
	// gateway's mu; reconcileMu serialises reconciliations of one session.
	members     map[string]*member
	memberSubs  map[string]*memberSub
	dormant     map[string]bool // members that left the group: durable kept, no subscription
	warned      map[string]bool // member+node already reported as unusable
	reconcileMu sync.Mutex
}

// agentState is the gateway's in-memory view of one agent: the deliveries
// waiting for it, and a signal to wake its long-poll.
type agentState struct {
	pending       map[string]*delivery // deliveryID → delivery
	notify        chan struct{}        // capacity 1: "something changed"
	lastSeenWrite time.Time
	name          string // as last seen on a poll; for the panel's events
}

// Configure wires dependencies. Called once by the runner before Run.
func (s *gateway) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("remote-agent requires DATABASE_URL")
	}
	s.db = res.DB
	s.nc = res.NATS
	s.logger = res.Logger
	s.inlineMax = res.InlineMaxBytes()

	js, err := s.nc.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream context: %w", err)
	}
	s.js = js

	store, err := claimcheck.OpenStoreFromEnv(ctx, s.logger)
	if err != nil {
		return err
	}
	s.store = store

	if raw := os.Getenv("AGENT_UPLOAD_MAX_BYTES"); raw != "" {
		n, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil || n <= 0 {
			s.logger.Warn("invalid AGENT_UPLOAD_MAX_BYTES; using the default", "value", raw, "default", defaultUploadMax)
		} else {
			s.uploadMax = n
		}
	}

	s.registerLimit = newRegisterLimiter(registerMaxFailuresFromEnv(s.logger))

	s.RegisterHTTPHandler("/agent/", s.agentRoutes())
	s.RegisterHTTPHandler("/events/", s.handleEvents())
	res.Health.SetReady(true)
	return nil
}

// RunStream is Run with large-upload streaming enabled. The SDK calls it
// instead of Run when a payload store is configured.
func (s *gateway) RunStream(ctx context.Context, publish sdk.PublishFunc, publishStream sdk.PublishStreamFunc) error {
	s.publishStream = publishStream
	return s.Run(ctx, publish)
}

// Run subscribes to connection commands, restores the pipelines that were
// running before this process started, and blocks until shutdown.
func (s *gateway) Run(ctx context.Context, publish sdk.PublishFunc) error {
	s.publish = publish

	var err error
	if s.startSub, err = s.nc.Subscribe("vrsky.commands.*.connection.start", s.onCommand(true)); err != nil {
		return fmt.Errorf("subscribe to start commands: %w", err)
	}
	if s.stopSub, err = s.nc.Subscribe("vrsky.commands.*.connection.stop", s.onCommand(false)); err != nil {
		return fmt.Errorf("subscribe to stop commands: %w", err)
	}
	s.logger.Info("Subscribed to NATS command topics")

	s.restoreRunning(ctx)
	go s.membershipLoop(ctx)

	<-ctx.Done()
	return nil
}

// Stop ends every session. Called by the runner after Run returns.
func (s *gateway) Stop(ctx context.Context) error {
	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}
	s.mu.Lock()
	s.closed = true
	all := make([]*connSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		all = append(all, sess)
	}
	s.sessions = map[string]*connSession{}
	s.mu.Unlock()
	// Side by side: each may wait out its stop grace for a handler whose
	// agent does not answer, and one after another that would add up past
	// the runner's shutdown window. The grace is shortened if the window is
	// shorter than it.
	grace := s.stopGrace
	if dl, ok := ctx.Deadline(); ok {
		grace = min(grace, max(time.Until(dl)-shutdownMargin, 0))
	}
	var wg sync.WaitGroup
	for _, sess := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.endSession(sess, grace)
		}()
	}
	wg.Wait()
	// Commands already running: a stop command took its session out of
	// s.sessions before the lines above, so they did not end it, and it may
	// still be in its grace. The runner closes the NATS connection once Stop
	// returns, and an ack sent after that is lost: the server redelivers
	// after the AckWait, and the next session writes the file again.
	running := make(chan struct{})
	go func() {
		s.commandsRunning.Wait()
		close(running)
	}()
	select {
	case <-running:
	case <-ctx.Done():
		s.logger.Warn("Shutting down with connection commands still running", "error", ctx.Err())
	}
	return nil
}

type commandMessage struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

func (s *gateway) onCommand(start bool) nats.MsgHandler {
	return func(msg *nats.Msg) {
		var cmd commandMessage
		if err := json.Unmarshal(msg.Data, &cmd); err != nil || cmd.ConnectionID == "" || cmd.TenantID == "" {
			s.logger.Warn("Ignoring malformed connection command", "subject", msg.Subject)
			return
		}
		// A stop must name the same tenant on its subject as in its payload
		// (#498). runStop checks the payload's tenant against the session's.
		if st, ok := sdk.CommandSubjectTenant(msg.Subject); !start && (!ok || st != cmd.TenantID) {
			s.logger.Warn("Ignoring stop command for another tenant", "connection_id", cmd.ConnectionID,
				"tenant_id", cmd.TenantID, "subject_tenant_id", st)
			return
		}
		// Stop has ended every session and waits for the commands it counted;
		// one arriving after it has nothing left to do.
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			s.logger.Info("Ignoring connection command during shutdown", "connection_id", cmd.ConnectionID, "start", start)
			return
		}
		s.commandsRunning.Add(1)
		s.mu.Unlock()
		// The turn is taken here, on the callback goroutine, so the
		// connection's commands run in the order this subscription saw them
		// (#429).
		turn := s.commands.queue(cmd.ConnectionID, cmd.TenantID, !start)
		// Off the NATS callback goroutine: a start queries the database, and
		// a stop waits for an in-flight delivery to let go.
		go func() {
			defer s.commandsRunning.Done()
			defer turn.done()
			turn.wait()
			// The timeout starts once it is this command's turn, so waiting
			// behind another command does not eat into it.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if start {
				s.runStart(ctx, turn, cmd.ConnectionID, cmd.TenantID)
			} else {
				s.runStop(ctx, cmd.ConnectionID, cmd.TenantID)
			}
		}()
	}
}

// restoreRunning starts a session for every pipeline the database says is
// running with a remote_agent node, through the shared helper every standing
// consumer uses (plans/stable-connections.md). Safe to repeat: starting binds
// each output durable at the position it kept.
func (s *gateway) restoreRunning(ctx context.Context) {
	sdk.RestoreRunning(ctx, s.db, s.logger, "remote_agent", s.startConnection)
}

// startConnection brings a pipeline's remote_agent nodes up, or does nothing if
// the pipeline has none, in its turn on the connection's lane: it waits for
// the connection's earlier commands to finish first (#429). Boot restore
// calls it; start commands take their turn on the callback goroutine and call
// runStart.
func (s *gateway) startConnection(ctx context.Context, connID, tenantID string) {
	turn := s.commands.queue(connID, tenantID, false)
	defer turn.done()
	turn.wait()
	s.runStart(ctx, turn, connID, tenantID)
}

// runStart is a start's work, run in its turn. The connection is looked up
// scoped by the tenant the command names, and every agent a node refers to
// must belong to that same tenant — this is where an agent from another
// workspace is refused.
func (s *gateway) runStart(ctx context.Context, turn *commandTurn, connID, tenantID string) {
	logger := s.logger.With("connection_id", connID, "tenant_id", tenantID)
	if turn.stoppedSince() || s.isClosed() {
		logger.Info("Start abandoned: a stop arrived before it ran")
		return
	}

	var nodesJSON, edgesJSON []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT nodes, edges FROM connections WHERE id::text = $1 AND tenant_id = $2`,
		connID, tenantID).Scan(&nodesJSON, &edgesJSON)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logger.Error("Could not load pipeline", "error", err)
		}
		return
	}
	inputs, outputs, err := parseRemoteNodes(nodesJSON, edgesJSON)
	if err != nil {
		logger.Error("Could not read pipeline nodes", "error", err)
		return
	}
	if s.afterPipelineLoad != nil {
		s.afterPipelineLoad(connID)
	}
	if len(inputs) == 0 && len(outputs) == 0 {
		// Not ours — or no longer ours after a redeploy.
		s.stopConnection(ctx, connID, tenantID)
		return
	}
	fp := fingerprint(inputs, outputs)

	s.mu.Lock()
	existing := s.sessions[connID]
	s.mu.Unlock()
	if existing != nil && existing.fingerprint == fp && existing.tenantID == tenantID {
		logger.Debug("Pipeline already running with this configuration")
		return
	}
	if existing != nil {
		s.stopConnection(ctx, connID, existing.tenantID)
	}

	for _, n := range inputs {
		if n.Target == targetGroup {
			continue
		}
		if msg := s.checkAgentNode(ctx, tenantID, n, agentproto.ModeRead); msg != "" {
			s.markError(ctx, connID, tenantID, msg)
			return
		}
	}
	for _, n := range outputs {
		if n.Target == targetGroup {
			continue
		}
		if msg := s.checkAgentNode(ctx, tenantID, n, agentproto.ModeWrite); msg != "" {
			s.markError(ctx, connID, tenantID, msg)
			return
		}
	}

	sctx, cancel := s.newSessionContext()
	sess := &connSession{
		connID: connID, tenantID: tenantID, fingerprint: fp,
		inputs: inputs, outputs: outputs, ctx: sctx, cancel: cancel,
		members: map[string]*member{}, memberSubs: map[string]*memberSub{}, dormant: map[string]bool{}, warned: map[string]bool{},
	}

	// Group nodes: resolve the members now, and refuse a node no member can
	// serve — the same early, readable refusal a single-agent node gets. A
	// member without the folder is skipped and named, not a reason to refuse.
	if groups := sess.groupNames(); len(groups) > 0 {
		members, err := s.liveMembers(ctx, tenantID, groups)
		if err != nil {
			cancel()
			s.markError(ctx, connID, tenantID, "could not look up the group's agents: "+err.Error())
			return
		}
		sess.members = members
		for _, n := range inputs {
			if n.Target == targetGroup && !anyMemberTakes(members, n, agentproto.ModeRead) {
				cancel()
				s.markError(ctx, connID, tenantID, fmt.Sprintf("node %s: no agent in group %q has a read folder named %q", n.NodeID, n.Group, n.Directory))
				return
			}
		}
		for _, n := range outputs {
			if n.Target == targetGroup && !anyMemberTakes(members, n, agentproto.ModeWrite) {
				cancel()
				s.markError(ctx, connID, tenantID, fmt.Sprintf("node %s: no agent in group %q has a write folder named %q", n.NodeID, n.Group, n.Directory))
				return
			}
		}
	}

	if hasSingleAgentOutput(outputs) {
		sub, err := messaging.Subscribe(s.js, messaging.SubscriberOpts{
			DurableName:   outputDurable(connID),
			FilterSubject: messaging.DataSubject(tenantID, connID),
			// One message in flight per pipeline, and it waits in the
			// handler — never NAK'd — while the agent is away. The dispatch
			// loop heartbeats only the message in the handler, so a second
			// prefetched message would sit un-heartbeated, expire, and burn
			// its delivery attempts toward the dead-letter queue.
			MaxAckPending: 1,
			AckWait:       s.outputAckWait,
			Logger:        logger,
		}, s.outputHandler(sess, outputScope{done: sctx.Done()}))
		if err != nil {
			cancel()
			s.markError(ctx, connID, tenantID, "could not subscribe to the pipeline: "+err.Error())
			return
		}
		sess.sub = sub
	}

	// A stop queued during the work above is newer than this start and wins,
	// and so does shutdown: end what was built (halt before cancel, as for
	// any session) instead of registering it. A stop queued after this point
	// waits for the turn to end and then stops the session.
	s.mu.Lock()
	abandon := s.closed || turn.stoppedSince()
	if !abandon {
		s.sessions[connID] = sess
		s.wakeAgentsLocked(sess)
	}
	s.mu.Unlock()
	if abandon {
		s.endSession(sess, s.stopGrace)
		logger.Info("Start abandoned: a stop arrived while it was in progress")
		return
	}

	// Per-member durables for the group outputs.
	s.reconcileGroups(ctx, sess)

	s.setStatus(ctx, connID, tenantID, "running")
	logger.Info("Remote agent pipeline started", "inputs", len(inputs), "outputs", len(outputs), "group_members", len(sess.members))
}

// newSessionContext is a session's context. Its cancel func carries the
// afterCancel test seam, so the seam moves with the cancel.
func (s *gateway) newSessionContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if hook := s.afterCancel; hook != nil {
		return ctx, func() { cancel(); hook() }
	}
	return ctx, cancel
}

func hasSingleAgentOutput(outputs []remoteNode) bool {
	for _, n := range outputs {
		if n.Target != targetGroup {
			return true
		}
	}
	return false
}

// runStop is a stop command's work, run in its turn: it ends the session and,
// if there was one, records "stopped". Written here, in the turn, the status
// lands after the "running" of any start before it, so a stop that arrived
// while that start was finishing still ends "stopped" (#429). A stop for a
// connection this gateway is not running writes nothing.
func (s *gateway) runStop(ctx context.Context, connID, tenantID string) {
	if s.stopConnection(ctx, connID, tenantID) {
		s.setStopped(ctx, connID, tenantID)
	}
}

// isClosed reports whether Stop has run.
func (s *gateway) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// stopConnection ends a pipeline's session, if it has one, and reports
// whether it did. It writes no status: a start replacing the session calls it
// too.
func (s *gateway) stopConnection(ctx context.Context, connID, tenantID string) bool {
	s.mu.Lock()
	sess := s.sessions[connID]
	var other string // the session's tenant when it is not tenantID (#498)
	if sess != nil && sess.tenantID == tenantID {
		delete(s.sessions, connID)
		s.wakeAgentsLocked(sess)
	} else {
		if sess != nil {
			other = sess.tenantID
		}
		sess = nil
	}
	s.mu.Unlock()
	if other != "" {
		s.logger.Warn("Ignoring stop for another tenant's pipeline", "connection_id", connID,
			"tenant_id", tenantID, "session_tenant_id", other)
	}
	if sess == nil {
		return false
	}
	s.endSession(sess, s.stopGrace)
	s.logger.Info("Remote agent pipeline stopped", "connection_id", connID)
	return true
}

// endSession ends a session and waits for its subscribers to let go.
// Must not be called with s.mu held: the output handler takes it to dequeue.
//
// The subscribers are stopped with stopSubscribers: halted, given up to
// grace (stopGrace, less in a shutdown short of time) to handle their last poll with the session still alive, and only
// then is the session cancelled. A message a halted subscriber's last poll
// brought is handled like any other (#576). A message whose agent does not
// write it within the grace, the one held for an offline agent above all, is
// NAK'd when the session is cancelled and waits for the next session.
//
// Every subscriber is halted before the cancel. Cancelling first left each
// dispatch loop running until its Stop was reached: it fetched the NAK'd
// message again, the ended handler NAK'd it again, and a stop that lost the
// CPU for a few milliseconds, or a member subscriber waiting its turn behind
// the pipeline's own Stop, burned all five attempts and dead-lettered the
// message (#444).
//
// It also waits for a reconcile of this session that is under way. A member
// subscriber the reconcile has started but not yet registered is not in
// memberSubs, so it could not be halted here, and the cancel would leave it
// NAK'ing and refetching until the reconcile took the lock (#461). Callers
// take the session out of s.sessions first, so a reconcile that gets the
// lock after this sees it ended and stops what it started, halt first. A
// reconcile checks for that before its membership query and before each
// subscribe, so a stop waits for at most the step in flight: the query (under
// the reconcile's context: reconcileTimeout for the background ones, the
// caller's in startConnection), or one subscribe and its stop (under
// JetStream's own timeouts, plus at most stopGrace).
func (s *gateway) endSession(sess *connSession, grace time.Duration) {
	sess.reconcileMu.Lock()
	defer sess.reconcileMu.Unlock()
	s.mu.Lock()
	subs := make([]*messaging.Subscriber, 0, len(sess.memberSubs)+1)
	if sess.sub != nil {
		subs = append(subs, sess.sub)
	}
	for _, ms := range sess.memberSubs {
		subs = append(subs, ms.sub)
	}
	sess.memberSubs = map[string]*memberSub{}
	s.mu.Unlock()
	s.stopSubscribers(grace, sess.cancel, subs...) // sess.cancel also ends every member's context
}

// stopSubscribers stops output subscribers whose handlers run in the scope
// that cancel ends (#576).
//
// A halted subscriber still finishes the poll it is in, and dispatches what
// that poll brought after Halt has returned (see messaging.Subscriber.Halt).
// Ending the scope right after the halt had such a message meet an ended
// session: errSessionEnded, a NAK, and a delivery attempt spent for nothing
// (the last one dead-letters it). So the scope stays alive until the
// subscribers are done: Stop returns once the dispatch loop has exited, which
// is after its last handler returned and the message was acked or NAK'd. Stop
// also drains the subscription, and that is harmless with the scope still
// alive: by then the loop has ended and holds no pull request.
//
// The wait is bounded by grace. A handler waiting for an agent that is
// offline, or that went away during the stop, would otherwise hold the stop
// for as long as the agent stays away. Once the grace has passed the scope is
// ended, which releases every such handler (the message is NAK'd and waits
// for the next session), and then the stops are waited for. The subscribers
// share the one grace: they are stopped side by side.
//
// Nothing a handler waits for depends on the session still being registered:
// an agent's poll hands out its pending deliveries and takes their acks
// whether or not the session is in s.sessions, and nothing here holds s.mu.
func (s *gateway) stopSubscribers(grace time.Duration, cancel func(), subs ...*messaging.Subscriber) {
	for _, sub := range subs {
		sub.Halt()
	}
	if s.afterHalt != nil {
		s.afterHalt()
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		var wg sync.WaitGroup
		for _, sub := range subs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sub.Stop()
			}()
		}
		wg.Wait()
	}()
	timer := time.NewTimer(grace)
	select {
	case <-stopped:
	case <-timer.C:
	}
	timer.Stop()
	cancel()
	<-stopped
}

// wakeAgentsLocked nudges every agent a session touches, so their long-polls
// return with the changed watch set. Caller holds s.mu.
func (s *gateway) wakeAgentsLocked(sess *connSession) {
	for _, n := range append(append([]remoteNode{}, sess.inputs...), sess.outputs...) {
		if n.AgentID != "" {
			s.agentLocked(n.AgentID).wake()
		}
	}
	for id := range sess.members {
		s.agentLocked(id).wake()
	}
}

// agentName is the name an agent last polled with, for the panel's events.
func (s *gateway) agentName(agentID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.agents[agentID]; a != nil && a.name != "" {
		return a.name
	}
	return agentID
}

// agentLocked returns the state for an agent, creating it. Caller holds s.mu.
func (s *gateway) agentLocked(agentID string) *agentState {
	a := s.agents[agentID]
	if a == nil {
		a = &agentState{pending: map[string]*delivery{}, notify: make(chan struct{}, 1)}
		s.agents[agentID] = a
	}
	return a
}

func (a *agentState) wake() {
	select {
	case a.notify <- struct{}{}:
	default:
	}
}

// checkAgentNode verifies that a node's agent exists in this tenant, is not
// revoked, and has reported the named directory in the needed mode. Returns a
// user-facing reason, or "" if the node is usable.
//
// The tenant condition is the isolation boundary for pipelines: without it, a
// pipeline in one workspace that named another workspace's agent ID would
// deliver into that workspace's machine.
func (s *gateway) checkAgentNode(ctx context.Context, tenantID string, n remoteNode, mode string) string {
	var dirsJSON []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT directories FROM agents
		WHERE id::text = $1 AND tenant_id::text = $2 AND revoked_at IS NULL`,
		n.AgentID, tenantID).Scan(&dirsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Sprintf("node %s: remote agent %s is not registered in this workspace, or has been revoked", n.NodeID, n.AgentID)
	}
	if err != nil {
		return fmt.Sprintf("node %s: could not look up remote agent: %v", n.NodeID, err)
	}
	var dirs []agentproto.Directory
	_ = json.Unmarshal(dirsJSON, &dirs)
	for _, d := range dirs {
		if d.Name == n.Directory {
			if d.Mode != mode {
				return fmt.Sprintf("node %s: folder %q on the agent is %s-only", n.NodeID, n.Directory, d.Mode)
			}
			return ""
		}
	}
	return fmt.Sprintf("node %s: the agent has no folder named %q", n.NodeID, n.Directory)
}

func (s *gateway) setStatus(ctx context.Context, connID, tenantID, status string) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW(), last_error = NULL
		WHERE id::text = $2 AND tenant_id = $3`, status, connID, tenantID); err != nil {
		s.logger.Warn("Could not update pipeline status", "connection_id", connID, "error", err)
	}
}

// setStopped records a stop. last_error is left alone: a billing stop has
// already put its reason there.
func (s *gateway) setStopped(ctx context.Context, connID, tenantID string) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE connections SET status = 'stopped', stopped_at = NOW(), updated_at = NOW()
		WHERE id::text = $1 AND tenant_id = $2`, connID, tenantID); err != nil {
		s.logger.Warn("Could not update pipeline status", "connection_id", connID, "error", err)
	}
}

func (s *gateway) markError(ctx context.Context, connID, tenantID, reason string) {
	s.logger.Error("Remote agent pipeline refused", "connection_id", connID, "tenant_id", tenantID, "reason", reason)
	if _, err := s.db.ExecContext(ctx, `
		UPDATE connections SET status = 'error', last_error = $1, updated_at = NOW()
		WHERE id::text = $2 AND tenant_id = $3`, reason, connID, tenantID); err != nil {
		s.logger.Warn("Could not record pipeline error", "connection_id", connID, "error", err)
	}
	s.events.emit(connID, event{Type: "failed", Message: reason})
}

func outputDurable(connID string) string { return "remote-agent-out-" + connID }

// parseRemoteNodes extracts a pipeline's remote_agent nodes, resolving each
// output's predecessor the way file-producer does so the output takes the
// message from the step before it and not the raw input.
func parseRemoteNodes(nodesJSON, edgesJSON []byte) (inputs, outputs []remoteNode, err error) {
	var nodes []struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, nil, err
	}
	var edges []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if len(edgesJSON) > 0 {
		_ = json.Unmarshal(edgesJSON, &edges)
	}
	kindOf := map[string]string{}
	for _, n := range nodes {
		kindOf[n.ID] = n.Type
	}

	for _, n := range nodes {
		var cfg struct {
			Type        string `json:"type"`
			RemoteAgent struct {
				Target          string `json:"target"`
				AgentID         string `json:"agent_id"`
				Group           string `json:"group"`
				Directory       string `json:"directory"`
				After           string `json:"after"`
				FilenamePattern string `json:"filename_pattern"`
			} `json:"remote_agent"`
		}
		if json.Unmarshal(n.Config, &cfg) != nil || cfg.Type != nodeType {
			continue
		}
		rn := remoteNode{
			NodeID:    n.ID,
			Target:    targetAgent,
			AgentID:   strings.TrimSpace(cfg.RemoteAgent.AgentID),
			Directory: strings.TrimSpace(cfg.RemoteAgent.Directory),
		}
		if cfg.RemoteAgent.Target == targetGroup {
			rn.Target, rn.AgentID, rn.Group = targetGroup, "", strings.TrimSpace(cfg.RemoteAgent.Group)
		}
		if rn.Directory == "" || (rn.Target == targetGroup && rn.Group == "") || (rn.Target == targetAgent && rn.AgentID == "") {
			continue // presence is enforced at start by the management API
		}
		switch n.Type {
		case "consumer":
			rn.After = cfg.RemoteAgent.After
			if rn.After != agentproto.AfterDelete {
				rn.After = agentproto.AfterMove
			}
			inputs = append(inputs, rn)
		case "producer":
			rn.FilenamePattern = cfg.RemoteAgent.FilenamePattern
			for _, e := range edges {
				if e.Target == n.ID {
					rn.PredecessorID = e.Source
					rn.PredIsConsumer = kindOf[e.Source] == "consumer"
					break
				}
			}
			outputs = append(outputs, rn)
		}
	}
	return inputs, outputs, nil
}

// fingerprint is a stable digest of a session's node configuration.
func fingerprint(inputs, outputs []remoteNode) string {
	parts := make([]string, 0, len(inputs)+len(outputs))
	for _, n := range inputs {
		parts = append(parts, fmt.Sprintf("in|%s|%s|%s|%s|%s|%s", n.NodeID, n.Target, n.AgentID, n.Group, n.Directory, n.After))
	}
	for _, n := range outputs {
		parts = append(parts, fmt.Sprintf("out|%s|%s|%s|%s|%s|%s|%s|%t", n.NodeID, n.Target, n.AgentID, n.Group, n.Directory,
			n.FilenamePattern, n.PredecessorID, n.PredIsConsumer))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:8])
}
