package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/doneset"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// saveEvery is how many publishes a poll makes between saves of the done set,
// besides the save at its end: a crash in a long poll publishes at most this
// many objects again (#538).
const saveEvery = 32

// fallbackNodeID keys the done set of a cloud-storage consumer node that has
// no id.
const fallbackNodeID = "cloud-storage-consumer"

// cloudConsumer watches a cloud-storage bucket per active connection, fetches
// new objects and publishes them into the pipeline. SDK Consumer: Configure
// wires deps, Run subscribes to command subjects and blocks, Stop cancels
// pollers.
type cloudConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	// publishStream is non-nil only when the SDK selected the streaming path
	// (ADR 0001) — i.e. this worker has a payload store. inlineMax is the size
	// above which the SDK would offload anyway, so it's the point where reading
	// the object into memory stops being worthwhile.
	publishStream sdk.PublishStreamFunc
	inlineMax     int

	// newStore opens an ObjectStore. Defaulted to objectstore.New in Configure;
	// tests inject a fake so the poller runs without a live bucket.
	newStore storeFactory
	// newEvents opens an eventSource (event mode). Defaulted to newEventSource
	// in Configure; tests inject a fake.
	newEvents eventSourceFactory

	// checkpoints keeps each poller's done set across restarts (#538).
	// Defaulted to Postgres in Configure; nil keeps it in memory only.
	checkpoints checkpoint.Store

	active map[string]*activeConn
	mu     sync.RWMutex
	// starts holds the claim of each start in progress (#397); guarded by mu.
	starts sdk.StartGuard

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// cloudConfig is the per-node configuration (config.cloud_storage). It embeds
// the provider-agnostic objectstore.Config (provider, bucket, prefix, creds,
// SSE) and adds the consumer-only poll/after-action fields. Credential fields
// are resolved from *_secret_id references before use.
type cloudConfig struct {
	objectstore.Config

	Mode                string `json:"mode"`                  // "poll" (default) | "event"
	EventQueueURL       string `json:"event_queue_url"`       // SQS queue URL (S3 event mode)
	EventQueueName      string `json:"event_queue_name"`      // Azure Storage Queue name (Azure event mode)
	EventSubscription   string `json:"event_subscription"`    // Pub/Sub subscription, path or bare name (GCS event mode)
	EventProject        string `json:"event_project"`         // GCP project ID (GCS event mode, when subscription is a bare name)
	EventEndpoint       string `json:"event_endpoint"`        // optional event-broker endpoint override (LocalStack SQS / Azurite queue / Pub/Sub emulator); NOT the object-store endpoint
	FilePattern         string `json:"file_pattern"`          // optional glob against the object base name, e.g. *.csv
	PollIntervalSeconds int    `json:"poll_interval_seconds"` // <= 0 means run once
	AfterAction         string `json:"after_action"`          // "delete" | "move" | "none" (default none)
	MovePrefix          string `json:"move_prefix"`           // destination prefix when after_action=move

	// nodeID is the consumer node's id, the key of its done set (#538).
	nodeID string
}

