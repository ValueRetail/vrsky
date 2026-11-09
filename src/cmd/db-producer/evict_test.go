package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
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

// slowTarget is a dialer that blocks in open until released. It remembers
// every pool it returned so a test can tell which ones were closed.
type slowTarget struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	mocks   []sqlmock.Sqlmock
}

func newSlowTarget() *slowTarget {
	return &slowTarget{entered: make(chan struct{}, 8), release: make(chan struct{})}
}

func (s *slowTarget) open(string) (*sql.DB, error) {
	s.entered <- struct{}{}
	<-s.release
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		return nil, err
	}
	mock.ExpectPing()
	mock.ExpectClose()
	s.mu.Lock()
	s.mocks = append(s.mocks, mock)
	s.mu.Unlock()
	return db, nil
}

// openAndClosed returns how many pools were opened and how many of those are
// closed. Every pool expects a ping (done by connect) and then a Close, so a
// pool's expectations are all met exactly when it has been closed.
func (s *slowTarget) openAndClosed() (opened, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, mock := range s.mocks {
		if mock.ExpectationsWereMet() == nil {
			closed++
		}
	}
	return len(s.mocks), closed
}

func slowTargetProducer(connID string, tc *TargetConnection) *dbProducer {
	return &dbProducer{
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		targetCache:     map[string][]*TargetConnection{connID: {tc}},
		targetCacheTime: map[string]time.Time{connID: time.Now()},
		targetCacheTTL:  time.Minute,
	}
}

// Eviction and Stop do not wait for a connect in flight, and the pool that
// connect then produces is closed, not leaked (#768).
func TestTargetPool_EvictDuringConnectIsPromptAndLeaksNothing(t *testing.T) {
	tests := []struct {
		name  string
		evict func(p *dbProducer, connID string)
	}{
		{"evict", func(p *dbProducer, connID string) { p.evictTargetCache(connID) }},
		{"stop", func(p *dbProducer, connID string) { _ = p.Stop(context.Background()) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSlowTarget()
			tc := &TargetConnection{NodeID: "n1", Config: TargetDBConfig{Host: "db", Table: "t"}}
			p := slowTargetProducer("c", tc)

			type result struct {
				db  *sql.DB
				err error
			}
			res := make(chan result, 1)
			go func() {
				db, err := tc.pool(context.Background(), s.open)
				res <- result{db, err}
			}()
			<-s.entered // connect is in flight

			evicted := make(chan struct{})
			go func() { tt.evict(p, "c"); close(evicted) }()
			select {
			case <-evicted:
			case <-time.After(5 * time.Second):
				t.Fatal("eviction blocked behind the connect")
			}

			close(s.release)
			r := <-res
			if !errors.Is(r.err, errTargetEvicted) || r.db != nil {
				t.Fatalf("pool = %v, %v; want nil, errTargetEvicted", r.db, r.err)
			}
			if opened, closed := s.openAndClosed(); opened != 1 || closed != opened {
				t.Errorf("opened %d pools, closed %d; want every one closed", opened, closed)
			}
			if tc.DB != nil {
				t.Error("evicted target holds a pool")
			}
		})
	}
}

// Concurrent first uses of one target open one pool and all get it.
func TestTargetPool_ConcurrentFirstUseOpensOnce(t *testing.T) {
	s := newSlowTarget()
	tc := &TargetConnection{NodeID: "n1", Config: TargetDBConfig{Host: "db", Table: "t"}}
	const n = 8
	dbs := make([]*sql.DB, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dbs[i], errs[i] = tc.pool(context.Background(), s.open)
		}()
	}
	<-s.entered
	time.Sleep(20 * time.Millisecond) // let the others queue behind the connect
	close(s.release)
	wg.Wait()
	for i := range n {
		if errs[i] != nil || dbs[i] != dbs[0] {
			t.Fatalf("caller %d: db = %p (want %p), err = %v", i, dbs[i], dbs[0], errs[i])
		}
	}
	if opened, closed := s.openAndClosed(); opened != 1 || closed != 0 {
		t.Errorf("opened %d, closed %d; want 1, 0", opened, closed)
	}
	tc.close()
	if opened, closed := s.openAndClosed(); closed != opened {
		t.Errorf("after close: opened %d, closed %d", opened, closed)
	}
}

// A failed open or ping is returned, not cached, and the next call retries.
func TestTargetPool_FailedConnectIsRetried(t *testing.T) {
	tc := &TargetConnection{NodeID: "n1", Config: TargetDBConfig{Host: "db", Table: "t"}}
	var calls atomic.Int32
	open := func(string) (*sql.DB, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("refused")
		}
		db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		if err != nil {
			return nil, err
		}
		mock.ExpectPing()
		return db, nil
	}
	if _, err := tc.pool(context.Background(), open); err == nil {
		t.Fatal("first pool: want the open error")
	}
	if tc.DB != nil {
		t.Fatal("failed open was cached")
	}
	db, err := tc.pool(context.Background(), open)
	if err != nil || db == nil {
		t.Fatalf("retry: db = %v, err = %v", db, err)
	}
	tc.close()
}

// A panic in open (the subscriber recovers it and NAKs) must not leave the
// connect marked in flight: a caller already waiting on it gets
// errConnectAborted, and the next call connects. Without the cleanup every
// later call would wait forever, since the handler ctx is never cancelled.
func TestTargetPool_PanicInConnectDoesNotWedgeTarget(t *testing.T) {
	tc := &TargetConnection{NodeID: "n1", Config: TargetDBConfig{Host: "db", Table: "t"}}
	entered := make(chan struct{})
	release := make(chan struct{})
	panicky := func(string) (*sql.DB, error) {
		close(entered)
		<-release
		panic("driver bug")
	}
	var opens atomic.Int32
	healthy := func(string) (*sql.DB, error) {
		opens.Add(1)
		db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		if err != nil {
			return nil, err
		}
		mock.ExpectPing()
		return db, nil
	}

	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		_, _ = tc.pool(context.Background(), panicky)
	}()
	<-entered

	// A caller that queues behind the panicking connect. If it arrives after
	// the cleanup it connects itself, so either outcome is valid; hanging is not.
	type result struct {
		db  *sql.DB
		err error
	}
	waiter := make(chan result, 1)
	go func() {
		db, err := tc.pool(context.Background(), healthy)
		waiter <- result{db, err}
	}()
	time.Sleep(20 * time.Millisecond) // let the waiter queue behind the connect
	close(release)

	select {
	case r := <-recovered:
		if r == nil {
			t.Fatal("the panic in open did not propagate to the caller")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("panicking caller did not return")
	}
	select {
	case r := <-waiter:
		if r.err != nil && !errors.Is(r.err, errConnectAborted) || r.err == nil && r.db == nil {
			t.Fatalf("waiter: db = %v, err = %v; want a pool or errConnectAborted", r.db, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter hangs after a panic in the connect it waited on")
	}

	next := make(chan result, 1)
	go func() {
		db, err := tc.pool(context.Background(), healthy)
		next <- result{db, err}
	}()
	select {
	case r := <-next:
		if r.err != nil || r.db == nil {
			t.Fatalf("next pool: db = %v, err = %v; want a connected pool", r.db, r.err)
		}
		tc.mu.Lock()
		cached := tc.DB
		tc.mu.Unlock()
		if cached != r.db {
			t.Error("the pool connected after the panic was not cached")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("next pool call hangs after a panic in an earlier connect")
	}
	if n := opens.Load(); n != 1 {
		t.Errorf("healthy opens = %d; want 1", n)
	}
	tc.close()
}
