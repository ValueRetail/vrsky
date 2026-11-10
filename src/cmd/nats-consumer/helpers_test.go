package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// These tests run two embedded NATS servers: VRSky's own (the harness's
// JetStream, holding VRSKY_DATA) and an "external" one standing in for a
// partner account, which the consumer reads from.

const (
	tTenant = "tenant-a"
	tConn   = "11111111-2222-3333-4444-555555555555"
	tStream = "RH_EVENTS"
	tDur    = "vrsky"
	tSubj   = "rh.evt.v1.tenant-a.>"
)

// external is the partner's server. Restart brings it back on the same port
// with the same store, as a server restart would.
type external struct {
	t    *testing.T
	opts *natsserver.Options
	srv  *natsserver.Server
}

func startExternal(t *testing.T, configure func(*natsserver.Options)) *external {
	t.Helper()
	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		NoLog:     true,
		NoSigs:    true,
		JetStream: true,
		StoreDir:  filepath.Join(t.TempDir(), "js"),
	}
	if configure != nil {
		configure(opts)
	}
	e := &external{t: t, opts: opts}
	e.start()
	// Keep the port a restart must reuse.
	e.opts.Port = e.srv.Addr().(*net.TCPAddr).Port
	t.Cleanup(func() { e.srv.Shutdown() })
	return e
}

func (e *external) start() {
	e.t.Helper()
	o := *e.opts // a server keeps and changes its options
	srv, err := natsserver.NewServer(&o)
	if err != nil {
		e.t.Fatalf("external server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		e.t.Fatal("external server did not come up")
	}
	e.srv = srv
}

func (e *external) restart() {
	e.t.Helper()
	e.srv.Shutdown()
	e.srv.WaitForShutdown()
	e.start()
}

func (e *external) url() string { return e.srv.ClientURL() }

// admin connects to the external server as the partner would, to set up
// streams and publish events.
func (e *external) admin(opts ...nats.Option) (*nats.Conn, nats.JetStreamContext) {
	e.t.Helper()
	nc, err := nats.Connect(e.url(), opts...)
	if err != nil {
		e.t.Fatalf("admin connect: %v", err)
	}
	e.t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		e.t.Fatalf("admin jetstream: %v", err)
	}
	return nc, js
}

func addStream(t *testing.T, js nats.JetStreamContext) {
	t.Helper()
	if _, err := js.AddStream(&nats.StreamConfig{Name: tStream, Subjects: []string{"rh.evt.v1.>"}}); err != nil {
		t.Fatalf("add stream: %v", err)
	}
}

// addDurable pre-creates the durable, as a partner that owns it would, with a
// short AckWait so redelivery tests do not wait 30 s.
func addDurable(t *testing.T, js nats.JetStreamContext, ackWait time.Duration) {
	t.Helper()
	if _, err := js.AddConsumer(tStream, &nats.ConsumerConfig{
		Durable:       tDur,
		FilterSubject: tSubj,
		AckPolicy:     nats.AckExplicitPolicy,
		AckWait:       ackWait,
		MaxDeliver:    -1,
	}); err != nil {
		t.Fatalf("add durable: %v", err)
	}
}

// publishEvent publishes one event; msgID "" leaves the header off.
func publishEvent(t *testing.T, js nats.JetStreamContext, subject, msgID, body string) {
	t.Helper()
	m := &nats.Msg{Subject: subject, Data: []byte(body), Header: nats.Header{}}
	if msgID != "" {
		m.Header.Set(nats.MsgIdHdr, msgID)
	}
	if _, err := js.PublishMsg(m); err != nil {
		t.Fatalf("publish event: %v", err)
	}
}

// vrsky is VRSky's own JetStream and a publish func shaped like the SDK's:
// marshal, publish with the envelope id as Nats-Msg-Id, wait for the ack.
type vrsky struct {
	js  nats.JetStreamContext
	pub *messaging.Publisher

	mu    sync.Mutex
	calls []*envelope.Envelope
	// hook runs before the publish; an error fails it.
	hook func(n int, env *envelope.Envelope) error
}

func startVRSky(t *testing.T) *vrsky {
	t.Helper()
	_, js, cleanup := harness.StartEmbeddedJetStream(t)
	t.Cleanup(cleanup)
	if err := messaging.EnsureStreams(js); err != nil {
		t.Fatalf("ensure streams: %v", err)
	}
	return &vrsky{js: js, pub: messaging.NewPublisher(js)}
}

func (v *vrsky) publish(ctx context.Context, env *envelope.Envelope) error {
	v.mu.Lock()
	v.calls = append(v.calls, env)
	n := len(v.calls)
	hook := v.hook
	v.mu.Unlock()
	if hook != nil {
		if err := hook(n, env); err != nil {
			return err
		}
	}
	body, err := envelope.Marshal(env)
	if err != nil {
		return err
	}
	return v.pub.Publish(ctx, env.TenantID, env.IntegrationID, env.ID, body)
}

