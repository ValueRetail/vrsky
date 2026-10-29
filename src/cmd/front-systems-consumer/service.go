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
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// frontSystemsConsumer receives Front Systems webhooks and (optionally)
// registers its callback URL for the configured event types. It is an SDK
// Consumer: Configure wires deps + the webhook handler, Run subscribes to the
// command subjects and blocks.
type frontSystemsConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	httpClient *http.Client

	// resolveTenant maps a connection id to its owning tenant (webhook routing).
	// Defaulted in Configure to the DB lookup; tests inject a stub.
	resolveTenant func(connID string) (string, error)

	// active holds the connections this process has started (and registered
	// webhooks for, when a callback URL is set), by connection ID. starts
	// keeps two starts of one connection (boot restore and a start command,
	// say) from both registering its webhooks, and lets a stop that arrives
	// mid-start win (#397, #429). Both under mu.
	mu     sync.Mutex
	active map[string]*activeConn
	starts sdk.StartGuard

	// pendingUnregister holds, by connection ID, webhooks a stop has taken
	// off an active connection that are not deleted at Front Systems yet. It
	// is drained with that connection's hook lock held, by the stop's own
	// goroutine or by the next start, whichever gets there first, so a start
	// never lists the webhooks before a stop's deletes are done (#503).
	// hookLocks has one mutex per connection, never pruned (like the guard's
	// seq): it serialises every Front Systems webhook call for the
	// connection. restoring is true while boot restore runs; closed once Stop
	// has run. All under mu.
	pendingUnregister map[string][]hookSet
	hookLocks         map[string]*sync.Mutex
	restoring         bool
	closed            bool

	// pending holds, by connection ID, a start that is wanted but has not
	// activated yet: one in progress, or one waiting to retry after a
	// transient failure (retry.go). The webhook route answers 503 for it, not
	// 410. A stop for its tenant, an activation, a permanent failure, giving
	// up and Stop remove it. Under mu.
	pending map[string]*pendingStart

	// Retry timing for failed starts; zero means the defaults in retry.go.
	// Tests shorten them.
	retryBase   time.Duration
	retryMax    time.Duration
	retryGiveUp time.Duration

	// bgCtx is cancelled by Stop; unregisters started by stop commands and
	// start retries run under it and are counted by bg. Created lazily under
	// mu.
	bgCtx    context.Context
	bgCancel context.CancelFunc
	bg       sync.WaitGroup

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// FrontSystemsConfig is the per-node configuration (config.front_systems). The
// two API keys are stored as `*_secret_id` references and resolved to plaintext
// at connection-start time.
type FrontSystemsConfig struct {
	BaseURL         string   `json:"base_url"`         // per-partner Azure APIM host (required)
	SubscriptionKey string   `json:"subscription_key"` // from subscription_key_secret_id
	APIKey          string   `json:"api_key"`          // from api_key_secret_id
	Events          []string `json:"events"`           // event types to register (e.g. SaleCreated)
	CallbackURL     string   `json:"callback_url"`     // our public callback; if set → auto-register
}

