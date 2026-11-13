package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	// ackTimeout bounds the double ack to the source.
	ackTimeout = 5 * time.Second
	// publishTimeout bounds one publish into the pipeline, so a VRSky stream
	// without a leader shows up as failed publishes, retried and logged,
	// instead of a connection that hangs without a word.
	publishTimeout = 30 * time.Second
	// publishRetryDelay is the first wait before a failed publish into the
	// pipeline is tried again; it doubles up to maxBackoff.
	publishRetryDelay = time.Second
	// minBackoff and maxBackoff bound the wait between redials after a
	// transient error; it doubles each time and resets after a good connect.
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// envelopeNamespace is the UUID namespace for envelope ids derived from a
// source message's key.
var envelopeNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("vrsky-nats-consumer"))

// envelopeID is the envelope id, and so VRSky's Nats-Msg-Id, for a source
// message: the same for every redelivery of it, so VRSky's JetStream drops a
// repeat inside its dedup window. The connection id is part of it, so two
// connections reading the same event do not swallow each other's publish.
// The length prefix keeps the two parts apart whatever bytes the key holds.
func envelopeID(connectionID, key string) string {
	return uuid.NewSHA1(envelopeNamespace, []byte(strconv.Itoa(len(connectionID))+":"+connectionID+key)).String()
}

// natsConsumer runs one pull loop per active connection. SDK Consumer:
// Configure wires deps, Run subscribes to the command subjects and blocks,
// Stop cancels the loops.
type natsConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn // VRSky's own NATS, for the command subjects
	publish sdk.PublishFunc
	logger  *slog.Logger

	// allowPrivate is the operator's dev switch (ALLOW_PRIVATE_UPSTREAMS): it
	// allows private addresses and plain connections, for a local server.
	allowPrivate bool
	// The redial schedule and the wait after a failed publish; tests
	// shorten them.
	minBackoff, maxBackoff, retryDelay time.Duration
	// apiTimeout bounds binding the durable, publishTimeout one publish into
	// the pipeline; tests shorten them. fetchWait, when set (tests only),
	// replaces the config's max_wait_seconds for a waiting pull.
	apiTimeout, fetchWait, publishTimeout time.Duration

	active map[string]*activeConn
	mu     sync.RWMutex
	// starts holds the claim of each start in progress (#397); guarded by mu.
	starts sdk.StartGuard

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// activeConn is one running loop. Its pointer tells this start's entry from a
// newer one's, so a loop that ends on its own clears only itself.
type activeConn struct {
	cancel context.CancelFunc
}

