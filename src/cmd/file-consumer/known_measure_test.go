package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// TestMeasureKnownBusyPoll measures what a busy poll costs in checkpoint
// traffic with a large done set (#426): bytes written and read, and the time
// spent in the store, per poll that publishes one new file. It is a
// measurement, not a check, so it only runs when VRSKY_MEASURE_KNOWN is set
// (to the number of names; 1 means 100 000), and it needs a real Postgres:
//
//	VRSKY_MEASURE_KNOWN=1 VRSKY_TEST_POSTGRES_URL=… go test -run TestMeasureKnownBusyPoll -v ./cmd/file-consumer/
func TestMeasureKnownBusyPoll(t *testing.T) {
	n := measureNames(t)
	e := newPGRestartEnv(t)
	tenant := e.tenant()
	connID := uuid.New().String()
	e.connection(connID, tenant, "inbox")
	store := &meteredStore{inner: checkpoint.NewPostgresStore(e.db)}
	r := newRestartRig(t, t.TempDir(), connID, tenant, store)
	r.db = e.db

	if err := os.MkdirAll(r.dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		name := "order-2026-10-28-" + strconv.Itoa(1000000+i) + ".csv"
		if err := os.WriteFile(filepath.Join(r.dir(), name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.boot()
	r.poll()
	start := store.take()
	t.Logf("first start with %d names: wrote %d bytes in %d saves, read %d bytes, %s in the store",
		n, start.written, start.saves, start.read, start.took)

	const busy = 10
	var total meter
	var pollTime time.Duration
	for i := 0; i < busy; i++ {
		r.write(fmt.Sprintf("new-%03d.json", i), []byte(`{"n":1}`))
		r.poll() // first sighting
		store.take()
		began := time.Now()
		r.poll() // stable: published, saved
		pollTime += time.Since(began)
		m := store.take()
		total.add(m)
	}
	r.requireTotal("busy polls", busy)
	t.Logf("per busy poll (mean of %d, %d names): wrote %d bytes, read %d bytes, %d saves, %d reads, %s in the store, %s for the whole poll",
		busy, n, total.written/busy, total.read/busy, total.saves/busy, total.gets/busy,
		total.took/busy, pollTime/busy)

	began := time.Now()
	r.shutdown(viaStopCommand)
	stop := store.take()
	t.Logf("stop: wrote %d bytes in %d saves, %s", stop.written, stop.saves, time.Since(began))
}

func measureNames(t *testing.T) int {
	v := os.Getenv("VRSKY_MEASURE_KNOWN")
	if v == "" {
		t.Skip("VRSKY_MEASURE_KNOWN not set: measurement only")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 1 {
		return 100000
	}
	return n
}

// meter is checkpoint traffic since the last take.
type meter struct {
	written, read int
	saves, gets   int
	took          time.Duration
}

func (m *meter) add(o meter) {
	m.written += o.written
	m.read += o.read
	m.saves += o.saves
	m.gets += o.gets
	m.took += o.took
}

// meteredStore counts the state bytes that go through a checkpoint store and
// the time spent in it.
type meteredStore struct {
	inner checkpoint.Store
	mu    sync.Mutex
	m     meter
}

func (s *meteredStore) take() meter {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.m
	s.m = meter{}
	return m
}

func (s *meteredStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	began := time.Now()
	err := s.inner.Save(ctx, cp)
	s.mu.Lock()
	s.m.saves++
	s.m.written += len(cp.State)
	s.m.took += time.Since(began)
	s.mu.Unlock()
	return err
}

func (s *meteredStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	began := time.Now()
	cp, err := s.inner.Get(ctx, tenantID, connectionID, nodeID)
	s.mu.Lock()
	s.m.gets++
	if cp != nil {
		s.m.read += len(cp.State)
	}
	s.m.took += time.Since(began)
	s.mu.Unlock()
	return cp, err
}

func (s *meteredStore) Delete(ctx context.Context, tenantID, connectionID, nodeID string) error {
	return s.inner.Delete(ctx, tenantID, connectionID, nodeID)
}

func (s *meteredStore) DeleteForConnection(ctx context.Context, tenantID, connectionID string) error {
	return s.inner.DeleteForConnection(ctx, tenantID, connectionID)
}