type nodeConfig struct {
	Type  string       `json:"type"`
	Cloud *cloudConfig `json:"cloud_storage"`
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
func (s *cloudConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("cloud-storage-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	s.db = res.DB
	s.nc = res.NATS
	s.logger = res.Logger
	s.inlineMax = res.InlineMaxBytes()
	s.active = make(map[string]*activeConn)
	if s.newStore == nil {
		s.newStore = objectstore.New
	}
	if s.newEvents == nil {
		s.newEvents = newEventSource
	}
	if s.checkpoints == nil {
		s.checkpoints = checkpoint.NewPostgresStore(res.DB)
	}
	s.RegisterHTTPHandler("/test-connection/", s.handleTestConnection())
	s.RegisterHTTPHandler("/sample-data/", s.handleSampleData())
	res.Health.SetReady(true)
	return nil
}

// RunStream is Run with the large-payload streaming path enabled (ADR 0001).
// The SDK calls it instead of Run when a payload store is configured; ingest
// then streams objects larger than the inline threshold straight from the
// bucket into the pipeline, so a multi-GB object is never held in memory.
func (s *cloudConsumer) RunStream(ctx context.Context, publish sdk.PublishFunc, publishStream sdk.PublishStreamFunc) error {
	s.publishStream = publishStream
	return s.Run(ctx, publish)
}

// Run subscribes to the connection command subjects and blocks until ctx is
// cancelled. Per-connection polling is driven from the command handlers.
func (s *cloudConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
	s.publish = publish

	startSub, err := s.nc.Subscribe("vrsky.commands.*.connection.start", s.handleStartCommand)
	if err != nil {
		return fmt.Errorf("subscribe start commands: %w", err)
	}
	s.startSub = startSub

	stopSub, err := s.nc.Subscribe("vrsky.commands.*.connection.stop", s.handleStopCommand)
	if err != nil {
		return fmt.Errorf("subscribe stop commands: %w", err)
	}
	s.stopSub = stopSub

	s.logger.Info("Subscribed to NATS command topics")
	// Bring back what the database says is running: start/stop are NATS commands
	// and are not persisted, so without this a restart silently stopped every
	// pipeline on this service until someone redeployed it.
	sdk.RestoreRunning(ctx, s.db, s.logger, "cloud_storage", s.startConnection)

	<-ctx.Done()
	return nil
}

// Stop cancels all pollers. The SDK runner calls this after Run returns.
func (s *cloudConsumer) Stop(ctx context.Context) error {
	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}
	s.mu.Lock()
	for id, ac := range s.active {
		s.logger.Info("Stopping cloud-storage poller", "connection_id", id)
		ac.cancel()
	}
	s.active = make(map[string]*activeConn)
	// A start still in flight must not leave a poller nobody will cancel.
	s.starts.StopAllLocked()
	s.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
	return nil
}

func (s *cloudConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("parse start command", "error", err)
		return
	}
	s.startConnection(context.Background(), cmd.ConnectionID, cmd.TenantID)
}

