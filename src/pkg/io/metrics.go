package io

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// PostgresConsumerMetrics holds all Prometheus metrics for PostgreSQL Consumer
type PostgresConsumerMetrics struct {
	// Counters
	ChangesCapturedTotal      *prometheus.CounterVec // By operation type
	BatchesPublishedTotal     prometheus.Counter
	ConnectionErrorsTotal     prometheus.Counter
	ParseErrorsTotal          prometheus.Counter

	// Histograms
	BatchSizeHistogram        prometheus.Histogram
	CaptureLatencyHistogram   prometheus.Histogram

	// Gauges
	LSNOffsetGauge            prometheus.Gauge
	PendingBatchSizeGauge     prometheus.Gauge
}

// PostgresProducerMetrics holds all Prometheus metrics for PostgreSQL Producer
type PostgresProducerMetrics struct {
	// Counters
	MessagesReceivedTotal     *prometheus.CounterVec // By operation type
	BatchesWrittenTotal       prometheus.Counter
	WriteErrorsTotal          prometheus.Counter
	DLQMessagesTotal          prometheus.Counter

	// Histograms
	BatchWriteLatencyHistogram prometheus.Histogram
	BatchSizeHistogram         prometheus.Histogram

	// Gauges
	PendingBatchSizeGauge     prometheus.Gauge
}

// NewPostgresConsumerMetrics creates and registers consumer metrics with the provided registry
// If registry is nil, uses prometheus.DefaultRegisterer for backward compatibility
func NewPostgresConsumerMetrics(reg prometheus.Registerer) *PostgresConsumerMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	factory := promauto.With(reg)

	return &PostgresConsumerMetrics{
		ChangesCapturedTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "postgres_consumer_changes_captured_total",
				Help: "Total number of changes captured from PostgreSQL, by operation type",
			},
			[]string{"operation"},
		),
		BatchesPublishedTotal: factory.NewCounter(
			prometheus.CounterOpts{
				Name: "postgres_consumer_batches_published_total",
				Help: "Total number of change batches published to NATS",
			},
		),
		ConnectionErrorsTotal: factory.NewCounter(
			prometheus.CounterOpts{
				Name: "postgres_consumer_connection_errors_total",
				Help: "Total number of PostgreSQL connection errors",
			},
		),
		ParseErrorsTotal: factory.NewCounter(
			prometheus.CounterOpts{
				Name: "postgres_consumer_parse_errors_total",
				Help: "Total number of message parsing errors",
			},
		),
		BatchSizeHistogram: factory.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "postgres_consumer_batch_size",
				Help:    "Distribution of batch sizes published to NATS",
				Buckets: []float64{1, 10, 50, 100, 500, 1000, 5000},
			},
		),
		CaptureLatencyHistogram: factory.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "postgres_consumer_capture_latency_seconds",
				Help:    "Latency from database change to NATS publish, in seconds",
				Buckets: prometheus.DefBuckets, // 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10
			},
		),
		LSNOffsetGauge: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: "postgres_consumer_lsn_offset",
				Help: "Current PostgreSQL LSN (Log Sequence Number) position",
			},
		),
		PendingBatchSizeGauge: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: "postgres_consumer_pending_batch_size",
				Help: "Current size of pending batch awaiting publish",
			},
		),
	}
}

// NewPostgresProducerMetrics creates and registers producer metrics with the provided registry
// If registry is nil, uses prometheus.DefaultRegisterer for backward compatibility
func NewPostgresProducerMetrics(reg prometheus.Registerer) *PostgresProducerMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	factory := promauto.With(reg)

	return &PostgresProducerMetrics{
		MessagesReceivedTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "postgres_producer_messages_received_total",
				Help: "Total number of messages received from NATS, by operation type",
			},
			[]string{"operation"},
		),
		BatchesWrittenTotal: factory.NewCounter(
			prometheus.CounterOpts{
				Name: "postgres_producer_batches_written_total",
				Help: "Total number of batches successfully written to PostgreSQL",
			},
		),
		WriteErrorsTotal: factory.NewCounter(
			prometheus.CounterOpts{
				Name: "postgres_producer_write_errors_total",
				Help: "Total number of write errors (before retry exhaustion)",
			},
		),
		DLQMessagesTotal: factory.NewCounter(
			prometheus.CounterOpts{
				Name: "postgres_producer_dlq_messages_total",
				Help: "Total number of messages sent to Dead Letter Queue",
			},
		),
		BatchWriteLatencyHistogram: factory.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "postgres_producer_batch_write_latency_seconds",
				Help:    "Latency of batch write operations to PostgreSQL, in seconds",
				Buckets: prometheus.DefBuckets,
			},
		),
		BatchSizeHistogram: factory.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "postgres_producer_batch_size",
				Help:    "Distribution of batch sizes written to PostgreSQL",
				Buckets: []float64{1, 10, 50, 100, 500, 1000, 5000},
			},
		),
		PendingBatchSizeGauge: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: "postgres_producer_pending_batch_size",
				Help: "Current size of pending batch awaiting write",
			},
		),
	}
}

// GetMetricsHandler returns the Prometheus metrics HTTP handler for the given registry
// If registry is nil, uses prometheus.DefaultRegisterer for backward compatibility
// The registry parameter should implement both Registerer and Gatherer (like prometheus.Registry)
func GetMetricsHandler(reg prometheus.Registerer) http.Handler {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	// prometheus.DefaultRegisterer implements both Registerer and Gatherer
	// Custom registries created with prometheus.NewRegistry() also implement both
	// Type assert to Gatherer since promhttp.HandlerFor requires a Gatherer
	gatherer, ok := reg.(prometheus.Gatherer)
	if !ok {
		// Fallback to DefaultRegisterer if the provided registry doesn't implement Gatherer
		gatherer = prometheus.DefaultRegisterer.(prometheus.Gatherer)
	}
	return promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})
}

// MetricsRegistry provides a centralized place for all metrics
type MetricsRegistry struct {
	Registry        prometheus.Registerer
	ConsumerMetrics *PostgresConsumerMetrics
	ProducerMetrics *PostgresProducerMetrics
}

// NewMetricsRegistry creates a new metrics registry with isolated metrics
// If reg is nil, uses prometheus.DefaultRegisterer for backward compatibility
func NewMetricsRegistry(reg prometheus.Registerer) *MetricsRegistry {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	return &MetricsRegistry{
		Registry:        reg,
		ConsumerMetrics: NewPostgresConsumerMetrics(reg),
		ProducerMetrics: NewPostgresProducerMetrics(reg),
	}
}
