package main

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A failed config or secret read must not ack the message (#704); a
// connection that isn't ours still acks.
func TestConfigRead_FailureIsNotAcked(t *testing.T) {
	harness.RunConfigReadCases(t, harness.ConfigReadSpec{
		New: func(t *testing.T, db *sql.DB) sdk.Producer {
			return &sftpProducer{
				db:        db,
				logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				cache:     map[string][]*sftpTarget{},
				cacheTime: map[string]time.Time{},
				cacheTTL:  time.Minute,
			}
		},
		Columns:          []string{"nodes", "edges"},
		OwnNode:          `{"type":"sftp","sftp":{"host":"sftp.example.com","password_secret_id":"00000000-0000-0000-0000-000000000001"}}`,
		OwnNodeHasSecret: true,
		PlaceholderField: "sftp.remote_dir",
		ForeignNode:      `{"type":"http","http":{"url":"https://example.com"},"auth_value_secret_id":"00000000-0000-0000-0000-000000000002"}`,
		Positive: func(t *testing.T, db *sql.DB) (sdk.Producer, string, func() bool) {
			fake := &fakeSFTP{}
			var mu sync.Mutex
			var password string
			p := &sftpProducer{
				db:        db,
				logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				cache:     map[string][]*sftpTarget{},
				cacheTime: map[string]time.Time{},
				cacheTTL:  time.Minute,
				now:       time.Now,
				dial: func(cfg *SFTPConfig) (sftpConn, error) {
					mu.Lock()
					password = cfg.Password
					mu.Unlock()
					return fake, nil
				},
			}
			node := fmt.Sprintf(`{"type":"sftp","sftp":{"host":"sftp.example.com","username":"u","password_secret_id":%q,"host_key":"pinned","remote_dir":"/upload"}}`, harness.TestSecretID)
			return p, node, func() bool {
				mu.Lock()
				defer mu.Unlock()
				fake.mu.Lock()
				defer fake.mu.Unlock()
				return len(fake.written) == 1 && password == harness.TestSecret
			}
		},
	})
}