// startConnection is the start command's work, also what RestoreRunning calls on
// boot for every pipeline the database says is running (plans/stable-connections.md).
func (s *cloudConsumer) startConnection(ctx context.Context, connectionID, tenantID string) {
	_ = ctx
	logger := s.logger.With("connection_id", connectionID, "tenant_id", tenantID)

	// Restore and the start-command handler call this on different goroutines.
	// The check and the claim are one critical section, so a second start for
	// the same connection stops here instead of starting a second poller (#397).
	// A start refused while another is in flight is not lost: it runs again if
	// that one gives up without activating (#424). The refused call returns
	// right away, so the retry gets its caller's ctx, before any reassignment.
	claim, ok := s.starts.ReserveOrRetry(&s.mu, tenantID, connectionID, func() bool { _, ok := s.active[connectionID]; return ok },
		func() { s.startConnection(ctx, connectionID, tenantID) })
	if !ok {
		logger.Warn("cloud-storage poller already running or starting")
		return
	}
	// Frees the claim on every early return below; a no-op once active.
	defer claim.Release()

	cfg, ok := s.getConfig(connectionID, tenantID)
	if !ok {
		logger.Debug("Not a cloud-storage consumer for this connection, ignoring")
		return
	}
	if cfg.Bucket == "" {
		logger.Error("cloud-storage config incomplete (need bucket)")
		return
	}
	if cfg.AfterAction == "move" && cfg.MovePrefix == "" {
		logger.Error("after_action=move requires move_prefix")
		return
	}
	if err := cfg.validateMoveOutsideListing(); err != nil {
		logger.Error("invalid move_prefix", "error", err)
		return
	}
	if cfg.Mode == "event" {
		if err := cfg.validateEventConfig(); err != nil {
			logger.Error("invalid event-mode config", "error", err)
			return
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	ac := &activeConn{cancel: cancel}
	// A stop that arrived during the work above has taken the claim back and wins.
	if !claim.Activate(func() { s.active[connectionID] = ac }) {
		cancel()
		logger.Info("Start abandoned: a stop arrived while it was in progress")
		return
	}
	// Through the guard's status order: a stop that cancelled the connection
	// since Activate and wrote "stopped" first makes this a no-op (#423).
	claim.RunningStatus().Write(func() { _ = s.updateConnectionStatus(connectionID, tenantID, "running") })

	if cfg.Mode == "event" {
		logger.Info("Starting cloud-storage event loop",
			"provider", cfg.providerOrDefault(), "bucket", cfg.Bucket, "event_target", cfg.eventTarget())
		go s.runEventLoop(ctx, ac, connectionID, tenantID, cfg)
		return
	}
	logger.Info("Starting cloud-storage poller",
		"provider", cfg.providerOrDefault(), "bucket", cfg.Bucket, "prefix", cfg.Prefix, "after_action", cfg.AfterAction)
	go s.runPoller(ctx, ac, connectionID, tenantID, cfg)
}

// activeConn is one start's entry in the active map. The poller gets the same
// pointer, so when it ends on its own it can tell whether the entry is still
// its own (#408).
type activeConn struct {
	cancel context.CancelFunc
}

// dropActive removes connID from the active map if the entry is still own.
// After a stop and a new start it belongs to the newer poller, and an old
// poller finishing must leave it alone, or the next start runs a second poller
// beside the new one (#408).
//
// It reports whether the entry was own. Only then may the poller write its
// final status; otherwise the newer start has written "running" and owns the
// status too (#456). The write goes through the returned StatusWrite, taken
// in the same critical section, so it cannot land over the "running" of a
// start that activates after the drop (#423).
func (s *cloudConsumer) dropActive(connID, tenantID string, own *activeConn) (sdk.StatusWrite, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[connID] != own {
		return sdk.StatusWrite{}, false
	}
	delete(s.active, connID)
	return s.starts.StopStatusLocked(tenantID, connID), true
}

func (s *cloudConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("parse stop command", "error", err)
		return
	}
	s.mu.Lock()
	// The stop must name the tenant the connection is starting or active for,
	// in its payload and on its subject; one naming another tenant changes
	// nothing (#498). A start still in progress gives up instead of finishing
	// after the stop (#397).
	starting, err := s.starts.StopForTenantLocked(msg.Subject, cmd.TenantID, cmd.ConnectionID)
	if err != nil {
		s.mu.Unlock()
		s.logger.Warn("Ignoring stop command for another tenant", "connection_id", cmd.ConnectionID, "error", err)
		return
	}
	ac, exists := s.active[cmd.ConnectionID]
	if exists {
		ac.cancel()
		delete(s.active, cmd.ConnectionID)
	}
	// Taken with the stop, and only when there is something to write: of this
	// "stopped" write and a start's "running" write, only the one for the later
	// of the stop and the start's Activate is left standing, whichever reaches
	// the database first (#423).
	var stopped sdk.StatusWrite
	if exists {
		stopped = s.starts.StopStatusLocked(cmd.TenantID, cmd.ConnectionID)
	}
	s.mu.Unlock()
	if starting {
		s.logger.Info("Stop arrived while the connection was starting; the start is abandoned", "connection_id", cmd.ConnectionID)
	}
	if exists {
		stopped.Write(func() { _ = s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped") })
		s.logger.Info("cloud-storage poller stopped", "connection_id", cmd.ConnectionID)
	}
}

// runPoller opens the store once, fetches immediately, then on the configured
// interval. List/Get errors are logged and retried on the next tick.
func (s *cloudConsumer) runPoller(ctx context.Context, own *activeConn, connID, tenantID string, cfg *cloudConfig) {
	logger := s.logger.With("connection_id", connID)

	store, err := s.newStore(ctx, &cfg.Config)
	if err != nil {
		logger.Error("open cloud-storage backend failed", "error", err)
		s.finishConn(own, connID, tenantID, "error")
		return
	}

	// done holds the objects already published, saved per tenant, connection
	// and node, so neither the next tick nor the next start publishes them
	// again (#538).
	done := s.doneSet(connID, tenantID, cfg)

	s.pollOnce(ctx, connID, tenantID, store, cfg, done, logger)

	// poll_interval_seconds <= 0 means run once (matches the UI label "0 = once").
	if cfg.PollIntervalSeconds <= 0 {
		logger.Info("cloud-storage one-shot poll complete")
		// A one-shot that could not read its done set published nothing.
		if !done.Loaded() {
			s.finishConn(own, connID, tenantID, "error")
			return
		}
		s.finishConn(own, connID, tenantID, "stopped")
		return
	}

	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollOnce(ctx, connID, tenantID, store, cfg, done, logger)
		}
	}
}

// doneSet returns the poller's done set, keyed by tenant, connection and node,
// for the bucket and prefix cfg names. A saved set for another provider,
// endpoint, account, bucket or prefix is not used.
func (s *cloudConsumer) doneSet(connID, tenantID string, cfg *cloudConfig) *doneset.Set {
	nodeID := cfg.nodeID
	if nodeID == "" {
		nodeID = fallbackNodeID
	}
	scope := strings.Join([]string{cfg.providerOrDefault(), cfg.Endpoint, cfg.AccountName, cfg.Bucket, cfg.Prefix}, "|")
	return doneset.New(s.checkpoints, doneset.Key{TenantID: tenantID, ConnectionID: connID, NodeID: nodeID}, scope)
}

// pollOnce lists the bucket under the prefix and fetches+publishes+after-actions
// each object that is not done: new, or replaced since it was published
// (#538). Until the saved done set has been read nothing is published:
// without it every object in the bucket would look new.
func (s *cloudConsumer) pollOnce(ctx context.Context, connID, tenantID string, store objectstore.ObjectStore, cfg *cloudConfig, done *doneset.Set, logger *slog.Logger) {
	if !done.Loaded() {
		found, err := done.Load(ctx)
		if err != nil {
			logger.Error("Could not read which objects were already published; publishing nothing until it can be read", "error", err)
			return
		}
		if found {
			logger.Info("Resuming: objects already published are not published again", "already_done", done.Len())
		} else {
			logger.Info("First start for this bucket and prefix: every object under it is published")
		}
	}

	objs, err := store.List(ctx, cfg.Prefix)
	if err != nil {
		logger.Error("cloud-storage list failed; will retry next cycle", "prefix", cfg.Prefix, "error", err)
		return
	}

	listing := make(map[string]doneset.Stamp, len(objs))
	for _, o := range objs {
		listing[o.Key] = doneset.StampOf(o.Size, o.LastModified)
	}
	// Objects gone from the listing leave the done set: added again, they are
	// published again.
	done.Observe(listing)
	// Also when a stop cuts the poll short: what it published is saved.
	defer s.saveDone(ctx, done, logger)

	refreshed := false
	sinceSave := 0
	for _, o := range objs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		stamp := listing[o.Key]
		if done.Done(o.Key, stamp) {
			continue
		}
		if !cfg.matchesFilePattern(o.Key) {
			continue
		}
		if !refreshed {
			// Once per poll, before the first publish: another poller of
			// this connection may have published it since this one loaded.
			refreshed = true
			if err := done.Refresh(ctx); err != nil {
				logger.Warn("Could not re-read which objects were already published", "error", err)
			} else if done.Done(o.Key, stamp) {
				continue
			}
		}
		if err := s.ingestObject(ctx, connID, tenantID, store, cfg, o.Key, logger); err != nil {
			logger.Error("ingest failed; will retry next cycle", "key", o.Key, "error", err)
			continue // leave the object in place
		}
		done.Mark(o.Key, stamp)
		if sinceSave++; sinceSave >= saveEvery {
			s.saveDone(ctx, done, logger)
			sinceSave = 0
		}
	}
}

