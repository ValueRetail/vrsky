package managementapi

import (
	"sync"
	"time"
)

// MetricsCache stores real-time metrics for connections
// Thread-safe in-memory storage with per-connection and per-component metrics
type MetricsCache struct {
	mu            sync.RWMutex
	connections   map[string]*ConnectionMetrics // key: connectionID
	listeners     []MetricsListener
	listenersMu   sync.RWMutex
	retentionTime time.Duration
	cleanupTicker *time.Ticker
	done          chan struct{}
}

// ConnectionMetrics contains aggregated metrics for a single connection
type ConnectionMetrics struct {
	ConnectionID     string                       `json:"connection_id"`
	TenantID         string                       `json:"tenant_id"`
	Status           string                       `json:"status"`     // running, stopped, error
	Components       map[string]*ComponentMetrics `json:"components"` // key: componentType (consumer, converter, filter, producer)
	TotalMessagesIn  int64                        `json:"total_messages_in"`
	TotalMessagesOut int64                        `json:"total_messages_out"`
	TotalErrors      int64                        `json:"total_errors"`
	LastUpdated      time.Time                    `json:"last_updated"`
	StartTime        *time.Time                   `json:"start_time"`
	StopTime         *time.Time                   `json:"stop_time"`
}

// ComponentMetrics contains metrics for a single component in a pipeline
type ComponentMetrics struct {
	ComponentType   string                 `json:"component_type"` // consumer, converter, filter, producer
	MessagesIn      int64                  `json:"messages_in"`
	MessagesOut     int64                  `json:"messages_out"`
	ErrorCount      int64                  `json:"error_count"`
	SuccessCount    int64                  `json:"success_count"`
	LastMessageTime time.Time              `json:"last_message_time"`
	AvgLatencyMs    float64                `json:"avg_latency_ms"`
	MaxLatencyMs    float64                `json:"max_latency_ms"`
	MinLatencyMs    float64                `json:"min_latency_ms"`
	Details         map[string]interface{} `json:"details,omitempty"`
	LastUpdated     time.Time              `json:"last_updated"`
}

// MetricsListener is called when metrics are updated
type MetricsListener interface {
	OnMetricsUpdate(connID string, metrics *ConnectionMetrics)
}

// ListenerFunc is a function adapter for MetricsListener
type ListenerFunc func(connID string, metrics *ConnectionMetrics)

func (f ListenerFunc) OnMetricsUpdate(connID string, metrics *ConnectionMetrics) {
	f(connID, metrics)
}

// NewMetricsCache creates a new metrics cache with specified retention time
func NewMetricsCache(retentionTime time.Duration) *MetricsCache {
	mc := &MetricsCache{
		connections:   make(map[string]*ConnectionMetrics),
		retentionTime: retentionTime,
		listeners:     make([]MetricsListener, 0),
		done:          make(chan struct{}),
	}

	// Start cleanup goroutine
	mc.cleanupTicker = time.NewTicker(retentionTime / 2)
	go mc.cleanupLoop()

	return mc
}

// UpdateOrCreateConnection updates or creates connection metrics
func (mc *MetricsCache) UpdateOrCreateConnection(connID, tenantID, status string) *ConnectionMetrics {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	cm, exists := mc.connections[connID]
	if !exists {
		cm = &ConnectionMetrics{
			ConnectionID: connID,
			TenantID:     tenantID,
			Status:       status,
			Components:   make(map[string]*ComponentMetrics),
			LastUpdated:  time.Now().UTC(),
		}
		mc.connections[connID] = cm
	} else {
		cm.Status = status
		cm.LastUpdated = time.Now().UTC()
	}

	// Notify listeners
	mc.notifyListeners(connID, cm)
	return cm
}

// GetConnectionMetrics retrieves metrics for a specific connection
func (mc *MetricsCache) GetConnectionMetrics(connID string) *ConnectionMetrics {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	return mc.connections[connID]
}

// GetAllMetrics returns all connection metrics
func (mc *MetricsCache) GetAllMetrics() map[string]*ConnectionMetrics {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	// Return a copy
	result := make(map[string]*ConnectionMetrics)
	for k, v := range mc.connections {
		result[k] = v
	}
	return result
}

