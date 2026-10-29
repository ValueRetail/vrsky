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
	"strconv"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/doneset"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	dialMaxAttempts = 3
	dialBaseBackoff = time.Second
)

// saveEvery is how many publishes a poll makes between saves of the done set,
// besides the save at its end: a crash in a long poll publishes at most this
// many files again (#538).
const saveEvery = 32

// fallbackNodeID keys the done set of an SFTP consumer node that has no id.
const fallbackNodeID = "sftp-consumer"

// The settle gap is the least time between the two looks that must find a
// file unchanged before it is published (#562): the poll interval, but at
// least defaultSettleMin, so a WAN upload that stalls a couple of seconds is
// not taken as finished, and at most defaultSettleMax, so a long interval
// (a daily pickup) does not need the poller to live a whole interval: a pod
// parked every night, or restarted by a deploy, would never publish.
const (
	defaultSettleMin = 5 * time.Second
	defaultSettleMax = 30 * time.Second
)

// sftpConsumer watches a remote SFTP directory per active connection, fetches
// new files and publishes them into the pipeline. SDK Consumer: Configure wires
// deps, Run subscribes to command subjects and blocks, Stop cancels pollers.
type sftpConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	// publishStream is non-nil only when the SDK selected the streaming path
	// (ADR 0001) — i.e. this worker has a payload store. inlineMax is the size
	// above which the SDK offloads anyway, so it marks where buffering a remote
	// file into memory stops paying off.
	publishStream sdk.PublishStreamFunc
	inlineMax     int

	// dial opens an SFTP connection. Defaulted to realDial in Configure; tests
	// inject a fake so the poller runs without a live server.
	dial dialFunc

	// checkpoints keeps each poller's done set across restarts (#538).
	// Defaulted to Postgres in Configure; nil keeps it in memory only.
	checkpoints checkpoint.Store

	// now is the poller's clock for the stability check (#562); nil means
	// time.Now. settleMin and settleMax bound the settle gap; zero means
	// defaultSettleMin and defaultSettleMax. Tests set them.
	now       func() time.Time
	settleMin time.Duration
	settleMax time.Duration

	active map[string]*activeConn
	mu     sync.RWMutex
	// starts holds the claim of each start in progress (#397); guarded by mu.
	starts sdk.StartGuard

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// SFTPConfig is the per-node configuration (config.sftp). Password and
// PrivateKey are resolved from *_secret_id references before use.
type SFTPConfig struct {
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Username            string `json:"username"`
	Password            string `json:"password"`     // resolved from password_secret_id
	PrivateKey          string `json:"private_key"`  // resolved from private_key_secret_id
	HostKey             string `json:"host_key"`     // optional pinned host key (authorized_keys line)
	RemoteDir           string `json:"remote_dir"`   // directory to watch
	FilePattern         string `json:"file_pattern"` // optional glob, e.g. *.csv
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	AfterAction         string `json:"after_action"` // "delete" | "move" | "none" (default none)
	MoveDir             string `json:"move_dir"`     // destination when after_action=move

	// nodeID is the consumer node's id, the key of its done set (#538).
	nodeID string
}

