package filter

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// FilterMetrics holds Prometheus metrics for the filter
type FilterMetrics struct {
	receivedCounter              prometheus.Counter
	acceptedCounter              prometheus.Counter
	rejectedCounter              prometheus.Counter
	failedCounter                prometheus.Counter
	routingFailureCounter        prometheus.Counter
	transformationFailureCounter prometheus.Counter
	processHistogram             prometheus.Histogram
}

// NewFilterMetrics creates and registers filter metrics
func NewFilterMetrics(filterID string, registerer prometheus.Registerer) *FilterMetrics {
	receivedCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vrsky_filter_messages_received_total",
		Help: "Total number of messages received by filter",
		ConstLabels: prometheus.Labels{
			"filter_id": filterID,
		},
	})

	acceptedCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vrsky_filter_messages_accepted_total",
		Help: "Total number of messages accepted by filter",
		ConstLabels: prometheus.Labels{
			"filter_id": filterID,
		},
	})

	rejectedCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vrsky_filter_messages_rejected_total",
		Help: "Total number of messages rejected by filter",
		ConstLabels: prometheus.Labels{
			"filter_id": filterID,
		},
	})

	failedCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vrsky_filter_messages_failed_total",
		Help: "Total number of messages that failed processing",
		ConstLabels: prometheus.Labels{
			"filter_id": filterID,
		},
	})

	routingFailureCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vrsky_filter_routing_failures_total",
		Help: "Total number of routing evaluation failures",
		ConstLabels: prometheus.Labels{
			"filter_id": filterID,
		},
	})

	transformationFailureCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vrsky_filter_transformation_failures_total",
		Help: "Total number of transformation failures",
		ConstLabels: prometheus.Labels{
			"filter_id": filterID,
		},
	})

	processHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "vrsky_filter_process_duration_seconds",
		Help: "Duration of message processing in seconds",
		ConstLabels: prometheus.Labels{
			"filter_id": filterID,
		},
		Buckets: prometheus.DefBuckets,
	})

	registerer.MustRegister(receivedCounter, acceptedCounter, rejectedCounter, failedCounter, routingFailureCounter, transformationFailureCounter, processHistogram)

	return &FilterMetrics{
		receivedCounter:              receivedCounter,
		acceptedCounter:              acceptedCounter,
		rejectedCounter:              rejectedCounter,
		failedCounter:                failedCounter,
		routingFailureCounter:        routingFailureCounter,
		transformationFailureCounter: transformationFailureCounter,
		processHistogram:             processHistogram,
	}
}

// RecordReceived increments the received counter
func (m *FilterMetrics) RecordReceived() {
	m.receivedCounter.Inc()
}

// RecordAccepted increments the accepted counter
func (m *FilterMetrics) RecordAccepted() {
	m.acceptedCounter.Inc()
}

// RecordRejected increments the rejected counter
func (m *FilterMetrics) RecordRejected() {
	m.rejectedCounter.Inc()
}

// RecordFailure increments the failed counter
func (m *FilterMetrics) RecordFailure() {
	m.failedCounter.Inc()
}

// RecordProcessDuration records the processing duration
func (m *FilterMetrics) RecordProcessDuration(duration time.Duration) {
	m.processHistogram.Observe(duration.Seconds())
}

// RecordRoutingFailure increments the routing failure counter (Priority 2)
func (m *FilterMetrics) RecordRoutingFailure() {
	m.routingFailureCounter.Inc()
}

// RecordTransformationFailure increments the transformation failure counter (Priority 2)
func (m *FilterMetrics) RecordTransformationFailure() {
	m.transformationFailureCounter.Inc()
}

// BackoffConfig holds exponential backoff configuration
type BackoffConfig struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
	MaxRetries   int
}

// DefaultBackoffConfig returns the default backoff configuration
func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     5 * time.Second,
		Multiplier:   2.0,
		MaxRetries:   3,
	}
}

// CalculateBackoffDelay calculates the backoff delay for a given retry attempt
func (bc *BackoffConfig) CalculateBackoffDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}

	delay := time.Duration(float64(bc.InitialDelay) * (bc.Multiplier * float64(attempt)))
	if delay > bc.MaxDelay {
		delay = bc.MaxDelay
	}

	return delay
}
