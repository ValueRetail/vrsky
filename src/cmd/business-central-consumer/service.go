package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	defaultScope       = "https://api.businesscentral.dynamics.com/.default"
	defaultAPIHost     = "https://api.businesscentral.dynamics.com"
	defaultEnvironment = "Production"
	defaultEntity      = "items"

	// BC's own last-modified field on the v2.0 API entities. Overridable
	// because a custom API page may expose a differently named one.
	defaultCursorField = "lastModifiedDateTime"

	// maxCursorSkew is how far ahead of now a record's cursor may be before
	// the consumer logs it at Warn as a likely bad stamp (#591). It is not the
	// watermark ceiling: that is now itself. A record dated after now, by any
	// amount, is published once and remembered but never becomes the
	// watermark, because a watermark in the future hides every record BC
	// stamps before it. Within the skew that is BC's clock running a little
	// ahead of this pod's, which is normal and not worth a Warn.
	//
	// It is also the line past which a stored watermark counts as frozen
	// (stored by the code before #591) and is recovered from.
	maxCursorSkew = 5 * time.Minute

	// frozenRecoveryMargin is how far before the checkpoint's last save a
	// frozen watermark's recovery read starts. It covers the length of the
	// poll that stored the bad watermark (records modified while it paged)
	// and BC's clock running behind this pod's.
	frozenRecoveryMargin = time.Hour

	// aheadWarnSize is the size of the remembered set of future-dated
	// records past which the node logs that its cursor_field is probably a
	// business date rather than a last-modified stamp.
	aheadWarnSize = 1000
)

// bcConsumer polls a Business Central OData entity per active connection and
// publishes the results. It is an SDK Consumer: Configure wires deps, Run
// subscribes to command subjects and blocks, Stop cancels pollers.
type bcConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	// Set when the SDK runs this connector with a payload store (RunStream):
	// pictures larger than inlineMax are streamed into it instead of read
	// into memory.
	publishStream sdk.PublishStreamFunc
	inlineMax     int
	checkpoints   checkpoint.Store

	// now is the clock the watermark ceiling is measured against; nil =
	// time.Now.
	now func() time.Time

	httpClient *http.Client

	active map[string]*poller
	mu     sync.RWMutex
	// starts holds the claim of each start in progress (#397); guarded by mu.
	starts sdk.StartGuard

	startSub, stopSub, resendSub *nats.Subscription
}

// poller is one running connection: how to stop it, and how to ask it for an
// immediate full poll.
type poller struct {
	cancel context.CancelFunc
	resend chan struct{} // capacity 1
}

// BCConfig is the per-node configuration (config.business_central). client_secret
// is stored as client_secret_secret_id and resolved to plaintext at start.
type BCConfig struct {
	AADTenantID  string `json:"aad_tenant_id"` // Entra tenant (GUID or domain)
	Environment  string `json:"environment"`   // e.g. Production
	CompanyID    string `json:"company_id"`    // BC company GUID
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"` // from client_secret_secret_id
	Entity       string `json:"entity"`        // e.g. items, customers, salesOrders
	Filter       string `json:"filter"`        // optional OData $filter

	// Incremental polling: remember the newest CursorField value published and
	// ask only for what changed since. Off by default — turning it on changes
	// what a running pipeline delivers, which is the connection owner's call.
	Incremental bool   `json:"incremental"`
	CursorField string `json:"cursor_field"`

	// PageSize asks Business Central to page the response, via
	// `Prefer: odata.maxpagesize`. Server-driven paging is the only thing that
	// makes BC emit @odata.nextLink — $top caps the result set instead of
	// paging it. Unset leaves BC on its own default, which returns most
	// entities whole.
	PageSize int `json:"page_size"`

	// NodeID keys the watermark. It comes from the connection's node, not from
	// the node's own config, so it is never carried in the stored JSON.
	NodeID string `json:"-"`

	// Optional overrides (default to the BC cloud endpoints; set for on-prem or tests).
	APIBaseURL string `json:"api_base_url"`
	TokenURL   string `json:"token_url"`
	Scope      string `json:"scope"`

	PollIntervalSeconds int `json:"poll_interval_seconds"`

	// Pictures makes the node also publish one message per record picture
	// (the image file), after the records it belongs to. See pictures.go.
	Pictures bool `json:"pictures"`

	// pictureSeen is record id → the picture this node last published for
	// it, so an unchanged picture is not downloaded again and a removed one
	// is reported to the till exactly once (a .no-picture marker). It is
	// persisted in the node's checkpoint state, so a restart or redeploy does
	// not re-send every picture. nil = not loaded yet.
	pictureSeen map[string]seenPicture

	// aheadSeen is record key → the cursor value it carried when it was
	// published, for the records the next poll's filter returns again: the
	// ones dated after now (#591), which do not move the watermark while they
	// are ahead, and, for an Edm.Date cursor read back with `ge`, the ones
	// dated on the watermark day (#602). It is what stops them being sent
	// again every poll. Persisted in the node's checkpoint state. nil = not
	// loaded yet.

	aheadSeen map[string]string
	// aheadWarned is set once the size Warn for aheadSeen was logged, so it
	// is logged once per process rather than every poll.
	aheadWarned bool

	// resendAll makes the next fetch ignore the incremental watermark and the
	// seen pictures, so everything goes out again — for a till that just
	// joined the pipeline's group. Cleared once that fetch completes.
	resendAll bool
}