type nodeConfig struct {
	Type string          `json:"type"`
	NATS json.RawMessage `json:"nats"`
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

type commandMessage struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

// Configure wires dependencies. Called once before Run.
func (c *natsConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("nats-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	c.db = res.DB
	c.nc = res.NATS
	c.logger = res.Logger
	c.mu.Lock()
	c.active = make(map[string]*activeConn)
	c.mu.Unlock()
	c.allowPrivate = safeurl.AllowPrivateFromEnv()
	if c.minBackoff == 0 {
		c.minBackoff = minBackoff
	}
	if c.maxBackoff == 0 {
		c.maxBackoff = maxBackoff
	}
	if c.retryDelay == 0 {
		c.retryDelay = publishRetryDelay
	}
	if c.apiTimeout == 0 {
		c.apiTimeout = apiTimeout
	}
	if c.publishTimeout == 0 {
		c.publishTimeout = publishTimeout
	}
	c.RegisterHTTPHandler("/test-connection/", c.handleTestConnection())
	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until ctx is
// cancelled.
func (c *natsConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
	c.publish = publish

	startSub, err := c.nc.Subscribe("vrsky.commands.*.connection.start", c.handleStartCommand)
	if err != nil {
		return fmt.Errorf("subscribe start commands: %w", err)
	}
	c.startSub = startSub

	stopSub, err := c.nc.Subscribe("vrsky.commands.*.connection.stop", c.handleStopCommand)
	if err != nil {
		return fmt.Errorf("subscribe stop commands: %w", err)
	}
	c.stopSub = stopSub

	c.logger.Info("Subscribed to NATS command topics")
	// Start and stop are NATS commands and are not persisted: bring back what
	// the database says is running.
	sdk.RestoreRunning(ctx, c.db, c.logger, "nats", c.restoreConnection)

	<-ctx.Done()
	return nil
}

// Stop cancels all loops. The SDK runner calls this after Run returns.
func (c *natsConsumer) Stop(ctx context.Context) error {
	if c.startSub != nil {
		_ = c.startSub.Unsubscribe()
	}
	if c.stopSub != nil {
		_ = c.stopSub.Unsubscribe()
	}
	c.mu.Lock()
	for id, ac := range c.active {
		c.logger.Info("Stopping NATS consumer", "connection_id", id)
		ac.cancel()
	}
	c.active = make(map[string]*activeConn)
	c.starts.StopAllLocked()
	c.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
	return nil
}

func (c *natsConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		c.logger.Error("parse start command", "error", err)
		return
	}
	c.startConnection(context.Background(), cmd.ConnectionID, cmd.TenantID)
}

// startConnection is the start command's work. Only a start command may create
// a missing durable.
func (c *natsConsumer) startConnection(ctx context.Context, connectionID, tenantID string) {
	c.start(ctx, connectionID, tenantID, true)
}

// restoreConnection is what RestoreRunning calls on boot for every pipeline
// the database says is running. It never creates the durable: one missing
// now was deleted while the pipeline ran, and creating it on every pod start
// (each deploy, each morning the cluster wakes) would replay the stream from
// deliver_policy each time. A missing durable ends the pipeline in status
// error instead; a start command creates it on purpose.
func (c *natsConsumer) restoreConnection(ctx context.Context, connectionID, tenantID string) {
	c.start(ctx, connectionID, tenantID, false)
}

func (c *natsConsumer) start(ctx context.Context, connectionID, tenantID string, mayCreate bool) {
	logger := c.logger.With("connection_id", connectionID, "tenant_id", tenantID)

	// The check and the claim are one critical section, so a second start for
	// the same connection stops here (#397); one refused while another is in
	// flight runs again if that one gives up (#424).
	claim, ok := c.starts.ReserveOrRetry(&c.mu, tenantID, connectionID, func() bool { _, ok := c.active[connectionID]; return ok },
		func() { c.start(ctx, connectionID, tenantID, mayCreate) })
	if !ok {
		logger.Warn("NATS consumer already running or starting")
		return
	}
	defer claim.Release()

	cfg, mine, cfgErr := c.getConfig(ctx, connectionID, tenantID)
	if !mine {
		logger.Debug("Not a NATS consumer for this connection, ignoring")
		return
	}
	if cfgErr == nil {
		cfgErr = cfg.validate(c.allowPrivate)
	}

	loopCtx, cancel := context.WithCancel(context.Background())
	own := &activeConn{cancel: cancel}
	// A stop that arrived during the work above has taken the claim back and wins.
	if !claim.Activate(func() { c.active[connectionID] = own }) {
		cancel()
		logger.Info("Start abandoned: a stop arrived while it was in progress")
		return
	}
	if cfgErr != nil {
		// Activated first so the error takes its place in the status order
		// like any other end of the connection.
		c.fail(own, connectionID, tenantID, cfgErr, logger)
		return
	}
	claim.RunningStatus().Write(func() { c.writeStatus(connectionID, tenantID, "running", "", logger) })

	logger.Info("Starting NATS consumer", "nats", cfg)
	go c.consumeLoop(loopCtx, own, connectionID, tenantID, cfg, mayCreate, logger)
}

func (c *natsConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		c.logger.Error("parse stop command", "error", err)
		return
	}
	c.mu.Lock()
	// The stop must name the tenant the connection is starting or active for,
	// in its payload and on its subject (#498).
	starting, err := c.starts.StopForTenantLocked(msg.Subject, cmd.TenantID, cmd.ConnectionID)
	if err != nil {
		c.mu.Unlock()
		c.logger.Warn("Ignoring stop command for another tenant", "connection_id", cmd.ConnectionID, "error", err)
		return
	}
	ac, exists := c.active[cmd.ConnectionID]
	if exists {
		ac.cancel()
		delete(c.active, cmd.ConnectionID)
	}
	var stopped sdk.StatusWrite
	if exists {
		stopped = c.starts.StopStatusLocked(cmd.TenantID, cmd.ConnectionID)
	}
	c.mu.Unlock()
	if starting {
		c.logger.Info("Stop arrived while the connection was starting; the start is abandoned", "connection_id", cmd.ConnectionID)
	}
	if exists {
		stopped.Write(func() { c.writeStatus(cmd.ConnectionID, cmd.TenantID, "stopped", "", c.logger) })
		c.logger.Info("NATS consumer stopped", "connection_id", cmd.ConnectionID)
	}
}

// fail ends a connection on a permanent error: it leaves the active map (if it
// is still this start's) and records status error with the reason.
func (c *natsConsumer) fail(own *activeConn, connectionID, tenantID string, cause error, logger *slog.Logger) {
	own.cancel()
	c.mu.Lock()
	if c.active[connectionID] != own {
		c.mu.Unlock()
		logger.Info("A stop or a newer start owns the connection; leaving its status alone", "reason", cause.Error())
		return
	}
	delete(c.active, connectionID)
	write := c.starts.StopStatusLocked(tenantID, connectionID)
	c.mu.Unlock()
	logger.Error("NATS consumer stopped on an error that retrying cannot fix", "reason", cause.Error())
	write.Write(func() { c.writeStatus(connectionID, tenantID, "error", cause.Error(), logger) })
}

// consumeLoop connects, pulls and forwards until ctx is cancelled or a
// permanent error ends the connection. Transient errors redial after a
// backoff that doubles up to maxBackoff and resets after a good connect.
func (c *natsConsumer) consumeLoop(ctx context.Context, own *activeConn, connID, tenantID string, cfg *NATSConfig, mayCreate bool, logger *slog.Logger) {
	backoff := c.minBackoff
	// create: a start command may create a missing durable, until the loop
	// has bound it once, unless create_durable is false. A durable that is
	// gone after that, or at a restore, was deleted on the server; creating
	// it again would replay the stream from deliver_policy, older than
	// VRSky's dedup window, so that is left to a deliberate new start.
	create := mayCreate && cfg.mayCreate()
	for ctx.Err() == nil {
		s, err := open(ctx, cfg, openOpts{allowPrivate: c.allowPrivate, create: create, apiWait: c.apiTimeout, dialWait: dialTimeout})
		if err == nil {
			create = false
			backoff = c.minBackoff
			logger.Info("Connected to the NATS server", "stream", cfg.Stream, "durable", cfg.Durable)
			for _, w := range s.warnings {
				logger.Warn(w)
			}
			err = c.drain(ctx, connID, tenantID, cfg, s, logger)
			s.close()
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errDurableMissing) {
			err = durableMissingError(cfg)
		}
		if sdk.IsPermanent(err) {
			c.fail(own, connID, tenantID, err, logger)
			return
		}
		logger.Warn("NATS source unavailable; will reconnect", "error", safeurl.Detail(err), "backoff", backoff)
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = min(2*backoff, c.maxBackoff)
	}
}

// durableMissingError is the reason for a durable the loop may not create.
func durableMissingError(cfg *NATSConfig) error {
	if !cfg.mayCreate() {
		return sdk.Permanent(fmt.Errorf("durable %q does not exist on stream %q, and create_durable is off; have it created on the NATS server, then start the pipeline again", cfg.Durable, cfg.Stream))
	}
	return sdk.Permanent(fmt.Errorf("durable %q no longer exists on the NATS server; stop and start the pipeline to create it anew, which delivers from deliver_policy (%s) again", cfg.Durable, cfg.DeliverPolicy))
}

// pullWait is how long a waiting pull lasts: max_wait_seconds, or the
// tests' shorter fetchWait.
func (c *natsConsumer) pullWait(cfg *NATSConfig) time.Duration {
	if c.fetchWait > 0 {
		return c.fetchWait
	}
	return cfg.maxWait()
}

// drain pulls messages and forwards them in order, acking each on the source
// only after VRSky's publish was acknowledged. A failed publish is retried in
// place, so nothing behind it goes first. It returns when ctx is cancelled,
// the connection closes, a pull fails, or a message cannot be published at
// all; what it still holds is handed back at once, in order.
func (c *natsConsumer) drain(ctx context.Context, connID, tenantID string, cfg *NATSConfig, s *session, logger *slog.Logger) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.nc.IsClosed() || s.done.Err() != nil {
			return sdk.Retriable(errors.New("the connection to the NATS server closed"))
		}
		msgs, err := pull(s, c.pullWait(cfg))
		if err != nil {
			return classifyJS(err, s.async, "pull from durable "+cfg.Durable)
		}
		if len(msgs) == 0 {
			// A pull the user may not send is not answered at all: it looks
			// like an empty stream. The server's permission violation is
			// the only sign, and it makes the connection end.
			if v := s.async.takePermission(); v != nil {
				return sdk.Permanent(fmt.Errorf("pull from durable %s: the NATS user may not do this%s: %w", cfg.Durable, permissionHint(v, s.async), v))
			}
		}
		for i, m := range msgs {
			if ctx.Err() != nil {
				// Stopping: no publish attempt for what is still held.
				for _, r := range msgs[i:] {
					_ = r.Nak()
				}
				return ctx.Err()
			}
			if err := c.forwardWithRetry(ctx, s, connID, tenantID, msgs[i:], logger); err != nil {
				for _, r := range msgs[i:] {
					_ = r.Nak()
				}
				return err
			}
			if err := c.ack(m, logger); err != nil {
				// A user that may not publish to $JS.ACK never acks anything:
				// every message would come back each AckWait and be published
				// again once VRSky's dedup window has passed. Stop instead.
				if v := s.async.takePermission(); v != nil {
					for _, r := range msgs[i+1:] {
						_ = r.Nak()
					}
					return sdk.Permanent(fmt.Errorf("the NATS user may not ack messages on durable %q (it needs publish on $JS.ACK.%s.>): %w", cfg.Durable, cfg.Stream, v))
				}
			}
		}
	}
}

