package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// TestFileConsumer_UploadRoundTrip drives the SDK-refactored file-consumer
// end-to-end with zero Docker: a connection start command on the embedded NATS
// makes it read the file-consumer node config from a mocked management DB and
// begin watching; a multipart upload to the registered /upload handler then
// publishes the file contents as an envelope onto the data stream.
func TestFileConsumer_UploadRoundTrip(t *testing.T) {
	const (
		connID = "conn-file-1"
		tenant = "tenant-x"
	)
	// The base is the mounted root; a configured path is resolved inside the
	// tenant's own subtree of it (pkg/tenantpath). Point the base at a temp dir
	// and ask for a relative directory, which is what a caller does now.
	base := t.TempDir()
	t.Setenv("FILE_CONSUMER_BASE_DIR", base)

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)

	nodes := `[{"id":"c1","type":"consumer","config":{"type":"file","file":{"path":"inbox"}}}]`
	mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(connID, tenant, "File Conn", []byte(nodes), []byte(`[]`)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE connections SET last_payload").WithArgs(sqlmock.AnyArg(), connID, tenant).WillReturnResult(sqlmock.NewResult(0, 1))
	// First start: no saved state, the empty folder is saved as done (#391).
	expectCheckpoints(mock, connID, tenant, 1, 1)

	c := &fileConsumer{}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "file-consumer", DB: mgmtDB})

	startAndWaitActive(t, h, c, connID, tenant)

	// Build a multipart upload and invoke the registered handler directly
	// (the handler publishes via the SDK-injected publish closure).
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "data.json")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(`{"hello":"file"}`)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/upload/"+connID, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	c.handleUpload()(rec, req)
	if rec.Code != 202 {
		t.Fatalf("upload status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if got.IntegrationID != connID {
		t.Errorf("integration id = %q, want %q", got.IntegrationID, connID)
	}
	if got.ContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", got.ContentType)
	}
	if string(got.Payload) != `{"hello":"file"}` {
		t.Errorf("payload = %q, want %q", got.Payload, `{"hello":"file"}`)
	}

	// The one last_payload write is the SDK's, and it names the tenant
	// (WithArgs above); the SDK ignores that Exec's error, so only the mock's
	// bookkeeping sees a wrong statement.
	harness.Eventually(t, 3*time.Second, "db expectations met", func() bool { return mock.ExpectationsWereMet() == nil })
}

// TestFileConsumer_WatchRoundTrip is the regression test for #143: a file
// dropped into the watched directory (not uploaded over HTTP) must be read and
// published into the pipeline. Previously the watcher only emitted a UI event.
func TestFileConsumer_WatchRoundTrip(t *testing.T) {
	const (
		connID = "conn-file-watch"
		tenant = "tenant-w"
	)
	// The base is the mounted root; a configured path is resolved inside the
	// tenant's own subtree of it (pkg/tenantpath). Point the base at a temp dir
	// and ask for a relative directory, which is what a caller does now.
	base := t.TempDir()
	t.Setenv("FILE_CONSUMER_BASE_DIR", base)
	watchDir := filepath.Join(base, tenant, "inbox")

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)

	nodes := `[{"id":"c1","type":"consumer","config":{"type":"file","file":{"path":"inbox"}}}]`
	mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(connID, tenant, "File Conn", []byte(nodes), []byte(`[]`)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE connections SET last_payload").WithArgs(sqlmock.AnyArg(), connID, tenant).WillReturnResult(sqlmock.NewResult(0, 1))
	// Start: read, save the first-start state. Publish: re-read, save (#391).
	expectCheckpoints(mock, connID, tenant, 2, 2)

	// A file is published on the second poll that sees it unchanged (#374);
	// a short interval keeps that well inside the deadline below.
	c := &fileConsumer{pollInterval: 200 * time.Millisecond}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "file-consumer", DB: mgmtDB})

	startAndWaitActive(t, h, c, connID, tenant)

	// Drop a NEW file into the watched dir. An active connection has already
	// taken its baseline (TestStartConnection_BaselineTakenBeforeActive), so
	// the poller sees this file as added and ingests it.
	if err := os.WriteFile(filepath.Join(watchDir, "watched.json"), []byte(`{"hello":"watch"}`), 0o644); err != nil {
		t.Fatalf("write watched file: %v", err)
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 12*time.Second)
	if got.IntegrationID != connID {
		t.Errorf("integration id = %q, want %q", got.IntegrationID, connID)
	}
	if string(got.Payload) != `{"hello":"watch"}` {
		t.Errorf("payload = %q, want %q", got.Payload, `{"hello":"watch"}`)
	}

	// The one last_payload write is the SDK's, and it names the tenant
	// (WithArgs above); the SDK ignores that Exec's error, so only the mock's
	// bookkeeping sees a wrong statement.
	harness.Eventually(t, 3*time.Second, "db expectations met", func() bool { return mock.ExpectationsWereMet() == nil })
}