type nodeConfig struct {
	Type string      `json:"type"`
	SFTP *SFTPConfig `json:"sftp"`
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
func (s *sftpConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("sftp-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	s.db = res.DB
	s.nc = res.NATS
	s.logger = res.Logger
	s.inlineMax = res.InlineMaxBytes()
	s.active = make(map[string]*activeConn)
	if s.dial == nil {
		s.dial = realDial
	}
	if s.checkpoints == nil {
		s.checkpoints = checkpoint.NewPostgresStore(res.DB)
	}
	s.RegisterHTTPHandler("/test-connection/", s.handleTestConnection())
	s.RegisterHTTPHandler("/sample-data/", s.handleSampleData())
	res.Health.SetReady(true)
	return nil
}

// RunStream is Run with large-file streaming enabled (ADR 0001). The SDK calls
// it instead of Run when a payload store is configured; remote files over the
// inline threshold are then streamed off the SFTP session straight into the
// pipeline instead of being read into memory whole.
func (s *sftpConsumer) RunStream(ctx context.Context, publish sdk.PublishFunc, publishStream sdk.PublishStreamFunc) error {
	s.publishStream = publishStream
	return s.Run(ctx, publish)
}

// Run subscribes to the connection command subjects and blocks until ctx is
// cancelled. Per-connection polling is driven from the command handlers.
func (s *sftpConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
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
	sdk.RestoreRunning(ctx, s.db, s.logger, "sftp", s.startConnection)

	<-ctx.Done()
	return nil
}

// Stop cancels all pollers. The SDK runner calls this after Run returns.
func (s *sftpConsumer) Stop(ctx context.Context) error {
	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}
	s.mu.Lock()
	for id, ac := range s.active {
		s.logger.Info("Stopping SFTP poller", "connection_id", id)
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

func (s *sftpConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("parse start command", "error", err)
		return
	}
	s.startConnection(context.Background(), cmd.ConnectionID, cmd.TenantID)
}

// startConnection is the start command's work, also what RestoreRunning calls on
// boot for every pipeline the database says is running (plans/stable-connections.md).
func (s *sftpConsumer) startConnection(ctx context.Context, connectionID, tenantID string) {
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
		logger.Warn("SFTP poller already running or starting")
		return
	}
	// Frees the claim on every early return below; a no-op once active.
	defer claim.Release()

	cfg, ok := s.getSFTPConfig(connectionID, tenantID)
	if !ok {
		logger.Debug("Not an SFTP consumer for this connection, ignoring")
		return
	}
	if cfg.Host == "" || cfg.Username == "" || cfg.RemoteDir == "" {
		logger.Error("SFTP config incomplete (need host, username, remote_dir)")
		return
	}
	if cfg.Password == "" && cfg.PrivateKey == "" {
		logger.Error("SFTP config has no credentials (set a password or a private key)")
		return
	}
	if cfg.HostKey == "" {
		logger.Warn("SFTP host_key not pinned; server identity will not be verified (set host_key in production)")
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

	logger.Info("Starting SFTP poller", "host", cfg.Host, "remote_dir", cfg.RemoteDir, "after_action", cfg.AfterAction)
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
func (s *sftpConsumer) dropActive(connID, tenantID string, own *activeConn) (sdk.StatusWrite, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[connID] != own {
		return sdk.StatusWrite{}, false
	}
	delete(s.active, connID)
	return s.starts.StopStatusLocked(tenantID, connID), true
}

func (s *sftpConsumer) handleStopCommand(msg *nats.Msg) {
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
		s.logger.Info("SFTP poller stopped", "connection_id", cmd.ConnectionID)
	}
}

// runPoller fetches once immediately then on the configured interval. Each
// cycle opens a fresh connection (natural reconnection) with bounded backoff.
func (s *sftpConsumer) runPoller(ctx context.Context, own *activeConn, connID, tenantID string, cfg *SFTPConfig) {
	logger := s.logger.With("connection_id", connID)
	// done holds the files already published, saved per tenant, connection
	// and node, so neither the next tick nor the next start publishes them
	// again (#538).
	done := s.doneSet(connID, tenantID, cfg)
	// pending holds the files seen but not yet published, until they stop
	// changing (#562). Memory only: after a restart the wait starts again,
	// one settle gap (at most 30 s) after the start's first poll.
	pending := make(map[string]sighting)

	var sched pollSchedule
	waiting, unreachable := s.pollOnce(ctx, connID, tenantID, cfg, done, pending, logger)
	sched.record(waiting, unreachable)

	// poll_interval_seconds <= 0 means run once (matches the UI label "0 = once").
	if cfg.PollIntervalSeconds <= 0 {
		// A file is published on the second look that finds it unchanged, so a
		// run that found something new looks once more, a settle gap later.
		// What is still changing then is left for the next run.
		if waiting > 0 {
			timer := time.NewTimer(s.settleGap(cfg))
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
				waiting, _ = s.pollOnce(ctx, connID, tenantID, cfg, done, pending, logger)
			}
			if waiting > 0 && ctx.Err() == nil {
				logger.Warn("SFTP one-shot left files that were still changing; the next run tries them again", "files", waiting)
			}
		}
		logger.Info("SFTP one-shot poll complete")
		// A one-shot that could not read its done set published nothing.
		status := "stopped"
		if !done.Loaded() {
			status = "error"
		}
		// Mark it stopped only if no newer start owns the entry, and with it
		// the status, by now (#408, #456).
		stopped, mine := s.dropActive(connID, tenantID, own)
		if !mine {
			logger.Info("A newer start owns the connection; leaving its status alone")
			return
		}
		stopped.Write(func() { _ = s.updateConnectionStatus(connID, tenantID, status) })
		return
	}

	// A timer re-armed after each poll, not a ticker: a ticker fires again as
	// soon as a slow poll returns, which would put the next look at the folder
	// right behind the last one (#374, #562).
	timer := time.NewTimer(sched.next(s, cfg))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			sched.record(s.pollOnce(ctx, connID, tenantID, cfg, done, pending, logger))
			timer.Reset(sched.next(s, cfg))
		}
	}
}

