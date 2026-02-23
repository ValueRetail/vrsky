package converter

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds all Prometheus metrics for the converter component
type Metrics struct {
	messagesReceived       prometheus.Counter
	messagesSucceeded      prometheus.Counter
	messagesFailed         *prometheus.CounterVec // Pointer to CounterVec for label tracking
	transformationDuration prometheus.Histogram
	retryAttempts          prometheus.Counter
}

// NewMetrics creates and registers all converter metrics
func NewMetrics(converterID, tenantID string) (*Metrics, error) {
	if converterID == "" || tenantID == "" {
		return nil, fmt.Errorf("converter_id and tenant_id are required")
	}

	labels := prometheus.Labels{
		"converter_id": converterID,
		"tenant_id":    tenantID,
	}

	// messagesReceived counts total messages received
	messagesReceived := prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace:   "vrsky",
			Subsystem:   "converter",
			Name:        "messages_received_total",
			Help:        "Total number of messages received by the converter",
			ConstLabels: labels,
		},
	)

	// messagesSucceeded counts successfully transformed messages
	messagesSucceeded := prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace:   "vrsky",
			Subsystem:   "converter",
			Name:        "messages_succeeded_total",
			Help:        "Total number of messages successfully transformed",
			ConstLabels: labels,
		},
	)

	// messagesFailed counts failed transformations by error category
	messagesFailed := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace:   "vrsky",
			Subsystem:   "converter",
			Name:        "messages_failed_total",
			Help:        "Total number of message transformation failures by error category",
			ConstLabels: labels,
		},
		[]string{"error_category"}, // Label for error type tracking
	)

	// transformationDuration tracks transformation latency
	transformationDuration := prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace:   "vrsky",
			Subsystem:   "converter",
			Name:        "transformation_duration_seconds",
			Help:        "Duration of message transformation in seconds",
			ConstLabels: labels,
			Buckets:     []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
		},
	)

	// retryAttempts tracks retry distribution
	retryAttempts := prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace:   "vrsky",
			Subsystem:   "converter",
			Name:        "retry_attempts_total",
			Help:        "Total number of retry attempts made",
			ConstLabels: labels,
		},
	)

	// Register metrics
	reg := prometheus.DefaultRegisterer
	if err := reg.Register(messagesReceived); err != nil {
		return nil, fmt.Errorf("register messagesReceived: %w", err)
	}
	if err := reg.Register(messagesSucceeded); err != nil {
		return nil, fmt.Errorf("register messagesSucceeded: %w", err)
	}
	if err := reg.Register(messagesFailed); err != nil {
		return nil, fmt.Errorf("register messagesFailed: %w", err)
	}
	if err := reg.Register(transformationDuration); err != nil {
		return nil, fmt.Errorf("register transformationDuration: %w", err)
	}
	if err := reg.Register(retryAttempts); err != nil {
		return nil, fmt.Errorf("register retryAttempts: %w", err)
	}

	return &Metrics{
		messagesReceived:       messagesReceived,
		messagesSucceeded:      messagesSucceeded,
		messagesFailed:         messagesFailed,
		transformationDuration: transformationDuration,
		retryAttempts:          retryAttempts,
	}, nil
}

// RecordMessageReceived increments the received messages counter
func (m *Metrics) RecordMessageReceived() {
	m.messagesReceived.Inc()
}

// RecordMessageSucceeded increments the succeeded messages counter
func (m *Metrics) RecordMessageSucceeded() {
	m.messagesSucceeded.Inc()
}

// RecordMessageFailed increments the failed messages counter by error category
func (m *Metrics) RecordMessageFailed(errorCategory string) {
	m.messagesFailed.WithLabelValues(errorCategory).Inc()
}

// RecordTransformationDuration records the transformation latency
func (m *Metrics) RecordTransformationDuration(duration time.Duration) {
	m.transformationDuration.Observe(duration.Seconds())
}

// RecordRetryAttempt increments the retry attempts counter.
// The attempt number is currently unused but kept for compatibility
// and potential future use (e.g., as a label or for logging).
func (m *Metrics) RecordRetryAttempt(_ int) {
	m.retryAttempts.Inc()
}
