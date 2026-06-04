package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// fakePublisher records published bodies — no broker.
type fakePublisher struct {
	mu   sync.Mutex
	body [][]byte
}

func (f *fakePublisher) Publish(_ context.Context, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(body))
	copy(cp, body)
	f.body = append(f.body, cp)
	return nil
}

func (f *fakePublisher) Close() error { return nil }

// TestRabbitProducer_Publish drives the producer end-to-end with zero Docker: an
// envelope is published, the producer reads its config from a mocked management
// DB and publishes the body to the fake publisher.
func TestRabbitProducer_Publish(t *testing.T) {
	const (
		connID = "rmq-out-1"
		tenant = "tenant-x"
	)
	fake := &fakePublisher{}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"p1","type":"producer","config":{"type":"rabbitmq","rabbitmq":{"url":"amqp://rabbit:5672","exchange":"events","routing_key":"orders.created"}}}]`
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
	}

	p := &rabbitProducer{
		dial: func(*RabbitMQConfig) (amqpPublisher, error) { return fake, nil },
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "rabbitmq-producer", DB: db})

	env := envelope.New()
	env.ID = "env-1"
	env.IntegrationID = connID
	env.TenantID = tenant
	env.Payload = []byte(`{"order":7}`)
	h.Publish(t, env)

	harness.Eventually(t, 5*time.Second, "message published to RabbitMQ", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.body) == 1 && string(fake.body[0]) == `{"order":7}`
	})
}
