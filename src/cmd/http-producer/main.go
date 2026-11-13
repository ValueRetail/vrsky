package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	iolib "io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthtoken"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/tlsconfig"
)

// httpProducer delivers pipeline envelopes to external HTTP endpoints. It is a
// connector built on the SDK: the runner owns NATS/JetStream, the durable
// subscription, the health server, signal handling and graceful shutdown; this
// type implements only Configure (DB + caches + the /events SSE API) and
// Deliver (send one envelope to every matching HTTP node).
type httpProducer struct {
	sdk.BaseProducer

	db     *sql.DB
	logger *slog.Logger

	// Cache for connection HTTP configs (multiple producer nodes per connection).
	configCache     map[string][]*HTTPConfig
	configCacheMu   sync.RWMutex
	configCacheTime map[string]time.Time
	configCacheTTL  time.Duration

	// SSE event subscribers + recent-event buffer for replay on connect.
	eventSubs      map[string][]chan HTTPEvent
	eventSubsMu    sync.RWMutex
	recentEvents   map[string][]HTTPEvent
	recentEventsMu sync.RWMutex

	// OAuth output (#97): resolve a grant's access token for nodes with
	// auth_type=oauth. resolveToken is defaulted in Configure to the oauthtoken
	// client; tests inject a stub.
	tokens       *oauthtoken.Client
	resolveToken func(ctx context.Context, tenantID, grantID string, force bool) (string, error)

	// Every outbound request goes through a safeurl client, which refuses
	// loopback, private, link-local and metadata addresses at dial time (#660).
	// allowPrivate is the operator's dev switch (ALLOW_PRIVATE_UPSTREAMS), read
	// in Configure and never set by a node. defaultClient serves nodes without
	// mTLS; it is built on first use, so a producer that was not configured
	// still gets a guarded client and never a plain http.Client.
	allowPrivate bool
	// requestTimeout bounds one request with no progress: a buffered request
	// end to end, a streamed one between body chunks and then for the drain
	// and the answer (see reqGuard). Zero means defaultRequestTimeout; tests set a short one.
	requestTimeout time.Duration
	// streamMinRate and streamMaxTime set the overall limit on one streamed
	// upload (see streamLimit). Zero means minStreamRate and maxStreamTime;
	// tests set small ones.
	streamMinRate int64
	streamMaxTime time.Duration
	defaultOnce   sync.Once
	defaultClient *http.Client

	// sent remembers the nodes that took a message whose delivery failed on
	// another node, so its redelivery skips them (#730, see sentSet). Created
	// on first use.
	sent     *sentSet
	sentOnce sync.Once
}

