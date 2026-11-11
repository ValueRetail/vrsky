package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func TestValidate(t *testing.T) {
	ok := func() NATSConfig {
		return NATSConfig{Servers: []string{"tls://nats.example.com:4222"}, Stream: tStream, Durable: tDur}
	}
	tests := []struct {
		name         string
		mut          func(*NATSConfig)
		allowPrivate bool
		want         string // "" = valid
	}{
		{"minimal", nil, false, ""},
		{"user and password", func(c *NATSConfig) { c.AuthType, c.Username, c.Password = authUserPassword, "rh", "pw" }, false, ""},
		{"loopback with the dev flag", func(c *NATSConfig) { c.Servers = []string{"nats://127.0.0.1:4222"} }, true, ""},

		{"no servers", func(c *NATSConfig) { c.Servers = nil }, false, "at least one"},
		{"loopback", func(c *NATSConfig) { c.Servers = []string{"tls://127.0.0.1:4222"} }, false, "private"},
		{"userinfo in the URL", func(c *NATSConfig) { c.Servers = []string{"tls://rh:" + tPassword + "@nats.example.com"} }, false, "user name"},
		{"http URL", func(c *NATSConfig) { c.Servers = []string{"https://nats.example.com"} }, false, "nats:// or tls://"},
		{"no stream", func(c *NATSConfig) { c.Stream = "" }, false, "stream is required"},
		{"no durable", func(c *NATSConfig) { c.Durable = "" }, false, "durable is required"},
		{"stream with a dot", func(c *NATSConfig) { c.Stream = "A.B" }, false, "not a valid JetStream name"},
		{"durable with a wildcard", func(c *NATSConfig) { c.Durable = "x>" }, false, "not a valid JetStream name"},
		{"filter with a space", func(c *NATSConfig) { c.FilterSubject = "a b" }, false, "spaces"},
		{"batch too big", func(c *NATSConfig) { c.BatchSize = maxBatchSize + 1 }, false, "batch_size"},
		{"unknown auth type", func(c *NATSConfig) { c.AuthType = "token" }, false, "auth_type"},
		{"user without password", func(c *NATSConfig) { c.AuthType, c.Username = authUserPassword, "rh" }, false, "stored password"},
		{"creds missing", func(c *NATSConfig) { c.AuthType = authCreds }, false, "stored credentials file"},
		{"creds not a creds file", func(c *NATSConfig) { c.AuthType, c.Creds = authCreds, "secret-"+tPassword }, false, "not a NATS .creds file"},
		{"creds with the JWT block only", func(c *NATSConfig) {
			c.AuthType, c.Creds = authCreds, "-----BEGIN NATS USER JWT-----\neyJ0\n------END NATS USER JWT------\n"
		}, false, "not a NATS .creds file"},
		{"ca_cert not PEM", func(c *NATSConfig) { c.CACert = "nope" }, false, "ca_cert"},
		{"deliver new", func(c *NATSConfig) { c.DeliverPolicy = deliverNew }, false, ""},
		{"deliver by start time", func(c *NATSConfig) { c.DeliverPolicy, c.OptStartTime = deliverByStartTime, "2026-11-01T00:00:00Z" }, false, ""},
		{"by start time without a time", func(c *NATSConfig) { c.DeliverPolicy = deliverByStartTime }, false, "opt_start_time"},
		{"by start time, bad time", func(c *NATSConfig) { c.DeliverPolicy, c.OptStartTime = deliverByStartTime, "yesterday" }, false, "RFC 3339"},
		{"start time without the policy", func(c *NATSConfig) { c.OptStartTime = "2026-11-01T00:00:00Z" }, false, "only used with"},
		{"unknown deliver policy", func(c *NATSConfig) { c.DeliverPolicy = "last" }, false, "deliver_policy"},

		{"nkey", func(c *NATSConfig) { c.AuthType, c.NKeySeed = authNKey, testUserSeed(t) }, false, ""},
		{"nkey seed file with a newline", func(c *NATSConfig) { c.AuthType, c.NKeySeed = authNKey, testUserSeed(t)+"\n" }, false, ""},
		{"nkey missing", func(c *NATSConfig) { c.AuthType = authNKey }, false, "stored NKey seed"},
		{"nkey not a seed", func(c *NATSConfig) { c.AuthType, c.NKeySeed = authNKey, "secret-"+tPassword }, false, "not a NATS NKey seed"},
		{"nkey an account seed", func(c *NATSConfig) { c.AuthType, c.NKeySeed = authNKey, testAccountSeed(t) }, false, "not a user seed"},
		{"partner prefixes", func(c *NATSConfig) { c.APIPrefix, c.InboxPrefix = "JS.RH.API", "_INBOX_vrsky_t_a" }, false, ""},
		{"api prefix with a wildcard", func(c *NATSConfig) { c.APIPrefix = "JS.*.API" }, false, "api_prefix"},
		{"api prefix with an empty token", func(c *NATSConfig) { c.APIPrefix = "JS..API" }, false, "empty token"},
		{"inbox prefix with a space", func(c *NATSConfig) { c.InboxPrefix = "_INBOX vrsky" }, false, "inbox_prefix"},
		{"inbox prefix with >", func(c *NATSConfig) { c.InboxPrefix = "_INBOX.>" }, false, "inbox_prefix"},
		{"max wait too long", func(c *NATSConfig) { c.MaxWaitSeconds = maxMaxWaitSeconds + 1 }, false, "max_wait_seconds"},
		// Negative values are refused, as the start check refuses them;
		// 0 (or absent) is the default.
		{"negative batch", func(c *NATSConfig) { c.BatchSize = -1 }, false, "batch_size must be from 1 to 256"},
		{"negative max wait", func(c *NATSConfig) { c.MaxWaitSeconds = -1 }, false, "max_wait_seconds must be from 1 to 30"},
		{"batch and wait 0 mean the defaults", func(c *NATSConfig) { c.BatchSize, c.MaxWaitSeconds = 0, 0 }, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ok()
			if tt.mut != nil {
				tt.mut(&cfg)
			}
			cfg.normalise()
			err := cfg.validate(tt.allowPrivate)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tt.want != "" && err == nil:
				t.Fatalf("allowed, want an error containing %q", tt.want)
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
			if err != nil && strings.Contains(err.Error(), tPassword) {
				t.Fatalf("error %q quotes a credential", err)
			}
		})
	}
}

