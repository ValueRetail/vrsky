package messaging

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Phase 1E (#70) metrics — exported on every worker's /metrics endpoint
// (workers run their own HTTP server with promhttp.Handler).
var (
	// publishCount counts every successful publish to the main stream.
	// Workers that emit derived data (filter, converter) inherit this
	// automatically because they call messaging.Publisher.
	publishCount = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "vrsky_messages_published_total",
			Help: "Number of messages successfully published to the main JetStream data stream.",
		},
		[]string{"tenant_id"},
	)

	// redeliveryCount counts every NAK or expired-ack-wait redelivery.
	// Workers increment this from the subscriber dispatch loop.
	redeliveryCount = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "vrsky_message_redelivery_total",
			Help: "Number of times JetStream redelivered a message to a worker.",
		},
		[]string{"durable"},
	)

	// dlqCount counts every message moved into the DLQ stream because a
	// worker exhausted MaxDeliver.
	dlqCount = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "vrsky_dlq_messages_total",
			Help: "Number of messages moved to the dead-letter queue.",
		},
		[]string{"pipeline_id", "worker"},
	)

	// processingSeconds measures end-to-end handler duration. Useful for
	// tuning AckWait and spotting slow workers.
	processingSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "vrsky_message_processing_seconds",
			Help:    "Time spent in a worker's handler function.",
			Buckets: prometheus.DefBuckets, // 0.005 to 10 seconds
		},
		[]string{"durable", "status"}, // status = ok | error | panic
	)
)
