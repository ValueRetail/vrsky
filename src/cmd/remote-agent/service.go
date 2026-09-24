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

// defaultUploadMax bounds one upload. The ingress passes bodies unbuffered and
// unlimited so large files can stream; this is the limit that actually holds.
const defaultUploadMax int64 = 2 << 30

// lastSeenWriteEvery throttles the last_seen_at write. A poll arrives at least
// every 25 s per agent; one write per 30 s keeps the online window (90 s) honest
// without a database write per poll.
const lastSeenWriteEvery = 30 * time.Second

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

	startSub, stopSub *nats.Subscription

	// lookupAgent resolves a credential hash to its agent. The database
	// lookup in production; replaced in tests so the HTTP paths can be driven
	// without a database round trip per request. Its SQL is tested directly.
	lookupAgent func(ctx context.Context, credentialHash string) (agentIdentity, bool, error)
	// outputAckWait is the output durable's AckWait: the crash-recovery
	// window, not a delivery deadline — the dispatch loop heartbeats a
	// message for as long as it waits for its agent.
	outputAckWait time.Duration

	now func() time.Time
}

func newGateway() *gateway {
	g := &gateway{
		sessions:      map[string]*connSession{},
		agents:        map[string]*agentState{},
		events:        newEventHub(),
		uploadMax:     defaultUploadMax,
		outputAckWait: 30 * time.Second,
		now:           time.Now,
	}
	g.lookupAgent = g.dbLookupAgent
	return g
}

// remoteNode is one remote_agent node of a pipeline.
type remoteNode struct {
	NodeID    string
	AgentID   string
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
	sub    *messaging.Subscriber // nil when the pipeline has no remote_agent output
}

// agentState is the gateway's in-memory view of one agent: the deliveries
// waiting for it, and a signal to wake its long-poll.
type agentState struct {
	pending       map[string]*delivery // deliveryID → delivery
	notify        chan struct{}        // capacity 1: "something changed"
	lastSeenWrite time.Time
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
	all := make([]*connSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		all = append(all, sess)
	}
	s.sessions = map[string]*connSession{}
	s.mu.Unlock()
	for _, sess := range all {
		s.endSession(sess)
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
		// Off the NATS callback goroutine: a start queries the database, and
		// a stop waits for an in-flight delivery to let go.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if start {
				s.startConnection(ctx, cmd.ConnectionID, cmd.TenantID)
			} else {
				s.stopConnection(ctx, cmd.ConnectionID, cmd.TenantID)
			}
		}()
	}
}

// restoreRunning starts a session for every pipeline the database says is
// running with a remote_agent node. Connection commands are not persisted, so
// without this a restart of this service would silently stop every
// agent-backed pipeline until someone redeployed it. It is safe to repeat:
// starting binds each output durable at the position it kept.
func (s *gateway) restoreRunning(ctx context.Context) {
	// lint:tenant-ok — fleet-wide boot scan; each row's own tenant scopes everything after.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, tenant_id FROM connections
		WHERE status = 'running' AND nodes::text LIKE '%"remote_agent"%'`)
	if err != nil {
		s.logger.Error("Could not list running pipelines to restore", "error", err)
		return
	}
	type pair struct{ id, tenant string }
	var todo []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.tenant); err == nil {
			todo = append(todo, p)
		}
	}
	_ = rows.Close()
	for _, p := range todo {
		s.startConnection(ctx, p.id, p.tenant)
	}
	s.logger.Info("Restored running pipelines", "count", len(todo))
}

// startConnection brings a pipeline's remote_agent nodes up, or does nothing if
// the pipeline has none. The connection is looked up scoped by the tenant the
// command names, and every agent a node refers to must belong to that same
// tenant — this is where an agent from another workspace is refused.
func (s *gateway) startConnection(ctx context.Context, connID, tenantID string) {
	logger := s.logger.With("connection_id", connID, "tenant_id", tenantID)

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
		if msg := s.checkAgentNode(ctx, tenantID, n, agentproto.ModeRead); msg != "" {
			s.markError(ctx, connID, tenantID, msg)
			return
		}
	}
	for _, n := range outputs {
		if msg := s.checkAgentNode(ctx, tenantID, n, agentproto.ModeWrite); msg != "" {
			s.markError(ctx, connID, tenantID, msg)
			return
		}
	}

	sctx, cancel := context.WithCancel(context.Background())
	sess := &connSession{
		connID: connID, tenantID: tenantID, fingerprint: fp,
		inputs: inputs, outputs: outputs, ctx: sctx, cancel: cancel,
	}
	if len(outputs) > 0 {
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
		}, s.outputHandler(sess))
		if err != nil {
			cancel()
			s.markError(ctx, connID, tenantID, "could not subscribe to the pipeline: "+err.Error())
			return
		}
		sess.sub = sub
	}

	s.mu.Lock()
	s.sessions[connID] = sess
	s.wakeAgentsLocked(sess)
	s.mu.Unlock()

	s.setStatus(ctx, connID, tenantID, "running")
	logger.Info("Remote agent pipeline started", "inputs", len(inputs), "outputs", len(outputs))
}

// stopConnection ends a pipeline's session, if it has one.
func (s *gateway) stopConnection(ctx context.Context, connID, tenantID string) {
	s.mu.Lock()
	sess := s.sessions[connID]
	if sess != nil && sess.tenantID == tenantID {
		delete(s.sessions, connID)
		s.wakeAgentsLocked(sess)
	} else {
		sess = nil
	}
	s.mu.Unlock()
	if sess == nil {
		return
	}
	s.endSession(sess)
	s.logger.Info("Remote agent pipeline stopped", "connection_id", connID)
}

// endSession cancels a session and waits for its subscriber to let go. The
// in-flight message, if any, is NAK'd and waits for the next session.
// Must not be called with s.mu held: the output handler takes it to dequeue.
func (s *gateway) endSession(sess *connSession) {
	sess.cancel()
	if sess.sub != nil {
		sess.sub.Stop()
	}
}

// wakeAgentsLocked nudges every agent a session touches, so their long-polls
// return with the changed watch set. Caller holds s.mu.
func (s *gateway) wakeAgentsLocked(sess *connSession) {
	for _, n := range append(append([]remoteNode{}, sess.inputs...), sess.outputs...) {
		s.agentLocked(n.AgentID).wake()
	}
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
				AgentID         string `json:"agent_id"`
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
			AgentID:   strings.TrimSpace(cfg.RemoteAgent.AgentID),
			Directory: strings.TrimSpace(cfg.RemoteAgent.Directory),
		}
		if rn.AgentID == "" || rn.Directory == "" {
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
		parts = append(parts, fmt.Sprintf("in|%s|%s|%s|%s", n.NodeID, n.AgentID, n.Directory, n.After))
	}
	for _, n := range outputs {
		parts = append(parts, fmt.Sprintf("out|%s|%s|%s|%s|%s|%t", n.NodeID, n.AgentID, n.Directory,
			n.FilenamePattern, n.PredecessorID, n.PredIsConsumer))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:8])
}