type HTTPEvent struct {
	Type       string `json:"type"` // "sent", "error", "info"
	Message    string `json:"message,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	Time       string `json:"time"`
	Payload    string `json:"payload,omitempty"`  // request body (truncated)
	Response   string `json:"response,omitempty"` // response body (truncated)
}

type HTTPConfig struct {
	NodeID         string            // the producer node's id in the connection
	URL            string            `json:"url"`
	Method         string            `json:"method"`
	Headers        map[string]string `json:"headers"`
	AuthType       string            `json:"auth_type"`      // "", "none", "oauth" or "bearer"
	OAuthGrantID   string            `json:"oauth_grant_id"` // grant id when auth_type=oauth
	PredecessorID  string
	PredIsConsumer bool

	// bearerToken is the token of an auth_type=bearer node, resolved from its
	// stored secret (http.token_secret_id, #850). It goes out as
	// Authorization: Bearer and nowhere else: never logged, emitted or
	// returned in an error.
	bearerToken string
	// authErr is why the node's bearer token cannot be used: it is written
	// into the node instead of stored as a secret, or auth_type=bearer has no
	// token. Nothing is sent to the node; its delivery fails and the message
	// is retried, then kept in the DLQ, like clientErr.
	authErr error

	// mTLS (#89): when the node carries a "tls" block with a resolved client
	// cert/key, client is a pre-built *http.Client presenting that cert to the
	// endpoint (built on the same guarded dialer, see newClient). nil means use
	// the default (non-mTLS) client.
	client *http.Client
	// clientErr is why the node's mTLS client could not be built (a cert or
	// key that does not parse). Nothing is sent to the node; its delivery
	// fails and the message is retried, then kept in the DLQ (#734).
	clientErr error
}

func main() {
	if err := sdk.RunProducer(context.Background(), "http-producer", &httpProducer{}); err != nil {
		slog.Error("http-producer exited", "error", err)
		os.Exit(1)
	}
}

// Configure wires the producer's dependencies. Called once by the runner before
// the subscription starts.
func (p *httpProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("http-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	p.configCache = make(map[string][]*HTTPConfig)
	p.configCacheTime = make(map[string]time.Time)
	if p.configCacheTTL == 0 {
		p.configCacheTTL = 5 * time.Minute
	}
	p.eventSubs = make(map[string][]chan HTTPEvent)
	p.recentEvents = make(map[string][]HTTPEvent)

	p.allowPrivate = safeurl.AllowPrivateFromEnv()
	if p.allowPrivate {
		p.logger.Warn("ALLOW_PRIVATE_UPSTREAMS is on: private and loopback addresses are reachable. Never set this in production.")
	}

	// OAuth output (#97): resolve access tokens for nodes with auth_type=oauth.
	p.tokens = oauthtoken.New(os.Getenv("MGMT_API_URL"), os.Getenv("OAUTH_TOKEN_SERVICE_SECRET"))
	if p.resolveToken == nil {
		p.resolveToken = func(ctx context.Context, tenantID, grantID string, force bool) (string, error) {
			if !p.tokens.Configured() {
				return "", errors.New("node uses OAuth but token resolution is not configured (set MGMT_API_URL + OAUTH_TOKEN_SERVICE_SECRET)")
			}
			if force {
				return p.tokens.ForceToken(ctx, tenantID, grantID)
			}
			return p.tokens.Token(ctx, tenantID, grantID)
		}
	}

	// Serve the live SSE event stream on the SDK auxiliary HTTP port
	// (WORKER_HTTP_PORT, 9400 in compose) via the custom-handler hook — the UI
	// connects to /events/{connectionID}.
	p.RegisterHTTPHandler("/events/", p.eventsHandler())

	p.logger.Info("http-producer configured")
	return nil
}

// defaultRequestTimeout is how long a request to a target may go without
// progress. A target that accepts the connection and never answers fails the
// delivery after this, as an ordinary retriable error (NAK, then the DLQ), so
// it cannot hold the dispatch goroutine (#763). A buffered request is held to
// it end to end; a streamed upload only while it stalls (#774).
const defaultRequestTimeout = 30 * time.Second

// A streamed upload that keeps moving is not held to defaultRequestTimeout, so
// it needs an overall limit of its own: the delivery context has no deadline
// and dispatch is serial, so a target that reads a few KB/s would otherwise
// hold the producer, and every tenant behind it, for as long as it likes. One
// attempt may take the request timeout plus the payload size at minStreamRate,
// and never more than maxStreamTime; when the size is not known it gets
// maxStreamTime. An attempt that runs over fails like a timeout (retriable,
// then the DLQ). The cap is reached at about 218 MiB; a larger payload needs a
// faster target (1 GiB about 1.2 MiB/s).
const (
	minStreamRate = 256 << 10 // bytes per second
	maxStreamTime = 15 * time.Minute
)

func (p *httpProducer) timeout() time.Duration {
	if p.requestTimeout > 0 {
		return p.requestTimeout
	}
	return defaultRequestTimeout
}

// streamLimit is the overall limit on one streamed upload of size bytes.
func (p *httpProducer) streamLimit(size int64) time.Duration {
	rate, maxTime := p.streamMinRate, p.streamMaxTime
	if rate <= 0 {
		rate = minStreamRate
	}
	if maxTime <= 0 {
		maxTime = maxStreamTime
	}
	return streamLimit(p.timeout(), size, rate, maxTime)
}

func streamLimit(timeout time.Duration, size, rate int64, maxTime time.Duration) time.Duration {
	if size <= 0 || size/rate >= int64(maxTime/time.Second) {
		return maxTime
	}
	// size < rate*maxTime in seconds here, so this cannot overflow.
	limit := timeout + time.Duration(size*int64(time.Second)/rate)
	return min(limit, maxTime)
}

// reqGuard enforces the request timeout. The http.Client has no Timeout of its
// own, because that would count the time spent writing the body and cut off a
// multi-GB upload that is making progress (#774). The guard cancels the
// request's context when the timer fires. A buffered request never resets it,
// so it is bounded end to end as before. A streamed one resets it every time
// the transport takes more body from the source (see progressReader), so a
// stalled upload fails after d without progress; a streamed request also has
// an overall limit (streamLimit) that progress does not reset.
type reqGuard struct {
	d        time.Duration
	limit    time.Duration // overall limit, 0 for none
	streamed bool
	cancel   context.CancelFunc

	mu        sync.Mutex
	timer     *time.Timer
	limiter   *time.Timer
	timedOut  bool // the progress timer fired
	overLimit bool // the overall limit fired
	done      bool
}

// newReqGuard returns a context that is cancelled when the guard fires or the
// parent is. limit > 0 adds an overall limit that reset does not move. Call
// stop when the request, response body included, is finished.
func newReqGuard(parent context.Context, d, limit time.Duration, streamed bool) (context.Context, *reqGuard) {
	ctx, cancel := context.WithCancel(parent)
	g := &reqGuard{d: d, limit: limit, streamed: streamed, cancel: cancel}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.timer = time.AfterFunc(d, func() { g.fire(&g.timedOut) })
	if limit > 0 {
		g.limiter = time.AfterFunc(limit, func() { g.fire(&g.overLimit) })
	}
	return ctx, g
}

func (g *reqGuard) fire(flag *bool) {
	g.mu.Lock()
	if !g.done && !g.timedOut && !g.overLimit {
		*flag = true
	}
	g.mu.Unlock()
	g.cancel()
}

// reset restarts the progress timer at d.
func (g *reqGuard) reset(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.done && !g.timedOut && !g.overLimit {
		g.timer.Reset(d)
	}
}

func (g *reqGuard) stop() {
	g.mu.Lock()
	g.done = true
	g.timer.Stop()
	if g.limiter != nil {
		g.limiter.Stop()
	}
	g.mu.Unlock()
	g.cancel()
}

// wrap marks an error from a request that the guard cut off as a deadline, so
// it stays retriable. err is the already redacted transport error.
func (g *reqGuard) wrap(err error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case err == nil:
		return nil
	case g.overLimit:
		return fmt.Errorf("%v: upload not finished within its %v limit: %w", err, g.limit, context.DeadlineExceeded)
	case g.timedOut && g.streamed:
		return fmt.Errorf("%v: timed out after %v without progress: %w", err, g.d, context.DeadlineExceeded)
	case g.timedOut:
		return fmt.Errorf("%v: timed out after %v: %w", err, g.d, context.DeadlineExceeded)
	}
	return err
}

// progressReader resets the guard each time the transport takes body bytes
// from the source. That is progress as the producer can see it: bytes leaving
// the source, not bytes the target has read. Up to a socket buffer's worth
// (a few MB) can still be on its way when the source is done, so at EOF the
// guard is set to twice the timeout: one for that tail to drain, one for the
// answer.
//
// The transport waits for its body write to return before it reports a
// cancelled request, so a source that blocks in Read (a stalled object-store
// stream) would hold the delivery open past the timeout. Read therefore runs
// the source read in its own goroutine, into a private buffer, and gives up
// when the request context ends. The abandoned read finishes when the source
// does, which closing the stream or ending the delivery context makes happen.
type progressReader struct {
	ctx context.Context
	r   iolib.Reader
	g   *reqGuard
	buf []byte
	eof bool
}

type readResult struct {
	n   int
	err error
}

func (pr *progressReader) Read(b []byte) (int, error) {
	if pr.eof {
		return 0, iolib.EOF
	}
	pr.g.reset(pr.g.d)
	if len(b) == 0 {
		return 0, nil
	}
	if cap(pr.buf) < len(b) {
		pr.buf = make([]byte, len(b))
	}
	buf := pr.buf[:len(b)]
	ch := make(chan readResult, 1)
	go func() {
		n, err := pr.r.Read(buf)
		ch <- readResult{n, err}
	}()
	select {
	case res := <-ch:
		copy(b, buf[:res.n])
		switch {
		case res.err == iolib.EOF:
			pr.eof = true
			pr.g.reset(2 * pr.g.d)
		case res.err != nil || res.n > 0:
			pr.g.reset(pr.g.d)
		}
		return res.n, res.err
	case <-pr.ctx.Done():
		// buf stays with the goroutine; the next Read, if any, allocates anew.
		pr.buf = nil
		return 0, pr.ctx.Err()
	}
}

// newClient builds the guarded client every delivery uses. A tenant may point a
// node at a public API over http or https, so the scheme is not restricted; the
// dial refuses blocked addresses either way, on every hop. Redirects stay on the
// same host, so a header the node set is never forwarded elsewhere. tlsCfg is
// the node's mTLS configuration, or nil. otelhttp wraps the transport so the
// outbound call is a child span of the pipeline trace and carries traceparent
// (a no-op when tracing is off).
func (p *httpProducer) newClient(tlsCfg *tls.Config) *http.Client {
	// No Client.Timeout: each request is bounded by its reqGuard instead.
	c := safeurl.NewClientWith(0, safeurl.Options{
		AllowPrivate:      p.allowPrivate,
		SameHostRedirects: true,
		TLSConfig:         tlsCfg,
	})
	c.Transport = otelhttp.NewTransport(c.Transport)
	return c
}

// clientFor returns the node's mTLS client, or the shared guarded client.
func (p *httpProducer) clientFor(httpCfg *HTTPConfig) *http.Client {
	if httpCfg.client != nil {
		return httpCfg.client
	}
	p.defaultOnce.Do(func() { p.defaultClient = p.newClient(nil) })
	return p.defaultClient
}

// Deliver sends one envelope to every matching HTTP-producer node configured
// for its connection. A transient failure (network error, 5xx, 429), a 401 or
// 403 (a wrong or expired credential), and a URL no request can be built from
// return sdk.Retriable (the SDK NAKs → retries → DLQs), so the message can be
// replayed once the node is fixed (#734). Any other 4xx is logged and acked:
// the target refused the message itself. A missing producer config for the
// connection is not an error — this binary just isn't the producer for it.
//
// With several nodes, the error names every node that failed, and a node that
// took the message (2xx, or a 4xx other than 401/403) is not sent it again
// when the message comes back for a failed sibling (#730, see sentSet).
func (p *httpProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	connectionID := env.IntegrationID
	if connectionID == "" {
		// No routing key — nothing this producer can do with it.
		return nil
	}

	httpConfigs, err := p.getHTTPConfigs(ctx, connectionID, env.TenantID)
	if err != nil {
		if sdk.IsConfigUnavailable(err) {
			return err // not acked: retried, then the DLQ
		}
		// Not an HTTP producer for this pipeline — ack and move on.
		p.logger.Debug("No HTTP producer config for connection", "connection_id", connectionID, "error", err)
		return nil
	}

	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	sent := p.sentSet()
	at := time.Now()
	var failed []error
	var done []sentKey  // took the message in this delivery or an earlier one
	var taken []sentKey // took it in this delivery
	for _, httpCfg := range p.eligibleConfigs(httpConfigs, lastProcessedBy) {
		key := sentKey{
			connID: connectionID, envelopeID: env.ID, nodeID: httpCfg.NodeID,
			target: targetHash(httpCfg.Method, httpCfg.URL),
		}
		if sent.has(key, at) {
			p.logger.Info("Node already took this message; skipping it on redelivery",
				"connection_id", connectionID, "node_id", httpCfg.NodeID, "envelope_id", env.ID)
			done = append(done, key)
			continue
		}
		if err := p.sendHTTPRequest(ctx, connectionID, httpCfg, env, nil); err != nil {
			failed = append(failed, fmt.Errorf("node %s: %w", httpCfg.NodeID, err))
			continue
		}
		done = append(done, key)
		taken = append(taken, key)
	}
	if len(failed) > 0 {
		sent.add(at, taken...)
		return sdk.Retriable(errors.Join(failed...))
	}
	sent.forget(done...)
	return nil
}

// sentSet returns the producer's skip set, creating it on first use.
func (p *httpProducer) sentSet() *sentSet {
	p.sentOnce.Do(func() {
		if p.sent == nil {
			p.sent = newSentSet(sentTTL, sentMax)
		}
	})
	return p.sent
}

// DeliverStream sends a large, offloaded payload straight from the pipeline's
// object stream as the request body, so a multi-GB POST never has to be held in
// worker memory (ADR 0001).
//
// This works because http-producer has no in-process retry loop for ordinary
// failures: a retriable error is returned to the SDK, JetStream redelivers, and
// the redelivery re-opens the stream from object storage. Message-level retry is
// stream-safe in a way an in-process one would not be.
//
// Two cases still need a second pass over a body that can only be read once, and
// are declined so the SDK falls back to buffered delivery:
//   - fan-out to more than one endpoint;
//   - auth_type=oauth, whose 401 handler re-sends the request with a refreshed
//     token. Converting that into a redelivery is not equivalent — the retry
//     would resolve the token from cache again and could loop on the same
//     expired credential until the message is DLQ'd.
func (p *httpProducer) DeliverStream(ctx context.Context, env *envelope.Envelope, body iolib.Reader) error {
	connectionID := env.IntegrationID
	if connectionID == "" {
		return nil
	}
	httpConfigs, err := p.getHTTPConfigs(ctx, connectionID, env.TenantID)
	if err != nil {
		if sdk.IsConfigUnavailable(err) {
			return err
		}
		p.logger.Debug("No HTTP producer config for connection", "connection_id", connectionID, "error", err)
		return nil
	}

	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	eligible := p.eligibleConfigs(httpConfigs, lastProcessedBy)
	if len(eligible) == 0 {
		return nil
	}
	if len(eligible) > 1 || eligible[0].AuthType == "oauth" {
		return sdk.ErrStreamUnsupported
	}
	if err := p.sendHTTPRequest(ctx, connectionID, eligible[0], env, body); err != nil {
		return sdk.Retriable(err)
	}
	return nil
}

// eligibleConfigs drops the endpoints whose predecessor predicate doesn't match
// this envelope. Shared by the buffered and streaming paths.
func (p *httpProducer) eligibleConfigs(configs []*HTTPConfig, lastProcessedBy string) []*HTTPConfig {
	out := make([]*HTTPConfig, 0, len(configs))
	for _, httpCfg := range configs {
		if httpCfg.PredIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !httpCfg.PredIsConsumer && httpCfg.PredecessorID != "" && lastProcessedBy != httpCfg.PredecessorID {
			continue
		}
		out = append(out, httpCfg)
	}
	return out
}

// sendHTTPRequest returns nil on a successful 2xx response or a 4xx other than
// 401/403, and a non-nil error on any *retriable* failure (network error, 5xx,
// 429, 401/403, a URL no request can be built from). Those 4xx return nil so
// the SDK doesn't retry them: the target refused the message itself and will
// on every redelivery. A 401/403 is about the node's credential and a bad URL
// about its config; both are kept in the DLQ for a replay after the fix (#734).
// body, when non-nil, is streamed as the request body instead of env.Payload —
// the payload is then never materialised in memory, so the activity feed shows
// its size but not a content preview.
func (p *httpProducer) sendHTTPRequest(ctx context.Context, connectionID string, httpCfg *HTTPConfig, env *envelope.Envelope, body iolib.Reader) error {
	size := int64(len(env.Payload))
	payloadPreview := string(env.Payload)
	if body != nil {
		size = env.PayloadSize
		payloadPreview = ""
	}
	if len(payloadPreview) > 2000 {
		payloadPreview = payloadPreview[:2000] + "..."
	}

	// httpCfg.URL may carry user:pass@; everything the producer logs, emits or
	// returns uses shownURL. Requests use httpCfg.URL.
	shownURL := safeurl.RedactURL(httpCfg.URL)

	p.emitEvent(connectionID, HTTPEvent{
		Type:    "info",
		Message: fmt.Sprintf("Sending %d bytes to %s", size, shownURL),
		Payload: payloadPreview,
		Time:    now(),
	})

	method := httpCfg.Method
	if method == "" {
		method = "POST"
	}

	// A URL no request can be built from is node config: nothing is sent, and
	// the message is retried and then kept in the DLQ for a replay once the
	// node is fixed (#734), like a refused address below.
	if _, err := http.NewRequestWithContext(ctx, method, httpCfg.URL, nil); err != nil {
		msg := "Failed to create request: " + requestErrText(err)
		p.logger.Error("HTTP request cannot be built", "error", requestErrText(err), "connection_id", connectionID, "node_id", httpCfg.NodeID)
		p.emitEvent(connectionID, HTTPEvent{Type: "error", Message: msg, Time: now()})
		return errors.New("cannot build request: " + requestErrText(err))
	}

	if httpCfg.clientErr != nil {
		p.logger.Error("HTTP destination's mTLS client cannot be built", "error", httpCfg.clientErr, "connection_id", connectionID, "node_id", httpCfg.NodeID)
		p.emitEvent(connectionID, HTTPEvent{Type: "error", Message: httpCfg.clientErr.Error(), Time: now()})
		return fmt.Errorf("%w; fix the node's certificate, then retry the message from the DLQ", httpCfg.clientErr)
	}

	// authErr never holds the token: it names the setting, not its value.
	if httpCfg.authErr != nil {
		p.logger.Error("HTTP destination's bearer token cannot be used", "error", httpCfg.authErr, "connection_id", connectionID, "node_id", httpCfg.NodeID)
		p.emitEvent(connectionID, HTTPEvent{Type: "error", Message: httpCfg.authErr.Error(), Time: now()})
		return fmt.Errorf("%w; fix the node, then retry the message from the DLQ", httpCfg.authErr)
	}

	// The URL is tenant config and the node's credential goes with the request,
	// so a literal private address or a non-http(s) scheme is refused here, with
	// a readable reason. A hostname that resolves to a private address is
	// refused at dial time (newClient). Both errors are returned as ordinary
	// failures: the message is retried on the usual backoff and then lands in the
	// DLQ, so the payload is kept for the operator rather than dropped, and no
	// request leaves the pod (#660).
	if err := safeurl.CheckRequestURL(httpCfg.URL, p.allowPrivate); err != nil {
		p.logger.Error("HTTP destination refused", "error", err, "connection_id", connectionID, "node_id", httpCfg.NodeID)
		p.emitEvent(connectionID, HTTPEvent{Type: "error", Message: "Destination refused: " + err.Error(), Time: now()})
		return fmt.Errorf("destination refused: %w", err)
	}

	client := p.clientFor(httpCfg)
	// The guards stay armed until the response has been read, so a target that
	// sends headers and then stalls is cut off too.
	var stops []func()
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()
	// sentTokens are the bearer tokens the requests carried, masked in the
	// response preview (see responsePreview).
	var sentTokens []string
	// buildAndSend creates a fresh request (re-readable body) per attempt and,
	// for auth_type=oauth or bearer, attaches a Bearer token. force=true
	// refreshes an OAuth token.
	buildAndSend := func(force bool) (*http.Response, error) {
		// The token comes first so its fetch does not use up the request's
		// timeout; the guard starts once only the request itself is left.
		bearer, useBearer := "", false
		if httpCfg.AuthType == "oauth" && httpCfg.OAuthGrantID == "" {
			// OAuth selected but no grant (e.g. the grant was revoked, which
			// clears the selection on the node). Rather than blocking the send
			// — or calling /oauth/grants//token with an empty id and getting an
			// opaque 500 — fall back to sending unauthenticated and warn. A
			// proper "reconnect required" hint in the UI is tracked in #98.
			p.logger.Warn("OAuth output has no grant selected; sending without authentication (reconnect the grant to authenticate)",
				"connection_id", connectionID, "url", shownURL)
		} else if httpCfg.AuthType == "oauth" {
			tok, terr := p.resolveToken(ctx, env.TenantID, httpCfg.OAuthGrantID, force)
			if terr != nil {
				return nil, fmt.Errorf("resolve oauth token: %w", terr)
			}
			bearer, useBearer = tok, true
		} else if httpCfg.AuthType == "bearer" {
			// getHTTPConfigs set the token or authErr, and authErr stopped
			// the delivery above.
			bearer, useBearer = httpCfg.bearerToken, true
		}
		if useBearer {
			sentTokens = append(sentTokens, bearer)
		}

		var limit time.Duration
		if body != nil {
			limit = p.streamLimit(env.PayloadSize)
		}
		reqCtx, guard := newReqGuard(ctx, p.timeout(), limit, body != nil)
		stops = append(stops, guard.stop)
		var reqBody iolib.Reader = bytes.NewReader(env.Payload)
		if body != nil {
			reqBody = &progressReader{ctx: reqCtx, r: body, g: guard}
		}
		req, _ := http.NewRequestWithContext(reqCtx, method, httpCfg.URL, reqBody)
		if body != nil {
			// Declare the length so the request goes out with Content-Length
			// rather than chunked encoding, which some endpoints reject. The
			// size is known from the offloaded object.
			req.ContentLength = env.PayloadSize
		}
		if env.ContentType != "" {
			req.Header.Set("Content-Type", env.ContentType)
		} else {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Message-ID", env.ID)
		for k, v := range httpCfg.Headers {
			req.Header.Set(k, v)
		}
		if useBearer {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, guard.wrap(redactTransportErr(err, shownURL))
		}
		return resp, nil
	}

	resp, err := buildAndSend(false)
	if err != nil {
		p.logger.Error("HTTP request failed", "error", err, "connection_id", connectionID, "node_id", httpCfg.NodeID)
		p.emitEvent(connectionID, HTTPEvent{
			Type: "error", Message: err.Error(),
			Time: now(),
		})
		return fmt.Errorf("transport: %w", err) // retriable (transport or token-service hiccup)
	}

	// OAuth: an access token may have expired between resolution and the call —
	// refresh once and retry (mirrors api-consumer's callEndpoint).
	if resp.StatusCode == http.StatusUnauthorized && httpCfg.AuthType == "oauth" && httpCfg.OAuthGrantID != "" {
		_ = resp.Body.Close()
		if body != nil {
			// Unreachable today — DeliverStream declines oauth precisely because
			// of this re-send. Guard it anyway: replaying a consumed stream would
			// silently send an empty body.
			return fmt.Errorf("401 on a streamed request: cannot replay the body to refresh the token")
		}
		p.emitEvent(connectionID, HTTPEvent{Type: "info", Message: "401 — refreshing OAuth token and retrying", Time: now()})
		resp, err = buildAndSend(true)
		if err != nil {
			p.emitEvent(connectionID, HTTPEvent{Type: "error", Message: err.Error(), Time: now()})
			return fmt.Errorf("after token refresh: %w", err) // retriable
		}
	}
	defer resp.Body.Close()

	respPreview := responsePreview(resp.Body, 4096, sentTokens)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		p.logger.Info("HTTP request sent", "connection_id", connectionID, "status", resp.StatusCode, "size", size)
		p.emitEvent(connectionID, HTTPEvent{
			Type: "sent", Message: fmt.Sprintf("%s %s → %d", method, shownURL, resp.StatusCode),
			StatusCode: resp.StatusCode, Payload: payloadPreview, Response: respPreview,
			Time: now(),
		})
		return nil
	}

	p.logger.Error("HTTP request returned error", "status", resp.StatusCode, "connection_id", connectionID, "node_id", httpCfg.NodeID)
	p.emitEvent(connectionID, HTTPEvent{
		Type: "error", Message: fmt.Sprintf("%s %s → %d", method, shownURL, resp.StatusCode),
		StatusCode: resp.StatusCode, Response: respPreview,
		Time: now(),
	})
	switch {
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("upstream %d", resp.StatusCode) // retriable
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		// The node's credential is wrong or expired (with OAuth, still after
		// the refresh above). Retried, then the DLQ: replay it once the
		// credential is fixed (#734).
		return fmt.Errorf("upstream %d: check the node's credential, then retry the message from the DLQ", resp.StatusCode)
	}
	return nil // any other 4xx: the target refused the message itself, don't retry
}

// responsePreview reads up to n bytes of a response for the activity feed,
// with every bearer token in tokens masked. Only a byte-for-byte copy is
// masked: a target that echoes the token escaped or encoded (JSON "\/",
// URL or base64 encoding) still shows it. A target that echoes the request
// headers back (httpbin does) would otherwise put the node's token in the
// feed, which every user of the tenant can read, while the secret itself is
// write-only (#850). It reads a token's length past n, so a token that
// straddles the cut is masked too; the mask is as long as the token, so
// nothing moves into the first n bytes.
func responsePreview(r iolib.Reader, n int, tokens []string) string {
	// Longest first, so a token that contains another is masked whole.
	tokens = slices.Clone(tokens)
	slices.SortFunc(tokens, func(a, b string) int { return len(b) - len(a) })
	extra := 0
	if len(tokens) > 0 {
		extra = len(tokens[0])
	}
	b, _ := iolib.ReadAll(iolib.LimitReader(r, int64(n+extra)))
	for _, t := range tokens {
		if t != "" {
			b = bytes.ReplaceAll(b, []byte(t), bytes.Repeat([]byte("*"), len(t)))
		}
	}
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}

// requestErrText is the text of an http.NewRequest error that is safe to show.
// A bad URL comes back as a *url.Error that quotes the whole input, user:pass@
// included, so only a fixed message is shown for it.
func requestErrText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return "URL is not valid"
	}
	return err.Error()
}

// redactTransportErr rewrites an http.Client error so it no longer quotes the
// request URL: the standard library hides the password but keeps the user name,
// the path and the query. The cause is still wrapped, so errors.Is sees it.
func redactTransportErr(err error, shownURL string) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s %q: %w", ue.Op, shownURL, ue.Err)
	}
	return err
}

// getHTTPConfigs returns all HTTP-producer node configs for a connection (with
// a short cache). // lint:tenant-ok — connection lookup by PK; tenant scoping is enforced upstream when the pipeline is deployed.
func (p *httpProducer) getHTTPConfigs(ctx context.Context, connectionID, tenantID string) ([]*HTTPConfig, error) {
	p.configCacheMu.RLock()
	if cfg, ok := p.configCache[connectionID]; ok {
		if time.Since(p.configCacheTime[connectionID]) < p.configCacheTTL {
			p.configCacheMu.RUnlock()
			return cfg, nil
		}
	}
	p.configCacheMu.RUnlock()

	var nodesJSON, edgesJSON []byte
	err := p.db.QueryRowContext(ctx, `SELECT nodes, edges FROM connections WHERE id = $1`, connectionID).Scan(&nodesJSON, &edgesJSON)
	if err != nil {
		return nil, sdk.ConfigReadError(err)
	}

	var nodes []struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, fmt.Errorf("failed to parse nodes: %w", err)
	}

	var edges []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if edgesJSON != nil {
		_ = json.Unmarshal(edgesJSON, &edges)
	}

	reader := crypto.NewSQLSecretReader(p.db)
	var configs []*HTTPConfig
	for _, node := range nodes {
		if node.Type != "producer" {
			continue
		}
		// Decrypt any *_secret_id references (e.g. an encrypted auth-header
		// value) so the typed config below sees plaintext. Only for our own
		// nodes: an unreadable secret on another connector's node is not a
		// reason to hold this message.
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(node.Config, &kind); err != nil || kind.Type != "http" {
			continue
		}
		// Before resolving: once resolved, http.token holds the stored
		// secret's value and a token written into the node looks the same.
		inlineToken := hasInlineToken(node.Config)
		resolved, rerr := crypto.ResolveNodeSecrets(ctx, reader, tenantID, node.Config)
		if rerr != nil {
			return nil, sdk.ConfigUnavailable(fmt.Errorf("resolve secrets for node %s: %w", node.ID, rerr))
		}
		node.Config = resolved

		var nodeConfig struct {
			Type string `json:"type"`
			HTTP struct {
				URL          string            `json:"url"`
				Method       string            `json:"method"`
				Headers      map[string]string `json:"headers"`
				AuthType     string            `json:"auth_type"`
				OAuthGrantID string            `json:"oauth_grant_id"`
				// Token is http.token_secret_id resolved (#850); one written
				// into the node is refused (inlineToken).
				Token string `json:"token"`
			} `json:"http"`
			// mTLS material (#89). In the stored config these are *_secret_id
			// refs; crypto.ResolveNodeSecrets has already replaced them with
			// plaintext PEM by the time we parse here.
			TLS tlsconfig.NodeConfig `json:"tls"`
		}
		if err := json.Unmarshal(node.Config, &nodeConfig); err != nil {
			continue
		}
		// A node with no URL is still ours: sendHTTPRequest refuses the empty
		// URL, so the message is retried and kept in the DLQ instead of acked
		// as foreign (#734).
		if nodeConfig.Type != "http" {
			continue
		}

		var predID string
		var predIsConsumer bool
		for _, e := range edges {
			if e.Target == node.ID {
				predID = e.Source
				for _, n := range nodes {
					if n.ID == predID && n.Type == "consumer" {
						predIsConsumer = true
						break
					}
				}
				break
			}
		}

		cfg := &HTTPConfig{
			NodeID:         node.ID,
			URL:            nodeConfig.HTTP.URL,
			Method:         nodeConfig.HTTP.Method,
			Headers:        nodeConfig.HTTP.Headers,
			AuthType:       nodeConfig.HTTP.AuthType,
			OAuthGrantID:   nodeConfig.HTTP.OAuthGrantID,
			PredecessorID:  predID,
			PredIsConsumer: predIsConsumer,
		}
		switch {
		case inlineToken:
			cfg.authErr = errors.New("the bearer token is written into the node (http.token); it must be stored as a secret (http.token_secret_id)")
		case cfg.AuthType == "bearer" && strings.TrimSpace(nodeConfig.HTTP.Token) == "":
			cfg.authErr = errors.New("auth_type is bearer but the node has no token; set http.token_secret_id")
		case cfg.AuthType == "bearer":
			// A token pasted with a trailing newline would make every
			// request fail as an invalid header.
			cfg.bearerToken = strings.TrimSpace(nodeConfig.HTTP.Token)
		}

		// mTLS: when the node presents a client cert, pre-build a dedicated
		// http.Client that presents it (and trusts the configured server CA, if
		// any). Built once here and reused for every send to this node.
		if nodeConfig.TLS.Enabled() {
			tlsCfg, err := tlsconfig.ClientConfig(
				[]byte(nodeConfig.TLS.Cert),
				[]byte(nodeConfig.TLS.Key),
				[]byte(nodeConfig.TLS.ClientCA),
			)
			if err != nil {
				// Node config, not "not ours": an error here used to make
				// Deliver ack the message. The node fails on its own, and its
				// siblings still get the message (#734).
				cfg.clientErr = fmt.Errorf("build mTLS client: %w", err)
			} else {
				// Same guarded dialer as every other node; only the TLS
				// config differs.
				cfg.client = p.newClient(tlsCfg)
			}
		}

		configs = append(configs, cfg)
	}

	if len(configs) == 0 {
		return nil, fmt.Errorf("no HTTP producer config found")
	}

	p.configCacheMu.Lock()
	p.configCache[connectionID] = configs
	p.configCacheTime[connectionID] = time.Now()
	p.configCacheMu.Unlock()

	return configs, nil
}

// hasInlineToken reports a bearer token written into the node's http block
// (http.token) instead of stored as a secret. Call it on the stored config,
// before secrets are resolved. The editor mints the token into the secrets
// vault on deploy (http.token_secret_id); a config written some other way
// with the value inline is refused rather than used, as the Bifrost catalogue
// producer and the NATS source do (#850).
//
// It decodes into a struct, like the read that uses the token: encoding/json
// matches keys without regard to case, so "Token" is read as the token too,
// and a check on the exact key "token" would let it through.
func hasInlineToken(nodeConfig json.RawMessage) bool {
	var c struct {
		HTTP struct {
			Token string `json:"token"`
		} `json:"http"`
	}
	if json.Unmarshal(nodeConfig, &c) != nil {
		return false
	}
	return strings.TrimSpace(c.HTTP.Token) != ""
}

// --- Event broadcasting (SSE) ---

func (p *httpProducer) subscribeEvents(connectionID string) (chan HTTPEvent, func()) {
	ch := make(chan HTTPEvent, 50)
	p.eventSubsMu.Lock()
	p.eventSubs[connectionID] = append(p.eventSubs[connectionID], ch)
	p.eventSubsMu.Unlock()

	return ch, func() {
		p.eventSubsMu.Lock()
		defer p.eventSubsMu.Unlock()
		subs := p.eventSubs[connectionID]
		for i, sub := range subs {
			if sub == ch {
				p.eventSubs[connectionID] = append(subs[:i], subs[i+1:]...)
				close(ch)
				break
			}
		}
	}
}

func (p *httpProducer) emitEvent(connectionID string, event HTTPEvent) {
	p.recentEventsMu.Lock()
	p.recentEvents[connectionID] = append(p.recentEvents[connectionID], event)
	if len(p.recentEvents[connectionID]) > 50 {
		p.recentEvents[connectionID] = p.recentEvents[connectionID][len(p.recentEvents[connectionID])-50:]
	}
	p.recentEventsMu.Unlock()

	p.eventSubsMu.RLock()
	defer p.eventSubsMu.RUnlock()
	for _, ch := range p.eventSubs[connectionID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func (p *httpProducer) getRecentEvents(connectionID string) []HTTPEvent {
	p.recentEventsMu.RLock()
	defer p.recentEventsMu.RUnlock()
	events := p.recentEvents[connectionID]
	cp := make([]HTTPEvent, len(events))
	copy(cp, events)
	return cp
}

// eventsHandler returns the /events/{connectionID} SSE handler, served on the
// SDK auxiliary HTTP port.
func (p *httpProducer) eventsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		connectionID := strings.TrimPrefix(r.URL.Path, "/events/")
		connectionID = strings.TrimSuffix(connectionID, "/")
		if connectionID == "" {
			http.Error(w, "Missing connection ID", http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch, unsub := p.subscribeEvents(connectionID)
		defer unsub()

		fmt.Fprintf(w, "data: {\"type\":\"connected\",\"message\":\"Listening for HTTP producer events\"}\n\n")
		flusher.Flush()

		// Replay recent events so the client catches up.
		for _, event := range p.getRecentEvents(connectionID) {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case event, ok := <-ch:
				if !ok {
					return
				}
				data, _ := json.Marshal(event)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

// --- Helpers ---

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// ServesConnection reports whether this connection has a matching HTTP destination —
// mirroring Deliver's own "no config -> not ours" semantics — so the SDK can
// ack foreign connections before rehydrating large payloads (sdk.ConnectionScoped).
func (p *httpProducer) ServesConnection(ctx context.Context, tenantID, connectionID string) bool {
	if connectionID == "" {
		return false
	}
	configs, err := p.getHTTPConfigs(ctx, connectionID, tenantID)
	if err != nil {
		// A failed read answers true so Deliver runs and returns it (#704);
		// a missing connection or node is not ours.
		return sdk.IsConfigUnavailable(err)
	}
	return len(configs) > 0
}
