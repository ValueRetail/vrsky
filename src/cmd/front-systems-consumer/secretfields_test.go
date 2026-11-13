package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A stored secret in a setting that is not a credential (#853): refused or
// dropped, and its value comes back in no answer, error or log line.

func TestGetConfig_SecretPlaceholderOutsideCredentials(t *testing.T) {
	db, mock := harness.LeakSecretsDB(t)
	mock.ExpectQuery("FROM connections").WillReturnRows(sqlmock.NewRows([]string{"nodes", "updated_at"}).AddRow([]byte(`[{"id":"src","type":"consumer","config":{"type":"front_systems","front_systems":{"resource":"`+harness.LeakPlaceholder+`"}}}]`), time.Now()))
	c := &frontSystemsConsumer{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, _, err := c.getConfig(context.Background(), "conn-1", "tenant-1")
	if err == nil || !strings.Contains(err.Error(), "front_systems.resource holds a {secret:…} placeholder") || strings.Contains(err.Error(), harness.LeakSecret) {
		t.Fatalf("err = %v", err)
	}
}

// Another source node with a placeholder is not ours to resolve, and does not
// stop our own node from loading.
func TestGetConfig_ForeignNodeDoesNotStopOurs(t *testing.T) {
	db, mock := harness.LeakSecretsDB(t)
	nodes := `[{"id":"other","type":"consumer","config":{"type":"kafka","kafka":{"topic":"` + harness.LeakPlaceholder + `"}}},` +
		`{"id":"src","type":"consumer","config":{"type":"front_systems","front_systems":{"base_url":"https://x.example.com"}}}]`
	mock.ExpectQuery("FROM connections").WillReturnRows(sqlmock.NewRows([]string{"nodes", "updated_at"}).AddRow([]byte(nodes), time.Now()))
	c := &frontSystemsConsumer{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, _, err := c.getConfig(context.Background(), "conn-1", "tenant-1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
}
