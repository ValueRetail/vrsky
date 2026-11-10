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
		// connection_id enables per-connection throughput in the UI dashboard.
		// Usage metering still aggregates as sum by (tenant_id), so adding this
		// label is non-breaking.
		[]string{"tenant_id", "connection_id"},
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

	// retryUndelayedCount counts failed deliveries NAK'd without a delay
	// because the subscriber already held its cap of messages waiting out a
	// retry delay (SubscriberOpts.MaxDelayed, #794). Such a message comes back
	// at once and, if it keeps failing, reaches the DLQ in about a second.
	retryUndelayedCount = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "vrsky_message_retry_undelayed_total",
			Help: "Failed deliveries retried at once, without the backoff, because too many messages were already waiting out a retry delay.",
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

	// dlqPublishFailedCount counts messages whose last delivery failed and
	// whose copy to the DLQ failed DLQPublishAttempts times (#811). Such a
	// message is left unacked; nats-server drops it from the durable when its
	// ack wait runs out, and it stays only in VRSKY_DATA, at the stream
	// sequence the worker logs, until that ages it out.
	dlqPublishFailedCount = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "vrsky_dlq_publish_failed_total",
			Help: "Messages that failed their last delivery and could not be copied to the dead-letter queue; they remain only in the data stream.",
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
