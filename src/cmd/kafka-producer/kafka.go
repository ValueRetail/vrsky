package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"github.com/segmentio/kafka-go/sasl/plain"
	"github.com/segmentio/kafka-go/sasl/scram"
)

// kafkaWriter is the minimal surface the producer needs. The real
// implementation wraps a kafka.Writer (RequiredAcks=RequireAll); tests inject a
// fake so Deliver runs without a broker.
type kafkaWriter interface {
	Write(ctx context.Context, key, value []byte) error
	Close() error
}

// writerFactory opens a writer for a config. Defaulted to realWriter in
// Configure; tests inject a fake.
type writerFactory func(cfg *KafkaConfig) (kafkaWriter, error)

type realKafkaWriter struct {
	w *kafka.Writer
}

func (rw *realKafkaWriter) Write(ctx context.Context, key, value []byte) error {
	return rw.w.WriteMessages(ctx, kafka.Message{Key: key, Value: value})
}

func (rw *realKafkaWriter) Close() error { return rw.w.Close() }

// realWriter builds a writer that waits for acks=all (acceptance criterion #2).
func realWriter(cfg *KafkaConfig) (kafkaWriter, error) {
	if len(cfg.Brokers) == 0 || cfg.Topic == "" {
		return nil, errors.New("kafka config incomplete (need brokers, topic)")
	}
	transport, err := buildTransport(cfg)
	if err != nil {
		return nil, err
	}
	w := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Brokers...),
		Topic:        cfg.Topic,
		Balancer:     &kafka.LeastBytes{},
		RequiredAcks: kafka.RequireAll, // acks=all
		Transport:    transport,
	}
	return &realKafkaWriter{w: w}, nil
}

// buildTransport assembles the SASL mechanism + TLS config for the writer.
func buildTransport(cfg *KafkaConfig) (*kafka.Transport, error) {
	t := &kafka.Transport{}
	mech, err := saslMechanism(cfg)
	if err != nil {
		return nil, err
	}
	t.SASL = mech
	tlsCfg, err := tlsConfig(cfg)
	if err != nil {
		return nil, err
	}
	t.TLS = tlsCfg
	return t, nil
}

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
	if cfg.AuthType == "mtls" && (cfg.ClientCert == "" || cfg.ClientKey == "") {
		return nil, errors.New("mtls requires client_cert and client_key")
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