// pollSchedule is what the poller's last polls say about when to poll next:
// how many files the last one left waiting to settle, and how many in a row
// could not connect or list.
type pollSchedule struct {
	waiting  int
	failures int
}

// record takes the outcome of a poll.
func (p *pollSchedule) record(waiting int, unreachable bool) {
	p.waiting = waiting
	if unreachable {
		p.failures++
	} else {
		p.failures = 0
	}
}

// next is how long to wait before the next poll.
func (p *pollSchedule) next(s *sftpConsumer, cfg *SFTPConfig) time.Duration {
	return s.nextPoll(cfg, p.waiting, p.failures)
}

// nextPoll is how long to wait after a poll that left waiting files to settle:
// the settle gap while some are, so a file goes out one gap after it was first
// seen unchanged, not one interval; the poll interval otherwise. After failures
// polls in a row that could not connect or list, the gap doubles with each
// one, up to the interval: a server refusing logins is not tried every 30 s
// from the cluster's one egress address, which a fail2ban-style ban would
// then shut out for every tenant on that host.
func (s *sftpConsumer) nextPoll(cfg *SFTPConfig, waiting, failures int) time.Duration {
	interval := time.Duration(cfg.PollIntervalSeconds) * time.Second
	gap := s.settleGap(cfg)
	if waiting == 0 || gap >= interval {
		return interval
	}
	for i := 1; i < failures && gap < interval; i++ {
		gap *= 2
	}
	return min(gap, interval)
}

// sighting is how a pending file looked: its stamp, and when the poller first
// saw it with that stamp.
type sighting struct {
	stamp doneset.Stamp
	at    time.Time
}

// settleGap is the least time between the two looks that must find a file
// unchanged before it is published: the poll interval, kept between the
// settle minimum and maximum. A one-shot run has no interval and gets the
// minimum.
func (s *sftpConsumer) settleGap(cfg *SFTPConfig) time.Duration {
	lo, hi := s.settleMin, s.settleMax
	if lo <= 0 {
		lo = defaultSettleMin
	}
	if hi <= 0 {
		hi = defaultSettleMax
	}
	gap := max(time.Duration(cfg.PollIntervalSeconds)*time.Second, lo)
	return min(gap, hi)
}

// clock returns the poller's current time.
func (s *sftpConsumer) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// settled reports whether name, listed at listedAt with stamp st, has stopped
// changing: the previous look saw the same size and mtime, at least gap
// before. Otherwise it records this look and the file waits. Until #562 a file
// was published on the first poll that listed it, so one still being uploaded
// went out half-written. Same rule as the file consumer (#374).
func settled(pending map[string]sighting, name string, st doneset.Stamp, listedAt time.Time, gap time.Duration) bool {
	prev, seen := pending[name]
	if !seen || prev.stamp != st {
		pending[name] = sighting{stamp: st, at: listedAt}
		return false
	}
	// Unchanged, but looked at again too soon to tell a finished file from a
	// slow upload: keep the first sighting and wait.
	return listedAt.Sub(prev.at) >= gap
}

