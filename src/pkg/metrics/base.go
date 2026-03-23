// Package metrics provides shared Prometheus metrics for VRSky components.
// All components (consumer, filter, converter, producer) use these base metrics
// with additional component-specific metrics as needed.
package metrics

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ComponentType identifies the type of component emitting metrics
type ComponentType string

const (
	TypeConsumer  ComponentType = "consumer"
	TypeFilter    ComponentType = "filter"
	TypeConverter ComponentType = "converter"
	TypeProducer  ComponentType = "producer"
)

// Labels contains the standard labels applied to all metrics
type Labels struct {
	TenantID     string
	ConnectionID string
	NodeID       string
	NodeType     string
}

// Base contains the standard metrics that all components should emit
type Base struct {
	messagesReceived   prometheus.Counter
	messagesProcessed  prometheus.Counter
	messagesFailed     *prometheus.CounterVec
	processingDuration prometheus.Histogram
	errorsTotal        *prometheus.CounterVec
	labels             Labels
	registerer         prometheus.Registerer
}

// Config holds configuration for creating component metrics
type Config struct {
	// TenantID is the tenant identifier
	TenantID string
	// ConnectionID is the pipeline/connection identifier
	ConnectionID string
	// NodeID is the unique node identifier in the pipeline
	NodeID string
	// NodeType is the component type (consumer, filter, converter, producer)
	NodeType ComponentType
	// Registerer is the Prometheus registry (defaults to DefaultRegisterer)
	Registerer prometheus.Registerer
}

// NewBase creates and registers the base metrics for a component
func NewBase(cfg Config) (*Base, error) {
	if cfg.TenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	if cfg.ConnectionID == "" {
		return nil, fmt.Errorf("connection_id is required")
	}
	if cfg.NodeID == "" {
		return nil, fmt.Errorf("node_id is required")
	}
	if cfg.NodeType == "" {
		return nil, fmt.Errorf("node_type is required")
	}

	if cfg.Registerer == nil {
		cfg.Registerer = prometheus.DefaultRegisterer
	}

	labels := Labels{
		TenantID:     cfg.TenantID,
		ConnectionID: cfg.ConnectionID,
		NodeID:       cfg.NodeID,
		NodeType:     string(cfg.NodeType),
	}

	constLabels := prometheus.Labels{
		"tenant_id":     cfg.TenantID,
		"connection_id": cfg.ConnectionID,
		"node_id":       cfg.NodeID,
		"node_type":     string(cfg.NodeType),
	}

	// Messages received counter
	messagesReceived := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   "vrsky",
		Subsystem:   string(cfg.NodeType),
		Name:        "messages_received_total",
		Help:        "Total number of messages received by the component",
		ConstLabels: constLabels,
	})

	// Messages successfully processed counter
	messagesProcessed := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   "vrsky",
		Subsystem:   string(cfg.NodeType),
		Name:        "messages_processed_total",
		Help:        "Total number of messages successfully processed",
		ConstLabels: constLabels,
	})

	// Messages failed counter (by error category)
	messagesFailed := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   "vrsky",
		Subsystem:   string(cfg.NodeType),
		Name:        "messages_failed_total",
		Help:        "Total number of failed messages by error category",
		ConstLabels: constLabels,
	}, []string{"error_category"})

	// Processing duration histogram
	processingDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:   "vrsky",
		Subsystem:   string(cfg.NodeType),
		Name:        "processing_duration_seconds",
		Help:        "Duration of message processing in seconds",
		ConstLabels: constLabels,
		Buckets:     []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0},
	})

	// General errors counter
	errorsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   "vrsky",
		Subsystem:   string(cfg.NodeType),
		Name:        "errors_total",
		Help:        "Total number of errors by type",
		ConstLabels: constLabels,
	}, []string{"error_type"})

	// Register all metrics
	collectors := []prometheus.Collector{
		messagesReceived,
		messagesProcessed,
		messagesFailed,
		processingDuration,
		errorsTotal,
	}

	for _, c := range collectors {
		if err := cfg.Registerer.Register(c); err != nil {
			// If metric is already registered, that's okay (happens in tests)
			if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
				return nil, fmt.Errorf("register metric: %w", err)
			}
		}
	}

	return &Base{
		messagesReceived:   messagesReceived,
		messagesProcessed:  messagesProcessed,
		messagesFailed:     messagesFailed,
		processingDuration: processingDuration,
		errorsTotal:        errorsTotal,
		labels:             labels,
		registerer:         cfg.Registerer,
	}, nil
}

// RecordReceived increments the messages received counter
func (m *Base) RecordReceived() {
	m.messagesReceived.Inc()
}

// RecordProcessed increments the messages processed counter
func (m *Base) RecordProcessed() {
	m.messagesProcessed.Inc()
}

// RecordFailed increments the messages failed counter for the given error category
func (m *Base) RecordFailed(errorCategory string) {
	m.messagesFailed.WithLabelValues(errorCategory).Inc()
}

// RecordDuration records the processing duration
func (m *Base) RecordDuration(duration time.Duration) {
	m.processingDuration.Observe(duration.Seconds())
}

// RecordError increments the errors counter for the given error type
func (m *Base) RecordError(errorType string) {
	m.errorsTotal.WithLabelValues(errorType).Inc()
}

// ObserveProcessing is a helper that records both duration and success/failure
func (m *Base) ObserveProcessing(start time.Time, err error) {
	duration := time.Since(start)
	m.RecordDuration(duration)

	if err != nil {
		m.RecordFailed("processing_error")
	} else {
		m.RecordProcessed()
	}
}

// Labels returns the metric labels
func (m *Base) Labels() Labels {
	return m.labels
}