// UpdateComponentMetrics updates metrics for a specific component
func (mc *MetricsCache) UpdateComponentMetrics(connID string, componentType string, update *ComponentMetrics) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	cm, exists := mc.connections[connID]
	if !exists {
		return // Connection not found
	}

	if cm.Components == nil {
		cm.Components = make(map[string]*ComponentMetrics)
	}

	// Merge with existing metrics
	existing, hasExisting := cm.Components[componentType]
	if hasExisting {
		update.SuccessCount = update.MessagesOut - existing.ErrorCount
		if update.AvgLatencyMs == 0 && existing.AvgLatencyMs > 0 {
			update.AvgLatencyMs = existing.AvgLatencyMs
		}
		if update.MaxLatencyMs == 0 && existing.MaxLatencyMs > 0 {
			update.MaxLatencyMs = existing.MaxLatencyMs
		}
		if update.MinLatencyMs == 0 && existing.MinLatencyMs > 0 {
			update.MinLatencyMs = existing.MinLatencyMs
		}
	}

	cm.Components[componentType] = update
	cm.LastUpdated = time.Now().UTC()

	// Update aggregated totals
	cm.TotalMessagesIn = 0
	cm.TotalMessagesOut = 0
	cm.TotalErrors = 0

	for _, comp := range cm.Components {
		cm.TotalMessagesIn += comp.MessagesIn
		cm.TotalMessagesOut += comp.MessagesOut
		cm.TotalErrors += comp.ErrorCount
	}

	// Notify listeners
	mc.notifyListeners(connID, cm)
}

// AddListener registers a listener for metrics updates
func (mc *MetricsCache) AddListener(listener MetricsListener) {
	mc.listenersMu.Lock()
	defer mc.listenersMu.Unlock()

	mc.listeners = append(mc.listeners, listener)
}

// RemoveListener unregisters a listener
func (mc *MetricsCache) RemoveListener(listener MetricsListener) {
	mc.listenersMu.Lock()
	defer mc.listenersMu.Unlock()

	for i, l := range mc.listeners {
		if l == listener {
			mc.listeners = append(mc.listeners[:i], mc.listeners[i+1:]...)
			return
		}
	}
}

// notifyListeners notifies all registered listeners about metrics update
// Uses a single dispatcher goroutine to avoid unbounded goroutine growth
func (mc *MetricsCache) notifyListeners(connID string, metrics *ConnectionMetrics) {
	mc.listenersMu.RLock()
	listeners := make([]MetricsListener, len(mc.listeners))
	copy(listeners, mc.listeners)
	mc.listenersMu.RUnlock()

	// Dispatch notifications in a single goroutine to avoid unbounded growth
	// under high-frequency metric updates
	if len(listeners) > 0 {
		go func(listenersCopy []MetricsListener, id string, m *ConnectionMetrics) {
			for _, listener := range listenersCopy {
				listener.OnMetricsUpdate(id, m)
			}
		}(listeners, connID, metrics)
	}
}

// SetConnectionStartTime records when a connection started
func (mc *MetricsCache) SetConnectionStartTime(connID string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if cm, exists := mc.connections[connID]; exists {
		now := time.Now().UTC()
		cm.StartTime = &now
		cm.LastUpdated = time.Now().UTC()
	}
}

// SetConnectionStopTime records when a connection stopped
func (mc *MetricsCache) SetConnectionStopTime(connID string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if cm, exists := mc.connections[connID]; exists {
		now := time.Now().UTC()
		cm.StopTime = &now
		cm.LastUpdated = time.Now().UTC()
	}
}

// DeleteConnection removes metrics for a connection
func (mc *MetricsCache) DeleteConnection(connID string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	delete(mc.connections, connID)
}

// cleanupLoop periodically removes stale metrics
func (mc *MetricsCache) cleanupLoop() {
	for {
		select {
		case <-mc.cleanupTicker.C:
			mc.cleanupStaleMetrics()
		case <-mc.done:
			mc.cleanupTicker.Stop()
			return
		}
	}
}

// cleanupStaleMetrics removes metrics that haven't been updated recently
func (mc *MetricsCache) cleanupStaleMetrics() {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	now := time.Now().UTC()
	cutoff := now.Add(-mc.retentionTime)

	for connID, cm := range mc.connections {
		if cm.LastUpdated.Before(cutoff) {
			delete(mc.connections, connID)
		}
	}
}

// Close stops the metrics cache cleanup loop
func (mc *MetricsCache) Close() error {
	close(mc.done)
	return nil
}

// GetStats returns cache statistics
func (mc *MetricsCache) GetStats() map[string]interface{} {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	return map[string]interface{}{
		"connections_tracked":    len(mc.connections),
		"listeners_registered":   len(mc.listeners),
		"retention_time_seconds": int64(mc.retentionTime.Seconds()),
	}
}
