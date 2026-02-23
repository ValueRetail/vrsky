package converter

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds all Prometheus metrics for the converter component
type Metrics struct {
	messagesReceived              prometheus.Counter
	messagesSucceeded             prometheus.Counter
	messagesFailed                *prometheus.CounterVec
	transformationDurationSuccess prometheus.Histogram   // Duration for successful transformations
	transformationDurationFailure prometheus.Histogram   // Duration for failed transformations
	retryAttempts                 *prometheus.CounterVec // Track retry attempts by attempt number
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
		[]string{"error_category"},
	)

	// transformationDurationSuccess tracks successful transformation latency
	transformationDurationSuccess := prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace:   "vrsky",
			Subsystem:   "converter",
			Name:        "transformation_duration_success_seconds",
			Help:        "Duration of successful message transformations in seconds",
			ConstLabels: labels,
			Buckets:     []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
		},
	)

	// transformationDurationFailure tracks failed transformation latency
	transformationDurationFailure := prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace:   "vrsky",
			Subsystem:   "converter",
			Name:        "transformation_duration_failure_seconds",
			Help:        "Duration of failed message transformations in seconds",
			ConstLabels: labels,
			Buckets:     []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
		},
	)

	// retryAttempts tracks retry distribution by attempt number
	retryAttempts := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace:   "vrsky",
			Subsystem:   "converter",
			Name:        "retry_attempts_total",
			Help:        "Total number of retry attempts made by attempt number",
			ConstLabels: labels,
		},
		[]string{"attempt"},
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
	if err := reg.Register(transformationDurationSuccess); err != nil {
		return nil, fmt.Errorf("register transformationDurationSuccess: %w", err)
	}
	if err := reg.Register(transformationDurationFailure); err != nil {
		return nil, fmt.Errorf("register transformationDurationFailure: %w", err)
	}
	if err := reg.Register(retryAttempts); err != nil {
		return nil, fmt.Errorf("register retryAttempts: %w", err)
	}

	return &Metrics{
		messagesReceived:              messagesReceived,
		messagesSucceeded:             messagesSucceeded,
		messagesFailed:                messagesFailed,
		transformationDurationSuccess: transformationDurationSuccess,
		transformationDurationFailure: transformationDurationFailure,
		retryAttempts:                 retryAttempts,
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

// RecordMessageFailed increments the failed messages counter
func (m *Metrics) RecordMessageFailed(errorCategory string) {
	m.messagesFailed.WithLabelValues(errorCategory).Inc()
}

// RecordTransformationDurationSuccess records the duration of successful transformations
func (m *Metrics) RecordTransformationDurationSuccess(duration time.Duration) {
	m.transformationDurationSuccess.Observe(duration.Seconds())
}

// RecordTransformationDurationFailure records the duration of failed transformations
func (m *Metrics) RecordTransformationDurationFailure(duration time.Duration) {
	m.transformationDurationFailure.Observe(duration.Seconds())
}

// RecordRetryAttempt increments the retry attempts counter for the specified attempt number.
// attempt should be 1, 2, 3, etc. to track which retry attempts are most common.
func (m *Metrics) RecordRetryAttempt(attempt int) {
	m.retryAttempts.WithLabelValues(fmt.Sprintf("%d", attempt)).Inc()
}