// pull returns the next messages, every pull request closed by the time it
// returns. That matters: while a request is open the server redelivers into
// it whatever ran out of AckWait, so a held message would come round again
// behind newer ones. It takes what is there without waiting, and only when
// that is nothing does it wait (up to wait) for one message. One gap is
// left: jetstream gives up on a no-wait pull after 1 s without an answer, and
// what the server sends after that lands nowhere and returns after AckWait.
// That takes a link slower than a second. The wait
// follows the connection, not the loop's context: a message it receives
// during a stop is handed back rather than left in flight.
func pull(s *session, wait time.Duration) ([]jetstream.Msg, error) {
	batch, err := s.cons.FetchNoWait(s.batch)
	if err != nil {
		return nil, err
	}
	msgs, err := collect(batch)
	if err != nil || len(msgs) > 0 {
		return msgs, err
	}
	if s.maxWait > 0 && s.maxWait < wait {
		wait = s.maxWait
	}
	fctx, cancel := context.WithTimeout(s.done, wait)
	defer cancel()
	batch, err = s.cons.Fetch(1, jetstream.FetchContext(fctx))
	if err != nil {
		return nil, err
	}
	return collect(batch)
}

// collect reads a batch to its end. Its own expiry or the connection's end
// is not an error: the caller sees the closed connection next.
func collect(batch jetstream.MessageBatch) ([]jetstream.Msg, error) {
	var out []jetstream.Msg
	for m := range batch.Messages() {
		out = append(out, m)
	}
	err := batch.Error()
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		err = nil
	}
	return out, err
}

