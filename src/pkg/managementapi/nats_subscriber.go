package managementapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"

	"github.com/nats-io/nats.go"
)

// NATSSubscriber subscribes to metrics and events from NATS
type NATSSubscriber struct {
	nc            *nats.Conn
	logger        *log.Logger
	cache         *MetricsCache
	subscriptions map[string]*nats.Subscription // key: subject
	mu            sync.RWMutex
	done          chan struct{}
}

// NewNATSSubscriber creates a new NATS subscriber
func NewNATSSubscriber(nc *nats.Conn, cache *MetricsCache, logger *log.Logger) *NATSSubscriber {
	if logger == nil {
		logger = log.New(io.Discard, "", 0) // Silence logger if not provided
	}
	return &NATSSubscriber{
		nc:            nc,
		logger:        logger,
		cache:         cache,
		subscriptions: make(map[string]*nats.Subscription),
		done:          make(chan struct{}),
	}
}

// SubscribeToTenantMetrics subscribes to all metrics for a specific tenant
// Subject: vrsky.metrics.{tenantID}.>
func (s *NATSSubscriber) SubscribeToTenantMetrics(tenantID string) error {
	subject := fmt.Sprintf("vrsky.metrics.%s.>", tenantID)

	sub, err := s.nc.Subscribe(subject, func(msg *nats.Msg) {
		s.handleMetricsMessage(msg)
	})

	if err != nil {
		s.logger.Printf("Failed to subscribe to %s: %v", subject, err)
		return fmt.Errorf("failed to subscribe to tenant metrics: %w", err)
	}

	s.mu.Lock()
	s.subscriptions[subject] = sub
	s.mu.Unlock()

	s.logger.Printf("Subscribed to tenant metrics: %s", subject)
	return nil
}

// SubscribeToConnectionEvents subscribes to events for a specific connection
// Subject: vrsky.events.{tenantID}.{connectionID}
func (s *NATSSubscriber) SubscribeToConnectionEvents(tenantID, connID string) error {
	subject := fmt.Sprintf("vrsky.events.%s.%s", tenantID, connID)

	sub, err := s.nc.Subscribe(subject, func(msg *nats.Msg) {
		s.handleEventMessage(msg)
	})

	if err != nil {
		s.logger.Printf("Failed to subscribe to %s: %v", subject, err)
		return fmt.Errorf("failed to subscribe to connection events: %w", err)
	}

	s.mu.Lock()
	s.subscriptions[subject] = sub
	s.mu.Unlock()

	s.logger.Printf("Subscribed to connection events: %s", subject)
	return nil
}

// UnsubscribeFromTenantMetrics unsubscribes from tenant metrics
func (s *NATSSubscriber) UnsubscribeFromTenantMetrics(tenantID string) error {
	subject := fmt.Sprintf("vrsky.metrics.%s.>", tenantID)
	return s.unsubscribe(subject)
}

// UnsubscribeFromConnectionEvents unsubscribes from connection events
func (s *NATSSubscriber) UnsubscribeFromConnectionEvents(tenantID, connID string) error {
	subject := fmt.Sprintf("vrsky.events.%s.%s", tenantID, connID)
	return s.unsubscribe(subject)
}

// unsubscribe is a helper to unsubscribe from a subject
func (s *NATSSubscriber) unsubscribe(subject string) error {
	s.mu.Lock()
	sub, exists := s.subscriptions[subject]
	if exists {
		delete(s.subscriptions, subject)
	}
	s.mu.Unlock()

	if !exists {
		return fmt.Errorf("not subscribed to subject: %s", subject)
	}

	if err := sub.Unsubscribe(); err != nil {
		s.logger.Printf("Failed to unsubscribe from %s: %v", subject, err)
		return fmt.Errorf("failed to unsubscribe: %w", err)
	}

	s.logger.Printf("Unsubscribed from: %s", subject)
	return nil
}

