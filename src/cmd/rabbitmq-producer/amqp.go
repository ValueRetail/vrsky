package main

import (
	"context"
	"fmt"
	"net/url"

	amqp "github.com/rabbitmq/amqp091-go"
)

// amqpPublisher is the minimal surface the producer needs. The real
// implementation wraps an amqp091 connection/channel; tests inject a fake.
type amqpPublisher interface {
	// Publish sends one persistent message to the configured exchange with the
	// routing key.
	Publish(ctx context.Context, body []byte) error
	Close() error
}

// dialFunc opens a publisher for a config. Defaulted to realDial in Configure;
// tests inject a fake.
type dialFunc func(cfg *RabbitMQConfig) (amqpPublisher, error)

type realAMQP struct {
	conn       *amqp.Connection
	ch         *amqp.Channel
	exchange   string
	routingKey string
}

func (r *realAMQP) Publish(ctx context.Context, body []byte) error {
	return r.ch.PublishWithContext(ctx, r.exchange, r.routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent, // delivery_mode=2 (acceptance criterion #2)
		Body:         body,
	})
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

// realDial connects and declares the exchange (and, for default-exchange use, a
// durable queue) so publishes land somewhere.
func realDial(cfg *RabbitMQConfig) (amqpPublisher, error) {
	conn, err := amqp.Dial(buildURL(cfg))
	if err != nil {
		return nil, fmt.Errorf("amqp dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("amqp channel: %w", err)
	}

	routingKey := cfg.RoutingKey
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
	} else if cfg.Queue != "" {
		// Default exchange: publish directly to a queue (routing key = queue name).
		if _, err := ch.QueueDeclare(cfg.Queue, true, false, false, false, nil); err != nil {
			_ = ch.Close()
			_ = conn.Close()
			return nil, fmt.Errorf("declare queue: %w", err)
		}
		routingKey = cfg.Queue
	}

	return &realAMQP{conn: conn, ch: ch, exchange: cfg.Exchange, routingKey: routingKey}, nil
}

// buildURL injects separate username/password into the AMQP URL when provided.
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