// saveDone saves the done set; a failure is retried at the next save.
func (s *cloudConsumer) saveDone(ctx context.Context, done *doneset.Set, logger *slog.Logger) {
	if err := done.Save(ctx); err != nil {
		logger.Error("Could not save which objects were published; retrying at the next save", "error", err)
	}
}

// runEventLoop drives event-driven ingestion: long-poll the event source,
// ingest each referenced object, and ack the message only after a successful
// publish (at-least-once). Runs until ctx is cancelled (stop command/shutdown).
func (s *cloudConsumer) runEventLoop(ctx context.Context, own *activeConn, connID, tenantID string, cfg *cloudConfig) {
	logger := s.logger.With("connection_id", connID, "mode", "event")

	store, err := s.newStore(ctx, &cfg.Config)
	if err != nil {
		logger.Error("open cloud-storage backend failed", "error", err)
		s.finishConn(own, connID, tenantID, "error")
		return
	}
	src, err := s.newEvents(ctx, cfg)
	if err != nil {
		logger.Error("open event source failed", "error", err)
		s.finishConn(own, connID, tenantID, "error")
		return
	}

	logger.Info("cloud-storage event loop started", "event_target", cfg.eventTarget())
	for {
		if ctx.Err() != nil {
			return
		}
		msgs, err := src.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Error("event receive failed; backing off", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, m := range msgs {
			if ctx.Err() != nil {
				return
			}
			ok := true
			for _, key := range m.objectKeys {
				// Same selection as poll mode, which lists under prefix and
				// then applies file_pattern (#594). A key outside it is not
				// an error: it is skipped and the message is acked below, so
				// the queue does not redeliver it. Nothing is published,
				// moved or deleted for it.
				if !cfg.wantsEventKey(key) {
					logger.Debug("event key outside prefix or file_pattern; skipped",
						"key", key, "prefix", cfg.Prefix, "file_pattern", cfg.FilePattern)
					continue
				}
				if err := s.ingestObject(ctx, connID, tenantID, store, cfg, key, logger); err != nil {
					logger.Error("ingest failed; leaving message for redelivery", "key", key, "error", err)
					ok = false
				}
			}
			// Ack only when every referenced object was published (and an empty
			// message — e.g. s3:TestEvent, or one whose keys were all skipped
			// above — is acked to drain it).
			if ok {
				if err := src.Ack(ctx, m.ackHandle); err != nil {
					logger.Warn("event ack failed", "error", err)
				}
			}
		}
	}
}

// ingestObject fetches one object, publishes it into the pipeline, and applies
// the after-action. Shared by poll and event modes.
func (s *cloudConsumer) ingestObject(ctx context.Context, connID, tenantID string, store objectstore.ObjectStore, cfg *cloudConfig, key string, logger *slog.Logger) error {
	size, streamed, err := s.fetchAndPublish(ctx, connID, tenantID, store, cfg, key)
	if err != nil {
		return err
	}
	logger.Info("cloud-storage object ingested", "key", key, "size", size, "streamed", streamed)
	if err := s.afterAction(ctx, store, cfg, key); err != nil {
		logger.Warn("after-action failed", "key", key, "action", cfg.AfterAction, "error", err)
	}
	return nil
}

// fetchAndPublish reads one object and publishes it, streaming when this worker
// supports it and the object is larger than the inline threshold — so a
// multi-GB object transfers with a bounded buffer instead of being read into
// memory whole. Returns the payload size and whether the streaming path was used.
func (s *cloudConsumer) fetchAndPublish(ctx context.Context, connID, tenantID string, store objectstore.ObjectStore, cfg *cloudConfig, key string) (int64, bool, error) {
	// inlineMax <= 0 means the SDK has offload switched off entirely (everything
	// travels inline), so streaming here would contradict that configuration —
	// and a negative value would panic when sizing the peek buffer below.
	if s.publishStream == nil || s.inlineMax <= 0 {
		data, ct, err := store.Get(ctx, key)
		if err != nil {
			return 0, false, fmt.Errorf("get: %w", err)
		}
		if ct == "" {
			ct = detectContentType(path.Base(key), data)
		}
		if err := s.publishObject(ctx, connID, tenantID, cfg.Bucket, key, ct, data); err != nil {
			return 0, false, fmt.Errorf("publish: %w", err)
		}
		return int64(len(data)), false, nil
	}

	rc, ct, err := store.GetStream(ctx, key)
	if err != nil {
		return 0, false, fmt.Errorf("get: %w", err)
	}
	defer rc.Close()

	// Read up to one byte past the inline threshold. That single read answers
	// both questions: whether the object is small enough to publish inline, and
	// what bytes detectContentType should sniff. Nothing larger is ever buffered.
	head := make([]byte, s.inlineMax+1)
	n, rerr := io.ReadFull(rc, head)
	if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		return 0, false, fmt.Errorf("read: %w", rerr)
	}
	head = head[:n]
	if ct == "" {
		ct = detectContentType(path.Base(key), head)
	}

	if n <= s.inlineMax {
		// Whole object already in hand — take the normal path so small objects
		// keep behaving exactly as before (including the UI preview).
		if err := s.publishObject(ctx, connID, tenantID, cfg.Bucket, key, ct, head); err != nil {
			return 0, false, fmt.Errorf("publish: %w", err)
		}
		return int64(n), false, nil
	}

	env := s.newObjectEnvelope(connID, tenantID, cfg.Bucket, key, ct)
	body := io.MultiReader(bytes.NewReader(head), rc)
	if err := s.publishStream(ctx, env, body); err != nil {
		return 0, false, fmt.Errorf("publish stream: %w", err)
	}
	return env.PayloadSize, true, nil
}

