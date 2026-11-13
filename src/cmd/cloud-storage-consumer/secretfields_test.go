package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A stored secret in a setting that is not a credential (#853): refused or
// dropped, and its value comes back in no answer, error or log line.

func TestSampleData_SecretPlaceholderOutsideCredentials(t *testing.T) {
	db, _ := harness.LeakSecretsDB(t)
	var logs bytes.Buffer
	c := &cloudConsumer{db: db, logger: slog.New(slog.NewTextHandler(&logs, nil))}
	body := `{"tenant_id":"tenant-1","bucket":"b","prefix":"` + harness.LeakPlaceholder + `"}`
	w := httptest.NewRecorder()
	c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body)))
	out := w.Body.String()
	if !strings.Contains(out, `"ok":false`) || !strings.Contains(out, "cloud_storage.prefix holds a {secret:…} placeholder") {
		t.Fatalf("answer = %s", out)
	}
	if strings.Contains(out, harness.LeakSecret) || strings.Contains(logs.String(), harness.LeakSecret) {
		t.Fatalf("the secret came back: %s / %s", out, logs.String())
	}
}

func TestGetConfig_SecretPlaceholderOutsideCredentials(t *testing.T) {
	db, mock := harness.LeakSecretsDB(t)
	nodes := `[{"id":"src","type":"consumer","config":{"type":"cloud_storage","cloud_storage":{"bucket":"b","prefix":"` + harness.LeakPlaceholder + `"}}}]`
	mock.ExpectQuery("FROM connections").WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	var logs bytes.Buffer
	c := &cloudConsumer{db: db, logger: slog.New(slog.NewTextHandler(&logs, nil))}
	if cfg, ok := c.getConfig("conn-1", "tenant-1"); ok || cfg != nil {
		t.Fatalf("config loaded: %+v", cfg)
	}
	if !strings.Contains(logs.String(), "cloud_storage.prefix holds a {secret:…} placeholder") || strings.Contains(logs.String(), harness.LeakSecret) {
		t.Fatalf("log = %s", logs.String())
	}
}

// The #868 review's reproduction, end to end through the sample-data handler
// with the real object store: a placeholder in the endpoint part of an Azure
// connection string was expanded, the store dialled http://127.0.0.1:1/<value>
// and the answer quoted that URL. The connection string takes a reference
// only now, so the request is refused before any store is opened.
func TestSampleData_ConnectionStringPlaceholderIsRefused(t *testing.T) {
	for _, cs := range []string{
		"BlobEndpoint=http://127.0.0.1:1/" + harness.LeakPlaceholder + ";SharedAccessSignature=sv=x",
		"DefaultEndpointsProtocol=http;AccountName=" + harness.LeakPlaceholder + ";AccountKey=a2V5;EndpointSuffix=invalid",
	} {
		db, _ := harness.LeakSecretsDB(t)
		var logs bytes.Buffer
		c := &cloudConsumer{db: db, logger: slog.New(slog.NewTextHandler(&logs, nil)), newStore: objectstore.New}
		body, _ := json.Marshal(map[string]string{"tenant_id": "tenant-1", "provider": "azure", "bucket": "b", "connection_string": cs})
		w := httptest.NewRecorder()
		c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", bytes.NewReader(body)))
		out := w.Body.String()
		if !strings.Contains(out, `"ok":false`) || !strings.Contains(out, "cloud_storage.connection_string holds a {secret:…} placeholder") {
			t.Fatalf("answer = %s", out)
		}
		if strings.Contains(out, harness.LeakSecret) || strings.Contains(logs.String(), harness.LeakSecret) {
			t.Fatalf("the secret came back: %s / %s", out, logs.String())
		}
	}
}

// The same in a stored node: the consumer does not start, and the log names
// the setting without the value.
func TestGetConfig_ConnectionStringPlaceholderIsRefused(t *testing.T) {
	db, mock := harness.LeakSecretsDB(t)
	nodes := `[{"id":"src","type":"consumer","config":{"type":"cloud_storage","cloud_storage":{"provider":"azure","bucket":"b",` +
		`"connection_string":"BlobEndpoint=http://127.0.0.1:1/` + harness.LeakPlaceholder + `;SharedAccessSignature=sv=x"}}}]`
	mock.ExpectQuery("FROM connections").WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	var logs bytes.Buffer
	c := &cloudConsumer{db: db, logger: slog.New(slog.NewTextHandler(&logs, nil))}
	if cfg, ok := c.getConfig("conn-1", "tenant-1"); ok || cfg != nil {
		t.Fatalf("config loaded: %+v", cfg)
	}
	if !strings.Contains(logs.String(), "cloud_storage.connection_string holds a {secret:…} placeholder") || strings.Contains(logs.String(), harness.LeakSecret) {
		t.Fatalf("log = %s", logs.String())
	}
}

// The reference form still works: connection_string_secret_id resolves.
func TestGetConfig_ConnectionStringReferenceResolves(t *testing.T) {
	db, mock := harness.LeakSecretsDB(t)
	nodes := `[{"id":"src","type":"consumer","config":{"type":"cloud_storage","cloud_storage":{"provider":"azure","bucket":"b",` +
		`"connection_string_secret_id":"` + harness.LeakSecretID + `"}}}]`
	mock.ExpectQuery("FROM connections").WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	c := &cloudConsumer{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cfg, ok := c.getConfig("conn-1", "tenant-1")
	if !ok || cfg.ConnectionString != harness.LeakSecret {
		t.Fatalf("connection string not resolved: ok=%v", ok)
	}
}
