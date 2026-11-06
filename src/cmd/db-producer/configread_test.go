package main

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A failed config or secret read must not ack the message (#704); a
// connection that isn't ours still acks.
func TestConfigRead_FailureIsNotAcked(t *testing.T) {
	harness.RunConfigReadCases(t, harness.ConfigReadSpec{
		New: func(t *testing.T, db *sql.DB) sdk.Producer {
			return &dbProducer{
				db:              db,
				logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
				targetCache:     map[string][]*TargetConnection{},
				targetCacheTime: map[string]time.Time{},
				targetCacheTTL:  time.Minute,
			}
		},
		Columns:          []string{"nodes", "edges"},
		OwnNode:          `{"type":"database","database":{"host":"db.example.com","password_secret_id":"00000000-0000-0000-0000-000000000001"}}`,
		OwnNodeHasSecret: true,
		ForeignNode:      `{"type":"http","http":{"url":"https://example.com"},"auth_value_secret_id":"00000000-0000-0000-0000-000000000002"}`,
		Positive: func(t *testing.T, db *sql.DB) (sdk.Producer, string, func() bool) {
			targetDB, targetMock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock target: %v", err)
			}
			t.Cleanup(func() { _ = targetDB.Close() })
			targetMock.ExpectBegin()
			targetMock.ExpectQuery("information_schema.tables").
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			targetMock.ExpectExec("CREATE TABLE").WillReturnResult(sqlmock.NewResult(0, 0))
			targetMock.ExpectExec("INSERT INTO").WillReturnResult(sqlmock.NewResult(1, 1))
			targetMock.ExpectCommit()
			var mu sync.Mutex
			var dsn string
			p := &dbProducer{
				db:              db,
				logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
				targetCache:     map[string][]*TargetConnection{},
				targetCacheTime: map[string]time.Time{},
				targetCacheTTL:  time.Minute,
				eventSubs:       map[string][]chan DBProdEvent{},
				recentEvents:    map[string][]DBProdEvent{},
				openTarget: func(connStr string) (*sql.DB, error) {
					mu.Lock()
					dsn = connStr
					mu.Unlock()
					return targetDB, nil
				},
			}
			node := fmt.Sprintf(`{"type":"database","database":{"host":"db.example.com","user":"u","password_secret_id":%q,"table":"events","mode":"create_insert"}}`, harness.TestSecretID)
			return p, node, func() bool {
				mu.Lock()
				defer mu.Unlock()
				return targetMock.ExpectationsWereMet() == nil && strings.Contains(dsn, "password='"+harness.TestSecret+"'")
			}
		},
	})
}