type nodeConfig struct {
	Type            string    `json:"type"`
	BusinessCentral *BCConfig `json:"business_central"`
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

func (c *bcConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("business-central-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	c.db = res.DB
	c.nc = res.NATS
	c.logger = res.Logger
	c.inlineMax = res.InlineMaxBytes()
	if c.checkpoints == nil {
		c.checkpoints = checkpoint.NewPostgresStore(res.DB)
	}
	c.active = make(map[string]*poller)
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	// Aux endpoint for the UI's pre-deploy "show data structure" preview.
	c.RegisterHTTPHandler("/sample-data/", c.handleSampleData())
	res.Health.SetReady(true)
	return nil
}

// RunStream is Run with a way to stream large payloads (pictures) into the
// payload store. The SDK calls it instead of Run when a store is configured.
func (c *bcConsumer) RunStream(ctx context.Context, publish sdk.PublishFunc, publishStream sdk.PublishStreamFunc) error {
	c.publishStream = publishStream
	return c.Run(ctx, publish)
}

func (c *bcConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
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
	resendSub, err := c.nc.Subscribe("vrsky.commands.*.connection.resend", c.handleResendCommand)
	if err != nil {
		return fmt.Errorf("subscribe resend commands: %w", err)
	}
	c.resendSub = resendSub

	c.logger.Info("Subscribed to NATS command topics")
	// Bring back what the database says is running: start/stop are NATS commands
	// and are not persisted, so without this a restart silently stopped every
	// pipeline on this service until someone redeployed it.
	sdk.RestoreRunning(ctx, c.db, c.logger, "business_central", c.startConnection)

	<-ctx.Done()
	return nil
}

func (c *bcConsumer) Stop(ctx context.Context) error {
	if c.startSub != nil {
		_ = c.startSub.Unsubscribe()
	}
	if c.stopSub != nil {
		_ = c.stopSub.Unsubscribe()
	}
	if c.resendSub != nil {
		_ = c.resendSub.Unsubscribe()
	}
	c.mu.Lock()
	for _, p := range c.active {
		p.cancel()
	}
	c.active = make(map[string]*poller)
	// A start still in flight must not leave a poller nobody will cancel.
	c.starts.StopAllLocked()
	c.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
	return nil
}

func (c *bcConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		c.logger.Error("parse start command", "error", err)
		return
	}
	c.startConnection(context.Background(), cmd.ConnectionID, cmd.TenantID)
}

// startConnection is the start command's work, also what RestoreRunning calls on
// boot for every pipeline the database says is running (plans/stable-connections.md).
func (c *bcConsumer) startConnection(ctx context.Context, connectionID, tenantID string) {
	_ = ctx
	logger := c.logger.With("connection_id", connectionID, "tenant_id", tenantID)

	// Restore and the start-command handler call this on different goroutines.
	// The check and the claim are one critical section, so a second start for
	// the same connection stops here instead of starting a second poller (#397).
	// A start refused while another is in flight is not lost: it runs again if
	// that one gives up without activating (#424). The refused call returns
	// right away, so the retry gets its caller's ctx, before any reassignment.
	claim, ok := c.starts.ReserveOrRetry(&c.mu, tenantID, connectionID, func() bool { _, ok := c.active[connectionID]; return ok },
		func() { c.startConnection(ctx, connectionID, tenantID) })
	if !ok {
		return
	}
	// Frees the claim on every early return below; a no-op once active.
	defer claim.Release()
	cfg, err := c.getConfig(context.Background(), connectionID, tenantID)
	if err != nil {
		logger.Debug("Not a Business Central consumer for this connection", "error", err)
		return
	}
	if cfg.AADTenantID == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.CompanyID == "" {
		logger.Error("Business Central config incomplete (need aad_tenant_id, company_id, client_id, client_secret_secret_id)")
		return
	}
	if cfg.PollIntervalSeconds <= 0 {
		logger.Error("Business Central consumer needs poll_interval_seconds > 0")
		return
	}
	if err := cfg.validatePictures(); err != nil {
		logger.Error("Business Central consumer not started", "error", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &poller{cancel: cancel, resend: make(chan struct{}, 1)}
	// A stop that arrived during the work above has taken the claim back and wins.
	if !claim.Activate(func() { c.active[connectionID] = p }) {
		cancel()
		logger.Info("Start abandoned: a stop arrived while it was in progress")
		return
	}

	logger.Info("Starting Business Central poller", "entity", cfg.effectiveEntity(), "interval", cfg.PollIntervalSeconds, "pictures", cfg.Pictures)
	go c.runPoller(ctx, connectionID, tenantID, cfg, p.resend)
}

// handleResendCommand asks a running poller to send everything again on an
// immediate poll: records regardless of the incremental watermark, pictures
// regardless of what it already sent. A connection this service does not run
// is ignored, as is a tenant mismatch.
func (c *bcConsumer) handleResendCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		return
	}
	c.mu.RLock()
	p := c.active[cmd.ConnectionID]
	c.mu.RUnlock()
	if p == nil {
		return
	}
	select {
	case p.resend <- struct{}{}:
	default: // one is already queued
	}
}

func (c *bcConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		return
	}
	c.mu.Lock()
	// The stop must name the tenant the connection is starting or active for,
	// in its payload and on its subject; one naming another tenant changes
	// nothing (#498). A start still in progress gives up instead of finishing
	// after the stop (#397).
	_, err := c.starts.StopForTenantLocked(msg.Subject, cmd.TenantID, cmd.ConnectionID)
	if err != nil {
		c.mu.Unlock()
		c.logger.Warn("Ignoring stop command for another tenant", "connection_id", cmd.ConnectionID, "error", err)
		return
	}
	if p, ok := c.active[cmd.ConnectionID]; ok {
		p.cancel()
		delete(c.active, cmd.ConnectionID)
	}
	c.mu.Unlock()
}

