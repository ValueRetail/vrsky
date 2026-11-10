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

	"github.com/nats-io/nkeys"

	"github.com/ValueRetail/vrsky/pkg/connectorurl"
)

const (
	authNone         = "none"
	authCreds        = "creds"
	authUserPassword = "user_password"

	defaultBatchSize = 10
	maxBatchSize     = 256
)

// NATSConfig is the per-node configuration (config.nats). Password and Creds
// are filled in from password_secret_id and creds_secret_id; a stored config
// that carries them in plaintext is refused (plaintextCredentials).
type NATSConfig struct {
	Servers       []string `json:"servers"`   // nats://host:4222 or tls://host:4222
	AuthType      string   `json:"auth_type"` // none | creds | user_password
	Username      string   `json:"username"`
	Password      string   `json:"password"` // resolved from password_secret_id
	Creds         string   `json:"creds"`    // .creds file (user JWT + NKey seed), resolved from creds_secret_id
	CACert        string   `json:"ca_cert"`  // PEM, optional: trust this CA for the server certificate
	Stream        string   `json:"stream"`
	Durable       string   `json:"durable"`
	FilterSubject string   `json:"filter_subject"` // optional; used only when VRSky creates the durable, checked otherwise
	BatchSize     int      `json:"batch_size"`     // messages per pull, default 10
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
	)
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
	if c.BatchSize <= 0 {
		c.BatchSize = defaultBatchSize
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
	if c.BatchSize > maxBatchSize {
		return fmt.Errorf("batch_size must be at most %d", maxBatchSize)
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
	default:
		return fmt.Errorf("auth_type %q is not one of none, creds, user_password", c.AuthType)
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
// tenant secret on deploy (password_secret_id, creds_secret_id); a config
// written some other way with the value inline is refused rather than used.
func plaintextCredentials(block json.RawMessage) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(block, &raw); err != nil {
		return nil // not an object: the typed parse reports it
	}
	for _, field := range []string{"password", "creds"} {
		var s string
		if v, ok := raw[field]; ok && json.Unmarshal(v, &s) == nil && s != "" {
			return fmt.Errorf("%s is stored in plaintext; store it as a secret (%s_secret_id)", field, field)
		}
	}
	return nil
}

// credentialSecretIDs are the only stored-secret references a nats block may
// carry. Any other *_secret_id would resolve into a setting whose value can
// come back in an error ("auth_type %q is not one of …"), and secrets are
// write-only.
var credentialSecretIDs = map[string]bool{"password_secret_id": true, "creds_secret_id": true}

// onlyCredentialSecrets drops every *_secret_id key from block except the two
// credential ones. A block that is not an object is returned as it is.
func onlyCredentialSecrets(block json.RawMessage) json.RawMessage {
	var raw map[string]json.RawMessage
	if json.Unmarshal(block, &raw) != nil || raw == nil {
		return block
	}
	changed := false
	for k := range raw {
		if strings.HasSuffix(k, "_secret_id") && !credentialSecretIDs[k] {
			delete(raw, k)
			changed = true
		}
	}
	if !changed {
		return block
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return block
	}
	return out
}
