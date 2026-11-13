package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// The #868 review's case on the destination: a placeholder in the endpoint
// part of an Azure connection string was expanded, the upload dialled
// http://127.0.0.1:1/<value> and the error, which becomes the DLQ copy's
// X-Vrsky-Last-Error, quoted that URL. Now the node is refused before any
// store is opened: retried, then dead-lettered, with the setting named and
// the value nowhere.
func TestDeliver_ConnectionStringPlaceholderIsRefused(t *testing.T) {
	db, mock := harness.LeakSecretsDB(t)
	node := `{"type":"cloud_storage","cloud_storage":{"provider":"azure","bucket":"b",` +
		`"connection_string":"BlobEndpoint=http://127.0.0.1:1/` + harness.LeakPlaceholder + `;SharedAccessSignature=sv=x"}}`
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("FROM connections").WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).
			AddRow([]byte(`[{"id":"dst","type":"producer","config":`+node+`}]`), []byte(`[]`)))
	}
	var logs bytes.Buffer
	opened := false
	p := &cloudProducer{
		db:        db,
		logger:    slog.New(slog.NewTextHandler(&logs, nil)),
		cache:     map[string][]*cloudTarget{},
		cacheTime: map[string]time.Time{},
		cacheTTL:  time.Minute,
		now:       time.Now,
		newStore: func(ctx context.Context, cfg *objectstore.Config) (objectstore.ObjectStore, error) {
			opened = true
			return objectstore.New(ctx, cfg)
		},
	}
	env := envelope.New()
	env.TenantID = "tenant-1"
	env.IntegrationID = "conn-1"
	env.ContentType = "application/json"
	env.Payload = []byte(`{"id":1}`)

	err := p.Deliver(context.Background(), env)
	switch {
	case err == nil:
		t.Fatal("delivered with a placeholder in the connection string")
	case sdk.IsPermanent(err):
		t.Fatalf("error = Permanent(%v): the message would be acked and lost", err)
	case !strings.Contains(err.Error(), "cloud_storage.connection_string holds a {secret:…} placeholder"):
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), harness.LeakSecret) || strings.Contains(logs.String(), harness.LeakSecret) {
		t.Fatalf("the secret came back: %v / %s", err, logs.String())
	}
	if opened {
		t.Error("a store was opened for a refused node")
	}
}