func (c *bcConsumer) runPoller(ctx context.Context, connID, tenantID string, cfg *BCConfig, resend <-chan struct{}) {
	logger := c.logger.With("connection_id", connID)
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(c.httpClient)

	poll := func() {
		if err := c.fetchAndPublish(ctx, connID, tenantID, cfg, tok, logger); err != nil && ctx.Err() == nil {
			logger.Error("Business Central fetch failed", "error", err)
		}
	}
	poll()
	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		case <-resend:
			logger.Info("Resend requested: next poll sends everything")
			cfg.resendAll = true
			poll()
		}
	}
}

// odataPage is the OData v4 collection envelope.
type odataPage struct {
	Value    []json.RawMessage `json:"value"`
	NextLink string            `json:"@odata.nextLink"`
}

// fetchAndPublish GETs the OData entity, follows @odata.nextLink, and publishes
// each page's records as one JSON-array envelope.
func (c *bcConsumer) fetchAndPublish(ctx context.Context, connID, tenantID string, cfg *BCConfig, tok *oauthcc.Client, logger *slog.Logger) error {
	if err := cfg.validatePictures(); err != nil {
		return err
	}
	cursor, rec := "", recovery{}
	if cfg.Incremental && !cfg.resendAll {
		cursor, rec = c.startCursor(cfg, c.loadCursor(ctx, tenantID, connID, cfg.NodeID, logger), logger)
	}
	if cfg.resendAll {
		cfg.pictureSeen = map[string]seenPicture{}
	} else if cfg.Pictures && cfg.pictureSeen == nil {
		cfg.pictureSeen = c.loadPictureState(ctx, tenantID, connID, cfg.NodeID, logger)
	}

	if cfg.resendAll {
		cfg.aheadSeen = map[string]string{}
	} else if cfg.Incremental && cfg.aheadSeen == nil {
		cfg.aheadSeen = c.loadAheadState(ctx, tenantID, connID, cfg.NodeID, logger)
	}

	next := cfg.entityURL(cursor)
	page, total := 0, 0
	var pics pictureCounts
	watermark := cursorTracker{ceiling: c.clock()}
	ahead := map[string]string{} // records dated after now this poll carried
	inFeed := map[string]bool{}  // record ids this poll carried, for the sweep
	for next != "" {
		page++
		body, err := c.get(ctx, tok, next, cfg.PageSize)
		if err != nil {
			return err
		}
		var p odataPage
		if err := json.Unmarshal(body, &p); err != nil {
			return fmt.Errorf("parse OData page: %w", err)
		}
		if len(p.Value) > 0 {
			out := p.Value
			if cfg.Incremental {
				out = c.withoutSentAhead(cfg, &watermark, p.Value, ahead, logger)
			}
			if len(out) > 0 {
				if err := c.publishRecords(ctx, connID, tenantID, cfg.effectiveEntity(), out); err != nil {
					return fmt.Errorf("publish records: %w", err)
				}
			}
			if cfg.Pictures {
				n, err := c.publishPictures(ctx, connID, tenantID, cfg, tok, p.Value, inFeed, logger)
				pics.add(n)
				if err != nil {
					return fmt.Errorf("publish pictures: %w", err)
				}
			}
			total += len(out)
		}
		next = p.NextLink
	}

	// An incremental feed carries only records BC marked as modified, and
	// removing or replacing a picture may not do that. Check the pictures we
	// remember for records this poll did not carry.
	if cfg.Pictures && cfg.Incremental {
		n, err := c.sweepPictures(ctx, connID, tenantID, cfg, tok, inFeed, logger)
		pics.add(n)
		if err != nil {
			return fmt.Errorf("sweep pictures: %w", err)
		}
	}

	// A resend is complete only once everything has gone out; a fetch that
	// fails halfway keeps the flag, so the next poll resends again.
	cfg.resendAll = false

	// Advance only once every page has landed. A fetch that dies halfway
	// resumes from the old watermark — the records it already published arrive
	// twice, which the pipeline is built for, rather than being skipped, which
	// it is not.
	//
	// A recovery read stores where it started when it found nothing to take
	// the watermark from (only future-dated records, say): keeping the stored
	// watermark there would put the frozen one back.
	newCursor, replace := "", false
	switch {
	case rec.resaved(ahead, watermark.ceiling):
		// The bounded window cannot be trusted (see recovery.resaved): store
		// no watermark, so the next poll reads from the start. The remembered
		// set is saved below, so the future-dated records this poll sent are
		// not sent a third time.
		newCursor, replace = "", true
		logger.Warn("Business Central recovery read found a future-dated record below the frozen watermark, so "+
			"the node may have saved again after it froze; the next poll reads the entity from the start",
			"watermark", rec.stored, "resume_from", cursor)
	case cfg.Incremental && watermark.raw != "":
		newCursor = watermark.raw
	case rec.active:
		newCursor, replace = cursor, true
	}
	// A date watermark is read back with `ge`, so the records dated on that
	// day come back on every poll, like the future-dated ones. Remember both.
	for k, v := range watermark.atWatermark {
		ahead[k] = v
	}
	aheadChanged := cfg.Incremental && !sameAhead(cfg.aheadSeen, ahead)
	if cfg.Incremental {
		cfg.aheadSeen = ahead
		c.warnAheadSize(cfg, logger)
	}
	if newCursor != "" || replace || cfg.Pictures || aheadChanged {
		c.saveProgress(ctx, tenantID, connID, cfg.NodeID, newCursor, replace, int64(total), cfg.nodeState(), logger)
	}

	if cfg.Pictures {
		logger.Info("Business Central fetch complete",
			"entity", cfg.effectiveEntity(), "records", total, "pages", page,
			"pictures_sent", pics.sent, "pictures_unchanged", pics.unchanged, "without_picture", pics.none,
			"pictures_removed", pics.removed, "pictures_swept", pics.swept,
			"incremental", cfg.Incremental, "since", cursor)
		return nil
	}
	logger.Info("Business Central fetch complete",
		"entity", cfg.effectiveEntity(), "records", total, "pages", page,
		"incremental", cfg.Incremental, "since", cursor)
	return nil
}

