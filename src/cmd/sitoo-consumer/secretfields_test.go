package main

import (
	"bytes"
	"context"
	"io"
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
	c := &sitooConsumer{db: db, logger: slog.New(slog.NewTextHandler(&logs, nil))}
	body := `{"tenant_id":"tenant-1","api_id":"id","resource":"` + harness.LeakPlaceholder + `"}`
	w := httptest.NewRecorder()
	c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body)))
	out := w.Body.String()
	if !strings.Contains(out, `"ok":false`) || !strings.Contains(out, "sitoo.resource holds a {secret:…} placeholder") {
		t.Fatalf("answer = %s", out)
	}
	if strings.Contains(out, harness.LeakSecret) || strings.Contains(logs.String(), harness.LeakSecret) {
		t.Fatalf("the secret came back: %s / %s", out, logs.String())
	}
}

func TestGetConfig_SecretPlaceholderOutsideCredentials(t *testing.T) {
	db, mock := harness.LeakSecretsDB(t)
	mock.ExpectQuery("FROM connections").WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(`[{"id":"src","type":"consumer","config":{"type":"sitoo","sitoo":{"resource":"` + harness.LeakPlaceholder + `"}}}]`)))
	c := &sitooConsumer{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, err := c.getSitooConfig(context.Background(), "conn-1", "tenant-1")
	if err == nil || !strings.Contains(err.Error(), "sitoo.resource holds a {secret:…} placeholder") || strings.Contains(err.Error(), harness.LeakSecret) {
		t.Fatalf("err = %v", err)
	}
}

// Another source node with a placeholder is not ours to resolve, and does not
// stop our own node from loading.
func TestGetConfig_ForeignNodeDoesNotStopOurs(t *testing.T) {
	db, mock := harness.LeakSecretsDB(t)
	nodes := `[{"id":"other","type":"consumer","config":{"type":"kafka","kafka":{"topic":"` + harness.LeakPlaceholder + `"}}},` +
		`{"id":"src","type":"consumer","config":{"type":"sitoo","sitoo":{"api_id":"id"}}}]`
	mock.ExpectQuery("FROM connections").WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	c := &sitooConsumer{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, err := c.getSitooConfig(context.Background(), "conn-1", "tenant-1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
}
