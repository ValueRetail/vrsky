package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A stored secret in a setting that is not a credential (#853): refused or
// dropped, and its value comes back in no answer, error or log line.

func TestGetConfig_SecretPlaceholderOutsideCredentials(t *testing.T) {
	db, mock := harness.LeakSecretsDB(t)
	nodes := `[{"id":"src","type":"consumer","config":{"type":"kafka","kafka":{"topic":"t","auth_type":"` + harness.LeakPlaceholder + `"}}}]`
	mock.ExpectQuery("FROM connections").WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	var logs bytes.Buffer
	c := &kafkaConsumer{db: db, logger: slog.New(slog.NewTextHandler(&logs, nil))}
	if cfg, ok := c.getKafkaConfig("conn-1", "tenant-1"); ok || cfg != nil {
		t.Fatalf("config loaded: %+v", cfg)
	}
	if !strings.Contains(logs.String(), "kafka.auth_type holds a {secret:…} placeholder") || strings.Contains(logs.String(), harness.LeakSecret) {
		t.Fatalf("log = %s", logs.String())
	}
}
