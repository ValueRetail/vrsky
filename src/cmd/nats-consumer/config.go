package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"

	"github.com/ValueRetail/vrsky/pkg/connectorurl"
)

const (
	authNone         = "none"
	authCreds        = "creds"
	authUserPassword = "user_password"
	authNKey         = "nkey"

	defaultBatchSize = 10
	maxBatchSize     = 256

	deliverAll         = "all"
	deliverNew         = "new"
	deliverByStartTime = "by_start_time"

	// defaultMaxWaitSeconds and maxMaxWaitSeconds bound how long one waiting
	// pull lasts; a stop can take that long to hand back what it delivers.
	defaultMaxWaitSeconds = 5
	maxMaxWaitSeconds     = 30
)

// NATSConfig is the per-node configuration (config.nats). Password, Creds and
// NKeySeed are filled in from password_secret_id, creds_secret_id and
// nkey_seed_secret_id; a stored config that carries them in plaintext is
// refused (plaintextCredentials).
type NATSConfig struct {
	Servers  []string `json:"servers"`   // nats://host:4222 or tls://host:4222
	AuthType string   `json:"auth_type"` // none | creds | user_password | nkey
	Username string   `json:"username"`
	Password string   `json:"password"`  // resolved from password_secret_id
	Creds    string   `json:"creds"`     // .creds file (user JWT + NKey seed), resolved from creds_secret_id
	NKeySeed string   `json:"nkey_seed"` // user NKey seed (SU…), resolved from nkey_seed_secret_id
	CACert   string   `json:"ca_cert"`   // PEM, optional: trust this CA for the server certificate; or ca_cert_secret_id
	// APIPrefix replaces $JS.API for an account that imports the JetStream
	// API from another account (a Retailhero partner account: JS.RH.API).
	APIPrefix string `json:"api_prefix"`
	// InboxPrefix replaces _INBOX for every reply subject, for a user that
	// may subscribe only to its own inboxes (_INBOX_vrsky_t_a).
	InboxPrefix string `json:"inbox_prefix"`
	// CreateDurable lets a start command create a missing durable; nil
	// (unset) means true, as before the setting existed. False binds only.
	CreateDurable *bool `json:"create_durable,omitempty"`
	// MaxWaitSeconds is how long a pull waits for a message when there is
	// none, default 5.
	MaxWaitSeconds int    `json:"max_wait_seconds"`
	Stream         string `json:"stream"`
	Durable        string `json:"durable"`
	FilterSubject  string `json:"filter_subject"` // optional; used only when VRSky creates the durable, checked otherwise
	BatchSize      int    `json:"batch_size"`     // most messages per pull, default 10; see pullBatch
	// DeliverPolicy is where a durable VRSky creates starts: all (default,
	// the whole stream), new (only what arrives after it exists) or
	// by_start_time (from OptStartTime, RFC 3339). Ignored for an existing
	// durable.
	DeliverPolicy string `json:"deliver_policy"`
	OptStartTime  string `json:"opt_start_time"`
}

// consumerDeliver maps DeliverPolicy to the durable's settings. validate has
// checked it.
func (c *NATSConfig) consumerDeliver() (jetstream.DeliverPolicy, *time.Time) {
	switch c.DeliverPolicy {
	case deliverNew:
		return jetstream.DeliverNewPolicy, nil
	case deliverByStartTime:
		t, _ := time.Parse(time.RFC3339, c.OptStartTime)
		return jetstream.DeliverByStartTimePolicy, &t
	}
	return jetstream.DeliverAllPolicy, nil
}

// LogValue keeps the credentials out of any log line the config reaches by
// accident.
func (c NATSConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Any("servers", c.Servers),
		slog.String("auth_type", c.AuthType),
		slog.String("stream", c.Stream),
		slog.String("durable", c.Durable),
		slog.String("filter_subject", c.FilterSubject),
		slog.String("deliver_policy", c.DeliverPolicy),
		slog.String("api_prefix", c.APIPrefix),
		slog.String("inbox_prefix", c.InboxPrefix),
		slog.Bool("create_durable", c.mayCreate()),
	)
}

// mayCreate reports whether a start command may create a missing durable.
func (c *NATSConfig) mayCreate() bool {
	return c.CreateDurable == nil || *c.CreateDurable
}