type nodeConfig struct {
	Type         string              `json:"type"`
	FrontSystems *FrontSystemsConfig `json:"front_systems"`
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// activeConn is one started connection: the tenant it was started for (the
// one whose (id, tenant) lookup found it) and the webhooks registered at
// Front Systems for it, to delete when it is stopped.
type activeConn struct {
	tenantID string
	hooks    hookSet
}

// hookSet is a connection's Front Systems webhook ids and the configuration
// (base URL and keys) to delete them with. cfg is nil when nothing was
// registered.
type hookSet struct {
	cfg *FrontSystemsConfig
	ids []string
}

// unregisterTimeout bounds one batch of unregister calls.
const unregisterTimeout = 30 * time.Second

// stopGrace is how long Stop waits for unregisters from stop commands before
// cancelling them.
const stopGrace = 5 * time.Second

type commandMessage struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

// Configure wires dependencies. Called once before Run.
func (c *frontSystemsConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("front-systems-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	c.db = res.DB
	c.nc = res.NATS
	c.logger = res.Logger
	if c.httpClient == nil {
		c.httpClient = &http.Client{
			Timeout:       60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if c.resolveTenant == nil {
		c.resolveTenant = c.getConnectionTenant
	}

	// The runner starts the HTTP server right after Configure, before Run
	// restores the running connections. Until Run's restore is done, the
	// webhook route answers 503 rather than 410 for a connection not started
	// yet: it may well be running.
	c.mu.Lock()
	c.restoring = true
	c.mu.Unlock()

	// Real-time events: Front Systems POSTs to /frontsystems/events/{connectionID}.
	c.RegisterHTTPHandler("/frontsystems/events/", c.handleWebhook())

	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until ctx done.
func (c *frontSystemsConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
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
	// Bring back what the database says is running: start/stop are NATS commands
	// and are not persisted, so without this a restart silently stopped every
	// pipeline on this service until someone redeployed it.
	// restoring has been true since Configure (set again here for callers
	// that skip it); the webhook route answers 503 until the scan is done.
	c.mu.Lock()
	c.restoring = true
	c.mu.Unlock()
	sdk.RestoreRunning(ctx, c.db, c.logger, "front_systems", c.startConnection)
	c.mu.Lock()
	c.restoring = false
	c.mu.Unlock()

	<-ctx.Done()
	return nil
}

// Stop unsubscribes. It does not unregister webhooks at Front Systems: the
// connections are still running in the database, and boot restore in the
// next process finds and reuses their registrations. Unregisters already
// started by stop commands get stopGrace to finish, then are cancelled.
func (c *frontSystemsConsumer) Stop(ctx context.Context) error {
	if c.startSub != nil {
		_ = c.startSub.Unsubscribe()
	}
	if c.stopSub != nil {
		_ = c.stopSub.Unsubscribe()
	}
	c.mu.Lock()
	c.closed = true
	c.active = nil
	// A start still in flight gives up instead of counting as started.
	c.starts.StopAllLocked()
	// Retries waiting for their next attempt end now, not after stopGrace.
	// The connections stay running in the database for the next process.
	for _, p := range c.pending {
		p.cancel()
	}
	c.pending = nil
	c.lifecycleCtxLocked()
	cancel := c.bgCancel
	c.mu.Unlock()

	// No stop command adds to bg once closed is set (it is checked under mu),
	// so this Wait sees every Add.
	done := make(chan struct{})
	go func() {
		c.bg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	case <-time.After(stopGrace):
	}
	// Cancelling ends the unregisters' calls and those of any start in
	// progress (they all run under the lifecycle context). One still waiting
	// for its connection's hook lock is not waited for past ctx.
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

// lifecycleCtxLocked returns the context background unregisters run under,
// cancelled by Stop. The caller holds mu.
func (c *frontSystemsConsumer) lifecycleCtxLocked() context.Context {
	if c.bgCtx == nil {
		c.bgCtx, c.bgCancel = context.WithCancel(context.Background())
	}
	return c.bgCtx
}

// hookLock returns the connection's hook lock. It takes mu, so the caller
// must not hold it.
func (c *frontSystemsConsumer) hookLock(connectionID string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hookLocks == nil {
		c.hookLocks = make(map[string]*sync.Mutex)
	}
	l, ok := c.hookLocks[connectionID]
	if !ok {
		l = &sync.Mutex{}
		c.hookLocks[connectionID] = l
	}
	return l
}

// refuseEvent decides what the webhook route does with an event for
// connectionID, whose row belongs to tenantID. It returns 0 to publish it, or
// the HTTP status to refuse it with. An event is published only for a
// connection this process started, and only for the tenant it was started
// for (#503).
func (c *frontSystemsConsumer) refuseEvent(connectionID, tenantID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ac, ok := c.active[connectionID]; ok {
		if ac.tenantID == tenantID {
			return 0
		}
		c.logger.Warn("Front Systems event refused: the connection's tenant is not the one it was started for",
			"connection_id", connectionID, "tenant_id", tenantID, "started_for", ac.tenantID)
		return http.StatusGone
	}
	// A start is in progress or waiting to retry: the connection is meant to
	// be running, so try later, not gone. Another tenant's event gets the
	// same 410 as for an active connection.
	if p, ok := c.pending[connectionID]; ok {
		if p.tenantID == tenantID {
			return http.StatusServiceUnavailable
		}
		c.logger.Warn("Front Systems event refused: the connection's tenant is not the one it is starting for",
			"connection_id", connectionID, "tenant_id", tenantID, "starting_for", p.tenantID)
		return http.StatusGone
	}
	// While boot restore runs, or the process is shutting down, the
	// connection may be running in the database: try later, not gone.
	if c.restoring || c.closed {
		return http.StatusServiceUnavailable
	}
	return http.StatusGone
}

func (c *frontSystemsConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		c.logger.Error("parse start command", "error", err)
		return
	}
	c.startConnection(context.Background(), cmd.ConnectionID, cmd.TenantID)
}

// startConnection is the start command's work, also what RestoreRunning calls on
// boot for every pipeline the database says is running (plans/stable-connections.md).
// A start that fails in a way a retry may fix is retried in the background
// (retry.go); one that fails for good marks the connection "error".
func (c *frontSystemsConsumer) startConnection(ctx context.Context, connectionID, tenantID string) {
	res := c.attemptStart(connectionID, tenantID, nil,
		func() { c.startConnection(ctx, connectionID, tenantID) })
	c.afterAttempt(connectionID, tenantID, res)
}

// attemptStart makes one start of the connection. p is the pending start a
// retry runs for; nil for a start command or boot restore, which attach to
// the connection's pending start (or make one). retry is what ReserveOrRetry
// keeps if this start is refused while another is in flight; nil from the
// retry loop, which tries again itself.
func (c *frontSystemsConsumer) attemptStart(connectionID, tenantID string, p *pendingStart, retry func()) startResult {
	logger := c.logger.With("connection_id", connectionID, "tenant_id", tenantID)

	// Restore and the start-command handler call this on different goroutines.
	// The check and the claim are one critical section, so a second start for
	// the same connection stops here instead of registering the webhooks again
	// (#397, #429). A start refused while another is in flight runs again if
	// that one gives up without activating (#424).
	//
	// The connection's pending start is attached in the same critical section
	// (the check runs with mu held), so any stop after it finds both the claim
	// and the pending start (#503). A retry whose pending start a stop has
	// already ended is refused there too.
	var created, ended bool
	claim, ok := c.starts.ReserveOrRetry(&c.mu, tenantID, connectionID,
		func() bool {
			if _, ok := c.active[connectionID]; ok {
				return true
			}
			if p != nil {
				ended = c.pending[connectionID] != p
				return ended
			}
			p, created = c.attachPendingLocked(connectionID, tenantID)
			return false
		},
		retry)
	if !ok {
		if ended {
			return startResult{outcome: startAbandoned, p: p}
		}
		logger.Warn("Front Systems connection already started or starting")
		if created {
			// Nobody runs for the pending start this call made: a start for
			// another tenant holds the claim (one for this tenant would have
			// made it first).
			c.mu.Lock()
			c.dropPendingLocked(connectionID, p)
			c.mu.Unlock()
		}
		return startResult{outcome: startRefused, p: p}
	}
	// Frees the claim on every early return below; a no-op once active. It
	// runs after the hook lock is unlocked (defers run last in, first out),
	// so a start it retries can take that lock.
	defer claim.Release()

	// Every webhook call for this connection runs under its hook lock. A
	// stop's deletes still pending are done first, so the lookup below cannot
	// find and reuse a webhook that is about to be deleted (#503).
	// The Front Systems calls run under the lifecycle context, so Stop's
	// cancel ends them. The hook lock is held through Activate, so a test's
	// StartGuard.AfterActivate hook must not wait for a stop's unregister of
	// this connection.
	hl := c.hookLock(connectionID)
	hl.Lock()
	defer hl.Unlock()
	c.mu.Lock()
	fsCtx := c.lifecycleCtxLocked()
	c.mu.Unlock()
	c.drainUnregisters(fsCtx, connectionID, logger)

	cfg, err := c.getConfig(fsCtx, connectionID, tenantID)
	if err != nil {
		return failedStart(p, err, startTransient, "")
	}
	// Auto-register our callback for the configured events, if a callback URL is
	// set. If not, the webhook is expected to be registered out of band and this
	// connector just receives on /frontsystems/events/{id}. A failed
	// registration leaves the connection not started; afterAttempt retries it
	// or marks it "error".
	ac := &activeConn{tenantID: tenantID}
	if cfg.CallbackURL != "" && len(cfg.Events) > 0 {
		ids, err := c.registerWebhooks(fsCtx, cfg, logger)
		if err != nil {
			kind, reason := classifyRegisterError(err)
			return failedStart(p, err, kind, reason)
		}
		ac.hooks = hookSet{cfg: cfg, ids: ids}
	}
	// A stop that arrived during the work above has taken the claim back and
	// wins: what this start registered is deleted again, still under the hook
	// lock. Not when the process is shutting down: the connection is then
	// still running, and boot restore reuses the registrations.
	if !claim.Activate(func() {
		if c.active == nil {
			c.active = make(map[string]*activeConn)
		}
		c.active[connectionID] = ac
		// Active now: the pending start, and any retry waiting with it, is done.
		c.dropPendingLocked(connectionID, p)
	}) {
		logger.Info("Start abandoned: a stop arrived while it was in progress")
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if !closed && len(ac.hooks.ids) > 0 {
			uctx, cancel := context.WithTimeout(fsCtx, unregisterTimeout)
			defer cancel()
			c.unregisterWebhooks(uctx, ac.hooks, logger)
		}
		return startResult{outcome: startAbandoned, p: p}
	}
	return startResult{outcome: startActivated, p: p}
}

// handleStopCommand forgets the connection, so the webhook route refuses its
// events from now on, makes a start in progress give up, and unregisters the
// connection's webhooks at Front Systems (#503). The unregister is best
// effort and runs on its own goroutine: the connection is stopped here
// whether or not it succeeds. A stop naming another tenant than the one the
// connection was started for leaves the connection running.
func (c *frontSystemsConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		c.logger.Error("parse stop command", "error", err)
		return
	}
	logger := c.logger.With("connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)
	var (
		unregister bool
		bgCtx      context.Context
	)
	c.mu.Lock()
	// The stop must name the tenant the connection is starting or active for,
	// in its payload and on its subject; one naming another tenant changes
	// nothing (#498): no forget, no unregister. A start still in progress
	// gives up instead of finishing after the stop (#397).
	starting, err := c.starts.StopForTenantLocked(msg.Subject, cmd.TenantID, cmd.ConnectionID)
	if err != nil {
		c.mu.Unlock()
		logger.Warn("Ignoring stop command for another tenant", "error", err)
		return
	}
	// A start waiting to retry ends here, and one in progress no longer
	// retries. The guard checked the tenant only if a start holds the claim
	// right now or the connection was active before, so it is checked again.
	retrying := false
	if p, ok := c.pending[cmd.ConnectionID]; ok {
		if p.tenantID != cmd.TenantID {
			c.mu.Unlock()
			logger.Warn("Ignoring stop command for another tenant", "starting_for", p.tenantID)
			return
		}
		c.dropPendingLocked(cmd.ConnectionID, p)
		retrying = true
	}
	if ac, ok := c.active[cmd.ConnectionID]; ok {
		delete(c.active, cmd.ConnectionID)
		// Handed over in the critical section that forgets the connection, so
		// a start that follows finds them pending and deletes them first.
		if len(ac.hooks.ids) > 0 && !c.closed {
			if c.pendingUnregister == nil {
				c.pendingUnregister = make(map[string][]hookSet)
			}
			c.pendingUnregister[cmd.ConnectionID] = append(c.pendingUnregister[cmd.ConnectionID], ac.hooks)
			bgCtx = c.lifecycleCtxLocked()
			c.bg.Add(1)
			unregister = true
		}
	}
	c.mu.Unlock()
	switch {
	case starting:
		logger.Info("Stop arrived while the connection was starting; the start is abandoned")
	case retrying:
		logger.Info("Stop arrived while the connection's start was waiting to retry; the retries end")
	}
	if unregister {
		go func() {
			defer c.bg.Done()
			hl := c.hookLock(cmd.ConnectionID)
			hl.Lock()
			defer hl.Unlock()
			c.drainUnregisters(bgCtx, cmd.ConnectionID, logger)
		}()
	}
}

// drainUnregisters deletes the webhooks stops have left pending for the
// connection. The caller holds the connection's hook lock.
func (c *frontSystemsConsumer) drainUnregisters(ctx context.Context, connectionID string, logger *slog.Logger) {
	c.mu.Lock()
	pending := c.pendingUnregister[connectionID]
	delete(c.pendingUnregister, connectionID)
	c.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, unregisterTimeout)
	defer cancel()
	for _, hs := range pending {
		c.unregisterWebhooks(ctx, hs, logger)
	}
}

// unregisterWebhooks deletes each webhook with DELETE {base}/api/webhooks/{id}.
// Best effort: a failure is logged and the others are still tried. A 404
// means it is gone already and counts as done.
func (c *frontSystemsConsumer) unregisterWebhooks(ctx context.Context, hs hookSet, logger *slog.Logger) {
	if hs.cfg == nil {
		return
	}
	for _, id := range hs.ids {
		status, resp, err := c.do(ctx, hs.cfg, http.MethodDelete, "/api/webhooks/"+url.PathEscape(id), nil)
		switch {
		case err != nil:
			logger.Warn("Could not unregister a Front Systems webhook; it stays registered", "webhook_id", id, "error", err)
		case status == http.StatusNotFound || (status >= 200 && status < 300):
			logger.Info("Unregistered Front Systems webhook", "webhook_id", id)
		default:
			logger.Warn("Could not unregister a Front Systems webhook; it stays registered",
				"webhook_id", id, "status", status, "body", snippet(resp))
		}
	}
}

// webhook is Front Systems' WebhookViewModel.
type webhook struct {
	ID    string `json:"id"`
	Event string `json:"event"`
	URL   string `json:"url"`
}

// registerWebhooks makes sure our callback URL is registered for each
// configured event type and returns the ids of those registrations, to
// delete on stop. It lists the existing webhooks first (GET
// {base}/api/webhooks) and reuses those with the same event and URL, so a
// restart or a stop and start doesn't register the callback again (#503).
// The rest it creates with POST {base}/api/webhooks {"event":..., "url":...},
// taking the id from the response when it has one and from a second list
// otherwise. A list that fails is logged and skipped: the events are then
// posted, as before.
func (c *frontSystemsConsumer) registerWebhooks(ctx context.Context, cfg *FrontSystemsConfig, logger *slog.Logger) ([]string, error) {
	callback := strings.TrimRight(cfg.CallbackURL, "/")
	existing, err := c.listWebhooks(ctx, cfg)
	if err != nil {
		logger.Warn("Could not list Front Systems webhooks; registering without checking for existing ones", "error", err)
	}
	var ids []string
	seen := map[string]bool{}
	add := func(found ...string) {
		for _, id := range found {
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	var noID []string    // events created without an id in the response
	var created []string // ids this call created, undone if a later event fails
	fail := func(err error) ([]string, error) {
		// The connection is not started, so no stop would delete these.
		c.unregisterWebhooks(ctx, hookSet{cfg: cfg, ids: created}, logger)
		return nil, err
	}
	for _, event := range cfg.Events {
		if found := matchingWebhooks(existing, event, callback); len(found) > 0 {
			add(found...)
			if len(found) > 1 {
				logger.Warn("Front Systems has this webhook more than once; all of them are removed on stop", "event", event, "count", len(found))
			}
			logger.Info("Front Systems webhook already registered", "event", event)
			continue
		}
		body, _ := json.Marshal(map[string]string{"event": event, "url": callback})
		status, resp, err := c.do(ctx, cfg, http.MethodPost, "/api/webhooks", body)
		if err != nil {
			return fail(fmt.Errorf("register %s: %w", event, err))
		}
		if status < 200 || status >= 300 {
			return fail(fmt.Errorf("register %s: %w", event, &fsStatusError{status: status, body: snippet(resp)}))
		}
		var hook webhook
		if json.Unmarshal(resp, &hook) == nil && hook.ID != "" {
			add(hook.ID)
			created = append(created, hook.ID)
		} else {
			noID = append(noID, event)
		}
		logger.Info("Registered Front Systems webhook", "event", event)
	}
	if len(noID) > 0 {
		listed, err := c.listWebhooks(ctx, cfg)
		if err != nil {
			logger.Warn("Could not read back the new Front Systems webhooks' ids; they will not be removed on stop", "events", noID, "error", err)
			return ids, nil
		}
		for _, event := range noID {
			found := matchingWebhooks(listed, event, callback)
			if len(found) == 0 {
				logger.Warn("New Front Systems webhook not in the list; it will not be removed on stop", "event", event)
			}
			add(found...)
		}
	}
	return ids, nil
}

// listWebhooks reads the webhooks registered under the connection's keys.
func (c *frontSystemsConsumer) listWebhooks(ctx context.Context, cfg *FrontSystemsConfig) ([]webhook, error) {
	status, resp, err := c.do(ctx, cfg, http.MethodGet, "/api/webhooks", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", status, snippet(resp))
	}
	// The response body is not documented: a bare array is expected, and the
	// usual wrappers are accepted too.
	var hooks []webhook
	if err := json.Unmarshal(resp, &hooks); err == nil {
		return hooks, nil
	}
	var wrapped struct {
		Value []webhook `json:"value"`
		Items []webhook `json:"items"`
		Data  []webhook `json:"data"`
	}
	if err := json.Unmarshal(resp, &wrapped); err != nil {
		return nil, fmt.Errorf("parse webhook list: %w (body: %s)", err, snippet(resp))
	}
	switch {
	case wrapped.Value != nil:
		return wrapped.Value, nil
	case wrapped.Items != nil:
		return wrapped.Items, nil
	case wrapped.Data != nil:
		return wrapped.Data, nil
	}
	return nil, fmt.Errorf("parse webhook list: no array in %s", snippet(resp))
}

// matchingWebhooks returns the ids of the webhooks for event that call
// callback (trailing slashes ignored, event names compared without case).
func matchingWebhooks(hooks []webhook, event, callback string) []string {
	var ids []string
	for _, h := range hooks {
		if h.ID != "" && strings.EqualFold(h.Event, event) && strings.TrimRight(h.URL, "/") == callback {
			ids = append(ids, h.ID)
		}
	}
	return ids
}

// do performs a Front Systems API request with the two required auth headers.
func (c *frontSystemsConsumer) do(ctx context.Context, cfg *FrontSystemsConfig, method, path string, body []byte) (int, []byte, error) {
	if cfg.BaseURL == "" {
		return 0, nil, fmt.Errorf("%w: front_systems base_url is required (per-partner Azure APIM host)", errBadBaseURL)
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cfg.BaseURL, "/")+path, rdr)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errBadBaseURL, err)
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", cfg.SubscriptionKey)
	req.Header.Set("x-api-key", cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, out, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// --- DB helpers ---

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its Front Systems consumer node config. Its errors are *startError, which
// say whether a retry may help: a connection that isn't there for this tenant
// or has no Front Systems consumer node is not ours to start (every consumer
// sees every start command); a database that doesn't answer is transient; a
// secret that can't be read or a node config that doesn't parse is permanent.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (c *frontSystemsConsumer) getConfig(ctx context.Context, connectionID, tenantID string) (*FrontSystemsConfig, error) {
	var nodesJSON json.RawMessage
	if err := c.db.QueryRowContext(ctx,
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &startError{kind: startNotOurs, err: fmt.Errorf("connection not found: %w", err)}
		}
		return nil, &startError{kind: startTransient, err: fmt.Errorf("look up connection: %w", err)}
	}
	var nodes []node
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, &startError{kind: startNotOurs, err: fmt.Errorf("parse nodes: %w", err)}
	}
	reader := crypto.NewSQLSecretReader(c.db)
	for _, n := range nodes {
		if n.Type != "consumer" {
			continue
		}
		// The node's type is read before its secrets, so another consumer's
		// node never has its secrets resolved here.
		var nc nodeConfig
		if err := json.Unmarshal(n.Config, &nc); err != nil || nc.Type != "front_systems" || nc.FrontSystems == nil {
			continue
		}
		resolved, rerr := crypto.ResolveSecretsInJSON(ctx, reader, tenantID, n.Config)
		if rerr != nil {
			if transientDBError(rerr) {
				return nil, &startError{kind: startTransient, err: fmt.Errorf("resolve secrets: %w", rerr)}
			}
			return nil, &startError{kind: startPermanent, err: fmt.Errorf("resolve secrets: %w", rerr),
				reason: "Front Systems: a secret the connection's keys refer to could not be read"}
		}
		nc = nodeConfig{}
		if err := json.Unmarshal(resolved, &nc); err != nil || nc.FrontSystems == nil {
			return nil, &startError{kind: startPermanent, err: fmt.Errorf("parse front_systems config after resolving secrets: %v", err),
				reason: "Front Systems: the node's configuration could not be read"}
		}
		return nc.FrontSystems, nil
	}
	return nil, &startError{kind: startNotOurs, err: errors.New("no front_systems consumer node found")}
}

// getConnectionTenant resolves the owning tenant for a connection id (webhook
// routing). lint:tenant-ok — resolves a connection's own tenant by PK.
func (c *frontSystemsConsumer) getConnectionTenant(connectionID string) (string, error) {
	var tenantID string
	err := c.db.QueryRow(`SELECT tenant_id FROM connections WHERE id = $1`, connectionID).Scan(&tenantID)
	return tenantID, err
}
