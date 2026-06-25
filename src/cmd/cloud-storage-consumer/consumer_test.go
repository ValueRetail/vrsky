package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// fakeStore is an in-memory objectstore.ObjectStore for tests — no cloud, no
// Docker.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	deleted []string
	copied  map[string]string // src -> dst
}

func (f *fakeStore) List(_ context.Context, prefix string) ([]objectstore.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]objectstore.Object, 0, len(f.objects))
	for k, b := range f.objects {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			out = append(out, objectstore.Object{Key: k, Size: int64(len(b))})
		}
	}
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, key string) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.objects[key], "", nil
}

func (f *fakeStore) Put(_ context.Context, key string, body []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	cp := make([]byte, len(body))
	copy(cp, body)
	f.objects[key] = cp
	return nil
}

func (f *fakeStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeStore) Copy(_ context.Context, src, dst string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.copied == nil {
		f.copied = map[string]string{}
	}
	f.copied[src] = dst
	if b, ok := f.objects[src]; ok {
		f.objects[dst] = b
	}
	return nil
}

// fakeEvents is an in-memory eventSource: it delivers one message of object
// keys, then blocks until ctx is cancelled (no hot loop).
type fakeEvents struct {
	mu    sync.Mutex
	keys  []string
	sent  bool
	acked []string
}

func (f *fakeEvents) Receive(ctx context.Context) ([]eventMessage, error) {
	f.mu.Lock()
	if !f.sent {
		f.sent = true
		f.mu.Unlock()
		return []eventMessage{{objectKeys: f.keys, ackHandle: "h1"}}, nil
	}
	f.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeEvents) Ack(_ context.Context, handle string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked = append(f.acked, handle)
	return nil
}

// TestCloudConsumer_EventMode drives event-driven ingestion with zero Docker: a
// fake event source delivers one object key, the consumer fetches it from the
// fake store, publishes it, and acks the message.
func TestCloudConsumer_EventMode(t *testing.T) {
	const (
		connID = "cloud-evt-1"
		tenant = "tenant-x"
	)
	fakeObj := &fakeStore{objects: map[string][]byte{"in/evt.json": []byte(`{"e":1}`)}}
	fe := &fakeEvents{keys: []string{"in/evt.json"}}

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"cloud_storage","cloud_storage":{"provider":"s3","bucket":"b","mode":"event","event_queue_url":"http://sqs.local/q"}}}]`
	mock.ExpectQuery("SELECT nodes FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &cloudConsumer{
		newStore:  func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return fakeObj, nil },
		newEvents: func(context.Context, *cloudConfig) (eventSource, error) { return fe, nil },
	}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "cloud-storage-consumer", DB: mgmtDB})

	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start: %v", err)
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if string(got.Payload) != `{"e":1}` {
		t.Errorf("payload = %q, want %q", got.Payload, `{"e":1}`)
	}

	harness.Eventually(t, 3*time.Second, "event message acked after publish", func() bool {
		fe.mu.Lock()
		defer fe.mu.Unlock()
		return len(fe.acked) == 1 && fe.acked[0] == "h1"
	})
}

// TestParseS3Notification covers key extraction + URL-decoding and TestEvent.
func TestParseS3Notification(t *testing.T) {
	body := `{"Records":[{"s3":{"object":{"key":"in/my+file%20name.json"}}}]}`
	keys := parseS3Notification([]byte(body))
	if len(keys) != 1 || keys[0] != "in/my file name.json" {
		t.Errorf("keys = %v, want [in/my file name.json]", keys)
	}
	if got := parseS3Notification([]byte(`{"Event":"s3:TestEvent"}`)); len(got) != 0 {
		t.Errorf("TestEvent should yield no keys, got %v", got)
	}
}

// TestCloudConsumer_FetchAndDelete drives the consumer end-to-end with zero
// Docker: a start command makes it read its config from a mocked management DB,
// fetch an object from a fake store, publish it onto the data stream, and delete
// it (after_action=delete).
func TestCloudConsumer_FetchAndDelete(t *testing.T) {
	const (
		connID = "cloud-conn-1"
		tenant = "tenant-x"
	)

	fake := &fakeStore{objects: map[string][]byte{"in/orders.json": []byte(`{"id":1}`)}}

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"cloud_storage","cloud_storage":{"provider":"s3","bucket":"b","prefix":"in/","after_action":"delete","poll_interval_seconds":0}}}]`
	mock.ExpectQuery("SELECT nodes FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &cloudConsumer{
		newStore: func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return fake, nil },
	}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "cloud-storage-consumer", DB: mgmtDB})

	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start: %v", err)
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if got.IntegrationID != connID {
		t.Errorf("integration id = %q, want %q", got.IntegrationID, connID)
	}
	if string(got.Payload) != `{"id":1}` {
		t.Errorf("payload = %q, want %q", got.Payload, `{"id":1}`)
	}
	if got.ContentType != "application/json" {
		t.Errorf("content-type = %q", got.ContentType)
	}

	harness.Eventually(t, 3*time.Second, "object deleted after ingest", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.deleted) == 1 && fake.deleted[0] == "in/orders.json"
	})
}

// TestCloudConsumer_AfterActionMove verifies after_action=move copies the object
// into move_prefix and deletes the original.
func TestCloudConsumer_AfterActionMove(t *testing.T) {
	const (
		connID = "cloud-conn-2"
		tenant = "tenant-x"
	)
	fake := &fakeStore{objects: map[string][]byte{"in/data.csv": []byte("a,b\n1,2\n")}}

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"cloud_storage","cloud_storage":{"provider":"s3","bucket":"b","prefix":"in/","after_action":"move","move_prefix":"in/processed","poll_interval_seconds":0}}}]`
	mock.ExpectQuery("SELECT nodes FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &cloudConsumer{
		newStore: func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return fake, nil },
	}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "cloud-storage-consumer", DB: mgmtDB})

	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start: %v", err)
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if got.ContentType != "text/csv" {
		t.Errorf("content-type = %q, want text/csv", got.ContentType)
	}

	harness.Eventually(t, 3*time.Second, "object moved into move_prefix", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.copied["in/data.csv"] == "in/processed/data.csv" &&
			len(fake.deleted) == 1 && fake.deleted[0] == "in/data.csv"
	})
}

// Close satisfies objectstore.ObjectStore (added when Close was introduced to release backend clients).
func (f *fakeStore) Close() error { return nil }