// maxWait is the longest a waiting pull lasts.
func (c *NATSConfig) maxWait() time.Duration {
	return time.Duration(c.MaxWaitSeconds) * time.Second
}

// normalise trims what an editor tends to leave around values and fills the
// defaults.
func (c *NATSConfig) normalise() {
	servers := c.Servers[:0:0]
	for _, s := range c.Servers {
		if s = strings.TrimSpace(s); s != "" {
			servers = append(servers, s)
		}
	}
	c.Servers = servers
	c.AuthType = strings.TrimSpace(c.AuthType)
	if c.AuthType == "" {
		c.AuthType = authNone
	}
	c.Username = strings.TrimSpace(c.Username)
	c.Stream = strings.TrimSpace(c.Stream)
	c.Durable = strings.TrimSpace(c.Durable)
	c.FilterSubject = strings.TrimSpace(c.FilterSubject)
	if c.BatchSize == 0 {
		c.BatchSize = defaultBatchSize
	}
	c.DeliverPolicy = strings.TrimSpace(c.DeliverPolicy)
	if c.DeliverPolicy == "" {
		c.DeliverPolicy = deliverAll
	}
	c.OptStartTime = strings.TrimSpace(c.OptStartTime)
	// nats.go adds the dot itself; one typed in would make an empty token.
	c.APIPrefix = strings.TrimSuffix(strings.TrimSpace(c.APIPrefix), ".")
	c.InboxPrefix = strings.TrimSuffix(strings.TrimSpace(c.InboxPrefix), ".")
	if c.MaxWaitSeconds == 0 {
		c.MaxWaitSeconds = defaultMaxWaitSeconds
	}
}

// validate checks the config from its values alone. Every error it returns is
// a configuration problem: retrying cannot fix it. No message quotes a
// credential.
func (c *NATSConfig) validate(allowPrivate bool) error {
	if err := connectorurl.NATS(c.Servers, allowPrivate); err != nil {
		return err
	}
	if err := checkName("stream", c.Stream); err != nil {
		return err
	}
	if err := checkName("durable", c.Durable); err != nil {
		return err
	}
	if c.FilterSubject != "" && strings.ContainsAny(c.FilterSubject, " \t\r\n") {
		return errors.New("filter_subject must not contain spaces")
	}
	// Absent or 0 means the default (normalise); a negative value is
	// refused, as the start check refuses it.
	if c.BatchSize < 0 || c.BatchSize > maxBatchSize {
		return fmt.Errorf("batch_size must be from 1 to %d", maxBatchSize)
	}
	if c.MaxWaitSeconds < 0 || c.MaxWaitSeconds > maxMaxWaitSeconds {
		return fmt.Errorf("max_wait_seconds must be from 1 to %d", maxMaxWaitSeconds)
	}
	if err := checkPrefix("api_prefix", c.APIPrefix); err != nil {
		return err
	}
	if err := checkPrefix("inbox_prefix", c.InboxPrefix); err != nil {
		return err
	}
	switch c.DeliverPolicy {
	case deliverAll, deliverNew:
		if c.OptStartTime != "" {
			return errors.New("opt_start_time is only used with deliver_policy by_start_time")
		}
	case deliverByStartTime:
		if _, err := time.Parse(time.RFC3339, c.OptStartTime); err != nil {
			return errors.New("deliver_policy by_start_time needs opt_start_time as an RFC 3339 time (2026-11-01T00:00:00Z)")
		}
	default:
		return fmt.Errorf("deliver_policy %q is not one of all, new, by_start_time", c.DeliverPolicy)
	}
	switch c.AuthType {
	case authNone:
	case authUserPassword:
		if c.Username == "" || c.Password == "" {
			return errors.New("user_password authentication needs a username and a stored password")
		}
	case authCreds:
		if c.Creds == "" {
			return errors.New("creds authentication needs a stored credentials file")
		}
		// ParseDecoratedJWT hands back undecorated input as it is, so the
		// file's own markers are what tell a .creds file from anything else.
		if !strings.Contains(c.Creds, "BEGIN NATS USER JWT") || !strings.Contains(c.Creds, "BEGIN USER NKEY SEED") {
			return errors.New("the stored credentials are not a NATS .creds file (it needs the user JWT and the NKey seed blocks)")
		}
		if _, err := nkeys.ParseDecoratedJWT([]byte(c.Creds)); err != nil {
			return errors.New("the stored credentials are not a NATS .creds file (no user JWT found)")
		}
		kp, err := nkeys.ParseDecoratedNKey([]byte(c.Creds))
		if err != nil {
			return errors.New("the stored credentials are not a NATS .creds file (no NKey seed found)")
		}
		kp.Wipe()
	case authNKey:
		if c.NKeySeed == "" {
			return errors.New("nkey authentication needs a stored NKey seed")
		}
		kp, err := userKeyPair(c.NKeySeed)
		if err != nil {
			return err
		}
		kp.Wipe()
	default:
		return fmt.Errorf("auth_type %q is not one of none, creds, user_password, nkey", c.AuthType)
	}
	if _, err := c.tlsConfig(allowPrivate); err != nil {
		return err
	}
	return nil
}

