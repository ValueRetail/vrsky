package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/tenantpath"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// fileConsumer watches a directory and accepts HTTP uploads per active
// connection, publishing file contents into the pipeline. It is an SDK
// Consumer: the runner provides NATS/DB/health/lifecycle; this type implements
// Configure (wire deps + register HTTP handlers), Run (subscribe to command
// subjects, block), and Stop (cancel watchers).
type fileConsumer struct {
	sdk.BaseConsumer

	db *sql.DB
	nc *nats.Conn
	// checkpoints keeps each watcher's done set across restarts (#391, known.go).
	// Configure sets the Postgres store unless a test set one; nil means
	// nothing is kept and every start takes the whole folder as done.
	checkpoints checkpoint.Store
	publish     sdk.PublishFunc // injected by the runner; the one data-emit path
	logger      *slog.Logger

	// publishStream is non-nil only when the SDK selected the streaming path
	// (ADR 0001) — i.e. this worker has a payload store. When it is available a
	// watched file is streamed from disk rather than read into memory, which is
	// what lifts the maxWatchFileBytes ceiling. inlineMax is the size above which
	// the SDK offloads anyway, so it marks where buffering stops paying off.
	publishStream sdk.PublishStreamFunc
	inlineMax     int

	baseDir  string // FILE_CONSUMER_BASE_DIR; default watch root when a node has no path
	hostHome string // HOST_HOME; used to expand a leading ~ in node paths

	uploadToken  string // FILE_CONSUMER_AUTH_TOKEN; empty leaves /upload open (compose default)
	uploadOrigin string // FILE_CONSUMER_ALLOWED_ORIGIN; the only origin given CORS access

	activeConnections map[string]*ActiveConnection
	// starts holds the claim of each start that has passed the "already
	// active" check and is not yet in activeConnections (#375, #397). Guarded
	// by mu; a connection is never both starting and active.
	starts sdk.StartGuard
	mu     sync.RWMutex

	// beforeStartWork, when set, runs once a start holds its reservation and
	// before the database lookup. Tests use it to line up concurrent starts.
	beforeStartWork func(connectionID string)

	// pollInterval is how often a watched directory is scanned; zero means
	// defaultPollInterval. Tests shorten it.
	pollInterval time.Duration
	// now is the watcher's clock; nil means time.Now. Tests inject one to
	// drive the stability window without sleeping.
	now func() time.Time
	// readDir lists a watch folder; nil means os.ReadDir. Tests inject one
	// that fails on demand (#398).
	readDir func(dir string) ([]os.DirEntry, error)

	// Event subscribers: connectionId → list of channels
	eventSubs   map[string][]chan FileEvent
	eventSubsMu sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// maxWatchFileBytes caps the size of a watched file the poller will read into
// memory before publishing (mirrors the 32 MiB cap on the HTTP upload path).
// It applies only to the buffered path: when the worker can stream (ADR 0001)
// a file over the inline threshold is sent straight from disk and this ceiling
// does not apply at all.
const maxWatchFileBytes = 32 << 20

// ingestWatchedFile publishes one newly-detected file, streaming it from disk
// when the worker supports that and the file is over the inline threshold —
// which is what allows files larger than maxWatchFileBytes to be ingested at
// all. Returns the envelope, the file size, and whether it was streamed.
func (s *fileConsumer) ingestWatchedFile(ctx context.Context, ac *ActiveConnection, name, path string) (*envelope.Envelope, int64, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("stat: %w", err)
	}
	size := info.Size()

	// Small enough to buffer, or no streaming available: existing behaviour,
	// including the size ceiling that protects worker memory.
	if s.publishStream == nil || s.inlineMax <= 0 || size <= int64(s.inlineMax) {
		if size > maxWatchFileBytes {
			return nil, size, false, fmt.Errorf("file exceeds the %d-byte limit and this worker cannot stream (no payload store configured)", int64(maxWatchFileBytes))
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, size, false, rerr
		}
		env, perr := s.ingestFile(ctx, ac, name, data, "watch:"+name)
		return env, size, false, perr
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, size, false, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	// Sniff the content type from a small head, then hand the whole file over as
	// head + remainder so nothing is read twice and nothing is buffered.
	head := make([]byte, contentSniffBytes)
	n, rerr := io.ReadFull(f, head)
	if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		return nil, size, false, fmt.Errorf("read: %w", rerr)
	}
	head = head[:n]

	env := &envelope.Envelope{
		ID:            uuid.New().String(),
		TenantID:      ac.TenantID,
		IntegrationID: ac.ConnectionID,
		ContentType:   detectContentType(name, head),
		Source:        "watch:" + name,
		StepHistory:   []string{"file-consumer"},
		CreatedAt:     time.Now().UTC(),
		Metadata:      map[string]interface{}{"filename": name},
	}
	if err := s.publishStream(ctx, env, io.MultiReader(bytes.NewReader(head), f)); err != nil {
		return nil, size, true, err
	}
	return env, size, true, nil
}

