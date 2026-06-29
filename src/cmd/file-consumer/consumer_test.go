package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	watchDir := t.TempDir()

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)

	nodes := fmt.Sprintf(`[{"id":"c1","type":"consumer","config":{"type":"file","file":{"path":%q}}}]`, watchDir)
	mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(connID, tenant, "File Conn", []byte(nodes), []byte(`[]`)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE connections SET last_payload").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &fileConsumer{}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "file-consumer", DB: mgmtDB})

	// Give the command subscription a moment to register, then start the connection.
	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start command: %v", err)
	}

	// Wait for the connection to become active (start command processed).
	harness.Eventually(t, 5*time.Second, "connection active", func() bool {
		return c.getActiveConnection(connID) != nil
	})

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
}

// TestFileConsumer_WatchRoundTrip is the regression test for #143: a file
// dropped into the watched directory (not uploaded over HTTP) must be read and
// published into the pipeline. Previously the watcher only emitted a UI event.
func TestFileConsumer_WatchRoundTrip(t *testing.T) {
	const (
		connID = "conn-file-watch"
		tenant = "tenant-w"
	)
	watchDir := t.TempDir()

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)

	nodes := fmt.Sprintf(`[{"id":"c1","type":"consumer","config":{"type":"file","file":{"path":%q}}}]`, watchDir)
	mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(connID, tenant, "File Conn", []byte(nodes), []byte(`[]`)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE connections SET last_payload").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &fileConsumer{}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "file-consumer", DB: mgmtDB})

	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start command: %v", err)
	}
	harness.Eventually(t, 5*time.Second, "connection active", func() bool {
		return c.getActiveConnection(connID) != nil
	})

	// Drop a NEW file into the watched dir AFTER the first scan so the poller
	// detects it as added and ingests it.
	if err := os.WriteFile(filepath.Join(watchDir, "watched.json"), []byte(`{"hello":"watch"}`), 0o644); err != nil {
		t.Fatalf("write watched file: %v", err)
	}

	// The watcher polls on a 5s ticker, so allow more than one interval.
	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 12*time.Second)
	if got.IntegrationID != connID {
		t.Errorf("integration id = %q, want %q", got.IntegrationID, connID)
	}
	if string(got.Payload) != `{"hello":"watch"}` {
		t.Errorf("payload = %q, want %q", got.Payload, `{"hello":"watch"}`)
	}
}
