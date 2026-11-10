package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	// dialTimeout bounds one TCP connect, apiTimeout the JetStream API calls
	// that bind the durable.
	dialTimeout = 10 * time.Second
	apiTimeout  = 10 * time.Second
	// pingInterval and maxPingsOut notice a dead TCP path that sends no
	// reset within about a minute, instead of nats.go's default four.
	pingInterval = 20 * time.Second
	maxPingsOut  = 2
	// sourceAckWait is the AckWait of a durable VRSky creates: how long the
	// external server waits for our ack before it redelivers.
	sourceAckWait = 30 * time.Second
)

// session is one live connection to the external server, bound to the durable.
type session struct {
	nc     *nats.Conn
	stream jetstream.Stream
	cons   jetstream.Consumer
	// done is cancelled when the connection closes (NoReconnect: any drop
	// closes it), so a fetch in flight ends at once instead of at its expiry.
	// It does not follow the loop's context: on a stop, the pull runs out
	// while what it delivers is handed back.
	done   context.Context
	cancel context.CancelFunc
	async  *asyncErrs
	// warnings are things about an existing durable worth an operator's eye.
	warnings []string
}

// openOpts are the settings of one connect.
type openOpts struct {
	allowPrivate bool
	// create makes a missing durable; without it the durable must exist
	// (the connection test, and a reconnect after VRSky bound it once).
	create bool
	// apiWait bounds the API calls: a request the user may not make is not
	// answered, so it ends at this timeout. dialWait bounds each TCP connect.
	apiWait, dialWait time.Duration
}

func (s *session) close() {
	if s == nil {
		return
	}
	s.nc.Close()
	s.cancel()
}

// asyncErrs keeps the permission violation the server reports asynchronously.
// A JetStream API request we may not publish just times out; this is how the
// timeout is told apart from a slow server.
type asyncErrs struct {
	mu        sync.Mutex
	violation error
}

func (a *asyncErrs) handler(_ *nats.Conn, _ *nats.Subscription, err error) {
	if errors.Is(err, nats.ErrPermissionViolation) {
		a.mu.Lock()
		a.violation = err
		a.mu.Unlock()
	}
}

// takePermission returns the violation seen since the last call and forgets
// it, so one denied request does not mark every later error as permanent.
func (a *asyncErrs) takePermission() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.violation
	a.violation = nil
	return v
}

// connect dials the external server. Its errors are classified: sdk.Permanent
// for what retrying cannot fix, sdk.Retriable otherwise.
func connect(cfg *NATSConfig, o openOpts) (*session, error) {
	tlsCfg, err := cfg.tlsConfig(o.allowPrivate)
	if err != nil {
		return nil, sdk.Permanent(err)
	}
	sctx, cancel := context.WithCancel(context.Background())
	async := &asyncErrs{}
	opts := []nats.Option{
		nats.Name("vrsky-nats-consumer"),
		nats.Timeout(o.dialWait),
		nats.PingInterval(pingInterval),
		nats.MaxPingsOutstanding(maxPingsOut),
		// Our loop redials, so every reconnect goes through the same error
		// classification: an auth failure after a credential rotation ends in
		// status error, not in an endless reconnect.
		nats.NoReconnect(),
		// Dial only what the tenant configured, not servers the cluster
		// gossips. The dialer below would refuse a private one anyway.
		nats.IgnoreDiscoveredServers(),
		nats.SetCustomDialer(safeurl.NewDialer(o.dialWait, o.allowPrivate)),
		nats.ErrorHandler(async.handler),
		nats.ClosedHandler(func(*nats.Conn) { cancel() }),
	}
	if tlsCfg != nil {
		opts = append(opts, nats.Secure(tlsCfg))
	}
	switch cfg.AuthType {
	case authCreds:
		opts = append(opts, nats.UserCredentialBytes([]byte(cfg.Creds)))
	case authUserPassword:
		opts = append(opts, nats.UserInfo(cfg.Username, cfg.Password))
	}

	// The list stays a list: joined with commas, a comma inside one URL
	// would become a server the URL check never saw.
	nopts := nats.GetDefaultOptions()
	nopts.Servers = append([]string(nil), cfg.Servers...)
	for _, opt := range opts {
		if err := opt(&nopts); err != nil {
			cancel()
			return nil, classifyConnect(err)
		}
	}
	nc, err := nopts.Connect()
	if err != nil {
		cancel()
		return nil, classifyConnect(err)
	}
	return &session{nc: nc, done: sctx, cancel: cancel, async: async}, nil
}

// classifyConnect sorts a connect error into permanent and retriable.
func classifyConnect(err error) error {
	switch {
	case errors.Is(err, nats.ErrAuthorization), errors.Is(err, nats.ErrAuthExpired),
		errors.Is(err, nats.ErrAuthRevoked), errors.Is(err, nats.ErrAccountAuthExpired):
		return sdk.Permanent(fmt.Errorf("the NATS server refused the credentials (%w)", err))
	case errors.Is(err, safeurl.ErrBlockedAddress):
		// Which address a name resolved to is internal detail; the reason
		// names the rule, not the address.
		return sdk.Permanent(errors.New("the server address is private, loopback or link-local, which is not allowed"))
	case errors.Is(err, nats.ErrSecureConnWanted):
		return sdk.Permanent(errors.New("the NATS server does not offer TLS, which is required"))
	case errors.Is(err, nats.ErrSecureConnRequired):
		return sdk.Permanent(errors.New("the NATS server requires TLS; use a tls:// URL"))
	}
	var verr *tls.CertificateVerificationError
	var uaErr x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var recErr tls.RecordHeaderError
	if errors.As(err, &verr) || errors.As(err, &uaErr) || errors.As(err, &hostErr) {
		// Fixed text: the x509 message names addresses and certificate details.
		return sdk.Permanent(errors.New("the NATS server's TLS certificate could not be verified (set ca_cert for a private CA)"))
	}
	if errors.As(err, &recErr) {
		return sdk.Permanent(errors.New("the NATS server did not answer with TLS"))
	}
	return sdk.Retriable(fmt.Errorf("connect to the NATS server: %w", err))
}