// cursorTracker keeps the newest cursor-field value seen in a fetch.
//
// The raw string is kept beside the parsed time because that string came from
// Business Central, so it is by construction a literal BC accepts back in a
// $filter. Comparison goes through the parsed time: "…:00.5Z" sorts before
// "…:00Z" as text while being the later instant.
//
// A value later than ceiling (when set) is not a watermark candidate (#591).
// The caller sets the ceiling to now: the next poll asks for records after
// the watermark, and BC stamps what it writes with its own clock, so a
// watermark in the future would hide every record BC writes until the clock
// passes it (forever, for a record stamped 2099). observe reports such
// records instead.
//
// A value can be an Edm.Date ("2026-10-29", no time of day) as well as a
// DateTime (#602). A date has its own ceiling, the day before the ceiling's
// UTC date (dateCeiling), and its watermark is read back with `ge`, so the
// tracker also keeps the records dated on the watermark day (atWatermark).
type cursorTracker struct {
	newest   time.Time
	raw      string
	dateOnly bool      // raw is an Edm.Date
	ceiling  time.Time // zero = no bound

	// atWatermark is record key → cursor value for the records dated on the
	// current date watermark (a DateTime watermark keeps none). The next
	// poll's `ge` filter returns them again.
	atWatermark map[string]string
}

// aheadRecord is a record whose cursor lies after the tracker's ceiling.
type aheadRecord struct {
	Key    string // the record's id, for dedupe and the log
	Cursor string // the cursor value as Business Central wrote it
}

// cursorDateLayout is the OData v4 Edm.Date literal, which BC writes in
// responses and accepts unquoted in a $filter.
const cursorDateLayout = "2006-01-02"

// parseCursor reads a cursor value: an RFC 3339 DateTime, or an Edm.Date.
func parseCursor(s string) (t time.Time, dateOnly, ok bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, false, true
	}
	if t, err := time.Parse(cursorDateLayout, s); err == nil {
		return t, true, true
	}
	return time.Time{}, false, false
}

// isDateCursor reports whether s is an Edm.Date value.
func isDateCursor(s string) bool {
	_, dateOnly, ok := parseCursor(s)
	return ok && dateOnly
}

