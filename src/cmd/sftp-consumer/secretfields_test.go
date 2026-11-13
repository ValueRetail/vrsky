package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A stored secret in a setting that is not a credential (#853): refused or
// dropped, and its value comes back in no answer, error or log line.

func TestSampleData_SecretPlaceholderOutsideCredentials(t *testing.T) {
	db, _ := harness.LeakSecretsDB(t)
	var logs bytes.Buffer
	c := &sftpConsumer{db: db, logger: slog.New(slog.NewTextHandler(&logs, nil))}
	body := `{"tenant_id":"tenant-1","host":"h","username":"u","remote_dir":"` + harness.LeakPlaceholder + `"}`
	w := httptest.NewRecorder()
	c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body)))
	out := w.Body.String()
	if !strings.Contains(out, `"ok":false`) || !strings.Contains(out, "sftp.remote_dir holds a {secret:…} placeholder") {
		t.Fatalf("answer = %s", out)
	}
	if strings.Contains(out, harness.LeakSecret) || strings.Contains(logs.String(), harness.LeakSecret) {
		t.Fatalf("the secret came back: %s / %s", out, logs.String())
	}
}

func TestGetConfig_SecretPlaceholderOutsideCredentials(t *testing.T) {
	db, mock := harness.LeakSecretsDB(t)
	nodes := `[{"id":"src","type":"consumer","config":{"type":"sftp","sftp":{"host":"h","username":"u","remote_dir":"` + harness.LeakPlaceholder + `"}}}]`
	mock.ExpectQuery("FROM connections").WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	var logs bytes.Buffer
	c := &sftpConsumer{db: db, logger: slog.New(slog.NewTextHandler(&logs, nil))}
	if cfg, ok := c.getSFTPConfig("conn-1", "tenant-1"); ok || cfg != nil {
		t.Fatalf("config loaded: %+v", cfg)
	}
	if !strings.Contains(logs.String(), "sftp.remote_dir holds a {secret:…} placeholder") || strings.Contains(logs.String(), harness.LeakSecret) {
		t.Fatalf("log = %s", logs.String())
	}
}