// finishConn removes the connection from the active set and marks its terminal
// status, unless a newer start owns the entry, and with it the status, by now
// (#408, #456).
func (s *cloudConsumer) finishConn(own *activeConn, connID, tenantID, status string) {
	write, ok := s.dropActive(connID, tenantID, own)
	if !ok {
		s.logger.Info("A newer start owns the connection; leaving its status alone",
			"connection_id", connID, "status", status)
		return
	}
	write.Write(func() { _ = s.updateConnectionStatus(connID, tenantID, status) })
}

// afterAction applies the configured post-ingest action to a fetched object.
func (s *cloudConsumer) afterAction(ctx context.Context, store objectstore.ObjectStore, cfg *cloudConfig, key string) error {
	switch cfg.AfterAction {
	case "delete":
		return store.Delete(ctx, key)
	case "move":
		if cfg.MovePrefix == "" {
			return fmt.Errorf("after_action=move but move_prefix is empty")
		}
		dst := path.Join(cfg.MovePrefix, path.Base(key))
		// Copying an object onto its own key and then deleting it loses it on
		// Azure and GCS (#567). Poll mode refuses such a config at start; in
		// event mode the notification decides which keys arrive, so a moved
		// object can come back here. Trimming leading slashes is deliberately
		// conservative: it also refuses some real S3/GCS moves (a.csv to
		// /a.csv), which fails safe by leaving the object in place.
		if strings.TrimLeft(dst, "/") == strings.TrimLeft(key, "/") {
			return fmt.Errorf("object %q is already in move_prefix %q; not moved", key, cfg.MovePrefix)
		}
		if err := store.Copy(ctx, key, dst); err != nil {
			return fmt.Errorf("copy to move_prefix: %w", err)
		}
		return store.Delete(ctx, key)
	default: // "none" / "" / "leave"
		return nil
	}
}