// contentSniffBytes is how much of a streamed file is read up front to detect
// its content type — enough for detectContentType, small enough to be free.
const contentSniffBytes = 512

// FileEvent represents an activity event for the UI
type FileEvent struct {
	Type     string `json:"type"` // "added", "deleted", "uploaded", "error", "connected"
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Time     string `json:"time"`
	Message  string `json:"message,omitempty"`
}

type ActiveConnection struct {
	ConnectionID string
	TenantID     string
	WatchDir     string
	// NodeID is the file consumer node's id, the key of its checkpoint.
	NodeID string
	Cancel context.CancelFunc

	// knownFiles holds names that are done: what loadKnown took as done at
	// start and every name published (or failed) since. They are not
	// ingested again while the name exists. pending holds names seen but not
	// yet stable, with the size and mtime from the last poll (#374). dirty
	// means knownFiles changed since it was last saved (#391). All three are
	// touched only by the watcher goroutine after startConnection returns.
	knownFiles map[string]bool
	pending    map[string]fileState
	dirty      bool
	// readFailures counts folder reads in a row that failed (#398), so only
	// the first is logged as a warning. baselineDeferred means the start could
	// not read the folder: knownFiles is not set yet, and each poll tries
	// loadKnown until a read works. Both are set by the start path before the
	// watcher goroutine runs, then touched only by that goroutine.
	readFailures     int
	baselineDeferred bool
}

// fileState is what the watcher compares between polls to decide that the
// writer has finished with a file. seenAt is when size and modTime were first
// seen with these values; it is not part of the comparison.
type fileState struct {
	size    int64
	modTime time.Time
	seenAt  time.Time
}

// Configure wires dependencies and registers the HTTP endpoints the UI uses
// (served on the SDK auxiliary HTTP port, WORKER_HTTP_PORT/9200). Called once
// before Run.
func (s *fileConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("file-consumer requires DATABASE_URL")
	}
	s.db = res.DB
	s.nc = res.NATS
	if s.checkpoints == nil {
		s.checkpoints = checkpoint.NewPostgresStore(res.DB)
	}
	s.logger = res.Logger
	s.inlineMax = res.InlineMaxBytes()
	// Under the lock like every other access: the map is read from NATS
	// callbacks and HTTP handlers that may already be running (#362).
	s.mu.Lock()
	s.activeConnections = make(map[string]*ActiveConnection)
	s.mu.Unlock()
	s.eventSubs = make(map[string][]chan FileEvent)
	s.baseDir = getEnv("FILE_CONSUMER_BASE_DIR", "/data/input")
	s.hostHome = os.Getenv("HOST_HOME")

	// /upload publishes caller-supplied bytes into a running pipeline, so an
	// open one is an injection primitive: it took any connection ID that
	// happened to be active, with no authentication and Access-Control-Allow-
	// Origin: *. The tenant check lives in the management API's proxy, which
	// knows the caller's session; this token is what stops anything else on
	// the cluster network from reaching past it. The port MUST NOT be exposed
	// through an ingress.
	s.uploadOrigin = getEnv("FILE_CONSUMER_ALLOWED_ORIGIN", "http://localhost:5173")
	s.uploadToken = os.Getenv("FILE_CONSUMER_AUTH_TOKEN")
	if s.uploadToken == "" {
		s.logger.Warn("FILE_CONSUMER_AUTH_TOKEN is not set: /upload accepts any caller that can reach this port")
	}

	s.RegisterHTTPHandler("/upload/", s.handleUpload())
	s.RegisterHTTPHandler("/events/", s.handleEvents())
	s.RegisterHTTPHandler("/sample-data/", s.handleSampleData())

	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until the runner
