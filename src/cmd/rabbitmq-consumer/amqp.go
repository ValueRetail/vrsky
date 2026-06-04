package main

import (
	"context"
	"fmt"
	"net/url"

	amqp "github.com/rabbitmq/amqp091-go"
)

// amqpDelivery is one received message plus ack/nack closures.
type amqpDelivery struct {
	Body []byte
	ack  func() error
	nack func() error
}

// amqpSource is the minimal surface the consumer needs. The real implementation
// wraps an amqp091 connection/channel; tests inject a fake.
type amqpSource interface {
	// Next blocks until a delivery arrives or ctx is cancelled. It does NOT ack
	// — the caller acks via the returned closure only after a successful publish.
	Next(ctx context.Context) (*amqpDelivery, error)
	Close() error
}

// dialFunc opens a source for a config. Defaulted to realDial in Configure;
// tests inject a fake.
type dialFunc func(cfg *RabbitMQConfig) (amqpSource, error)

type realAMQP struct {
	conn *amqp.Connection
	ch   *amqp.Channel
	recv <-chan amqp.Delivery
}

func (r *realAMQP) Next(ctx context.Context) (*amqpDelivery, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case d, ok := <-r.recv:
		if !ok {
			return nil, fmt.Errorf("amqp delivery channel closed")
		}
		dd := d // capture for the closures
		return &amqpDelivery{
			Body: d.Body,
			ack:  func() error { return dd.Ack(false) },
			nack: func() error { return dd.Nack(false, true) }, // requeue
		}, nil
	}
}

func (r *realAMQP) Close() error {
	if r.ch != nil {
		_ = r.ch.Close()
	}
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}

// realDial connects, declares/binds the queue, and starts a manual-ack consumer
// with prefetch=1 (so we ack one message at a time, in lock-step with publish).
func realDial(cfg *RabbitMQConfig) (amqpSource, error) {
	if cfg.Queue == "" {
		return nil, fmt.Errorf("rabbitmq config needs a queue")
	}
	conn, err := amqp.Dial(buildURL(cfg))
	if err != nil {
		return nil, fmt.Errorf("amqp dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("amqp channel: %w", err)
	}

	if _, err := ch.QueueDeclare(cfg.Queue, true, false, false, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("declare queue: %w", err)
	}
	if cfg.Exchange != "" {
		etype := cfg.ExchangeType
		if etype == "" {
			etype = "topic"
		}
		if err := ch.ExchangeDeclare(cfg.Exchange, etype, true, false, false, false, nil); err != nil {
			_ = ch.Close()
			_ = conn.Close()
			return nil, fmt.Errorf("declare exchange: %w", err)
		}
		if err := ch.QueueBind(cfg.Queue, cfg.RoutingKey, cfg.Exchange, false, nil); err != nil {
			_ = ch.Close()
			_ = conn.Close()
			return nil, fmt.Errorf("bind queue: %w", err)
		}
	}
	if err := ch.Qos(1, 0, false); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("set qos: %w", err)
	}

	recv, err := ch.Consume(cfg.Queue, "", false /* autoAck */, false, false, false, nil)
	if err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("consume: %w", err)
	}
	return &realAMQP{conn: conn, ch: ch, recv: recv}, nil
}

// buildURL injects separate username/password into the AMQP URL when provided
// (so the password can come from the secrets vault rather than the URL).
func buildURL(cfg *RabbitMQConfig) string {
	if cfg.Username == "" && cfg.Password == "" {
		return cfg.URL
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return cfg.URL
	}
	u.User = url.UserPassword(cfg.Username, cfg.Password)
	return u.String()
}
