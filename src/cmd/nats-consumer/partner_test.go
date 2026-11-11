package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// These tests run against a server configured the way Retailhero renders a
// VRSky partner account (Retailhero scripts/nats-partner.py, #2680): an
// application account with JetStream, and a per-tenant account with one
// NKey user, no JetStream of its own, the consumer API of exactly one
// durable imported under JS.RH.API, the ack subjects under their own name,
// replies only on _INBOX_vrsky_t_a, and no stream info. TLS with a CA of the
// test's own.

const (
	pTenant    = "T-A"
	pStream    = "RH_EVT_V1_PRODUCT"
	pDurable   = "vrsky_product-version_T-A"
	pSubject   = "rh.evt.v1.T-A.product.product.version"
	pAPIPrefix = "JS.RH.API"
	pInbox     = "_INBOX_vrsky_t_a"
	pAdminPass = "admin-pass-for-tests"
)

type partnerServer struct {
	srv    *natsserver.Server
	caPEM  string
	seed   string // the partner user's seed
	rootCA *x509.CertPool
}

// startPartnerServer renders the two-account configuration and starts it.
func startPartnerServer(t *testing.T) *partnerServer {
	t.Helper()
	return startPartnerServerGrants(t, true)
}

// startPartnerServerGrants is startPartnerServer; withNext false leaves the
// pull (CONSUMER.MSG.NEXT) out of the partner user's publish permissions.
func startPartnerServerGrants(t *testing.T, withNext bool) *partnerServer {
	t.Helper()
	dir := t.TempDir()
	caPEM, certFile, keyFile, pool := testPKI(t, dir)

	ukp, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("create user nkey: %v", err)
	}
	upk, _ := ukp.PublicKey()
	seed, _ := ukp.Seed()

	conf := partnerConf("host: 127.0.0.1\nport: -1", certFile, keyFile, filepath.Join(dir, "js"), upk, withNext)
	path := filepath.Join(dir, "nats.conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatalf("write server config: %v", err)
	}
	opts, err := natsserver.ProcessConfigFile(path)
	if err != nil {
		t.Fatalf("server config: %v", err)
	}
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("partner server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("partner server did not come up")
	}
	t.Cleanup(srv.Shutdown)
	return &partnerServer{srv: srv, caPEM: caPEM, seed: string(seed), rootCA: pool}
}

// partnerConf renders the server configuration with Retailhero's grants for
// one tenant (nats-partner.py); listen holds the address lines. withNext
// false leaves the pull (CONSUMER.MSG.NEXT) out of the partner user's
// permissions.
func partnerConf(listen, certFile, keyFile, storeDir, userPub string, withNext bool) string {
	api := map[string]string{
		"create": fmt.Sprintf("$JS.API.CONSUMER.CREATE.%s.%s.%s", pStream, pDurable, pSubject),
		"info":   fmt.Sprintf("$JS.API.CONSUMER.INFO.%s.%s", pStream, pDurable),
		"next":   fmt.Sprintf("$JS.API.CONSUMER.MSG.NEXT.%s.%s", pStream, pDurable),
	}
	ack := fmt.Sprintf("$JS.ACK.%s.%s.>", pStream, pDurable)
	inside := func(s string) string { return pAPIPrefix + "." + strings.TrimPrefix(s, "$JS.API.") }
	nextGrant := inside(api["next"])
	if !withNext {
		nextGrant = "vrsky.test.no-pull-grant"
	}

	return fmt.Sprintf(`
%s
tls {
  cert_file: %q
  key_file: %q
  timeout: 2
}
jetstream {
  store_dir: %q
}
accounts {
  RETAILHERO: {
    jetstream: enabled
    users: [ { user: admin, password: %q } ]
    exports: [
      { stream: %q, accounts: [VRSKY_T_A] }
      { service: %q, accounts: [VRSKY_T_A] }
      { service: %q, accounts: [VRSKY_T_A] }
      { service: %q, accounts: [VRSKY_T_A], response_type: stream }
      { service: %q, accounts: [VRSKY_T_A] }
    ]
  }
  VRSKY_T_A: {
    users: [
      {
        nkey: %s
        permissions: {
          publish: [%q, %q, %q, %q]
          subscribe: [%q, %q]
        }
      }
    ]
    imports: [
      { stream: { account: RETAILHERO, subject: %q } }
      { service: { account: RETAILHERO, subject: %q }, to: %q }
      { service: { account: RETAILHERO, subject: %q }, to: %q }
      { service: { account: RETAILHERO, subject: %q }, to: %q }
      { service: { account: RETAILHERO, subject: %q } }
    ]
  }
}
`, listen, certFile, keyFile, storeDir, pAdminPass,
		pSubject, api["create"], api["info"], api["next"], ack,
		userPub,
		inside(api["create"]), inside(api["info"]), nextGrant, ack,
		pInbox+".>", pSubject,
		pSubject,
		api["create"], inside(api["create"]),
		api["info"], inside(api["info"]),
		api["next"], inside(api["next"]),
		ack)
}