// cancels ctx. Per-connection directory watching is driven from the handlers.
// RunStream is Run with large-file streaming enabled (ADR 0001). The SDK calls
// it instead of Run when a payload store is configured; watched files above the
// inline threshold are then streamed from disk into the pipeline, so size is
// bounded by the copy buffer rather than by worker memory.
func (s *fileConsumer) RunStream(ctx context.Context, publish sdk.PublishFunc, publishStream sdk.PublishStreamFunc) error {
	s.publishStream = publishStream
	return s.Run(ctx, publish)
}

func (s *fileConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
	s.publish = publish
	s.logger.Info("Starting File Consumer Service")

	startSub, err := s.nc.Subscribe("vrsky.commands.*.connection.start", s.handleStartCommand)
	if err != nil {
		return fmt.Errorf("failed to subscribe to start commands: %w", err)
	}
	s.startSub = startSub

	stopSub, err := s.nc.Subscribe("vrsky.commands.*.connection.stop", s.handleStopCommand)
	if err != nil {
		return fmt.Errorf("failed to subscribe to stop commands: %w", err)
	}
	s.stopSub = stopSub

	s.logger.Info("Subscribed to NATS command topics")
	// Bring back what the database says is running: start/stop are NATS commands
	// and are not persisted, so without this a restart silently stopped every
	// pipeline on this service until someone redeployed it.
	sdk.RestoreRunning(ctx, s.db, s.logger, "file", s.startConnection)

	<-ctx.Done()
	return nil
}

// Stop cancels all directory watchers. The SDK runner calls this after Run
// returns; it also shuts down the aux HTTP server it owns.
func (s *fileConsumer) Stop(ctx context.Context) error {
	s.logger.Info("Stopping File Consumer Service")

	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}

	s.mu.Lock()
	for connId, ac := range s.activeConnections {
		s.logger.Info("Stopping file watcher", "connection_id", connId)
		ac.Cancel()
	}
	s.activeConnections = make(map[string]*ActiveConnection)
	// A start still in flight must not leave a watcher nobody will cancel.
	s.starts.StopAllLocked()
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return nil
	}
}

type CommandMessage struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

func (s *fileConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse start command", "error", err, "data", string(msg.Data))
		return
	}
	s.startConnection(context.Background(), cmd.ConnectionID, cmd.TenantID)
}