// forwardWithRetry publishes held[0] into the pipeline, retrying it with a
// growing wait while VRSky's publish fails. Each wait tells the server that
// every held message is in progress, so no AckWait runs out and no delivery
// is spent while VRSky is down. It gives up on a stop, a closed connection,
// or an error no retry can fix (a message too large for VRSky's NATS), which
// ends the connection with status error rather than blocking it unseen.
func (c *natsConsumer) forwardWithRetry(ctx context.Context, s *session, connID, tenantID string, held []jetstream.Msg, logger *slog.Logger) error {
	m := held[0]
	wait := c.retryDelay
	// The longest wait stays under half the durable's AckWait, so the
	// in-progress signal arrives before it runs out.
	longest := c.maxBackoff
	if info := s.cons.CachedInfo(); info != nil && info.Config.AckWait > 0 && info.Config.AckWait/2 < longest {
		longest = max(info.Config.AckWait/2, 100*time.Millisecond)
	}
	for attempt := 1; ; attempt++ {
		err := c.forward(ctx, connID, tenantID, m)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if sdk.IsPermanent(err) || errors.Is(err, nats.ErrMaxPayload) {
			return sdk.Permanent(fmt.Errorf("a message on %s cannot be published into the pipeline: %w", m.Subject(), err))
		}
		logger.Error("Publish into the pipeline failed; retrying the same message", "error", err, "attempt", attempt, "retry_in", wait)
		for _, h := range held {
			_ = h.InProgress()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done.Done():
			return sdk.Retriable(errors.New("the connection to the NATS server closed while a publish was retried"))
		case <-time.After(wait):
		}
		wait = min(2*wait, longest)
	}
}