func (p *partnerServer) url() string {
	return fmt.Sprintf("tls://127.0.0.1:%d", p.srv.Addr().(*net.TCPAddr).Port)
}

// admin is Retailhero's side: the application account, which owns the
// stream and the durable.
func (p *partnerServer) admin(t *testing.T) nats.JetStreamContext {
	t.Helper()
	nc, err := nats.Connect(p.url(), nats.UserInfo("admin", pAdminPass), nats.Secure(&tls.Config{RootCAs: p.rootCA, MinVersion: tls.VersionTLS12}))
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("admin jetstream: %v", err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name: pStream, Subjects: []string{"rh.evt.v1.*.product.>"}, Duplicates: 200 * time.Millisecond,
	}); err != nil {
		t.Fatalf("add stream: %v", err)
	}
	return js
}

// addPartnerDurable creates the durable as Retailhero's messaging-up does:
// pull, explicit ack, bounded redelivery, filtered to the one subject.
func addPartnerDurable(t *testing.T, js nats.JetStreamContext, ackWait time.Duration) {
	t.Helper()
	if _, err := js.AddConsumer(pStream, &nats.ConsumerConfig{
		Durable: pDurable, FilterSubject: pSubject, AckPolicy: nats.AckExplicitPolicy,
		AckWait: ackWait, MaxDeliver: 10, MaxAckPending: 100,
	}); err != nil {
		t.Fatalf("add durable: %v", err)
	}
}

// partnerConfig is the connector's config for the partner account, the way
// the docs page tells a tenant to fill it in.
func (p *partnerServer) partnerConfig(url string) *NATSConfig {
	no := false
	cfg := &NATSConfig{
		Servers: []string{url}, AuthType: authNKey, NKeySeed: p.seed, CACert: p.caPEM,
		APIPrefix: pAPIPrefix, InboxPrefix: pInbox, CreateDurable: &no,
		Stream: pStream, Durable: pDurable, FilterSubject: pSubject, DeliverPolicy: deliverNew,
	}
	cfg.normalise()
	return cfg
}

func partnerAckFloor(t *testing.T, js nats.JetStreamContext) (floor uint64, pending int) {
	t.Helper()
	info, err := js.ConsumerInfo(pStream, pDurable)
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	return info.AckFloor.Stream, info.NumAckPending
}

func partnerID(eventID string) string { return envelopeID(tConn, "id:"+eventID) }