// open connects and binds the durable. With o.create, a missing durable is
// created; without, it is reported as errDurableMissing.
func open(ctx context.Context, cfg *NATSConfig, o openOpts) (*session, error) {
	s, err := connect(cfg, o)
	if err != nil {
		return nil, err
	}
	js, err := jetstream.New(s.nc)
	if err != nil {
		s.close()
		return nil, sdk.Retriable(err)
	}
	actx, cancel := context.WithTimeout(ctx, o.apiWait)
	defer cancel()

	s.stream, err = js.Stream(actx, cfg.Stream)
	if err != nil {
		s.close()
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil, sdk.Permanent(fmt.Errorf("stream %q does not exist on the NATS server", cfg.Stream))
		}
		return nil, classifyJS(err, s.async, "look up stream "+cfg.Stream)
	}
	s.cons, err = bindConsumer(actx, s.stream, cfg, o.create)
	if err != nil {
		s.close()
		return nil, classifyJS(err, s.async, "bind durable "+cfg.Durable)
	}
	if info := s.cons.CachedInfo(); info != nil && info.Config.MaxDeliver > 0 {
		s.warnings = append(s.warnings, fmt.Sprintf("durable %q gives up on a message after %d deliveries; a connection loss or crash counts as one, so set MaxDeliver -1", cfg.Durable, info.Config.MaxDeliver))
	}
	return s, nil
}

// errDurableMissing is what a connection test gets for a durable that the
// first start will create.
var errDurableMissing = errors.New("durable missing")

// bindConsumer returns the durable, creating it when it is missing. An
// existing durable is used as it is and never updated: it may belong to the
// partner, and one that does not fit is a configuration error.
func bindConsumer(ctx context.Context, stream jetstream.Stream, cfg *NATSConfig, create bool) (jetstream.Consumer, error) {
	cons, err := stream.Consumer(ctx, cfg.Durable)
	if errors.Is(err, jetstream.ErrConsumerNotFound) {
		if !create {
			return nil, errDurableMissing
		}
		return stream.CreateConsumer(ctx, jetstream.ConsumerConfig{
			Durable:       cfg.Durable,
			FilterSubject: cfg.FilterSubject,
			AckPolicy:     jetstream.AckExplicitPolicy,
			DeliverPolicy: jetstream.DeliverAllPolicy,
			AckWait:       sourceAckWait,
			// Unlimited: a long VRSky outage must not make the partner's
			// server give up on a message we never took.
			MaxDeliver: -1,
		})
	}
	if errors.Is(err, jetstream.ErrNotPullConsumer) {
		return nil, sdk.Permanent(fmt.Errorf("durable %q is a push consumer; it must be a pull consumer", cfg.Durable))
	}
	if err != nil {
		return nil, err
	}
	if err := durableFits(cons.CachedInfo(), cfg); err != nil {
		return nil, sdk.Permanent(err)
	}
	return cons, nil
}

// durableFits checks an existing durable against what the loop needs.
func durableFits(info *jetstream.ConsumerInfo, cfg *NATSConfig) error {
	if info == nil {
		return nil
	}
	c := info.Config
	if c.DeliverSubject != "" {
		return fmt.Errorf("durable %q is a push consumer; it must be a pull consumer", cfg.Durable)
	}
	if c.AckPolicy != jetstream.AckExplicitPolicy {
		return fmt.Errorf("durable %q does not use explicit ack (ack policy %s)", cfg.Durable, c.AckPolicy)
	}
	if cfg.FilterSubject == "" {
		return nil
	}
	if c.FilterSubject == cfg.FilterSubject || (len(c.FilterSubjects) == 1 && c.FilterSubjects[0] == cfg.FilterSubject) {
		return nil
	}
	have := c.FilterSubject
	if have == "" && len(c.FilterSubjects) > 0 {
		have = strings.Join(c.FilterSubjects, ", ")
	}
	if have == "" {
		have = "none"
	}
	return fmt.Errorf("durable %q filters on %q, not %q; VRSky does not change an existing durable", cfg.Durable, have, cfg.FilterSubject)
}

// classifyJS sorts a JetStream API or fetch error into permanent and
// retriable. what names the step for the reason.
func classifyJS(err error, async *asyncErrs, what string) error {
	if sdk.IsPermanent(err) {
		return err
	}
	if v := async.takePermission(); v != nil {
		return sdk.Permanent(fmt.Errorf("%s: the NATS user may not do this: %w", what, v))
	}
	switch {
	case errors.Is(err, jetstream.ErrJetStreamNotEnabled), errors.Is(err, jetstream.ErrJetStreamNotEnabledForAccount):
		return sdk.Permanent(fmt.Errorf("%s: JetStream is not enabled for this account on the NATS server", what))
	case errors.Is(err, jetstream.ErrConsumerNotFound), errors.Is(err, jetstream.ErrConsumerDeleted):
		// Deleted under us: the next open recreates it or reports why not.
		return sdk.Retriable(fmt.Errorf("%s: %w", what, err))
	}
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) && apiErr.Code >= 400 && apiErr.Code < 500 && apiErr.Code != 408 {
		return sdk.Permanent(fmt.Errorf("%s: %w", what, err))
	}
	return sdk.Retriable(fmt.Errorf("%s: %w", what, err))
}