// doneSet returns the poller's done set, keyed by tenant, connection and node,
// for the remote folder cfg names. A saved set for another host, user, port or
// folder is not used.
func (s *sftpConsumer) doneSet(connID, tenantID string, cfg *SFTPConfig) *doneset.Set {
	nodeID := cfg.nodeID
	if nodeID == "" {
		nodeID = fallbackNodeID
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	scope := "sftp://" + cfg.Username + "@" + cfg.Host + ":" + strconv.Itoa(port) + path.Clean("/"+cfg.RemoteDir)
	return doneset.New(s.checkpoints, doneset.Key{TenantID: tenantID, ConnectionID: connID, NodeID: nodeID}, scope)
}

// pollOnce dials, lists the remote dir, and fetches+publishes+after-actions each
// file that is not done (new, or replaced since it was published, #538) and
// has stopped changing (settled, #562). Connection errors are logged and
// retried on the next tick. Until the saved done set has been read nothing is
// published: without it every file in the folder would look new. It returns
// how many files it left waiting to settle (after a failed connect or listing,
// every pending file, and unreachable is true); a file whose publish failed is
// not counted, it is retried at the next poll as before.
func (s *sftpConsumer) pollOnce(ctx context.Context, connID, tenantID string, cfg *SFTPConfig, done *doneset.Set, pending map[string]sighting, logger *slog.Logger) (waiting int, unreachable bool) {
	if !done.Loaded() {
		found, err := done.Load(ctx)
		if err != nil {
			logger.Error("Could not read which files were already published; publishing nothing until it can be read", "error", err)
			return 0, false
		}
		if found {
			logger.Info("Resuming: files already published are not published again", "already_done", done.Len())
		} else {
			logger.Info("First start for this folder: every file in it is published")
		}
	}

	conn, err := s.dialWithBackoff(ctx, cfg, logger)
	if err != nil {
		logger.Error("SFTP connect failed; will retry next cycle", "error", err)
		// Files seen before still wait for their second look: try again
		// a settle gap later, not a whole interval (backing off, nextPoll).
		return len(pending), true
	}
	defer conn.Close()

	files, err := conn.List(cfg.RemoteDir)
	if err != nil {
		logger.Error("SFTP list failed", "dir", cfg.RemoteDir, "error", err)
		return len(pending), true
	}
	// The time of this look, for the stability check (#562): the listing is
	// what the check compares, not when each file's turn comes in the loop.
	listedAt := s.clock()
	gap := s.settleGap(cfg)

	listing := make(map[string]doneset.Stamp, len(files))
	for _, f := range files {
		listing[f.Name] = doneset.StampOf(f.Size, f.ModTime)
	}
	// Files gone from the folder leave the done set: added again, they are
	// published again. They stop being pending too.
	done.Observe(listing)
	for name := range pending {
		if _, ok := listing[name]; !ok {
			delete(pending, name)
		}
	}
	// Also when a stop cuts the poll short: what it published is saved.
	defer s.saveDone(ctx, done, logger)

	refreshed := false
	sinceSave := 0
	for _, f := range files {
		select {
		case <-ctx.Done():
			return waiting, false
		default:
		}
		stamp := listing[f.Name]
		if done.Done(f.Name, stamp) {
			delete(pending, f.Name)
			continue
		}
		if cfg.FilePattern != "" {
			if match, _ := path.Match(cfg.FilePattern, f.Name); !match {
				continue
			}
		}
		if !settled(pending, f.Name, stamp, listedAt, gap) {
			waiting++
			continue
		}
		if !refreshed {
			// Once per poll, before the first publish: another poller of
			// this connection may have published it since this one loaded.
			refreshed = true
			if err := done.Refresh(ctx); err != nil {
				logger.Warn("Could not re-read which files were already published", "error", err)
			} else if done.Done(f.Name, stamp) {
				continue
			}
		}

		remotePath := joinRemote(cfg.RemoteDir, f.Name)
		streamed, err := s.fetchAndPublish(ctx, conn, connID, tenantID, f, remotePath)
		if err != nil {
			logger.Error("SFTP ingest failed", "file", f.Name, "error", err)
			continue // leave the file in place and pending; retried next cycle
		}
		delete(pending, f.Name)
		done.Mark(f.Name, stamp)
		logger.Info("SFTP file ingested", "file", f.Name, "size", f.Size, "streamed", streamed)
		if sinceSave++; sinceSave >= saveEvery {
			s.saveDone(ctx, done, logger)
			sinceSave = 0
		}

		if err := s.afterAction(conn, cfg, f.Name, remotePath); err != nil {
			logger.Warn("after-action failed", "file", f.Name, "action", cfg.AfterAction, "error", err)
		}
	}
	return waiting, false
}

// saveDone saves the done set; a failure is retried at the next save.
func (s *sftpConsumer) saveDone(ctx context.Context, done *doneset.Set, logger *slog.Logger) {
	if err := done.Save(ctx); err != nil {
		logger.Error("Could not save which files were published; retrying at the next save", "error", err)
	}
}

// dialWithBackoff tries to open a connection up to dialMaxAttempts times with
// exponential backoff, honouring ctx cancellation.
func (s *sftpConsumer) dialWithBackoff(ctx context.Context, cfg *SFTPConfig, logger *slog.Logger) (sftpConn, error) {
	var lastErr error
	backoff := dialBaseBackoff
	for attempt := 1; attempt <= dialMaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		conn, err := s.dial(cfg)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		logger.Warn("SFTP dial attempt failed", "attempt", attempt, "error", err)
		if attempt < dialMaxAttempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
			}
		}
	}
	return nil, lastErr
}

