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

type writtenMsg struct {
	key   []byte
	value []byte
}

// fakeWriter records produced messages — no broker.
type fakeWriter struct {
	mu  sync.Mutex
	got []writtenMsg
}

func (f *fakeWriter) Write(_ context.Context, key, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, writtenMsg{key: key, value: value})
	return nil
}

func (f *fakeWriter) Close() error { return nil }

// TestKafkaProducer_Produce drives the producer end-to-end with zero Docker: an
// envelope is published, the producer reads its config from a mocked management
// DB and writes the body (with the kafka_key from metadata) to the fake writer.
func TestKafkaProducer_Produce(t *testing.T) {
	const (
		connID = "kafka-out-1"
		tenant = "tenant-x"
	)
	fake := &fakeWriter{}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"p1","type":"producer","config":{"type":"kafka","kafka":{"brokers":["kafka:9092"],"topic":"events","auth_type":"none"}}}]`
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
	}

	p := &kafkaProducer{
		newWriter: func(*KafkaConfig) (kafkaWriter, error) { return fake, nil },
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "kafka-producer", DB: db})

	env := envelope.New()
	env.ID = "env-1"
	env.IntegrationID = connID
	env.TenantID = tenant
	env.Payload = []byte(`{"order":42}`)
	env.Metadata = map[string]interface{}{"kafka_key": "k-1"}
	h.Publish(t, env)

	harness.Eventually(t, 5*time.Second, "message produced to Kafka", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.got) == 1
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if string(fake.got[0].value) != `{"order":42}` {
		t.Errorf("produced value = %q", fake.got[0].value)
	}
	if string(fake.got[0].key) != "k-1" {
		t.Errorf("produced key = %q, want k-1", fake.got[0].key)
	}
}