func TestNormalise(t *testing.T) {
	cfg := NATSConfig{Servers: []string{" tls://a.example.com ", "", "tls://b.example.com"}, Stream: " S ", Durable: "d\n"}
	cfg.normalise()
	if len(cfg.Servers) != 2 || cfg.Servers[0] != "tls://a.example.com" || cfg.Stream != "S" || cfg.Durable != "d" {
		t.Errorf("normalised = %+v", cfg)
	}
	if cfg.AuthType != authNone || cfg.BatchSize != defaultBatchSize {
		t.Errorf("defaults: auth %q batch %d", cfg.AuthType, cfg.BatchSize)
	}
	if cfg.maxWait() != defaultMaxWaitSeconds*time.Second || !cfg.mayCreate() {
		t.Errorf("defaults: max wait %s, may create %v", cfg.maxWait(), cfg.mayCreate())
	}
	p := NATSConfig{APIPrefix: " JS.RH.API. ", InboxPrefix: "_INBOX_vrsky_t_a.", MaxWaitSeconds: 12}
	p.normalise()
	if p.APIPrefix != "JS.RH.API" || p.InboxPrefix != "_INBOX_vrsky_t_a" || p.maxWait() != 12*time.Second {
		t.Errorf("prefixes and wait normalised = %q, %q, %s", p.APIPrefix, p.InboxPrefix, p.maxWait())
	}
	var off NATSConfig
	if err := json.Unmarshal([]byte(`{"create_durable":false}`), &off); err != nil || off.mayCreate() {
		t.Errorf("create_durable false: may create %v (%v)", off.mayCreate(), err)
	}
}