// afterAction applies the configured post-ingest action to a fetched file.
func (s *sftpConsumer) afterAction(conn sftpConn, cfg *SFTPConfig, name, remotePath string) error {
	switch cfg.AfterAction {
	case "delete":
		return conn.Remove(remotePath)
	case "move":
		dst := cfg.MoveDir
		if dst == "" {
			return fmt.Errorf("after_action=move but move_dir is empty")
		}
		if err := conn.MkdirAll(dst); err != nil {
			return fmt.Errorf("mkdir move_dir: %w", err)
		}
		return conn.Rename(remotePath, joinRemote(dst, name))
	default: // "none" / "" / "leave"
		return nil
	}
}

// fetchAndPublish reads one remote file and publishes it, streaming it off the
// SFTP session when the worker supports that and the file is over the inline
// threshold — so a multi-GB file is never held in memory. The size comes from
// the directory listing, so no probing read is needed. Reports whether the
// streaming path was used.
func (s *sftpConsumer) fetchAndPublish(ctx context.Context, conn sftpConn, connID, tenantID string, f remoteFile, remotePath string) (bool, error) {
	if s.publishStream == nil || s.inlineMax <= 0 || f.Size <= int64(s.inlineMax) {
		data, err := conn.Read(remotePath)
		if err != nil {
			return false, fmt.Errorf("read: %w", err)
		}
		if err := s.publishFile(ctx, connID, tenantID, f.Name, data); err != nil {
			return false, fmt.Errorf("publish: %w", err)
		}
		return false, nil
	}

	rc, err := conn.Open(remotePath)
	if err != nil {
		return false, fmt.Errorf("open: %w", err)
	}
	defer rc.Close()

	// Sniff the content type from a small head, then stream head + remainder so
	// nothing is read twice and nothing is buffered.
	head := make([]byte, contentSniffBytes)
	n, rerr := io.ReadFull(rc, head)
	if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		return false, fmt.Errorf("read: %w", rerr)
	}
	head = head[:n]

	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = detectContentType(f.Name, head)
	env.Source = "sftp:" + f.Name
	env.StepHistory = []string{"sftp-consumer"}
	env.Metadata = map[string]interface{}{"filename": f.Name}
	if err := s.publishStream(ctx, env, io.MultiReader(bytes.NewReader(head), rc)); err != nil {
		return true, fmt.Errorf("publish stream: %w", err)
	}
	return true, nil
}

// contentSniffBytes is how much of a streamed file is read up front to detect
// its content type — enough for detectContentType, small enough to be free.
const contentSniffBytes = 512

func (s *sftpConsumer) publishFile(ctx context.Context, connID, tenantID, filename string, data []byte) error {
	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = detectContentType(filename, data)
	env.Source = "sftp:" + filename
	env.Payload = data
	env.PayloadSize = int64(len(data))
	env.StepHistory = []string{"sftp-consumer"}
	env.Metadata = map[string]interface{}{"filename": filename}
	return s.publish(ctx, env)
}

// --- DB helpers ---

// getSFTPConfig loads the connection, resolves *_secret_id references to
// plaintext, and extracts the SFTP consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (s *sftpConsumer) getSFTPConfig(connectionID, tenantID string) (*SFTPConfig, bool) {
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
		if nc.Type == "sftp" && nc.SFTP != nil {
			nc.SFTP.nodeID = n.ID
			return nc.SFTP, true
		}
	}
	return nil, false
}

func (s *sftpConsumer) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := s.db.Exec(query, status, connectionID, tenantID)
	return err
}

// detectContentType picks a MIME type from the filename extension, falling back
// to a light content sniff.
func detectContentType(filename string, data []byte) string {
	switch {
	case hasSuffixFold(filename, ".json"):
		return "application/json"
	case hasSuffixFold(filename, ".xml"):
		return "application/xml"
	case hasSuffixFold(filename, ".csv"):
		return "text/csv"
	case hasSuffixFold(filename, ".txt"):
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
