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

// NewPostgresConsumerMetrics creates and registers consumer metrics
func NewPostgresConsumerMetrics() *PostgresConsumerMetrics {
	return &PostgresConsumerMetrics{
		ChangesCapturedTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "postgres_consumer_changes_captured_total",
				Help: "Total number of changes captured from PostgreSQL, by operation type",
			},
			[]string{"operation"},
		),
		BatchesPublishedTotal: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "postgres_consumer_batches_published_total",
				Help: "Total number of change batches published to NATS",
			},
		),
		ConnectionErrorsTotal: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "postgres_consumer_connection_errors_total",
				Help: "Total number of PostgreSQL connection errors",
			},
		),
		ParseErrorsTotal: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "postgres_consumer_parse_errors_total",
				Help: "Total number of message parsing errors",
			},
		),
		BatchSizeHistogram: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "postgres_consumer_batch_size",
				Help:    "Distribution of batch sizes published to NATS",
				Buckets: []float64{1, 10, 50, 100, 500, 1000, 5000},
			},
		),
		CaptureLatencyHistogram: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "postgres_consumer_capture_latency_seconds",
				Help:    "Latency from database change to NATS publish, in seconds",
				Buckets: prometheus.DefBuckets, // 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10
			},
		),
		LSNOffsetGauge: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "postgres_consumer_lsn_offset",
				Help: "Current PostgreSQL LSN (Log Sequence Number) position",
			},
		),
		PendingBatchSizeGauge: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "postgres_consumer_pending_batch_size",
				Help: "Current size of pending batch awaiting publish",
			},
		),
	}
}

// NewPostgresProducerMetrics creates and registers producer metrics
func NewPostgresProducerMetrics() *PostgresProducerMetrics {
	return &PostgresProducerMetrics{
		MessagesReceivedTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "postgres_producer_messages_received_total",
				Help: "Total number of messages received from NATS, by operation type",
			},
			[]string{"operation"},
		),
		BatchesWrittenTotal: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "postgres_producer_batches_written_total",
				Help: "Total number of batches successfully written to PostgreSQL",
			},
		),
		WriteErrorsTotal: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "postgres_producer_write_errors_total",
				Help: "Total number of write errors (before retry exhaustion)",
			},
		),
		DLQMessagesTotal: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "postgres_producer_dlq_messages_total",
				Help: "Total number of messages sent to Dead Letter Queue",
			},
		),
		BatchWriteLatencyHistogram: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "postgres_producer_batch_write_latency_seconds",
				Help:    "Latency of batch write operations to PostgreSQL, in seconds",
				Buckets: prometheus.DefBuckets,
			},
		),
		BatchSizeHistogram: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "postgres_producer_batch_size",
				Help:    "Distribution of batch sizes written to PostgreSQL",
				Buckets: []float64{1, 10, 50, 100, 500, 1000, 5000},
			},
		),
		PendingBatchSizeGauge: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "postgres_producer_pending_batch_size",
				Help: "Current size of pending batch awaiting write",
			},
		),
	}
}

// GetMetricsHandler returns the Prometheus metrics HTTP handler
func GetMetricsHandler() http.Handler {
	return promhttp.Handler()
}

// MetricsRegistry provides a centralized place for all metrics
type MetricsRegistry struct {
	ConsumerMetrics *PostgresConsumerMetrics
	ProducerMetrics *PostgresProducerMetrics
}

// NewMetricsRegistry creates a new metrics registry
func NewMetricsRegistry() *MetricsRegistry {
	return &MetricsRegistry{
		ConsumerMetrics: NewPostgresConsumerMetrics(),
		ProducerMetrics: NewPostgresProducerMetrics(),
	}
}