// ack confirms m on the source after its publish. Not bound to the loop's
// context: a publish that went through just before a stop still gets its
// ack, which saves a redelivery.
func (c *natsConsumer) ack(m jetstream.Msg, logger *slog.Logger) error {
	actx, cancel := context.WithTimeout(context.Background(), ackTimeout)
	defer cancel()
	err := m.DoubleAck(actx)
	if err != nil {
		// Redelivered later; the envelope id makes the repeat a no-op.
		logger.Warn("Ack to the NATS server failed after the publish; the message may be redelivered", "error", err)
	}
	return err
}

// sourceKey is the idempotency key of a source message: "id:" and its
// Nats-Msg-Id, or "seq:" and the stream name and sequence when the publisher
// set none. The prefixes keep a publisher's Msg-Id "RH_EVENTS:5" apart from
// the fifth message of a stream without one.
func sourceKey(m jetstream.Msg) (key string, meta *jetstream.MsgMetadata, err error) {
	meta, err = m.Metadata()
	if err != nil {
		return "", nil, fmt.Errorf("read message metadata: %w", err)
	}
	if id := m.Headers().Get(nats.MsgIdHdr); id != "" {
		return "id:" + id, meta, nil
	}
	return "seq:" + meta.Stream + ":" + strconv.FormatUint(meta.Sequence.Stream, 10), meta, nil
}