// dateCeiling is the latest date that may become a date watermark when the
// ceiling for a DateTime is now: the day before now's UTC date.
//
// A date has no time of day and the watermark is read back with `ge`, so
// nothing dated on or after it is lost. What the ceiling protects is a date
// ahead of the day BC itself is on. BC's local date is up to 14 hours ahead
// of or 12 behind UTC; a watermark on today's UTC date could be a day ahead
// of BC's today and hide the records BC stamps with its own date. Yesterday
// (UTC) is never ahead of BC's today in any time zone. The records dated
// after it are still sent, once, and are remembered like any future-dated
// record.
func dateCeiling(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day()-1, 0, 0, 0, 0, time.UTC)
}

func (t *cursorTracker) observe(records []json.RawMessage, field string) []aheadRecord {
	var ahead []aheadRecord
	for _, rec := range records {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rec, &fields); err != nil {
			continue
		}
		raw, ok := fields[field]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		parsed, dateOnly, ok := parseCursor(s)
		if !ok {
			continue
		}
		if !t.ceiling.IsZero() {
			limit := t.ceiling
			if dateOnly {
				limit = dateCeiling(t.ceiling)
			}
			if parsed.After(limit) {
				ahead = append(ahead, aheadRecord{Key: recordKey(rec, fields), Cursor: s})
				continue
			}
		}
		switch {
		case parsed.After(t.newest):
			t.newest, t.raw, t.dateOnly = parsed, s, dateOnly
			t.atWatermark = nil
			if dateOnly {
				t.atWatermark = map[string]string{recordKey(rec, fields): s}
			}
		case dateOnly && t.dateOnly && parsed.Equal(t.newest):
			t.atWatermark[recordKey(rec, fields)] = s
		}
	}
	return ahead
}