// The tests' fetchWait wins when set; otherwise the config's max wait.
func TestPullWait(t *testing.T) {
	cfg := &NATSConfig{MaxWaitSeconds: 7}
	if got := (&natsConsumer{}).pullWait(cfg); got != 7*time.Second {
		t.Errorf("pullWait = %s, want 7s", got)
	}
	if got := (&natsConsumer{fetchWait: time.Second}).pullWait(cfg); got != time.Second {
		t.Errorf("pullWait with the test override = %s, want 1s", got)
	}
}

// A denied subject names the setting it points at.
func TestPermissionHint(t *testing.T) {
	tests := []struct{ msg, want string }{
		{`nats: permissions violation: Permissions Violation for Publish to "$JS.API.CONSUMER.INFO.S.d"`, "api_prefix"},
		{`nats: permissions violation: Permissions Violation for Subscription to "_INBOX.abc.*"`, "inbox_prefix"},
		{`nats: permissions violation: Permissions Violation for Publish to "JS.RH.API.CONSUMER.INFO.S.other"`, ""},
	}
	for _, tt := range tests {
		got := permissionHint(errors.New(tt.msg), &asyncErrs{})
		if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
			t.Errorf("%s: hint %q, want one naming %q", tt.msg, got, tt.want)
		}
		// A setting already made is not suggested again.
		if got := permissionHint(errors.New(tt.msg), &asyncErrs{apiPrefix: true, inboxPrefix: true}); got != "" {
			t.Errorf("%s: hint %q with both prefixes set", tt.msg, got)
		}
	}
}

func TestStreamMissing(t *testing.T) {
	if !streamMissing(jetstream.ErrStreamNotFound) {
		t.Error("ErrStreamNotFound")
	}
	if !streamMissing(&jetstream.APIError{Code: 404, ErrorCode: jetstream.JSErrCodeStreamNotFound}) {
		t.Error("the API error code for stream not found")
	}
	if streamMissing(&jetstream.APIError{Code: 404, ErrorCode: jetstream.JSErrCodeConsumerNotFound}) {
		t.Error("consumer not found taken for stream not found")
	}
}

func testUserSeed(t *testing.T) string {
	t.Helper()
	kp, err := nkeys.CreateUser()
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := kp.Seed()
	return string(seed)
}

func testAccountSeed(t *testing.T) string {
	t.Helper()
	kp, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := kp.Seed()
	return string(seed)
}

// TLS is required unless the operator's dev switch is on; with it, a CA or a
// tls:// server still asks for TLS.
func TestTLSConfig(t *testing.T) {
	tests := []struct {
		name         string
		servers      []string
		allowPrivate bool
		want         bool
	}{
		{"production, nats://", []string{"nats://nats.example.com"}, false, true},
		{"dev, nats://", []string{"nats://127.0.0.1:4222"}, true, false},
		{"dev, tls://", []string{"tls://127.0.0.1:4222"}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NATSConfig{Servers: tt.servers}
			got, err := cfg.tlsConfig(tt.allowPrivate)
			if err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tt.want {
				t.Errorf("TLS config set = %v, want %v", got != nil, tt.want)
			}
		})
	}
}