// forward publishes one source message into the pipeline.
func (c *natsConsumer) forward(ctx context.Context, connID, tenantID string, m jetstream.Msg) error {
	key, meta, err := sourceKey(m)
	if err != nil {
		return err
	}
	body := m.Data()
	env := envelope.New()
	env.ID = envelopeID(connID, key)
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = m.Headers().Get("Content-Type")
	if env.ContentType == "" {
		env.ContentType = detectContentType(body)
	}
	env.Source = "nats-consumer"
	env.Payload = body
	env.PayloadSize = int64(len(body))
	env.StepHistory = []string{"nats-consumer"}
	env.Metadata = map[string]interface{}{
		"nats_subject":    m.Subject(),
		"nats_stream":     meta.Stream,
		"nats_stream_seq": meta.Sequence.Stream,
	}
	if id := m.Headers().Get(nats.MsgIdHdr); id != "" {
		env.Metadata["nats_msg_id"] = id
	}
	// Bounded: a publish that gets no answer is a failed one, and retried.
	limit := c.publishTimeout
	if limit <= 0 {
		limit = publishTimeout
	}
	pctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return c.publish(pctx, env)
}

// --- DB helpers ---

// getConfig loads the connection's NATS consumer node. mine is false when the
// connection has none. err is a configuration error for a node that is ours:
// a plaintext credential, an unreadable block or a secret that will not
// resolve.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (c *natsConsumer) getConfig(ctx context.Context, connectionID, tenantID string) (cfg *NATSConfig, mine bool, err error) {
	var nodesJSON json.RawMessage
	if err := c.db.QueryRowContext(ctx,
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON); err != nil {
		c.logger.Debug("connection not found", "error", err, "connection_id", connectionID)
		return nil, false, nil
	}
	var nodes []node
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		c.logger.Warn("parse nodes", "error", err, "connection_id", connectionID)
		return nil, false, nil
	}
	for _, n := range nodes {
		if n.Type != "consumer" {
			continue
		}
		var nc nodeConfig
		if json.Unmarshal(n.Config, &nc) != nil || nc.Type != "nats" {
			continue
		}
		if len(nc.NATS) == 0 || string(nc.NATS) == "null" {
			return nil, true, errors.New("the nats settings block is missing")
		}
		// Checked before resolving: after it, a resolved secret and an
		// inline value look the same.
		if err := plaintextCredentials(nc.NATS); err != nil {
			return nil, true, err
		}
		// Only the credentials and the CA resolve; a stray *_secret_id is
		// dropped and a {secret:} placeholder refused (#853).
		resolved, err := crypto.ResolveBlockSecrets(ctx, crypto.NewSQLSecretReader(c.db), tenantID, "nats", nc.NATS)
		if err != nil {
			// The resolver's message names the field, never a value.
			return nil, true, fmt.Errorf("resolve stored credentials: %w", err)
		}
		cfg = &NATSConfig{}
		if err := json.Unmarshal(resolved, cfg); err != nil {
			return nil, true, errors.New("the nats settings block is not valid JSON for this connector")
		}
		cfg.normalise()
		return cfg, true, nil
	}
	return nil, false, nil
}

// writeStatus records a status; reason is last_error for status error and is
// cleared on running.
func (c *natsConsumer) writeStatus(connectionID, tenantID, status, reason string, logger *slog.Logger) {
	var err error
	switch status {
	case "running":
		_, err = c.db.Exec(`UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW(), last_error = NULL WHERE id = $2 AND tenant_id = $3`,
			status, connectionID, tenantID)
	case "error":
		_, err = c.db.Exec(`UPDATE connections SET status = $1, last_error = $2, stopped_at = NOW(), updated_at = NOW() WHERE id = $3 AND tenant_id = $4`,
			status, reason, connectionID, tenantID)
	default:
		_, err = c.db.Exec(`UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`,
			status, connectionID, tenantID)
	}
	if err != nil {
		logger.Warn("Could not update the pipeline status", "status", status, "error", err)
	}
}

// sleepCtx waits for d or ctx cancellation; returns false if ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func detectContentType(data []byte) string {
	if len(data) > 0 && (data[0] == '{' || data[0] == '[') {
		return "application/json"
	}
	return "application/octet-stream"
}