// recordKey identifies a record across polls: its OData id, else its number,
// else a hash of the whole record (so an edit counts as a new record, which
// is right for an entity without an id).
func recordKey(rec json.RawMessage, fields map[string]json.RawMessage) string {
	for _, f := range []string{"id", "number"} {
		var s string
		if raw, ok := fields[f]; ok && json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
	}
	sum := sha256.Sum256(rec)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (c *bcConsumer) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// withoutSentAhead feeds a page to the watermark and returns the records to
// publish. A record dated after now does not move the watermark. It is
// published once; because the watermark stayed behind it, the filter returns
// it on every poll until the clock catches up, so a record already sent with
// the same cursor value is left out, including on the poll where the clock
// finally reaches it and it becomes the watermark. Edited again, its cursor
// changes and it goes out again.
//
// A record a few seconds ahead is BC's clock running ahead of this pod's: it
// is held one poll and passes quietly. Only one dated more than
// maxCursorSkew ahead is logged at Warn, as a likely bad stamp.
func (c *bcConsumer) withoutSentAhead(cfg *BCConfig, w *cursorTracker, records []json.RawMessage,
	ahead map[string]string, logger *slog.Logger) []json.RawMessage {
	field := cfg.effectiveCursorField()
	for _, h := range w.observe(records, field) {
		ahead[h.Key] = h.Cursor
		if prev, ok := cfg.aheadSeen[h.Key]; ok && prev == h.Cursor {
			continue
		}
		t, dateOnly, ok := parseCursor(h.Cursor)
		if !ok {
			continue
		}
		// A DateTime is logged when more than maxCursorSkew ahead. A date
		// when it is later than tomorrow's UTC date: today's and tomorrow's
		// are just BC's own date in another time zone.
		warnAfter, layout := w.ceiling.Add(maxCursorSkew), time.RFC3339
		if dateOnly {
			warnAfter, layout = dateCeiling(w.ceiling).AddDate(0, 0, 2), cursorDateLayout
		}
		if !t.After(warnAfter) {
			continue
		}
		logger.Warn("Business Central record is dated in the future; "+
			"published, but the incremental watermark ignores it",
			"record_id", h.Key, "cursor_field", field,
			"cursor_value", h.Cursor, "bound", warnAfter.UTC().Format(layout))
	}
	if len(cfg.aheadSeen) == 0 {
		return records
	}
	out := make([]json.RawMessage, 0, len(records))
	for _, rec := range records {
		var fields map[string]json.RawMessage
		if json.Unmarshal(rec, &fields) == nil {
			var cur string
			if raw, ok := fields[field]; ok && json.Unmarshal(raw, &cur) == nil {
				if prev, ok := cfg.aheadSeen[recordKey(rec, fields)]; ok && prev == cur {
					continue
				}
			}
		}
		out = append(out, rec)
	}
	return out
}

// warnAheadSize logs once when the remembered set grows past aheadWarnSize.
// For a DateTime cursor that is future-dated records. A last-modified stamp
// is only ever a little ahead, so a large set means cursor_field is a
// business date (a due date): every poll then re-reads all those records, and
// the checkpoint carries each one. For a date cursor it is the records dated
// on the watermark day and later, which every poll reads again.
func (c *bcConsumer) warnAheadSize(cfg *BCConfig, logger *slog.Logger) {
	if len(cfg.aheadSeen) <= aheadWarnSize {
		cfg.aheadWarned = false
		return
	}
	if cfg.aheadWarned {
		return
	}
	cfg.aheadWarned = true
	dateOnly := false
	for _, v := range cfg.aheadSeen { // one field, one type: any value tells
		dateOnly = isDateCursor(v)
		break
	}
	if dateOnly {
		logger.Warn("many Business Central records are dated on or after the date watermark; every poll "+
			"re-reads them and the checkpoint lists each one. A date cursor_field has no time of day, "+
			"so the whole watermark day is read again; a last-modified DateTime field avoids that",
			"cursor_field", cfg.effectiveCursorField(), "records", len(cfg.aheadSeen))
		return
	}
	logger.Warn("many Business Central records are dated in the future; every poll re-reads them. "+
		"Is cursor_field a last-modified field?",
		"cursor_field", cfg.effectiveCursorField(), "records", len(cfg.aheadSeen))
}

func sameAhead(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// loadAheadState reads the remembered far-future records back from the
// checkpoint row. Nothing remembered costs one re-send of each, not data.
func (c *bcConsumer) loadAheadState(ctx context.Context, tenantID, connID, nodeID string, logger *slog.Logger) map[string]string {
	seen := map[string]string{}
	if c.checkpoints == nil || nodeID == "" {
		return seen
	}
	cp, err := c.checkpoints.Get(ctx, tenantID, connID, nodeID)
	if err != nil {
		logger.Error("read Business Central cursor state; far-future records may be sent again", "error", err)
		return seen
	}
	if cp == nil || len(cp.State) == 0 {
		return seen
	}
	var st pictureState
	if err := json.Unmarshal(cp.State, &st); err != nil {
		logger.Error("parse Business Central cursor state; far-future records may be sent again", "error", err)
		return seen
	}
	if st.Ahead != nil {
		seen = st.Ahead
	}
	return seen
}

// loadCursor returns the node's checkpoint row (the watermark is its
// LastProcessedMessageID), or nil for a first run. A store that cannot be
// read is logged and treated as a first run: a full re-read is noisy, whereas
// refusing to poll is an outage.
func (c *bcConsumer) loadCursor(ctx context.Context, tenantID, connID, nodeID string, logger *slog.Logger) *checkpoint.Checkpoint {
	if c.checkpoints == nil || nodeID == "" {
		return nil
	}
	cp, err := c.checkpoints.Get(ctx, tenantID, connID, nodeID)
	if err != nil {
		logger.Error("read Business Central watermark; polling from the start", "error", err)
		return nil
	}
	return cp
}

// recovery describes how a fetch gets past a frozen watermark.
type recovery struct {
	// active: the stored watermark was frozen and this fetch reads from
	// somewhere else; the caller then stores an explicit watermark at the
	// end, so the frozen one cannot come back.
	active bool
	// bounded: the read starts shortly before LastProcessedAt rather than
	// from the start of the entity.
	bounded bool
	stored  string    // the frozen watermark as stored
	frozen  time.Time // it, parsed
}

// resaved reports whether a bounded recovery read saw a reason to distrust
// its window. The window rests on the frozen node never having saved after
// the poll that stored the frozen watermark. But the code before #591 did
// save again whenever it read an even later value (2099, then 2100), and
// then LastProcessedAt is that second save; records modified between the
// two saves are older than the window. The read itself shows the evidence:
// it returns every future-dated record, and a value more than maxCursorSkew
// ahead yet below the stored watermark is one the node once froze on, or
// was ahead of a freeze on. Then the caller falls back to a full read.
//
// Not detected: the first far-future record deleted since, or the same
// record moved from 2099 to 2100 (one value only). Over-detected: several
// far-future values already present when the node first froze (a full read
// that was not needed; the safe direction).
func (r recovery) resaved(ahead map[string]string, now time.Time) bool {
	if !r.bounded {
		return false
	}
	for _, v := range ahead {
		t, err := time.Parse(time.RFC3339, v)
		if err == nil && t.After(now.Add(maxCursorSkew)) && t.Before(r.frozen) {
			return true
		}
	}
	return false
}

// startCursor picks the watermark a fetch filters on, from the checkpoint
// row, and says whether this fetch recovers from a frozen one.
//
// Frozen means more than maxCursorSkew ahead of now. Since #591 the consumer
// never stores a watermark later than its own clock, so only the code before
// it (which took any value, 2099 included) leaves one. Such a node has read
// nothing since: every poll asked for records after 2099.
//
// Without pictures, the code before #591 saved the checkpoint only when the
// watermark moved, so its LastProcessedAt is when the frozen watermark was
// stored, and every record stamped before the poll that stored it was read
// by that poll or an earlier one. The recovery read starts
// frozenRecoveryMargin before it: it re-sends that hour and sends what the
// frozen node missed, not the whole entity.
//
// With pictures, every poll saved, so LastProcessedAt is the last poll and
// says nothing about when the node froze; the same is true when it is not
// set. Then the entity is read from the start. That re-sends everything once,
// which is costly, but resuming from "now minus a bound" instead would drop
// whatever was modified between the freeze and that bound, with nothing but a
// log line to show for it.
//
// A node that froze and then read an even later far-future value saved again
// then, so LastProcessedAt is that second save; recovery.resaved catches what
// it can of that case and turns it into a full read.
func (c *bcConsumer) startCursor(cfg *BCConfig, cp *checkpoint.Checkpoint, logger *slog.Logger) (string, recovery) {
	if cp == nil {
		return "", recovery{}
	}
	stored := cp.LastProcessedMessageID
	now := c.clock()
	t, err := time.Parse(time.RFC3339, stored)
	if err != nil || !t.After(now.Add(maxCursorSkew)) {
		return stored, recovery{}
	}
	rec := recovery{active: true, stored: stored, frozen: t}
	if cp.LastProcessedAt.IsZero() || cfg.Pictures || stateHasPictures(cp.State) {
		logger.Warn("stored Business Central watermark is in the future and the checkpoint does not say when "+
			"it was stored; reading the entity from the start, so every record is sent again once",
			"watermark", stored, "last_processed_at", cp.LastProcessedAt, "pictures", cfg.Pictures)
		return "", rec
	}
	from := cp.LastProcessedAt
	if from.After(now) {
		from = now
	}
	cursor := from.Add(-frozenRecoveryMargin).UTC().Format(time.RFC3339)
	logger.Warn("stored Business Central watermark is in the future; re-reading from shortly before it was stored. "+
		"Records modified in that hour are sent again",
		"watermark", stored, "last_processed_at", cp.LastProcessedAt, "resume_from", cursor)
	rec.bounded = true
	return cursor, rec
}

// stateHasPictures reports whether a checkpoint's state was ever saved by a
// node with pictures on: the picture state is written as "pictures", an
// object even when no record had one. (A node without pictures that
// remembers future-dated records writes "pictures":null.)
func stateHasPictures(state json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if len(state) == 0 || json.Unmarshal(state, &fields) != nil {
		return false
	}
	v, ok := fields["pictures"]
	return ok && string(v) != "null"
}

// saveProgress stores the watermark and the node's state in one checkpoint
// row. An empty cursor keeps the stored watermark, unless replace is set:
// then cursor is stored as given, "" included (the next poll reads from the
// start).
func (c *bcConsumer) saveProgress(ctx context.Context, tenantID, connID, nodeID, cursor string, replace bool,
	count int64, state json.RawMessage, logger *slog.Logger) {
	if c.checkpoints == nil || nodeID == "" {
		return
	}
	if cursor == "" && !replace {
		if cp, err := c.checkpoints.Get(ctx, tenantID, connID, nodeID); err == nil && cp != nil {
			cursor = cp.LastProcessedMessageID
		}
	}
	err := c.checkpoints.Save(ctx, &checkpoint.Checkpoint{
		TenantID:               tenantID,
		ConnectionID:           connID,
		NodeID:                 nodeID,
		LastProcessedMessageID: cursor,
		LastProcessedAt:        c.clock().UTC(),
		MessageCount:           count,
		State:                  state,
	})
	if err != nil {
		// The records are already published; failing to remember how far we got
		// costs a repeat next poll, not data.
		logger.Error("save Business Central watermark", "error", err, "cursor", cursor)
	}
}

func (c *bcConsumer) get(ctx context.Context, tok *oauthcc.Client, fullURL string, pageSize int) ([]byte, error) {
	access, err := tok.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	if pageSize > 0 {
		// A request, not a command: BC may cap it, and says what it applied in
		// the Preference-Applied response header. Whatever it settles on, a
		// page smaller than the result set is what produces @odata.nextLink.
		req.Header.Set("Prefer", fmt.Sprintf("odata.maxpagesize=%d", pageSize))
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("business central %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (c *bcConsumer) publishRecords(ctx context.Context, connID, tenantID, entity string, records []json.RawMessage) error {
	payload, err := json.Marshal(records)
	if err != nil {
		return err
	}
	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = "application/json"
	env.Source = "business-central-consumer"
	env.Payload = payload
	env.PayloadSize = int64(len(payload))
	env.StepHistory = []string{"business-central-consumer"}
	env.Metadata = map[string]interface{}{"entity": entity, "record_count": len(records)}
	return c.publish(ctx, env)
}

// --- config helpers ---

func (cfg *BCConfig) effectiveEntity() string {
	if cfg.Entity != "" {
		return strings.Trim(cfg.Entity, "/")
	}
	return defaultEntity
}

func (cfg *BCConfig) effectiveScope() string {
	if cfg.Scope != "" {
		return cfg.Scope
	}
	return defaultScope
}

func (cfg *BCConfig) effectiveTokenURL() string {
	if cfg.TokenURL != "" {
		return cfg.TokenURL
	}
	return fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", cfg.AADTenantID)
}

// normalise strips surrounding whitespace from the fields that end up in a
// URL or a header.
//
// A GUID pasted out of the Azure portal arrives with a trailing space more
// often than not, and the tenant id goes straight into the API path: Entra
// accepted it, Business Central answered `400 RequestDataInvalid`, and
// nothing in that message points at a space you cannot see. The client secret
// is deliberately left alone — it is opaque, and trimming a credential is a
// guess about its contents rather than about its shape.
func (cfg *BCConfig) normalise() {
	for _, f := range []*string{
		&cfg.AADTenantID, &cfg.Environment, &cfg.CompanyID, &cfg.ClientID,
		&cfg.Entity, &cfg.Filter, &cfg.CursorField,
		&cfg.APIBaseURL, &cfg.TokenURL, &cfg.Scope,
	} {
		*f = strings.TrimSpace(*f)
	}
}

func (cfg *BCConfig) effectiveCursorField() string {
	if cfg.CursorField != "" {
		return cfg.CursorField
	}
	return defaultCursorField
}

// filterWithCursor composes the configured $filter with the incremental
// watermark. The configured filter is parenthesised so an `or` inside it
// cannot swallow the cursor clause: `a or b and cursor` binds as
// `a or (b and cursor)`, which would re-read everything matching `a`.
//
// `gt` rather than `ge` for a DateTime: the watermark is the newest value
// already published, so `ge` would redeliver it on every poll forever. The
// cost is a record written in the same instant as the watermark but after the
// fetch read it — BC timestamps to the millisecond, so that is a narrow
// window, and it is the standard trade for a timestamp watermark.
//
// A date (Edm.Date, `2026-10-29`, written without quotes in OData v4) has no
// time of day: `gt` would hide every record added later on the watermark day.
// So it is `ge`, and the consumer leaves out the records of that day it has
// already sent (cursorTracker.atWatermark, cfg.aheadSeen).
func (cfg *BCConfig) filterWithCursor(cursor string) string {
	if cursor == "" {
		return cfg.Filter
	}
	op := "gt"
	if isDateCursor(cursor) {
		op = "ge"
	}
	clause := fmt.Sprintf("%s %s %s", cfg.effectiveCursorField(), op, cursor)
	if cfg.Filter == "" {
		return clause
	}
	return fmt.Sprintf("(%s) and %s", cfg.Filter, clause)
}

// companyURL is the API v2.0 root scoped to the company.
func (cfg *BCConfig) companyURL() string {
	host := cfg.APIBaseURL
	if host == "" {
		env := cfg.Environment
		if env == "" {
			env = defaultEnvironment
		}
		host = fmt.Sprintf("%s/v2.0/%s/%s/api/v2.0", defaultAPIHost, cfg.AADTenantID, env)
	}
	return fmt.Sprintf("%s/companies(%s)", strings.TrimRight(host, "/"), cfg.CompanyID)
}

// entityURL builds the first-page API v2.0 URL, scoped to the company. A
// non-empty cursor narrows it to what changed since the last complete fetch.
func (cfg *BCConfig) entityURL(cursor string) string {
	u := fmt.Sprintf("%s/%s", cfg.companyURL(), cfg.effectiveEntity())
	if filter := cfg.filterWithCursor(cursor); filter != "" {
		u += "?$filter=" + url.QueryEscape(filter)
	}
	return u
}

// --- DB ---

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its Business Central consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (c *bcConsumer) getConfig(ctx context.Context, connectionID, tenantID string) (*BCConfig, error) {
	var nodesJSON json.RawMessage
	if err := c.db.QueryRow(
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON); err != nil {
		return nil, fmt.Errorf("connection not found: %w", err)
	}
	var nodes []node
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, fmt.Errorf("parse nodes: %w", err)
	}
	reader := crypto.NewSQLSecretReader(c.db)
	for _, n := range nodes {
		if n.Type != "consumer" {
			continue
		}
		resolved, rerr := crypto.ResolveSecretsInJSON(ctx, reader, tenantID, n.Config)
		if rerr != nil {
			return nil, fmt.Errorf("resolve secrets: %w", rerr)
		}
		var nc nodeConfig
		if err := json.Unmarshal(resolved, &nc); err != nil {
			continue
		}
		if nc.Type == "business_central" && nc.BusinessCentral != nil {
			nc.BusinessCentral.NodeID = n.ID
			nc.BusinessCentral.normalise()
			return nc.BusinessCentral, nil
		}
	}
	return nil, errors.New("no business_central consumer node found")
}