// The partner account reads its tenant's events through the imported API,
// with its own inbox and the NKey, binds the existing durable without stream
// info, and acks each one after VRSky stored it.
func TestPartner_ReadsAndAcksWithoutStreamInfo(t *testing.T) {
	ps := startPartnerServer(t)
	js := ps.admin(t)
	addPartnerDurable(t, js, 30*time.Second)
	publishEvent(t, js, pSubject, "evt-1", `{"event_id":"evt-1"}`)
	publishEvent(t, js, pSubject, "evt-2", `{"event_id":"evt-2"}`)
	// Another tenant's event on the same stream: the durable's filter and the
	// account's grants keep it out.
	publishEvent(t, js, "rh.evt.v1.T-B.product.product.version", "evt-b", `{}`)

	v := startVRSky(t)
	c := newTestConsumer(v.publish)
	logs := &lockedBuf{}
	c.logger = slog.New(slog.NewTextHandler(logs, nil))
	rec := withStatusDB(t, c)
	cancel, done := c.runLoop(ps.partnerConfig(ps.url()))
	defer stop(t, cancel, done)

	harness.Eventually(t, wait, "both stored and acked", func() bool {
		floor, pending := partnerAckFloor(t, js)
		return v.stored(t) == 2 && floor == 2 && pending == 0
	})
	calls := v.published()
	if len(calls) != 2 || calls[0].ID != partnerID("evt-1") || calls[1].ID != partnerID("evt-2") {
		t.Fatalf("published %d envelope(s), want evt-1 then evt-2 by Nats-Msg-Id", len(calls))
	}
	if calls[0].Metadata["nats_msg_id"] != "evt-1" || calls[0].Metadata["nats_subject"] != pSubject {
		t.Errorf("metadata = %v", calls[0].Metadata)
	}
	if reason, ok := rec.errorWrite(); ok {
		t.Fatalf("the connection ended: %s", reason)
	}
	// The double ack's confirmation comes back across the account boundary
	// (through the imported $JS.ACK service and the custom inbox).
	if strings.Contains(logs.String(), "Ack to the NATS server failed") {
		t.Fatalf("a double ack got no confirmation: %s", logs.String())
	}
}

// A crash after VRSky's publish and before the source ack: the partner's
// server redelivers after AckWait, the next process publishes the same
// envelope id, and VRSKY_DATA keeps one copy.
func TestPartner_CrashBeforeAck_RedeliveredOnce(t *testing.T) {
	ps := startPartnerServer(t)
	js := ps.admin(t)
	addPartnerDurable(t, js, time.Second)
	publishEvent(t, js, pSubject, "evt-1", `{"event_id":"evt-1"}`)

	v := startVRSky(t)
	px := startProxy(t, ps.srv.Addr().String())
	// The certificate names 127.0.0.1, which the proxy is on too.
	pxURL := strings.Replace(px.url(), "nats://", "tls://", 1)

	var mu sync.Mutex
	var cancelA context.CancelFunc
	a := newTestConsumer(func(ctx context.Context, env *envelope.Envelope) error {
		err := v.publish(ctx, env)
		px.kill()
		mu.Lock()
		cancelA()
		mu.Unlock()
		return err
	})
	mu.Lock()
	cancelA, doneA := a.runLoop(ps.partnerConfig(pxURL))
	mu.Unlock()
	select {
	case <-doneA:
	case <-time.After(wait):
		t.Fatal("process A did not stop")
	}
	if got := v.stored(t); got != 1 {
		t.Fatalf("after A: VRSKY_DATA holds %d, want 1", got)
	}
	if floor, _ := partnerAckFloor(t, js); floor != 0 {
		t.Fatalf("A's ack reached the source (ack floor %d)", floor)
	}

	b := newTestConsumer(v.publish)
	cancelB, doneB := b.runLoop(ps.partnerConfig(ps.url()))
	defer stop(t, cancelB, doneB)
	harness.Eventually(t, wait, "redelivered and acked", func() bool {
		floor, pending := partnerAckFloor(t, js)
		return floor == 1 && pending == 0
	})
	calls := v.published()
	if len(calls) != 2 || calls[0].ID != calls[1].ID || calls[0].ID != partnerID("evt-1") {
		t.Fatalf("publish calls = %d, want 2 with evt-1's envelope id", len(calls))
	}
	if got := v.stored(t); got != 1 {
		t.Fatalf("VRSKY_DATA holds %d after the redelivery, want 1", got)
	}
}