// startConnection is the start command's work, also what RestoreRunning calls on
// boot for every pipeline the database says is running (plans/stable-connections.md).
func (s *fileConsumer) startConnection(ctx context.Context, connectionID, tenantID string) {
	s.logger.Info("Received start command", "connection_id", connectionID, "tenant_id", tenantID)

	// The boot restore and the start-command handler run on different
	// goroutines. The check and the claim are one critical section, so a
	// second start for the same connection stops here instead of starting a
	// second watcher (#375).
	claim, ok := s.starts.Reserve(&s.mu, connectionID, func() bool { _, ok := s.activeConnections[connectionID]; return ok })
	if !ok {
		s.logger.Warn("File watcher already active or starting", "connection_id", connectionID)
		return
	}
	// Frees the claim on every early return below, so a later start can try
	// again. A no-op once Activate has moved it to activeConnections.
	defer claim.Release()
	if s.beforeStartWork != nil {
		s.beforeStartWork(connectionID)
	}

	conn, err := s.getConnection(connectionID, tenantID)
	if err != nil {
		s.logger.Error("Failed to fetch connection", "error", err, "connection_id", connectionID)
		return
	}

	if !s.hasFileConsumer(conn) {
		s.logger.Debug("Not a file consumer, ignoring", "connection_id", connectionID)
		return
	}

	// Get watch directory from node config, fall back to {connectionId} inside
	// the tenant's own root.
	watchDir := s.extractWatchDir(conn)
	if watchDir == "" {
		watchDir = connectionID
	}

	// Expand ~ to host home directory. This has to happen BEFORE the tenant
	// resolve below, or "~" would be taken as a literal directory name inside
	// the tenant root instead of the home path the user meant — and, expanded,
	// a home path outside the mounted volume is refused rather than silently
	// watched.
	if len(watchDir) > 0 && watchDir[0] == '~' {
		home := s.hostHome
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		if home != "" {
			watchDir = filepath.Join(home, watchDir[1:])
		}
	}

	// Confine the directory to this tenant's subtree. Until this existed the
	// configured path was used verbatim, so a tenant could point a file source
	// at the shared output root and ingest every other tenant's files — the
	// consumer and producer share one RWX volume in the cluster.
	//
	// Refusing here rather than clamping is deliberate: a source silently
	// watching a different directory than its config names is the failure this
	// is meant to end, so the connection fails to start and says why.
	resolved, err := tenantpath.Resolve(s.baseDir, tenantID, watchDir)
	if err != nil {
		s.logger.Error("Refusing to start: watch directory is outside the workspace's own files",
			"error", err, "connection_id", connectionID, "tenant_id", tenantID, "configured", watchDir)
		return
	}
	watchDir = resolved

	if err := os.MkdirAll(watchDir, 0o755); err != nil {
		s.logger.Error("Failed to create watch directory", "error", err, "dir", watchDir)
		return
	}

	// Decide which files in the folder are done before the connection shows
	// as active or running. Decided later, in the watcher goroutine, a file
	// dropped in between was counted as already there and silently never
	// published (#362). On the first start that is every file in the folder;
	// after that, only those saved as published, so a file that landed just
	// before a stop or restart is still published (#391).
	ac := &ActiveConnection{
		ConnectionID: connectionID,
		TenantID:     tenantID,
		WatchDir:     watchDir,
		NodeID:       s.fileNodeID(conn),
	}
	if err := s.loadKnown(ctx, ac); err != nil {
		// Start anyway, so the connection that shows as running is watched,
		// and take the baseline on the first poll whose read works (#398).
		// Nothing is published or saved before that. Refusing the start left
		// the pipeline "running" with nothing watching the folder.
		ac.baselineDeferred = true
		ac.readFailures = 1
		s.logger.Warn("Could not read the watch folder at start; watching anyway, nothing is published until a read works",
			"error", err, "connection_id", connectionID, "watch_dir", watchDir)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ac.Cancel = cancel

	if !claim.Activate(func() { s.activeConnections[connectionID] = ac }) {
		cancel()
		s.logger.Info("Start abandoned: a stop arrived while it was in progress", "connection_id", connectionID)
		return
	}
	s.saveKnown(ctx, ac, s.logger.With("connection_id", connectionID, "watch_dir", watchDir))

	if err := s.updateConnectionStatus(connectionID, tenantID, "running"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}

	// Start directory watch goroutine (only tracks additions/deletions, no auto-processing)
	go s.watchDirectory(ctx, ac)

	s.logger.Info("File watcher started",
		"connection_id", connectionID,
		"watch_dir", watchDir)
}

func (s *fileConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse stop command", "error", err, "data", string(msg.Data))
		return
	}

	s.logger.Info("Received stop command", "connection_id", cmd.ConnectionID)

	s.mu.Lock()
	ac, exists := s.activeConnections[cmd.ConnectionID]
	if exists {
		ac.Cancel()
		delete(s.activeConnections, cmd.ConnectionID)
	}
	// A start still in progress is told to give up rather than finish after
	// the stop and leave a watcher running (#375). It is removed, not only
	// marked, so a start that comes after this stop is not refused.
	starting := s.starts.StopLocked(cmd.ConnectionID)
	s.mu.Unlock()

	if !exists {
		if starting {
			// No status write: the management API already set "stopped", and
			// the start may yet have turned out not to be a file connection.
			s.logger.Info("Stop arrived while the file watcher was starting; the start is abandoned", "connection_id", cmd.ConnectionID)
			return
		}
		s.logger.Warn("File watcher not active", "connection_id", cmd.ConnectionID)
		return
	}

	if err := s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}

	s.logger.Info("File watcher stopped", "connection_id", cmd.ConnectionID)
}