func TestPlaintextCredentials(t *testing.T) {
	tests := []struct {
		block string
		want  string
	}{
		{`{"password_secret_id":"0b1c","creds_secret_id":"9f2e"}`, ""},
		{`{"password":""}`, ""},
		{`{"password":"` + tPassword + `"}`, "password is stored in plaintext"},
		{`{"creds":"-----BEGIN NATS USER JWT-----"}`, "creds is stored in plaintext"},
		{`{"nkey_seed":"SU` + tPassword + `"}`, "nkey_seed is stored in plaintext"},
		{`{"nkey_seed_secret_id":"0b1c","ca_cert":"-----BEGIN CERTIFICATE-----"}`, ""},
		// encoding/json reads these keys into the credential fields too
		// (#859 review): every spelling is refused.
		{`{"Auth_Type":"nkey","NKEY_SEED":"SU` + tPassword + `"}`, "nkey_seed is stored in plaintext"},
		{`{"auth_type":"nkey","nkey_seed_secret_id":"0b1c","Nkey_Seed":"SU` + tPassword + `"}`, "nkey_seed is stored in plaintext"},
		{`{"nkey_ſeed":"SU` + tPassword + `"}`, "nkey_seed is stored in plaintext"},
		{`{"Password":"` + tPassword + `"}`, "password is stored in plaintext"},
		{`{"Creds":"-----BEGIN NATS USER JWT-----"}`, "creds is stored in plaintext"},
		// Two spellings: refused whichever comes last.
		{`{"Password":"` + tPassword + `","password":""}`, "password is stored in plaintext"},
		{`{"password":"","pAssword":"` + tPassword + `"}`, "password is stored in plaintext"},
		{`{"Auth_Type":"nkey","nkey_seed_secret_id":"0b1c"}`, ""},
		{`{"Auth_Type":"user_password","Username":"rh","password_secret_id":"0b1c","Password":""}`, ""},
	}
	for _, tt := range tests {
		err := plaintextCredentials([]byte(tt.block))
		if tt.want == "" {
			if err != nil {
				t.Errorf("%s: refused: %v", tt.block, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error %v, want %q", tt.block, err, tt.want)
		}
		if err != nil && strings.Contains(err.Error(), tPassword) {
			t.Errorf("error quotes the value: %v", err)
		}
	}
}

// A config logged by accident does not print its credentials.
func TestConfigLogValue(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := &NATSConfig{Servers: []string{"tls://a.example.com"}, Password: tPassword, Creds: "SUAseed" + tPassword, NKeySeed: "SU" + tPassword, Stream: tStream}
	logger.Info("x", "nats", cfg)
	logger.Info("y", "nats", *cfg)
	if strings.Contains(buf.String(), tPassword) {
		t.Fatalf("log line holds a credential: %s", buf.String())
	}
	if !strings.Contains(buf.String(), tStream) {
		t.Fatalf("log line lost the stream: %s", buf.String())
	}
}

// The envelope id is stable for one source key on one connection, and
// differs across keys and across connections.
func TestEnvelopeID(t *testing.T) {
	a := envelopeID(tConn, "evt-1")
	if a != envelopeID(tConn, "evt-1") {
		t.Error("same key, different id")
	}
	if a == envelopeID(tConn, "evt-2") {
		t.Error("different keys, same id")
	}
	if a == envelopeID("other-connection", "evt-1") {
		t.Error("two connections share an id for the same event")
	}
	if envelopeID("a", "b\x00c") == envelopeID("a\x00b", "c") {
		t.Error("the separator does not keep connection and key apart")
	}
}

func TestDurableFits(t *testing.T) {
	cfg := &NATSConfig{Durable: tDur, FilterSubject: tSubj}
	tests := []struct {
		name string
		c    jetstream.ConsumerConfig
		want string
	}{
		{"fits", jetstream.ConsumerConfig{AckPolicy: jetstream.AckExplicitPolicy, FilterSubject: tSubj}, ""},
		{"fits with filter_subjects", jetstream.ConsumerConfig{AckPolicy: jetstream.AckExplicitPolicy, FilterSubjects: []string{tSubj}}, ""},
		{"push", jetstream.ConsumerConfig{AckPolicy: jetstream.AckExplicitPolicy, DeliverSubject: "x"}, "push consumer"},
		{"ack all", jetstream.ConsumerConfig{AckPolicy: jetstream.AckAllPolicy, FilterSubject: tSubj}, "explicit ack"},
		{"no filter", jetstream.ConsumerConfig{AckPolicy: jetstream.AckExplicitPolicy}, `filters on "none"`},
		{"two filters", jetstream.ConsumerConfig{AckPolicy: jetstream.AckExplicitPolicy, FilterSubjects: []string{"a", "b"}}, `"a, b"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := durableFits(&jetstream.ConsumerInfo{Config: tt.c}, cfg)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want %q", err, tt.want)
			}
		})
	}
	// Without a configured filter any pull, explicit-ack durable fits.
	if err := durableFits(&jetstream.ConsumerInfo{Config: jetstream.ConsumerConfig{AckPolicy: jetstream.AckExplicitPolicy, FilterSubject: "x.>"}}, &NATSConfig{Durable: tDur}); err != nil {
		t.Errorf("no configured filter: %v", err)
	}
}

func TestClassifyConnect(t *testing.T) {
	tests := []struct {
		err       error
		permanent bool
	}{
		{nats.ErrAuthorization, true},
		{nats.ErrAuthExpired, true},
		{nats.ErrAuthRevoked, true},
		{fmt.Errorf("dial: %w", safeurl.ErrBlockedAddress), true},
		{nats.ErrSecureConnWanted, true},
		{nats.ErrSecureConnRequired, true},
		{nats.ErrNoServers, false},
		{errors.New("dial tcp: connection refused"), false},
	}
	for _, tt := range tests {
		if got := sdk.IsPermanent(classifyConnect(tt.err)); got != tt.permanent {
			t.Errorf("%v: permanent = %v, want %v", tt.err, got, tt.permanent)
		}
	}
}

func TestClassifyJS(t *testing.T) {
	none := &asyncErrs{}
	violated := &asyncErrs{}
	violated.handler(nil, nil, fmt.Errorf("%w: publish to $JS.API", nats.ErrPermissionViolation))
	tests := []struct {
		name      string
		err       error
		async     *asyncErrs
		permanent bool
	}{
		{"timeout", nats.ErrTimeout, none, false},
		{"timeout after a permission violation", nats.ErrTimeout, violated, true},
		{"jetstream off", jetstream.ErrJetStreamNotEnabled, none, true},
		{"consumer deleted", jetstream.ErrConsumerDeleted, none, false},
		{"bad request", &jetstream.APIError{Code: 400, ErrorCode: 10084, Description: "bad"}, none, true},
		{"unavailable", &jetstream.APIError{Code: 503, Description: "unavailable"}, none, false},
		{"already permanent", sdk.Permanent(errors.New("x")), none, true},
	}
	for _, tt := range tests {
		if got := sdk.IsPermanent(classifyJS(tt.err, tt.async, "step")); got != tt.permanent {
			t.Errorf("%s: permanent = %v, want %v", tt.name, got, tt.permanent)
		}
	}
}

// Only the two credential references are resolved; any other *_secret_id is
// dropped before resolving, so a secret cannot come back in a validation error.
func TestOnlyCredentialSecrets(t *testing.T) {
	in := []byte(`{"stream":"S","password_secret_id":"a","creds_secret_id":"b","auth_type_secret_id":"c","stream_secret_id":"d","nkey_seed_secret_id":"e","ca_cert_secret_id":"f","api_prefix_secret_id":"g"}`)
	var out map[string]any
	if err := json.Unmarshal(onlyCredentialSecrets(in), &out); err != nil {
		t.Fatal(err)
	}
	if out["password_secret_id"] != "a" || out["creds_secret_id"] != "b" || out["stream"] != "S" ||
		out["nkey_seed_secret_id"] != "e" || out["ca_cert_secret_id"] != "f" {
		t.Errorf("kept = %v", out)
	}
	if _, ok := out["auth_type_secret_id"]; ok {
		t.Errorf("auth_type_secret_id survived: %v", out)
	}
	if _, ok := out["stream_secret_id"]; ok {
		t.Errorf("stream_secret_id survived: %v", out)
	}
	if _, ok := out["api_prefix_secret_id"]; ok {
		t.Errorf("api_prefix_secret_id survived: %v", out)
	}
	if got := onlyCredentialSecrets([]byte(`"x"`)); string(got) != `"x"` {
		t.Errorf("a non-object changed: %s", got)
	}
}