// Replays reach the pipeline once: the relay publishing the same event again
// after the source stream's own dedup window (a new stream sequence, the same
// Nats-Msg-Id), and the operator recreating the durable so it delivers
// everything again.
func TestPartner_IdempotentReplay(t *testing.T) {
	ps := startPartnerServer(t)
	js := ps.admin(t)
	addPartnerDurable(t, js, 30*time.Second)
	publishEvent(t, js, pSubject, "evt-1", `{"event_id":"evt-1"}`)
	time.Sleep(300 * time.Millisecond) // past the stream's 200 ms dedup window
	publishEvent(t, js, pSubject, "evt-1", `{"event_id":"evt-1"}`)
	info, err := js.StreamInfo(pStream)
	if err != nil || info.State.Msgs != 2 {
		t.Fatalf("source stream should hold the event twice: %v, %v", info, err)
	}

	v := startVRSky(t)
	c := newTestConsumer(v.publish)
	cancel, done := c.runLoop(ps.partnerConfig(ps.url()))
	harness.Eventually(t, wait, "both deliveries acked", func() bool {
		floor, pending := partnerAckFloor(t, js)
		return floor == 2 && pending == 0
	})
	stop(t, cancel, done)
	if got := v.stored(t); got != 1 {
		t.Fatalf("VRSKY_DATA holds %d after a re-published event, want 1", got)
	}

	// The operator recreates the durable from the start of the stream.
	if err := js.DeleteConsumer(pStream, pDurable); err != nil {
		t.Fatalf("delete durable: %v", err)
	}
	addPartnerDurable(t, js, 30*time.Second)
	c2 := newTestConsumer(v.publish)
	cancel2, done2 := c2.runLoop(ps.partnerConfig(ps.url()))
	defer stop(t, cancel2, done2)
	harness.Eventually(t, wait, "replayed and acked", func() bool {
		floor, pending := partnerAckFloor(t, js)
		return floor == 2 && pending == 0
	})
	if n := len(v.published()); n != 4 {
		t.Errorf("publish calls = %d, want 4 (two deliveries, twice)", n)
	}
	if got := v.stored(t); got != 1 {
		t.Fatalf("VRSKY_DATA holds %d after the replay, want 1", got)
	}
}

// VRSky creates its own durable through the create grant, which names the
// filter: with the exact subject it works, without a filter the server
// refuses it.
func TestPartner_CreatesOwnDurable(t *testing.T) {
	t.Run("with the granted filter", func(t *testing.T) {
		ps := startPartnerServer(t)
		js := ps.admin(t)
		publishEvent(t, js, pSubject, "evt-old", `{}`)

		v := startVRSky(t)
		c := newTestConsumer(v.publish)
		cfg := ps.partnerConfig(ps.url())
		cfg.CreateDurable = nil // default: a start may create it
		cancel, done := c.runLoop(cfg)
		defer stop(t, cancel, done)

		harness.Eventually(t, wait, "durable created", func() bool {
			_, err := js.ConsumerInfo(pStream, pDurable)
			return err == nil
		})
		publishEvent(t, js, pSubject, "evt-new", `{}`)
		harness.Eventually(t, wait, "new event stored", func() bool { return v.stored(t) == 1 })
		info, err := js.ConsumerInfo(pStream, pDurable)
		if err != nil {
			t.Fatalf("consumer info: %v", err)
		}
		if info.Config.FilterSubject != pSubject || info.Config.DeliverPolicy != nats.DeliverNewPolicy {
			t.Errorf("created durable: filter %q, deliver %v", info.Config.FilterSubject, info.Config.DeliverPolicy)
		}
		if calls := v.published(); calls[0].ID != partnerID("evt-new") {
			t.Error("deliver_policy new delivered the old event")
		}
	})
	t.Run("without a filter", func(t *testing.T) {
		ps := startPartnerServer(t)
		ps.admin(t)
		v := startVRSky(t)
		c := newTestConsumer(v.publish)
		rec := withStatusDB(t, c)
		cfg := ps.partnerConfig(ps.url())
		cfg.CreateDurable, cfg.FilterSubject = nil, ""
		cancel, done := c.runLoop(cfg)
		select {
		case <-done:
		case <-time.After(wait):
			cancel()
			t.Fatal("the loop kept retrying a refused create")
		}
		if reason, ok := rec.errorWrite(); !ok || !strings.Contains(reason, "may not do this") {
			t.Fatalf("status error reason = %q (written %v)", reason, ok)
		}
	})
}

