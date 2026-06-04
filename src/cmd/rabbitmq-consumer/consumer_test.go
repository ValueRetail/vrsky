package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// fakeSource yields queued bodies then blocks until ctx is cancelled. It records
// which deliveries were acked.
type fakeSource struct {
	mu     sync.Mutex
	bodies [][]byte
	idx    int
	acked  [][]byte
}

func (f *fakeSource) Next(ctx context.Context) (*amqpDelivery, error) {
	f.mu.Lock()
	if f.idx < len(f.bodies) {
		b := f.bodies[f.idx]
		f.idx++
		f.mu.Unlock()
		return &amqpDelivery{
			Body: b,
			ack: func() error {
				f.mu.Lock()
				f.acked = append(f.acked, b)
				f.mu.Unlock()
				return nil
			},
			nack: func() error { return nil },
		}, nil
	}
	f.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeSource) Close() error { return nil }

// TestRabbitConsumer_PublishThenAck drives the consumer end-to-end with zero
// Docker: a start command makes it read its config from a mocked management DB,
// receive a delivery from a fake source, publish it onto the data stream, and
// ack only afterwards (acceptance criterion #1).
func TestRabbitConsumer_PublishThenAck(t *testing.T) {
	const (
		connID = "rmq-conn-1"
		tenant = "tenant-x"
	)

	fake := &fakeSource{bodies: [][]byte{[]byte(`{"hello":"rabbit"}`)}}

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"rabbitmq","rabbitmq":{"url":"amqp://rabbit:5672","queue":"orders"}}}]`
	mock.ExpectQuery("SELECT nodes FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &rabbitConsumer{
		dial: func(*RabbitMQConfig) (amqpSource, error) { return fake, nil },
	}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "rabbitmq-consumer", DB: mgmtDB})

	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start: %v", err)
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if got.IntegrationID != connID {
		t.Errorf("integration id = %q, want %q", got.IntegrationID, connID)
	}
	if string(got.Payload) != `{"hello":"rabbit"}` {
		t.Errorf("payload = %q", got.Payload)
	}

	harness.Eventually(t, 3*time.Second, "delivery acked after publish", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.acked) == 1 && string(fake.acked[0]) == `{"hello":"rabbit"}`
	})
}