func (v *vrsky) published() []*envelope.Envelope {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]*envelope.Envelope(nil), v.calls...)
}

// stored is how many messages VRSKY_DATA holds: what reached the pipeline
// after JetStream's dedup, unlike the publish calls.
func (v *vrsky) stored(t *testing.T) uint64 {
	t.Helper()
	info, err := v.js.StreamInfo(messaging.MainStreamName)
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	return info.State.Msgs
}

// newTestConsumer is a consumer with short timings, wired without the SDK
// runner, for driving consumeLoop directly.
func newTestConsumer(publish sdk.PublishFunc) *natsConsumer {
	return &natsConsumer{
		publish:      publish,
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		allowPrivate: true,
		minBackoff:   50 * time.Millisecond,
		maxBackoff:   200 * time.Millisecond,
		retryDelay:   200 * time.Millisecond,
		apiTimeout:   2 * time.Second,
		fetchWait:    time.Second,
		active:       make(map[string]*activeConn),
	}
}

// run starts consumeLoop for cfg as an active connection and returns its
// cancel and a channel closed when the loop returns.
func (c *natsConsumer) runLoop(cfg *NATSConfig) (context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	own := &activeConn{cancel: cancel}
	c.mu.Lock()
	c.active[tConn] = own
	c.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.consumeLoop(ctx, own, tConn, tTenant, cfg, c.logger)
	}()
	return cancel, done
}

func baseConfig(url string) *NATSConfig {
	cfg := &NATSConfig{Servers: []string{url}, Stream: tStream, Durable: tDur, FilterSubject: tSubj}
	cfg.normalise()
	return cfg
}

// consumerInfo reads the durable's state on the external server.
func consumerInfo(t *testing.T, js nats.JetStreamContext) *nats.ConsumerInfo {
	t.Helper()
	info, err := js.ConsumerInfo(tStream, tDur)
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	return info
}

// proxy is a TCP relay in front of the external server. kill drops every
// relayed connection at once, as a crash or a network cut would: whatever
// the client writes afterwards never reaches the server. New connections are
// still accepted, so a redial gets through.
type proxy struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func startProxy(t *testing.T, upstream string) *proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &proxy{ln: ln}
	go func() {
		for {
			down, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", upstream)
			if err != nil {
				_ = down.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, down, up)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(up, down); _ = up.Close() }()
			go func() { _, _ = io.Copy(down, up); _ = down.Close() }()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); p.kill() })
	return p
}

func (p *proxy) url() string { return "nats://" + p.ln.Addr().String() }

func (p *proxy) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

// operatorServer configures decentralised (JWT) auth with one account that
// has JetStream, and returns a .creds file for a user in it.
func operatorServer(t *testing.T) (configure func(*natsserver.Options), creds string) {
	t.Helper()
	okp, _ := nkeys.CreateOperator()
	opk, _ := okp.PublicKey()
	oc := jwt.NewOperatorClaims(opk)

	sysKP, _ := nkeys.CreateAccount()
	sysPK, _ := sysKP.PublicKey()
	oc.SystemAccount = sysPK
	sysJWT, err := jwt.NewAccountClaims(sysPK).Encode(okp)
	if err != nil {
		t.Fatalf("system account jwt: %v", err)
	}

	akp, _ := nkeys.CreateAccount()
	apk, _ := akp.PublicKey()
	ac := jwt.NewAccountClaims(apk)
	ac.Limits.JetStreamLimits = jwt.JetStreamLimits{MemoryStorage: -1, DiskStorage: -1, Streams: -1, Consumer: -1}
	accJWT, err := ac.Encode(okp)
	if err != nil {
		t.Fatalf("account jwt: %v", err)
	}

	ukp, _ := nkeys.CreateUser()
	upk, _ := ukp.PublicKey()
	userJWT, err := jwt.NewUserClaims(upk).Encode(akp)
	if err != nil {
		t.Fatalf("user jwt: %v", err)
	}
	seed, _ := ukp.Seed()
	file, err := jwt.FormatUserConfig(userJWT, seed)
	if err != nil {
		t.Fatalf("creds file: %v", err)
	}

	resolver := &natsserver.MemAccResolver{}
	if err := resolver.Store(sysPK, sysJWT); err != nil {
		t.Fatalf("store system account: %v", err)
	}
	if err := resolver.Store(apk, accJWT); err != nil {
		t.Fatalf("store account: %v", err)
	}
	return func(o *natsserver.Options) {
		o.TrustedOperators = []*jwt.OperatorClaims{oc}
		o.SystemAccount = sysPK
		o.AccountResolver = resolver
	}, string(file)
}