// What retrying cannot fix ends in status error with a reason that points at
// the setting, and the seed appears nowhere.
func TestPartner_PermanentErrors(t *testing.T) {
	other, _ := nkeys.CreateUser()
	otherSeed, _ := other.Seed()
	keep := func(*NATSConfig) {}
	tests := []struct {
		name      string
		mut       func(*NATSConfig)
		noDurable bool
		noPull    bool
		want      string
	}{
		{"seed the server does not know", func(c *NATSConfig) { c.NKeySeed = string(otherSeed) }, false, false, "refused the credentials"},
		{"no API prefix", func(c *NATSConfig) { c.APIPrefix = "" }, false, false, "set api_prefix"},
		{"no inbox prefix", func(c *NATSConfig) { c.InboxPrefix = "" }, false, false, "set inbox_prefix"},
		// The granted durable is not created yet (the owner has no relay).
		{"durable missing, creation off", keep, true, false, "create_durable is off"},
		{"a durable the account has no grant for", func(c *NATSConfig) { c.Durable = "vrsky_other" }, false, false, "may not do this"},
		{"untrusted certificate", func(c *NATSConfig) { c.CACert = "" }, false, false, "could not be verified"},
		// A denied pull gets no answer at all; it must not look like an
		// empty stream forever.
		{"may not pull", keep, false, true, "pull from durable " + pDurable + ": the NATS user may not do this"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := startPartnerServerGrants(t, !tt.noPull)
			js := ps.admin(t)
			if !tt.noDurable {
				addPartnerDurable(t, js, 30*time.Second)
			}

			v := startVRSky(t)
			c := newTestConsumer(v.publish)
			logs := &lockedBuf{}
			c.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			rec := withStatusDB(t, c)
			cfg := ps.partnerConfig(ps.url())
			tt.mut(cfg)
			if err := cfg.validate(true); err != nil {
				t.Fatalf("config refused before connecting: %v", err)
			}
			cancel, done := c.runLoop(cfg)
			select {
			case <-done:
			case <-time.After(wait):
				cancel()
				t.Fatal("the loop kept retrying a permanent error")
			}
			reason, ok := rec.errorWrite()
			if !ok || !strings.Contains(reason, tt.want) {
				t.Fatalf("status error reason = %q (written %v), want %q", reason, ok, tt.want)
			}
			for _, s := range []string{ps.seed, string(otherSeed)} {
				if strings.Contains(reason, s) || strings.Contains(logs.String(), s) {
					t.Fatal("a seed reached the reason or the log")
				}
			}
		})
	}
}

// The connection test binds the same way: no stream info, prefixes used. A
// missing durable is an error when creation is off.
func TestPartner_TestConnection(t *testing.T) {
	ps := startPartnerServer(t)
	js := ps.admin(t)
	c := newTestConsumer(nil)
	body := map[string]any{
		"servers": []string{ps.url()}, "auth_type": "nkey", "nkey_seed": ps.seed, "ca_cert": ps.caPEM,
		"api_prefix": pAPIPrefix, "inbox_prefix": pInbox, "create_durable": false,
		"stream": pStream, "durable": pDurable, "filter_subject": pSubject,
	}
	got := postTest(t, c, body)
	if got["ok"] != false || !strings.Contains(fmt.Sprint(got["error"]), "create_durable is off") {
		t.Fatalf("missing durable with creation off: %v", got)
	}
	if strings.Contains(fmt.Sprint(got), ps.seed) {
		t.Fatal("the answer quotes the seed")
	}
	addPartnerDurable(t, js, 30*time.Second)
	if got := postTest(t, c, body); got["ok"] != true {
		t.Fatalf("test failed: %v", got)
	}
}