// defaultPollInterval is how often a watched directory is scanned.
const defaultPollInterval = 5 * time.Second

// watchDirectory polls the watch directory until ctx is cancelled and
// publishes each new file once its writer has finished (pollOnce).
func (s *fileConsumer) watchDirectory(ctx context.Context, ac *ActiveConnection) {
	interval := s.interval()
	logger := s.logger.With("connection_id", ac.ConnectionID, "watch_dir", ac.WatchDir)
	logger.Info("Starting directory watch", "interval", interval.String())

	// A timer re-armed after each poll, not a ticker: a ticker fires again as
	// soon as a slow poll (a big file being published) returns, which would put
	// the next poll right behind the last one.
	timer := time.NewTimer(interval)
	defer timer.Stop()

	// ac.knownFiles already holds what startConnection took as done.
	for {
		select {
		case <-ctx.Done():
			logger.Info("Directory watch stopped")
			return
		case <-timer.C:
			s.pollOnce(ctx, ac)
			timer.Reset(interval)
		}
	}
}

// interval is the effective poll interval.
func (s *fileConsumer) interval() time.Duration {
	if s.pollInterval > 0 {
		return s.pollInterval
	}
	return defaultPollInterval
}

// clock returns the watcher's current time.
func (s *fileConsumer) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// pollOnce scans the watch directory once. A new file is published only when
// its size and mtime are the same as at the previous poll and at least one
// poll interval has passed between the two looks, so a file still being
// written is not read half-way. Until #374 it was read on the first poll that
// saw its name, and the finished file was then never published. Temp names
// are left alone until they are renamed to their final name. A poll that
// changed the done set saves it at the end (#391).
func (s *fileConsumer) pollOnce(ctx context.Context, ac *ActiveConnection) {
	logger := s.logger.With("connection_id", ac.ConnectionID, "watch_dir", ac.WatchDir)
	if ac.baselineDeferred {
		s.takeDeferredBaseline(ctx, ac, logger)
		return
	}
	if ac.knownFiles == nil {
		ac.knownFiles = make(map[string]bool)
	}
	if ac.pending == nil {
		ac.pending = make(map[string]fileState)
	}

	currentFiles, err := s.listFiles(ac.WatchDir)
	if err != nil {
		// Skip the poll. Read as an empty folder, a share that blipped for one
		// poll dropped every known name and the next good read published the
		// whole folder again (#398). The done set, pending files and the
		// checkpoint stay as they are; a folder that is really gone holds no
		// files, and the first good read prunes what went with it.
		s.noteReadFailure(ac, err, logger)
		return
	}
	if ac.readFailures > 0 {
		logger.Info("Watch folder readable again", "skipped_polls", ac.readFailures)
		ac.readFailures = 0
	}
	refreshed := false

	// Detect new files and ingest them into the pipeline. Previously this only
	// emitted a UI "added" event and never published the file's contents, so
	// the directory-watch source silently did nothing downstream (#143).
	for name := range currentFiles {
		if ctx.Err() != nil {
			// Stopping. What is left is published by the next start (#391).
			break
		}
		if ac.knownFiles[name] {
			continue
		}
		if isTempName(name) {
			logger.Debug("Skipping temp file name until it is renamed", "file", name)
			continue
		}
		path := filepath.Join(ac.WatchDir, name)
		info, err := os.Stat(path)
		if err != nil {
			// Gone or unreadable since the listing; look again next poll.
			delete(ac.pending, name)
			continue
		}
		now := fileState{size: info.Size(), modTime: info.ModTime(), seenAt: s.clock()}
		prev, seen := ac.pending[name]
		if !seen || prev.size != now.size || !prev.modTime.Equal(now.modTime) {
			ac.pending[name] = now
			continue
		}
		if now.seenAt.Sub(prev.seenAt) < s.interval() {
			// Unchanged, but looked at again too soon to tell a finished file
			// from a slow writer. Keep the first sighting and wait.
			continue
		}
		if !refreshed {
			// Once per poll, before the first publish: another watcher for
			// this connection may have published it since this one started.
			refreshed = true
			s.mergeSaved(ctx, ac, currentFiles, logger)
			if ac.knownFiles[name] {
				continue
			}
		}
		delete(ac.pending, name)
		ac.knownFiles[name] = true
		ac.dirty = true

		logger.Info("File added", "file", name)
		env, size, streamed, ingErr := s.ingestWatchedFile(ctx, ac, name, path)
		if ingErr != nil {
			if ctx.Err() != nil {
				// Cut off by a stop, not a bad file: leave it for the next
				// start instead of saving it as done.
				delete(ac.knownFiles, name)
				logger.Info("Stopped while publishing a file; it is published again on the next start", "file", name)
				break
			}
			logger.Error("Failed to ingest detected file", "file", name, "error", ingErr)
			s.emitEvent(ac.ConnectionID, FileEvent{
				Type: "error", Filename: name, Size: size, Message: ingErr.Error(),
				Time: time.Now().UTC().Format(time.RFC3339),
			})
			continue
		}
		logger.Info("File ingested from watch dir", "file", name, "size", size, "streamed", streamed, "envelope_id", env.ID)
		s.emitEvent(ac.ConnectionID, FileEvent{
			Type: "added", Filename: name, Size: size,
			Time: time.Now().UTC().Format(time.RFC3339),
		})
	}

	// Detect deleted files. A name that comes back later is new again.
	for name := range ac.knownFiles {
		if !currentFiles[name] {
			logger.Info("File deleted", "file", name)
			delete(ac.knownFiles, name)
			ac.dirty = true
			s.emitEvent(ac.ConnectionID, FileEvent{
				Type: "deleted", Filename: name,
				Time: time.Now().UTC().Format(time.RFC3339),
			})
		}
	}
	for name := range ac.pending {
		if !currentFiles[name] {
			delete(ac.pending, name)
		}
	}
	s.saveKnown(ctx, ac, logger)
}