// newObjectEnvelope builds the envelope for one ingested object, minus the
// payload — shared by the inline and streaming paths so both carry identical
// metadata.
func (s *cloudConsumer) newObjectEnvelope(connID, tenantID, bucket, key, contentType string) *envelope.Envelope {
	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = contentType
	env.Source = "cloud-storage:" + key
	env.StepHistory = []string{"cloud-storage-consumer"}
	env.Metadata = map[string]interface{}{"object_key": key, "bucket": bucket, "filename": path.Base(key)}
	return env
}

func (s *cloudConsumer) publishObject(ctx context.Context, connID, tenantID, bucket, key, contentType string, data []byte) error {
	env := s.newObjectEnvelope(connID, tenantID, bucket, key, contentType)
	env.Payload = data
	env.PayloadSize = int64(len(data))
	return s.publish(ctx, env)
}

// --- DB helpers ---

// getConfig loads the connection, resolves *_secret_id references to plaintext,
// and extracts the cloud-storage consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (s *cloudConsumer) getConfig(connectionID, tenantID string) (*cloudConfig, bool) {
	var nodesJSON json.RawMessage
	err := s.db.QueryRow(
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON)
	if err != nil {
		s.logger.Debug("connection not found", "error", err, "connection_id", connectionID)
		return nil, false
	}

	resolved, err := crypto.ResolveSecretsInJSON(context.Background(), crypto.NewSQLSecretReader(s.db), tenantID, nodesJSON)
	if err != nil {
		s.logger.Error("resolve secrets", "error", err, "connection_id", connectionID)
		return nil, false
	}

	var nodes []node
	if err := json.Unmarshal(resolved, &nodes); err != nil {
		s.logger.Warn("parse nodes", "error", err, "connection_id", connectionID)
		return nil, false
	}
	for _, n := range nodes {
		if n.Type != "consumer" {
			continue
		}
		var nc nodeConfig
		if err := json.Unmarshal(n.Config, &nc); err != nil {
			continue
		}
		if nc.Type == "cloud_storage" && nc.Cloud != nil {
			nc.Cloud.nodeID = n.ID
			return nc.Cloud, true
		}
	}
	return nil, false
}

func (s *cloudConsumer) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := s.db.Exec(query, status, connectionID, tenantID)
	return err
}

func (c *cloudConfig) providerOrDefault() string {
	if c.Provider == "" {
		return objectstore.ProviderS3
	}
	return c.Provider
}