// checkName refuses a stream or durable name JetStream would not accept. The
// name goes into an API subject, so a "." or a wildcard would address
// something else.
func checkName(field, name string) error {
	if name == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(name) > 255 || strings.ContainsAny(name, " \t\r\n.*>/\\") {
		return fmt.Errorf("%s %q is not a valid JetStream name (no spaces, dots, wildcards or slashes)", field, name)
	}
	return nil
}

// userKeyPair parses a user NKey seed, raw ("SU…") or in a decorated block.
// Its errors never quote the input.
func userKeyPair(seed string) (nkeys.KeyPair, error) {
	kp, err := nkeys.ParseDecoratedNKey([]byte(seed))
	if err != nil {
		return nil, errors.New("the stored NKey seed is not a NATS NKey seed")
	}
	pub, err := kp.PublicKey()
	if err != nil || !nkeys.IsValidPublicUserKey(pub) {
		kp.Wipe()
		return nil, errors.New("the stored NKey seed is not a user seed (it must start with SU)")
	}
	return kp, nil
}

// checkPrefix refuses an API or inbox prefix that is not a plain subject: a
// wildcard would make nats.go or the server address something else, and an
// empty token is not a subject.
func checkPrefix(field, prefix string) error {
	if prefix == "" {
		return nil
	}
	if len(prefix) > 255 || strings.ContainsAny(prefix, " \t\r\n*>") {
		return fmt.Errorf("%s %q must be a subject without spaces or wildcards", field, prefix)
	}
	for _, tok := range strings.Split(prefix, ".") {
		if tok == "" {
			return fmt.Errorf("%s %q has an empty token", field, prefix)
		}
	}
	return nil
}

// tlsConfig returns the client TLS settings, or nil when the connection may be
// plain: only with the operator's dev switch, no CA and no tls:// server.
// Without the switch TLS is always required, the counterpart of the http
// producer's https-only rule.
func (c *NATSConfig) tlsConfig(allowPrivate bool) (*tls.Config, error) {
	wantTLS := !allowPrivate || c.CACert != ""
	for _, s := range c.Servers {
		if u, err := url.Parse(s); err == nil && strings.EqualFold(u.Scheme, "tls") {
			wantTLS = true
		}
	}
	if !wantTLS {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(c.CACert)) {
			return nil, errors.New("ca_cert is not a PEM certificate")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// plaintextCredentials reports a credential stored in the clear in the node's
// nats block, before secrets are resolved. The builder stores each one as a
// tenant secret on deploy (password_secret_id, creds_secret_id,
// nkey_seed_secret_id); a config written some other way with the value inline
// is refused rather than used.
//
// encoding/json matches a key to a field without regard to case, so the typed
// parse reads "Password" or "NKEY_SEED" as the credential too. Every key that
// folds to a credential field is checked (strings.EqualFold is the rule
// encoding/json uses), not only one of them: the typed parse sees the block
// re-encoded with its keys sorted, so which of two spellings wins there need
// not be the one that would win here.
func plaintextCredentials(block json.RawMessage) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(block, &raw); err != nil {
		return nil // not an object: the typed parse reports it
	}
	for _, field := range []string{"password", "creds", "nkey_seed"} {
		for key, v := range raw {
			var s string
			if strings.EqualFold(key, field) && json.Unmarshal(v, &s) == nil && s != "" {
				return fmt.Errorf("%s is stored in plaintext; store it as a secret (%s_secret_id)", field, field)
			}
		}
	}
	return nil
}
