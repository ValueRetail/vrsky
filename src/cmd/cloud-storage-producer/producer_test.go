package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
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
func (f *fakeStore) GetStream(context.Context, string) (io.ReadCloser, string, error) {
	return io.NopCloser(bytes.NewReader(nil)), "", nil
}

func (f *fakeStore) PutStream(ctx context.Context, key string, body io.Reader, ct string) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	return f.Put(ctx, key, b, ct)
}

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

// TestCloudProducer_TemplateMissingFieldFallback verifies a key template that
// references a payload field that doesn't exist falls back to a generated key
// (and writes the object) rather than dropping the message.
func TestCloudProducer_TemplateMissingFieldFallback(t *testing.T) {
	const (
		connID = "cloud-out-4"
		tenant = "tenant-x"
	)
	fake := &fakeStore{}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	// Template references {{.id}} but the payload has no "id" field.
	nodes := `[{"id":"p1","type":"producer","config":{"type":"cloud_storage","cloud_storage":{"provider":"s3","bucket":"b","prefix":"out","key_template":"orders/{{.id}}.json"}}}]`
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
	env.ID = "env-x"
	env.IntegrationID = connID
	env.TenantID = tenant
	env.Payload = []byte(`{"name":"no-id-here"}`)
	h.Publish(t, env)

	harness.Eventually(t, 5*time.Second, "object written under fallback key (not dropped)", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.put) == 1
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	for k := range fake.put {
		if !strings.HasPrefix(k, "out/") || k == "out/" {
			t.Errorf("fallback key = %q, want non-empty under out/", k)
		}
	}
}

// Close satisfies objectstore.ObjectStore (added when Close was introduced to release backend clients).
func (f *fakeStore) Close() error { return nil }

// mediaProducer is a producer wired to a fake store, for driving upload
// directly: the naming rule is in upload, and the NATS round trip adds
// nothing to these cases.
func mediaProducer(fake *fakeStore) *cloudProducer {
	return &cloudProducer{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		newStore: func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return fake, nil },
		now:      func() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) },
	}
}

func mediaConfig() *cloudConfig {
	cfg := &cloudConfig{KeyTemplate: "orders/{{.id}}_{{.timestamp}}.json"}
	cfg.Bucket = "b"
	cfg.Prefix = "catalogue"
	return cfg
}

func pictureEnvelope(name string) *envelope.Envelope {
	env := envelope.New()
	env.ID = "env-pic"
	env.ContentType = "image/jpeg"
	env.Payload = []byte("\xff\xd8\xff")
	env.Metadata = map[string]interface{}{"filename": name}
	return env
}

func (f *fakeStore) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ks []string
	for k := range f.put {
		ks = append(ks, k)
	}
	return ks
}

// TestCloudProducer_MediaKeepsFilename: a picture travelling beside the
// records keeps its own name under the prefix — the key template names the
// records (#281). Bifrost matches pictures by `<number>.<ext>`, so a uuid or
// the records' template would make them unusable. Inline and streamed alike.
func TestCloudProducer_MediaKeepsFilename(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		t.Run(map[bool]string{false: "inline", true: "streamed"}[streamed], func(t *testing.T) {
			fake := &fakeStore{}
			p := mediaProducer(fake)
			env := pictureEnvelope("1896-S.jpg")
			var body io.Reader
			if streamed {
				body = bytes.NewReader(env.Payload)
			}
			if err := p.upload(context.Background(), mediaConfig(), env, body); err != nil {
				t.Fatalf("upload: %v", err)
			}
			if got := fake.keys(); len(got) != 1 || got[0] != "catalogue/1896-S.jpg" {
				t.Fatalf("keys = %v, want [catalogue/1896-S.jpg]", got)
			}
			if ct := fake.putCT["catalogue/1896-S.jpg"]; ct != "image/jpeg" {
				t.Errorf("content-type = %q, want image/jpeg", ct)
			}
		})
	}
}

// TestCloudProducer_MediaFilenameCannotEscapePrefix: the name comes from
// upstream metadata, so it is reduced to a base name before it touches the key.
func TestCloudProducer_MediaFilenameCannotEscapePrefix(t *testing.T) {
	for _, name := range []string{"../../x.jpg", `..\..\x.jpg`, "/etc/x.jpg", "sub/dir/x.jpg"} {
		fake := &fakeStore{}
		if err := mediaProducer(fake).upload(context.Background(), mediaConfig(), pictureEnvelope(name), nil); err != nil {
			t.Fatalf("%q: upload: %v", name, err)
		}
		if got := fake.keys(); len(got) != 1 || got[0] != "catalogue/x.jpg" {
			t.Errorf("%q: keys = %v, want [catalogue/x.jpg]", name, got)
		}
	}
}

// TestCloudProducer_NonMediaIgnoresFilename pins today's behaviour for
// records: a JSON record with a filename in its metadata still takes the key
// template, and an unnamed picture still gets the generated key.
func TestCloudProducer_NonMediaIgnoresFilename(t *testing.T) {
	fake := &fakeStore{}
	p := mediaProducer(fake)

	env := envelope.New()
	env.ID = "env-rec"
	env.ContentType = "application/json"
	env.Payload = []byte(`{"id":"42"}`)
	env.Metadata = map[string]interface{}{"filename": "export.json"}
	if err := p.upload(context.Background(), mediaConfig(), env, nil); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if got := fake.keys(); len(got) != 1 || got[0] != "catalogue/orders/42_20240102T030405Z.json" {
		t.Fatalf("record keys = %v, want the templated key", got)
	}

	fake = &fakeStore{}
	p = mediaProducer(fake)
	unnamed := pictureEnvelope("")
	cfg := mediaConfig()
	cfg.KeyTemplate = "" // default {{.uuid}}
	if err := p.upload(context.Background(), cfg, unnamed, nil); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if got := fake.keys(); len(got) != 1 || got[0] != "catalogue/env-pic" {
		t.Fatalf("unnamed picture keys = %v, want [catalogue/env-pic]", got)
	}
}
