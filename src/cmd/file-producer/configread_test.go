package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// file-producer already retried a failed config read; it now marks the error
// sdk.ErrConfigUnavailable so the SDK logs it at Error with the connection id
// (#704). The message must still not be acked.
func TestConfigRead_FailureIsNotAcked(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	for i := 0; i < 3; i++ { // ServesConnection, Deliver, DeliverStream
		mock.ExpectQuery(`FROM connections`).WillReturnError(errors.New("pq: the database system is shutting down"))
	}
	p := &fileProducer{
		db:              db,
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		configCache:     map[string][]*ConnectionConfig{},
		configCacheTime: map[string]time.Time{},
		configCacheTTL:  time.Minute,
	}
	ctx := context.Background()
	env := envelope.New()
	env.TenantID = "tenant-1"
	env.IntegrationID = "conn-1"
	env.Payload = []byte(`{"id":1}`)

	if !p.ServesConnection(ctx, env.TenantID, env.IntegrationID) {
		t.Error("ServesConnection = false: the message would be acked and lost")
	}
	if err := p.Deliver(ctx, env); !sdk.IsConfigUnavailable(err) || sdk.IsPermanent(err) {
		t.Errorf("Deliver = %v, want a retriable error marked sdk.ErrConfigUnavailable", err)
	}
	if err := p.DeliverStream(ctx, env, strings.NewReader(`{"id":1}`)); !sdk.IsConfigUnavailable(err) || sdk.IsPermanent(err) {
		t.Errorf("DeliverStream = %v, want a retriable error marked sdk.ErrConfigUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("config reads: %v", err)
	}
}
