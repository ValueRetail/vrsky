package main

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// fakeSFTP is an in-memory sftpConn for tests — no SSH server, no Docker.
type fakeSFTP struct {
	mu      sync.Mutex
	files   map[string][]byte // name → contents (in the watch dir)
	removed []string
	moved   map[string]string // oldPath → newPath
	// mtimes is each file's mtime in the listing; zero when unset (#538).
	mtimes map[string]time.Time
}

func (f *fakeSFTP) List(string) ([]remoteFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]remoteFile, 0, len(f.files))
	for name, b := range f.files {
		out = append(out, remoteFile{Name: name, Size: int64(len(b)), ModTime: f.mtimes[name]})
	}
	return out, nil
}

func (f *fakeSFTP) Read(filePath string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[path_base(filePath)], nil
}

func (f *fakeSFTP) Open(filePath string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return io.NopCloser(bytes.NewReader(f.files[path_base(filePath)])), nil
}

func (f *fakeSFTP) Remove(filePath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := path_base(filePath)
	delete(f.files, name)
	f.removed = append(f.removed, name)
	return nil
}

func (f *fakeSFTP) Rename(oldPath, newPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.moved == nil {
		f.moved = map[string]string{}
	}
	f.moved[oldPath] = newPath
	delete(f.files, path_base(oldPath))
	return nil
}

func (f *fakeSFTP) MkdirAll(string) error { return nil }
func (f *fakeSFTP) Close() error          { return nil }

// path_base returns the last path element (avoids importing path in the test
// just for this).
func path_base(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// TestSFTPConsumer_FetchAndDelete drives the consumer end-to-end with zero
// Docker: a start command makes it read its config from a mocked management DB,
// fetch a file from a fake SFTP server, publish it onto the data stream, and
// delete it (after_action=delete).
func TestSFTPConsumer_FetchAndDelete(t *testing.T) {
	const (
		connID = "sftp-conn-1"
		tenant = "tenant-x"
	)

	fake := &fakeSFTP{files: map[string][]byte{"orders.json": []byte(`{"id":1}`)}}

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"sftp","sftp":{"host":"sftp","username":"u","password":"p","remote_dir":"/in","after_action":"delete","poll_interval_seconds":0}}}]`
	mock.ExpectQuery("SELECT nodes FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &sftpConsumer{
		dial:        func(*SFTPConfig) (sftpConn, error) { return fake, nil },
		checkpoints: newMemStore(),
		// The one-shot's second look (#562), without the 5 s wait.
		oneShotSettle: 50 * time.Millisecond,
	}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "sftp-consumer", DB: mgmtDB})

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

	// after_action=delete must remove the file from the server.
	harness.Eventually(t, 3*time.Second, "file deleted after ingest", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.removed) == 1 && fake.removed[0] == "orders.json"
	})
}

// TestSFTPConsumer_AfterActionMove verifies after_action=move relocates the
// file into move_dir instead of deleting it.
func TestSFTPConsumer_AfterActionMove(t *testing.T) {
	const (
		connID = "sftp-conn-2"
		tenant = "tenant-x"
	)
	fake := &fakeSFTP{files: map[string][]byte{"data.csv": []byte("a,b\n1,2\n")}}

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"sftp","sftp":{"host":"sftp","username":"u","password":"p","remote_dir":"/in","after_action":"move","move_dir":"/in/processed","poll_interval_seconds":0}}}]`
	mock.ExpectQuery("SELECT nodes FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &sftpConsumer{
		dial:        func(*SFTPConfig) (sftpConn, error) { return fake, nil },
		checkpoints: newMemStore(),
		// The one-shot's second look (#562), without the 5 s wait.
		oneShotSettle: 50 * time.Millisecond,
	}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "sftp-consumer", DB: mgmtDB})

	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start: %v", err)
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if got.ContentType != "text/csv" {
		t.Errorf("content-type = %q, want text/csv", got.ContentType)
	}

	harness.Eventually(t, 3*time.Second, "file moved into move_dir", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.moved["/in/data.csv"] == "/in/processed/data.csv"
	})
}
