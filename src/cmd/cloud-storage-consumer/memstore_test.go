package main

import (
	"context"
	"sync"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// memStore is checkpoint.InMemoryStore behind a mutex, so a poller goroutine
// and the test can use it at once. Tests that run the consumer through the
// harness with a sqlmock database inject it: Configure would otherwise wire
// the done set (#538) to Postgres on the mock.
type memStore struct {
	mu    sync.Mutex
	inner *checkpoint.InMemoryStore
}

func newMemStore() *memStore { return &memStore{inner: checkpoint.NewInMemoryStore()} }

func (m *memStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *cp
	return m.inner.Save(ctx, &c)
}

func (m *memStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp, err := m.inner.Get(ctx, tenantID, connectionID, nodeID)
	if cp == nil || err != nil {
		return cp, err
	}
	c := *cp
	return &c, nil
}

func (m *memStore) Delete(ctx context.Context, tenantID, connectionID, nodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inner.Delete(ctx, tenantID, connectionID, nodeID)
}

func (m *memStore) DeleteForConnection(ctx context.Context, tenantID, connectionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inner.DeleteForConnection(ctx, tenantID, connectionID)
}
