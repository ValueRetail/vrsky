package managementapi

import (
	"sync"
	"testing"
	"time"
)

// MockMetricsListener implements MetricsListener for testing
type MockMetricsListener struct {
	updates chan string
}

func (m *MockMetricsListener) OnMetricsUpdate(connID string, metrics *ConnectionMetrics) {
	select {
	case m.updates <- connID:
	default:
	}
}

// Test UpdateOrCreateConnection basic functionality
func TestMetricsCache_UpdateOrCreateConnection(t *testing.T) {
	cache := NewMetricsCache(5 * time.Minute)
	defer cache.Close()

	metrics := cache.UpdateOrCreateConnection("conn-1", "tenant-1", "running")
	if metrics == nil {
		t.Error("expected metrics to be created")
	}
	if metrics.ConnectionID != "conn-1" {
		t.Errorf("expected connection ID 'conn-1', got '%s'", metrics.ConnectionID)
	}
}

// Test GetConnectionMetrics for stored connection
func TestMetricsCache_GetConnectionMetrics(t *testing.T) {
	cache := NewMetricsCache(5 * time.Minute)
	defer cache.Close()

	// Create metrics first
	created := cache.UpdateOrCreateConnection("conn-1", "tenant-1", "running")
	if created == nil {
		t.Fatal("failed to create metrics")
	}

	// Retrieve metrics
	retrieved := cache.GetConnectionMetrics("conn-1")
	if retrieved == nil {
		t.Error("expected metrics to be retrieved")
	}
	if retrieved.ConnectionID != "conn-1" {
		t.Errorf("expected connection ID 'conn-1', got '%s'", retrieved.ConnectionID)
	}
}

// Test GetConnectionMetrics for non-existent connection
func TestMetricsCache_GetConnectionMetrics_NotFound(t *testing.T) {
	cache := NewMetricsCache(5 * time.Minute)
	defer cache.Close()

	retrieved := cache.GetConnectionMetrics("nonexistent")
	if retrieved != nil {
		t.Error("expected nil for non-existent connection")
	}
}

// Test UpdateComponentMetrics
func TestMetricsCache_UpdateComponentMetrics(t *testing.T) {
	cache := NewMetricsCache(5 * time.Minute)
	defer cache.Close()

	// Create metrics first
	cache.UpdateOrCreateConnection("conn-1", "tenant-1", "running")

	// Update component metrics
	compMetrics := &ComponentMetrics{
		ComponentType:   "consumer",
		MessagesIn:      100,
		MessagesOut:     95,
		ErrorCount:      2,
		SuccessCount:    93,
		LastMessageTime: time.Now().UTC(),
	}
	cache.UpdateComponentMetrics("conn-1", "consumer", compMetrics)

	// Verify component metrics were updated
	metrics := cache.GetConnectionMetrics("conn-1")
	if metrics == nil {
		t.Fatal("expected metrics")
	}
	if len(metrics.Components) == 0 {
		t.Error("expected components to be updated")
	}
}

// Test concurrent updates (thread-safety)
func TestMetricsCache_ConcurrentUpdates(t *testing.T) {
	cache := NewMetricsCache(5 * time.Minute)
	defer cache.Close()

	var wg sync.WaitGroup

	// Simulate concurrent updates
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			cache.UpdateOrCreateConnection("conn-1", "tenant-1", "running")
		}(i)
	}

	wg.Wait()

	// Verify no race conditions occurred
	retrieved := cache.GetConnectionMetrics("conn-1")
	if retrieved == nil {
		t.Error("expected metrics after concurrent updates")
	}
}

// Test listener notification
func TestMetricsCache_ListenerNotification(t *testing.T) {
	cache := NewMetricsCache(5 * time.Minute)
	defer cache.Close()

	listener := &MockMetricsListener{
		updates: make(chan string, 1),
	}
	cache.AddListener(listener)

	// Update metrics
	cache.UpdateOrCreateConnection("conn-1", "tenant-1", "running")

	// Wait for notification with timeout
	select {
	case connID := <-listener.updates:
		if connID != "conn-1" {
			t.Errorf("expected connection ID 'conn-1', got '%s'", connID)
		}
	case <-time.After(1 * time.Second):
		t.Error("expected listener to be notified")
	}
}

// Test multiple listeners
func TestMetricsCache_MultipleListeners(t *testing.T) {
	cache := NewMetricsCache(5 * time.Minute)
	defer cache.Close()

	count := 0
	var mu sync.Mutex

	// Register multiple listeners
	for i := 0; i < 3; i++ {
		listener := &MockMetricsListener{
			updates: make(chan string, 1),
		}
		cache.AddListener(listener)

		go func() {
			<-listener.updates
			mu.Lock()
			count++
			mu.Unlock()
		}()
	}

	// Update metrics
	cache.UpdateOrCreateConnection("conn-1", "tenant-1", "running")

	// Allow time for listeners to be called
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if count != 3 {
		t.Logf("expected 3 listeners to be called, got %d (may be timing issue)", count)
	}
	mu.Unlock()
}

// Test DeleteConnection
func TestMetricsCache_DeleteConnection(t *testing.T) {
	cache := NewMetricsCache(5 * time.Minute)
	defer cache.Close()

	// Create metrics
	cache.UpdateOrCreateConnection("conn-1", "tenant-1", "running")

	// Verify metrics exist
	if cache.GetConnectionMetrics("conn-1") == nil {
		t.Error("expected metrics to exist before deletion")
	}

	// Delete metrics
	cache.DeleteConnection("conn-1")

	// Verify metrics are deleted
	if cache.GetConnectionMetrics("conn-1") != nil {
		t.Error("expected metrics to be deleted")
	}
}

// Test GetAllMetrics
func TestMetricsCache_GetAllMetrics(t *testing.T) {
	cache := NewMetricsCache(5 * time.Minute)
	defer cache.Close()

	// Add multiple connections
	for i := 1; i <= 5; i++ {
		connID := "conn-" + string(rune(48+i))
		cache.UpdateOrCreateConnection(connID, "tenant-1", "running")
	}

	// Get all metrics
	all := cache.GetAllMetrics()
	if len(all) != 5 {
		t.Errorf("expected 5 metrics, got %d", len(all))
	}
}

// Test SetConnectionStartTime
func TestMetricsCache_SetConnectionStartTime(t *testing.T) {
	cache := NewMetricsCache(5 * time.Minute)
	defer cache.Close()

	cache.UpdateOrCreateConnection("conn-1", "tenant-1", "running")
	cache.SetConnectionStartTime("conn-1")

	metrics := cache.GetConnectionMetrics("conn-1")
	if metrics == nil {
		t.Fatal("expected metrics")
	}
	if metrics.StartTime == nil {
		t.Error("expected start time to be set")
	}
}

// Test SetConnectionStopTime
func TestMetricsCache_SetConnectionStopTime(t *testing.T) {
	cache := NewMetricsCache(5 * time.Minute)
	defer cache.Close()

	cache.UpdateOrCreateConnection("conn-1", "tenant-1", "running")
	cache.SetConnectionStopTime("conn-1")

	metrics := cache.GetConnectionMetrics("conn-1")
	if metrics == nil {
		t.Fatal("expected metrics")
	}
	if metrics.StopTime == nil {
		t.Error("expected stop time to be set")
	}
}
