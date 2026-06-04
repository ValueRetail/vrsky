package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"github.com/segmentio/kafka-go/sasl/plain"
	"github.com/segmentio/kafka-go/sasl/scram"
)

// fetchedMessage is one Kafka record plus a closure that commits its offset.
// Decoupling commit into a closure keeps the kafkaReader interface free of
// kafka-go types, so tests can supply a fake.
type fetchedMessage struct {
	Key    []byte
	Value  []byte
	commit func(ctx context.Context) error
}

// kafkaReader is the minimal surface the consumer needs. The real
// implementation wraps a kafka.Reader (consumer group); tests inject a fake.
type kafkaReader interface {
	// Fetch blocks until a message is available or ctx is cancelled. It does
	// NOT commit — the caller commits via the returned closure only after a
	// successful publish.
	Fetch(ctx context.Context) (*fetchedMessage, error)
	Close() error
}

// readerFactory opens a reader for a config. Defaulted to realReader in
// Configure; tests inject a fake.
type readerFactory func(cfg *KafkaConfig) (kafkaReader, error)

type realKafkaReader struct {
	r *kafka.Reader
}

func (rk *realKafkaReader) Fetch(ctx context.Context) (*fetchedMessage, error) {
	m, err := rk.r.FetchMessage(ctx)
	if err != nil {
		return nil, err
	}
	return &fetchedMessage{
		Key:    m.Key,
		Value:  m.Value,
		commit: func(c context.Context) error { return rk.r.CommitMessages(c, m) },
	}, nil
}

func (rk *realKafkaReader) Close() error { return rk.r.Close() }

// realReader builds a consumer-group reader with the configured auth.
func realReader(cfg *KafkaConfig) (kafkaReader, error) {
	if len(cfg.Brokers) == 0 || cfg.Topic == "" || cfg.ConsumerGroup == "" {
		return nil, errors.New("kafka config incomplete (need brokers, topic, consumer_group)")
	}
	dialer, err := buildDialer(cfg)
	if err != nil {
		return nil, err
	}
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: cfg.Brokers,
		Topic:   cfg.Topic,
		GroupID: cfg.ConsumerGroup,
		Dialer:  dialer,
		// CommitInterval 0 → commits are synchronous and explicit (we call
		// CommitMessages ourselves only after a successful publish).
	})
	return &realKafkaReader{r: r}, nil
}

// buildDialer assembles the SASL mechanism + TLS config from KafkaConfig.
func buildDialer(cfg *KafkaConfig) (*kafka.Dialer, error) {
	d := &kafka.Dialer{Timeout: 10 * time.Second, DualStack: true}

	mech, err := saslMechanism(cfg)
	if err != nil {
		return nil, err
	}
	d.SASLMechanism = mech

	tlsCfg, err := tlsConfig(cfg)
	if err != nil {
		return nil, err
	}
	d.TLS = tlsCfg
	return d, nil
}

// saslMechanism returns the SASL mechanism for the auth type, or nil for
// none/mtls (which carry no SASL).
func saslMechanism(cfg *KafkaConfig) (sasl.Mechanism, error) {
	switch cfg.AuthType {
	case "", "none", "mtls":
		return nil, nil
	case "sasl-plain":
		return plain.Mechanism{Username: cfg.Username, Password: cfg.Password}, nil
	case "sasl-scram-256":
		return scram.Mechanism(scram.SHA256, cfg.Username, cfg.Password)
	case "sasl-scram-512":
		return scram.Mechanism(scram.SHA512, cfg.Username, cfg.Password)
	default:
		return nil, fmt.Errorf("unsupported auth_type %q", cfg.AuthType)
	}
}

// tlsConfig builds a *tls.Config when TLS material is configured (mTLS, or a CA
// cert for SASL-over-TLS). Returns nil for plaintext.
func tlsConfig(cfg *KafkaConfig) (*tls.Config, error) {
	needTLS := cfg.AuthType == "mtls" || cfg.CACert != "" || (cfg.ClientCert != "" && cfg.ClientKey != "")
	if !needTLS {
		return nil, nil
	}
	t := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.CACert)) {
			return nil, errors.New("ca_cert is not valid PEM")
		}
		t.RootCAs = pool
	}
	if cfg.AuthType == "mtls" {
		if cfg.ClientCert == "" || cfg.ClientKey == "" {
			return nil, errors.New("mtls requires client_cert and client_key")
		}
	}
	if cfg.ClientCert != "" && cfg.ClientKey != "" {
		cert, err := tls.X509KeyPair([]byte(cfg.ClientCert), []byte(cfg.ClientKey))
		if err != nil {
			return nil, fmt.Errorf("client cert/key: %w", err)
		}
		t.Certificates = []tls.Certificate{cert}
	}
	return t, nil
}