// takeDeferredBaseline is a poll for a connection whose start could not read
// its folder (#398). It tries loadKnown again; the first read that works sets
// the baseline exactly as a start would (the saved set, or first-start rules)
// and saves it if that changed anything. Nothing is published in this poll,
// as nothing is in a start: new files go through the stability check from
// the next poll on.
func (s *fileConsumer) takeDeferredBaseline(ctx context.Context, ac *ActiveConnection, logger *slog.Logger) {
	if ctx.Err() != nil {
		return
	}
	if err := s.loadKnown(ctx, ac); err != nil {
		s.noteReadFailure(ac, err, logger)
		return
	}
	logger.Info("Watch folder readable; baseline taken, publishing from here",
		"skipped_polls", ac.readFailures, "files", len(ac.knownFiles))
	ac.baselineDeferred = false
	ac.readFailures = 0
	s.saveKnown(ctx, ac, logger)
}

// noteReadFailure logs a failed folder read: at Warn for the first in a run,
// then at Debug, so a share that stays down does not log every poll.
func (s *fileConsumer) noteReadFailure(ac *ActiveConnection, err error, logger *slog.Logger) {
	ac.readFailures++
	msg := "Could not read the watch folder; skipping this poll"
	if errors.Is(err, fs.ErrNotExist) {
		msg = "Watch folder is missing; skipping this poll"
	}
	if ac.readFailures == 1 {
		logger.Warn(msg, "error", err)
	} else {
		logger.Debug(msg, "error", err, "failed_polls", ac.readFailures)
	}
}

// isTempName reports whether a file name looks like one a writer is still
// filling and will rename when done: dotfiles, ~ prefixes, and .tmp or .part
// suffixes in any case.
func isTempName(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~") {
		return true
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tmp") || strings.HasSuffix(lower, ".part")
}

