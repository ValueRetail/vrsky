package main

import (
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// fakeSFTP is an in-memory sftpConn for tests — no SSH server, no Docker.
type fakeSFTP struct {
	mu      sync.Mutex
	written map[string][]byte // remote path → contents
	mkdirs  []string
}

func (f *fakeSFTP) MkdirAll(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mkdirs = append(f.mkdirs, dir)
	return nil
}

func (f *fakeSFTP) Write(filePath string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.written == nil {
		f.written = map[string][]byte{}
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	f.written[filePath] = cp
	return nil
}

func (f *fakeSFTP) Close() error { return nil }

// TestSFTPProducer_UploadTemplatedName drives the producer end-to-end with zero
// Docker: an envelope is published, the producer reads its config from a mocked
// management DB, renders the filename template against the payload, and uploads
// the body to the fake SFTP server at the expected path.
func TestSFTPProducer_UploadTemplatedName(t *testing.T) {
	const (
		connID = "sftp-out-1"
		tenant = "tenant-x"
	)

	fake := &fakeSFTP{}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	// Inline plaintext password (the resolver leaves non-_secret_id keys as-is),
	// remote_dir + a filename template referencing a payload field + timestamp.
	nodes := `[{"id":"p1","type":"producer","config":{"type":"sftp","sftp":{"host":"sftp","username":"u","password":"p","remote_dir":"/upload","filename_template":"order_{{.id}}_{{.timestamp}}.json"}}}]`
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
	}

	fixed := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	p := &sftpProducer{
		dial: func(*SFTPConfig) (sftpConn, error) { return fake, nil },
		now:  func() time.Time { return fixed },
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "sftp-producer", DB: db})

	env := envelope.New()
	env.ID = "env-1"
	env.IntegrationID = connID
	env.TenantID = tenant
	env.Payload = []byte(`{"id":"42","name":"Acme"}`)
	h.Publish(t, env)

	wantPath := "/upload/order_42_20240102T030405Z.json"
	harness.Eventually(t, 5*time.Second, "file uploaded with templated name", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		_, ok := fake.written[wantPath]
		return ok
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := string(fake.written[wantPath]); got != `{"id":"42","name":"Acme"}` {
		t.Errorf("uploaded body = %q", got)
	}
	if len(fake.mkdirs) == 0 || fake.mkdirs[0] != "/upload" {
		t.Errorf("expected MkdirAll(/upload), got %v", fake.mkdirs)
	}
}

// TestSFTPProducer_DefaultFilename verifies the default template (no template
// configured) names the file from the envelope id.
func TestSFTPProducer_DefaultFilename(t *testing.T) {
	const (
		connID = "sftp-out-2"
		tenant = "tenant-x"
	)
	fake := &fakeSFTP{}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"p1","type":"producer","config":{"type":"sftp","sftp":{"host":"sftp","username":"u","password":"p","remote_dir":"/out"}}}]`
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
	}

	p := &sftpProducer{
		dial: func(*SFTPConfig) (sftpConn, error) { return fake, nil },
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "sftp-producer", DB: db})

	env := envelope.New()
	env.ID = "abc123"
	env.IntegrationID = connID
	env.TenantID = tenant
	env.Payload = []byte(`not-json`)
	h.Publish(t, env)

	harness.Eventually(t, 5*time.Second, "file uploaded with default name", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		_, ok := fake.written["/out/abc123.json"]
		return ok
	})
}
