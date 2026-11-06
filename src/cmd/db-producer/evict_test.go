package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// A delivery holding a target from a list that is then evicted (a redeploy's
// command, on another goroutine) or replaced (TTL refresh) must not open a
// pool on it: nothing would ever close that pool. It gets a retryable error
// and the redelivery reads the current config.
func TestTargetPool_NotReopenedAfterEviction(t *testing.T) {
	tests := []struct {
		name  string
		evict func(p *dbProducer, connID string)
	}{
		{"evicted by a connection command", func(p *dbProducer, connID string) { p.evictTargetCache(connID) }},
		{"stopped", func(p *dbProducer, connID string) { _ = p.Stop(context.Background()) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opened atomic.Int32
			targetDB, _, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer targetDB.Close()
			const connID = "conn-evict"
			tc := &TargetConnection{NodeID: "n1", Config: TargetDBConfig{Host: "db", Table: "t"}}
			p := &dbProducer{
				logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
				targetCache:     map[string][]*TargetConnection{connID: {tc}},
				targetCacheTime: map[string]time.Time{connID: time.Now()},
				targetCacheTTL:  time.Minute,
			}
			open := func(string) (*sql.DB, error) { opened.Add(1); return targetDB, nil }

			tt.evict(p, connID) // between the delivery's lookup and its pool()

			db, err := tc.pool(context.Background(), open)
			if !errors.Is(err, errTargetEvicted) {
				t.Fatalf("pool after eviction: db = %v, err = %v, want errTargetEvicted", db, err)
			}
			if n := opened.Load(); n != 0 {
				t.Errorf("opened %d pools on an evicted target, want 0", n)
			}
			if tc.DB != nil {
				t.Error("evicted target holds a live pool")
			}
		})
	}
}

// The DSN a target is dialled with carries the connect timeout.
func TestTargetDSN_HasConnectTimeout(t *testing.T) {
	mgmt, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mgmt.Close()
	mock.ExpectQuery("FROM connections WHERE id").WithArgs("c1").
		WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow(
			[]byte(`[{"id":"n1","type":"producer","config":{"type":"database","database":{"host":"db","table":"t"}}}]`), []byte(`[]`)))
	p := &dbProducer{
		db:              mgmt,
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		targetCache:     map[string][]*TargetConnection{},
		targetCacheTime: map[string]time.Time{},
		targetCacheTTL:  time.Minute,
	}
	tcs, err := p.getTargetConnections(context.Background(), "c1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "connect_timeout=10"; !containsWord(tcs[0].dsn, want) {
		t.Errorf("target DSN %q has no %s", tcs[0].dsn, want)
	}
}

func containsWord(s, w string) bool {
	return slices.Contains(strings.Fields(s), w)
}