// handleMetricsMessage processes incoming metrics from NATS
// Expected message format:
// Subject: vrsky.metrics.{tenantID}.{componentType}.{connectionID}
// Body: JSON-encoded MetricsPublish
func (s *NATSSubscriber) handleMetricsMessage(msg *nats.Msg) {
	parts := strings.Split(msg.Subject, ".")
	// Expected: vrsky.metrics.{tenantID}.{componentType}.{connectionID}
	if len(parts) < 5 {
		s.logger.Printf("Invalid metrics subject format: %s", msg.Subject)
		return
	}

	tenantID := parts[2]
	componentType := parts[3]
	connID := parts[4]

	var metricsMsg MetricsPublish
	if err := json.Unmarshal(msg.Data, &metricsMsg); err != nil {
		s.logger.Printf("Failed to unmarshal metrics message: %v", err)
		return
	}

	// Update cache
	_ = s.cache.UpdateOrCreateConnection(connID, tenantID, "running")

	compMetrics := &ComponentMetrics{
		ComponentType:   componentType,
		MessagesIn:      metricsMsg.MessagesIn,
		MessagesOut:     metricsMsg.MessagesOut,
		ErrorCount:      metricsMsg.ErrorCount,
		LastMessageTime: metricsMsg.LastMessageTime,
		AvgLatencyMs:    metricsMsg.AvgLatencyMs,
		Details:         metricsMsg.Details,
		LastUpdated:     metricsMsg.Timestamp,
	}

	s.cache.UpdateComponentMetrics(connID, componentType, compMetrics)

	s.logger.Printf("Updated metrics for connection %s (tenant %s): %s received %d messages",
		connID, tenantID, componentType, metricsMsg.MessagesIn)
}

// handleEventMessage processes incoming events from NATS
// Expected message format:
// Subject: vrsky.events.{tenantID}.{connectionID}
// Body: JSON-encoded ConnectionEvent
func (s *NATSSubscriber) handleEventMessage(msg *nats.Msg) {
	parts := strings.Split(msg.Subject, ".")
	// Expected: vrsky.events.{tenantID}.{connectionID}
	if len(parts) < 4 {
		s.logger.Printf("Invalid event subject format: %s", msg.Subject)
		return
	}

	tenantID := parts[2]
	connID := parts[3]

	var event ConnectionEvent
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		s.logger.Printf("Failed to unmarshal event message: %v", err)
		return
	}

	s.logger.Printf("Received event for connection %s (tenant %s): %s",
		connID, tenantID, event.EventType)

	// Update connection status in cache if it's a status event
	if event.EventType == "connection.started" {
		s.cache.SetConnectionStartTime(connID)
	} else if event.EventType == "connection.stopped" {
		s.cache.SetConnectionStopTime(connID)
	}
}

// HealthCheck returns the health status of the subscriber
func (s *NATSSubscriber) HealthCheck() map[string]interface{} {
	s.mu.RLock()
	subscriptionCount := len(s.subscriptions)
	s.mu.RUnlock()

	return map[string]interface{}{
		"nats_connected":       s.nc.IsConnected(),
		"subscriptions_active": subscriptionCount,
	}
}

// GetSubscriptionCount returns the number of active subscriptions
func (s *NATSSubscriber) GetSubscriptionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.subscriptions)
}

// Close unsubscribes from all subjects and closes the subscriber
func (s *NATSSubscriber) Close() error {
	s.mu.Lock()
	subs := make([]*nats.Subscription, 0, len(s.subscriptions))
	for _, sub := range s.subscriptions {
		subs = append(subs, sub)
	}
	s.subscriptions = make(map[string]*nats.Subscription)
	s.mu.Unlock()

	// Unsubscribe from all subscriptions
	for _, sub := range subs {
		if err := sub.Unsubscribe(); err != nil {
			s.logger.Printf("Error unsubscribing: %v", err)
		}
	}

	close(s.done)
	return nil
}
