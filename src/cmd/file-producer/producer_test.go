package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// TestGetConnectionConfigs_NoFileNode is the regression guard for the disk-fill
// bug: the file-producer subscribes to ALL pipeline data, so for a connection
// with no file-output node (e.g. webhook→http) it must return zero configs and
// write nothing — not fall back to dumping every message into the default dir.
func TestGetConnectionConfigs_NoFileNode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	const connID = "conn-webhook-http"
	// A webhook→http pipeline: a consumer + an http producer, no file node.
	nodes := `[{"id":"wh","type":"consumer","config":{"type":"http"}},` +
		`{"id":"hp","type":"producer","config":{"type":"http","http":{"url":"http://x/post"}}}]`
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery("FROM connections WHERE id").
		WithArgs(connID).
		WillReturnRows(sqlmock.NewRows([]string{"name", "nodes", "edges"}).
			AddRow("Webhook HTTP", []byte(nodes), []byte(`[]`)))

	p := &fileProducer{
		db:               db,
		defaultOutputDir: "/data/output",
		configCache:      make(map[string][]*ConnectionConfig),
		configCacheTime:  make(map[string]time.Time),
		configCacheTTL:   time.Minute,
	}

	configs, err := p.getConnectionConfigs(context.Background(), connID)
	if err != nil {
		t.Fatalf("getConnectionConfigs: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("expected 0 file configs for a non-file pipeline, got %d (%+v)", len(configs), configs)
	}
	// Guard the guard: ensure it actually queried the DB (didn't short-circuit
	// or serve stale cache) — otherwise this test could pass for the wrong reason.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestFileProducer_RoundTrip proves the SDK refactor end-to-end with zero
// Docker: an envelope published into embedded JetStream flows through the SDK
// runner → fileProducer.Deliver → a file on disk, with per-connection config
// served from a mocked database.
func TestFileProducer_RoundTrip(t *testing.T) {
	outDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", outDir) // also seeds allowedRoots

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	const connID = "conn-rt-1"
	nodes := fmt.Sprintf(`[{"id":"prod1","type":"producer","config":{"type":"file","file":{"path":%q}}}]`, outDir)
	// The config cache may query more than once across the run; allow repeats.
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"name", "nodes", "edges"}).
				AddRow("Round Trip", []byte(nodes), []byte(`[]`)))
	}

	p := &fileProducer{}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "file-producer", DB: db})

	env := envelope.New()
	env.ID = "rt-env-1"
	env.IntegrationID = connID
	env.TenantID = "tenant-1"
	env.ContentType = "application/json"
	env.Payload = []byte(`{"hello":"world"}`)
	h.Publish(t, env)

	// The folder feature writes under <outDir>/<sanitized connection name>/.
	wantPath := filepath.Join(outDir, "Round Trip", "rt-env-1.json")
	harness.Eventually(t, 5*time.Second, "file written to "+wantPath, func() bool {
		_, err := os.Stat(wantPath)
		return err == nil
	})

	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != `{"hello":"world"}` {
		t.Errorf("file content = %q", string(data))
	}
}