// startAndWaitActive publishes the start command for a connection until the
// consumer has it active. The command subscription is made in Run, after
// Configure, so a single publish after a fixed sleep could go out before
// anyone listened and be lost on a slow runner (#362). Repeating it is safe:
// NATS runs the handler for one subscription at a time, and a start for a
// connection that is already active returns before touching the database.
func startAndWaitActive(t *testing.T, h *harness.ConsumerHarness, c *fileConsumer, connID, tenant string) {
	t.Helper()
	cmd, err := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err != nil {
		t.Fatalf("marshal start command: %v", err)
	}
	const within = 10 * time.Second
	deadline := time.Now().Add(within)
	for {
		if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
			t.Fatalf("publish start command: %v", err)
		}
		retry := time.Now().Add(250 * time.Millisecond)
		for time.Now().Before(retry) {
			if c.getActiveConnection(connID) != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			t.Fatalf("connection %s not active within %s of repeated start commands", connID, within)
		}
	}
}

// Files already in the directory are never ingested; anything written after
// the connection is active must be. That only holds if the baseline is taken
// before the connection shows as active. It used to be taken later, in the
// watcher goroutine, so a file written in between was silently counted as
// already there (#362).
func TestStartConnection_BaselineTakenBeforeActive(t *testing.T) {
	const (
		connID = "conn-file-baseline"
		tenant = "tenant-b"
	)
	base := t.TempDir()
	dir := filepath.Join(base, tenant, "inbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "before.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"file","file":{"path":"inbox"}}}]`
	mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(connID, tenant, "File Conn", []byte(nodes), []byte(`[]`)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &fileConsumer{
		db:                mgmtDB,
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		baseDir:           base,
		activeConnections: make(map[string]*ActiveConnection),
	}
	c.startConnection(context.Background(), connID, tenant)

	ac := c.getActiveConnection(connID)
	if ac == nil {
		t.Fatal("connection not active after startConnection returned")
	}
	t.Cleanup(ac.Cancel)
	if want := map[string]bool{"before.json": true}; !reflect.DeepEqual(ac.knownFiles, want) {
		t.Errorf("baseline when the connection became active = %v, want %v", ac.knownFiles, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db expectations: %v", err)
	}
}

// expectCheckpoints adds the file node's checkpoint reads (no saved state) and
// saves, all for this tenant, connection and node "c1" (#391). Without them
// the checkpoint calls fail on the mock and the consumer quietly falls back to
// the first-start rule, so these tests would not run the restart path at all.
func expectCheckpoints(mock sqlmock.Sqlmock, connID, tenant string, gets, saves int) {
	for i := 0; i < gets; i++ {
		mock.ExpectQuery("FROM connection_node_checkpoints").WithArgs(tenant, connID, "c1").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}))
	}
	for i := 0; i < saves; i++ {
		mock.ExpectExec("INSERT INTO connection_node_checkpoints").
			WithArgs(tenant, connID, "c1", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
}