// testPKI writes a CA and a server certificate for 127.0.0.1 and returns the
// CA's PEM, the server's cert and key files, and a pool with the CA.
func testPKI(t *testing.T, dir string) (caPEM, certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test run CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "nats"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(srvKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	certFile, keyFile = filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	pool.AddCert(caCert)
	return caPEM, certFile, keyFile, pool
}

// End to end through the SDK runner: the seed and the CA come from stored
// secrets (nkey_seed_secret_id, ca_cert_secret_id), the start command binds
// the partner's durable without creating it, and the event reaches VRSky.
func TestPartner_StartCommand_StoredSeedAndCA(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testKey)
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")

	ps := startPartnerServer(t)
	js := ps.admin(t)
	addPartnerDurable(t, js, 30*time.Second)
	publishEvent(t, js, pSubject, "evt-1", `{"event_id":"evt-1"}`)

	seedCipher, err := crypto.Encrypt(ps.seed, testKey)
	if err != nil {
		t.Fatalf("encrypt seed: %v", err)
	}
	caCipher, err := crypto.Encrypt(ps.caPEM, testKey)
	if err != nil {
		t.Fatalf("encrypt CA: %v", err)
	}
	const seedID, caID = "7a1b9d34-0f5c-4c8e-9a52-1a2b3c4d5e6f", "8b2c9d34-0f5c-4c8e-9a52-1a2b3c4d5e6f"
	nodes, _ := json.Marshal([]map[string]any{{
		"id": "src", "type": "consumer",
		"config": map[string]any{"type": "nats", "nats": map[string]any{
			"servers": []string{ps.url()}, "auth_type": "nkey", "nkey_seed_secret_id": seedID, "ca_cert_secret_id": caID,
			"api_prefix": pAPIPrefix, "inbox_prefix": pInbox, "create_durable": false,
			"stream": pStream, "durable": pDurable, "filter_subject": pSubject,
		}},
	}})

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(tConn, tTenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow(nodes))
	mock.ExpectQuery("SELECT ciphertext FROM secrets").WithArgs(seedID, tTenant).
		WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(seedCipher))
	mock.ExpectQuery("SELECT ciphertext FROM secrets").WithArgs(caID, tTenant).
		WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(caCipher))
	mock.ExpectExec(`UPDATE connections SET status = \$1, started_at = NOW\(\), updated_at = NOW\(\), last_error = NULL`).
		WithArgs("running", tConn, tTenant).WillReturnResult(sqlmock.NewResult(0, 1))

	c := &natsConsumer{}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "nats-consumer", DB: db})
	cmd, _ := json.Marshal(map[string]string{"connection_id": tConn, "tenant_id": tTenant})
	harness.Eventually(t, wait, "connection active", func() bool {
		if err := h.NATS().Publish("vrsky.commands."+tTenant+".connection.start", cmd); err != nil {
			t.Fatalf("publish start: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		c.mu.RLock()
		defer c.mu.RUnlock()
		_, ok := c.active[tConn]
		return ok
	})
	got := h.ExpectEnvelope(t, harness.MatchTenant(tTenant), wait)
	if got.ID != partnerID("evt-1") {
		t.Errorf("envelope id %s, want evt-1's", got.ID)
	}
	harness.Eventually(t, wait, "acked on the source", func() bool {
		floor, pending := partnerAckFloor(t, js)
		return floor == 1 && pending == 0
	})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}
