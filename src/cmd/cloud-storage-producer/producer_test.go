package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// fakeStore is an in-memory objectstore.ObjectStore for tests — no cloud, no
// Docker.
type fakeStore struct {
	mu    sync.Mutex
	put   map[string][]byte
	putCT map[string]string
}

func (f *fakeStore) List(context.Context, string) ([]objectstore.Object, error) { return nil, nil }
func (f *fakeStore) Get(context.Context, string) ([]byte, string, error)        { return nil, "", nil }
func (f *fakeStore) Delete(context.Context, string) error                       { return nil }
func (f *fakeStore) Copy(context.Context, string, string) error                 { return nil }

func (f *fakeStore) Put(_ context.Context, key string, body []byte, ct string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.put == nil {
		f.put = map[string][]byte{}
		f.putCT = map[string]string{}
	}
	cp := make([]byte, len(body))
	copy(cp, body)
	f.put[key] = cp
	f.putCT[key] = ct
	return nil
}

// TestCloudProducer_UploadTemplatedKey drives the producer end-to-end with zero
// Docker: an envelope is published, the producer reads its config from a mocked
// management DB, renders the key template against the payload (with prefix), and
// writes the body to the fake store at the expected key.
func TestCloudProducer_UploadTemplatedKey(t *testing.T) {
	const (
		connID = "cloud-out-1"
		tenant = "tenant-x"
	)

	fake := &fakeStore{}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"p1","type":"producer","config":{"type":"cloud_storage","cloud_storage":{"provider":"s3","bucket":"b","prefix":"out","key_template":"order_{{.id}}_{{.timestamp}}.json"}}}]`
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
	}

	fixed := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	p := &cloudProducer{
		newStore: func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return fake, nil },
		now:      func() time.Time { return fixed },
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "cloud-storage-producer", DB: db})

	env := envelope.New()
	env.ID = "env-1"
	env.IntegrationID = connID
	env.TenantID = tenant
	env.ContentType = "application/json"
	env.Payload = []byte(`{"id":"42","name":"Acme"}`)
	h.Publish(t, env)

	wantKey := "out/order_42_20240102T030405Z.json"
	harness.Eventually(t, 5*time.Second, "object uploaded with templated key", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		_, ok := fake.put[wantKey]
		return ok
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := string(fake.put[wantKey]); got != `{"id":"42","name":"Acme"}` {
		t.Errorf("uploaded body = %q", got)
	}
	if got := fake.putCT[wantKey]; got != "application/json" {
		t.Errorf("content-type = %q, want application/json", got)
	}
}

// TestCloudProducer_DefaultKey verifies the default template (no template
// configured) names the object from the envelope id and falls back to an
// octet-stream content type.
func TestCloudProducer_DefaultKey(t *testing.T) {
	const (
		connID = "cloud-out-2"
		tenant = "tenant-x"
	)
	fake := &fakeStore{}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"p1","type":"producer","config":{"type":"cloud_storage","cloud_storage":{"provider":"s3","bucket":"b"}}}]`
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
	}

	p := &cloudProducer{
		newStore: func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return fake, nil },
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "cloud-storage-producer", DB: db})

	env := envelope.New()
	env.ID = "abc123"
	env.IntegrationID = connID
	env.TenantID = tenant
	env.Payload = []byte(`not-json`)
	h.Publish(t, env)

	harness.Eventually(t, 5*time.Second, "object uploaded with default key", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		_, ok := fake.put["abc123"]
		return ok
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.putCT["abc123"]; got != "application/octet-stream" {
		t.Errorf("content-type = %q, want application/octet-stream", got)
	}
}

// TestCloudProducer_EmptyEnvelopeID verifies the default key is non-empty even
// when the envelope has no ID (e.g. api-consumer) — a generated UUID is used
// instead of dropping the message.
func TestCloudProducer_EmptyEnvelopeID(t *testing.T) {
	const (
		connID = "cloud-out-3"
		tenant = "tenant-x"
	)
	fake := &fakeStore{}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"p1","type":"producer","config":{"type":"cloud_storage","cloud_storage":{"provider":"s3","bucket":"b","prefix":"out"}}}]`
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
	}

	p := &cloudProducer{
		newStore: func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return fake, nil },
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "cloud-storage-producer", DB: db})

	env := envelope.New()
	// env.ID intentionally left empty (mirrors api-consumer-sourced envelopes).
	env.IntegrationID = connID
	env.TenantID = tenant
	env.Payload = []byte(`{"no":"id-field"}`)
	h.Publish(t, env)

	harness.Eventually(t, 5*time.Second, "object written under a generated key", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.put) == 1
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	for k := range fake.put {
		if !strings.HasPrefix(k, "out/") || k == "out/" {
			t.Errorf("key = %q, want non-empty under out/", k)
		}
	}
}