// listFiles returns the set of non-directory file names in dir. A failed
// read, a missing folder included, is an error and never an empty set: the
// callers would take every known name as deleted (#398).
func (s *fileConsumer) listFiles(dir string) (map[string]bool, error) {
	readDir := s.readDir
	if readDir == nil {
		readDir = os.ReadDir
	}
	entries, err := readDir(dir)
	if err != nil {
		return nil, err
	}
	files := make(map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() {
			files[entry.Name()] = true
		}
	}
	return files, nil
}

func detectContentType(filename string, data []byte) string {
	ext := filepath.Ext(filename)
	switch ext {
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".csv":
		return "text/csv"
	case ".txt":
		return "text/plain"
	case ".html":
		return "text/html"
	}
	// Try to detect from content
	if len(data) > 0 && (data[0] == '{' || data[0] == '[') {
		return "application/json"
	}
	if len(data) > 0 && data[0] == '<' {
		return "application/xml"
	}
	return "application/octet-stream"
}

func (s *fileConsumer) getActiveConnection(connectionID string) *ActiveConnection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeConnections[connectionID]
}

// Connection represents a pipeline connection from the database
type Connection struct {
	ID       string
	TenantID string
	Name     string
	Nodes    json.RawMessage
	Edges    json.RawMessage
}

type Node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

func (s *fileConsumer) getConnection(connectionID, tenantID string) (*Connection, error) {
	var conn Connection
	err := s.db.QueryRow(
		`SELECT id, tenant_id, name, nodes, edges FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&conn.ID, &conn.TenantID, &conn.Name, &conn.Nodes, &conn.Edges)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("connection not found: %s", connectionID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query connection: %w", err)
	}
	return &conn, nil
}

// extractWatchDir gets the watch directory from the file consumer node config
func (s *fileConsumer) extractWatchDir(conn *Connection) string {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		return ""
	}
	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}
		var config struct {
			Type string `json:"type"`
			File struct {
				Path string `json:"path"`
			} `json:"file"`
		}
		if err := json.Unmarshal(node.Config, &config); err != nil {
			continue
		}
		if config.Type == "file" && config.File.Path != "" {
			return config.File.Path
		}
	}
	return ""
}

func (s *fileConsumer) hasFileConsumer(conn *Connection) bool {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		return false
	}
	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}
		// Check config.type == "file"
		var config map[string]json.RawMessage
		if err := json.Unmarshal(node.Config, &config); err != nil {
			continue
		}
		if typeRaw, ok := config["type"]; ok {
			var configType string
			if json.Unmarshal(typeRaw, &configType) == nil && configType == "file" {
				return true
			}
		}
	}
	return false
}

func (s *fileConsumer) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := s.db.Exec(query, status, connectionID, tenantID)
	if err != nil {
		return fmt.Errorf("failed to update connection status: %w", err)
	}
	s.logger.Info("Updated connection status", "connection_id", connectionID, "status", status)
	return nil
}

// --- Event broadcasting (SSE) ---

func (s *fileConsumer) subscribeEvents(connectionID string) (chan FileEvent, func()) {
	ch := make(chan FileEvent, 50)
	s.eventSubsMu.Lock()
	s.eventSubs[connectionID] = append(s.eventSubs[connectionID], ch)
	s.eventSubsMu.Unlock()

	return ch, func() {
		s.eventSubsMu.Lock()
		defer s.eventSubsMu.Unlock()
		subs := s.eventSubs[connectionID]
		for i, sub := range subs {
			if sub == ch {
				s.eventSubs[connectionID] = append(subs[:i], subs[i+1:]...)
				close(ch)
				break
			}
		}
	}
}

// emitEvent sends an event to all subscribers for a connection
func (s *fileConsumer) emitEvent(connectionID string, event FileEvent) {
	s.eventSubsMu.RLock()
	defer s.eventSubsMu.RUnlock()
	for _, ch := range s.eventSubs[connectionID] {
		select {
		case ch <- event:
		default: // drop if channel full
		}
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