// validateEventConfig checks that the provider-specific event-mode identifier is
// set (SQS queue URL for S3, queue name for Azure, subscription for GCS).
func (c *cloudConfig) validateEventConfig() error {
	switch c.providerOrDefault() {
	case objectstore.ProviderAzure:
		if c.EventQueueName == "" {
			return fmt.Errorf("mode=event (azure) requires event_queue_name")
		}
	case objectstore.ProviderGCS:
		if c.EventSubscription == "" {
			return fmt.Errorf("mode=event (gcs) requires event_subscription")
		}
		// A bare subscription name (no path) needs a project to build the full
		// subscription path; catch it here rather than failing later in newEventSource.
		if !strings.Contains(c.EventSubscription, "/") && c.EventProject == "" {
			return fmt.Errorf("mode=event (gcs) requires event_project when event_subscription is a bare name")
		}
	default: // s3
		if c.EventQueueURL == "" {
			return fmt.Errorf("mode=event (s3) requires event_queue_url")
		}
	}
	return nil
}

// matchesFilePattern reports whether the object's base name matches
// file_pattern; an empty pattern matches everything. A pattern path.Match
// rejects (a malformed glob) matches nothing, as in poll mode before #594.
func (c *cloudConfig) matchesFilePattern(key string) bool {
	if c.FilePattern == "" {
		return true
	}
	match, _ := path.Match(c.FilePattern, path.Base(key))
	return match
}

// wantsEventKey reports whether a key named by a bucket notification is one
// this connection ingests: under prefix and matching file_pattern, the same
// selection poll mode makes (#594). The providers match prefix as a plain
// string, not a folder, when they list, so it is a plain string prefix here
// too: prefix "in" selects "in-done/a.csv".
func (c *cloudConfig) wantsEventKey(key string) bool {
	return strings.HasPrefix(key, c.Prefix) && c.matchesFilePattern(key)
}

// validateMoveOutsideListing refuses an after_action=move whose moved objects
// prefix would select again (#567, #594). They would be published a second
// time and then moved onto their own key and deleted, which loses them on
// Azure and GCS.
//
// The providers match prefix as a plain string, not a folder: prefix "in"
// lists "in-done/a.csv" too. A move writes path.Join(move_prefix, base name),
// so every destination is dir+b with b a base name (not empty, no "/"). One of
// them is selected when dir starts with prefix (equal, under it, or an empty
// prefix), or when prefix starts with dir and the rest of prefix has no "/"
// (a base name starting with that rest is selected). Leading slashes are
// ignored on both sides, which refuses more rather than less.
//
// Poll mode selects by listing prefix. Event mode selects the keys of the
// bucket notification that are under prefix (wantsEventKey), so the same
// rule holds there: a moved object notifies as a new object, and prefix is
// what keeps it out. afterAction still refuses a move onto the object's own
// key, as a second line.
func (c *cloudConfig) validateMoveOutsideListing() error {
	if c.AfterAction != "move" || c.MovePrefix == "" {
		return nil
	}
	prefix := strings.TrimLeft(c.Prefix, "/")
	dir := strings.TrimLeft(path.Clean(c.MovePrefix), "/")
	if dir == "." {
		dir = ""
	}
	if dir != "" {
		dir += "/"
	}
	rest, under := strings.CutPrefix(prefix, dir)
	if !strings.HasPrefix(dir, prefix) && (!under || strings.Contains(rest, "/")) {
		return nil
	}
	if c.Prefix == "" {
		return fmt.Errorf("move_prefix %q is inside prefix \"\" (the whole bucket): moved objects would be published again", c.MovePrefix)
	}
	return fmt.Errorf("move_prefix %q is inside what prefix %q lists (prefix is matched as text, not as a folder; end it with \"/\" to read one folder): moved objects would be published again", c.MovePrefix, c.Prefix)
}

// eventTarget returns the provider-specific event identifier, for logging.
func (c *cloudConfig) eventTarget() string {
	switch c.providerOrDefault() {
	case objectstore.ProviderAzure:
		return c.EventQueueName
	case objectstore.ProviderGCS:
		return c.EventSubscription
	default:
		return c.EventQueueURL
	}
}

// detectContentType picks a MIME type from the object's base name, falling back
// to a light content sniff.
func detectContentType(name string, data []byte) string {
	switch {
	case hasSuffixFold(name, ".json"):
		return "application/json"
	case hasSuffixFold(name, ".xml"):
		return "application/xml"
	case hasSuffixFold(name, ".csv"):
		return "text/csv"
	case hasSuffixFold(name, ".txt"):
		return "text/plain"
	}
	if len(data) > 0 && (data[0] == '{' || data[0] == '[') {
		return "application/json"
	}
	return "application/octet-stream"
}

func hasSuffixFold(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return equalFold(s[len(s)-len(suffix):], suffix)
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
