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

// fakeReader yields queued messages then blocks until ctx is cancelled (like an
// idle broker). It records which offsets were committed.
type fakeReader struct {
	mu        sync.Mutex
	msgs      [][]byte
	idx       int
	committed [][]byte
}

func (f *fakeReader) Fetch(ctx context.Context) (*fetchedMessage, error) {
	f.mu.Lock()
	if f.idx < len(f.msgs) {
		v := f.msgs[f.idx]
		f.idx++
		f.mu.Unlock()
		return &fetchedMessage{
			Value: v,
			commit: func(context.Context) error {
				f.mu.Lock()
				f.committed = append(f.committed, v)
				f.mu.Unlock()
				return nil
			},
		}, nil
	}
	f.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeReader) Close() error { return nil }

// TestKafkaConsumer_PublishThenCommit drives the consumer end-to-end with zero
// Docker: a start command makes it read its config from a mocked management DB,
// fetch a message from a fake reader, publish it onto the data stream, and
// commit the offset only after the publish (acceptance criterion #1).
func TestKafkaConsumer_PublishThenCommit(t *testing.T) {
	const (
		connID = "kafka-conn-1"
		tenant = "tenant-x"
	)

	fake := &fakeReader{msgs: [][]byte{[]byte(`{"hello":"kafka"}`)}}

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"kafka","kafka":{"brokers":["kafka:9092"],"topic":"orders","consumer_group":"vrsky","auth_type":"none"}}}]`
	mock.ExpectQuery("SELECT nodes FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &kafkaConsumer{
		newReader: func(*KafkaConfig) (kafkaReader, error) { return fake, nil },
	}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "kafka-consumer", DB: mgmtDB})

	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start: %v", err)
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if got.IntegrationID != connID {
		t.Errorf("integration id = %q, want %q", got.IntegrationID, connID)
	}
	if string(got.Payload) != `{"hello":"kafka"}` {
		t.Errorf("payload = %q", got.Payload)
	}

	// The offset must be committed only after the publish succeeded.
	harness.Eventually(t, 3*time.Second, "offset committed after publish", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.committed) == 1 && string(fake.committed[0]) == `{"hello":"kafka"}`
	})
}
