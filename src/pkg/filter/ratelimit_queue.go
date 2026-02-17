package filter

import (
	"log/slog"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// RateLimitQueue is a bounded FIFO queue for rate-limited messages
type RateLimitQueue struct {
	messages chan *QueuedMessage
	size     int
	logger   *slog.Logger
}

// QueuedMessage represents a message in the rate limit queue
type QueuedMessage struct {
	Envelope  *envelope.Envelope
	RuleID    string
	Timestamp time.Time
}

// NewRateLimitQueue creates a new bounded queue
func NewRateLimitQueue(size int, logger *slog.Logger) *RateLimitQueue {
	if logger == nil {
		logger = slog.Default()
	}

	return &RateLimitQueue{
		messages: make(chan *QueuedMessage, size),
		size:     size,
		logger:   logger,
	}
}

// Enqueue adds a message to the queue
// Returns error if queue is full
func (q *RateLimitQueue) Enqueue(msg *QueuedMessage) error {
	select {
	case q.messages <- msg:
		return nil
	default:
		// Queue is full
		return ErrQueueFull
	}
}

// Dequeue retrieves a message from the queue
// Returns nil if queue is empty
func (q *RateLimitQueue) Dequeue() *QueuedMessage {
	select {
	case msg := <-q.messages:
		return msg
	default:
		return nil
	}
}

// Size returns the current number of messages in the queue
func (q *RateLimitQueue) Size() int {
	return len(q.messages)
}

// IsFull checks if the queue is at capacity
func (q *RateLimitQueue) IsFull() bool {
	return len(q.messages) >= q.size
}

// Close gracefully shuts down the queue
func (q *RateLimitQueue) Close() error {
	close(q.messages)
	return nil
}

// Drain drains all messages from the queue
// Used for cleanup
func (q *RateLimitQueue) Drain() []*QueuedMessage {
	var msgs []*QueuedMessage
	for {
		select {
		case msg := <-q.messages:
			if msg != nil {
				msgs = append(msgs, msg)
			}
		default:
			return msgs
		}
	}
}
