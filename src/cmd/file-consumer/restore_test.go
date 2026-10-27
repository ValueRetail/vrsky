package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A file watcher the database says is running starts watching again on
// boot, with no start command (plans/stable-connections.md).
func TestFileConsumer_RestoresOnBoot(t *testing.T) {
	const (
		connID = "conn-file-restored"
		tenant = "tenant-r"
	)
	// Paths resolve inside the tenant's subtree of the mounted base
	// (pkg/tenantpath), as in the other file-consumer tests.
	base := t.TempDir()
	t.Setenv("FILE_CONSUMER_BASE_DIR", base)
	in := filepath.Join(base, tenant, "inbox")
	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}
	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)

	mock.ExpectQuery("SELECT id::text, tenant_id FROM connections").WithArgs("file").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id"}).AddRow(connID, tenant))
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"file","file":{"path":"inbox"}}}]`
	mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(connID, tenant, "Files", []byte(nodes), []byte(`[]`)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE connections SET last_payload").WithArgs(sqlmock.AnyArg(), connID, tenant).WillReturnResult(sqlmock.NewResult(0, 1))

	c := &fileConsumer{}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "file-consumer", DB: mgmtDB})
	// Configure allocates the active map under c.mu, so polling it before the
	// runner gets that far is safe; no head start needed (#362). The baseline
	// is taken before the connection shows here, so the file below is new.
	harness.Eventually(t, 5*time.Second, "watcher restored from the database", func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		_, ok := c.activeConnections[connID]
		return ok
	})
	if err := os.WriteFile(filepath.Join(in, "after-restart.json"), []byte(`{"hello":"restart"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 10*time.Second)
	if got.IntegrationID != connID {
		t.Fatalf("published for %s, want %s", got.IntegrationID, connID)
	}

	// The one last_payload write is the SDK's, and it names the tenant
	// (WithArgs above); the SDK ignores that Exec's error, so only the mock's
	// bookkeeping sees a wrong statement.
	harness.Eventually(t, 3*time.Second, "db expectations met", func() bool { return mock.ExpectationsWereMet() == nil })
}
