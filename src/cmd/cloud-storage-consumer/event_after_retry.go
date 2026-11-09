package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/doneset"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// Event mode has no listing and no done set, so a delete or move that failed
// after a successful publish would leave the object in the bucket for good
// (#672). The event loop hands such a key to an afterRetryQueue: a small
// in-memory list, one per connection, that tries the after-action again with a
// backoff. It never fetches or publishes again. It is lost on restart and with
// the connection; a key that cannot be retried is logged at Error and dropped.
//
// A retry must not remove an object that was written again under the same key
// after the failure: that replacement may not be published yet. So the event
// loop stats each object before it fetches it, the queue keeps that stamp, and
// a retry stats the object again first. A changed stamp means a replacement,
// which is left for its own notification. Stamping before the fetch fails
// safe: a replacement between the Stat and the fetch is published under the
// old stamp, and the retry then sees a mismatch and leaves the object, a
// duplicate publish at worst. The first after-action makes the same check
// (#707). The moment between a Stat and its Delete (or Copy) is closed by a
// conditional delete or copy where the store honours one (#718).

const (
	// eventAfterQueueMax bounds the keys waiting for a retry per connection.
	eventAfterQueueMax = 1000
	// eventAfterMaxRetries is how many times a key is retried after its first
	// failure. With the poll-mode backoff (1 min doubling to 1 h) the last
	// retry comes about three hours after the first failure.
	eventAfterMaxRetries = 8
)

// errMoveOntoSelf is the afterAction refusal to move an object onto its own
// key (#567). Retrying cannot change it, so it is never queued.
var errMoveOntoSelf = errors.New("a move onto its own key is refused")

// afterRetryTuning overrides the queue's limits; zero fields take the defaults
// above and doneset.RetryBase/RetryMax. Tests set it.
type afterRetryTuning struct {
	base, max  time.Duration
	maxRetries int
	capacity   int
}

// stampOf is the version of an object as a listing or Stat shows it: size,
// last-modified and the store's version (ETag or generation). Compare two
// with cloudConfig.match.
func stampOf(o objectstore.Object) doneset.Stamp {
	st := doneset.StampOf(o.Size, o.LastModified)
	st.Version = o.Version
	return st
}

type afterRetryItem struct {
	stamp   doneset.Stamp // what was published
	retries int           // retries made so far
	next    time.Time
}

type afterRetryQueue struct {
	logger *slog.Logger
	action string // for logs
	bucket string // for logs
	// match compares the stamp a retry finds with the published one.
	match doneset.Match
	// stat looks the object up before a retry; do runs the after-action, on
	// the object only while it is at version where the store can say (#718).
	stat func(ctx context.Context, key string) (objectstore.Object, error)
	do   func(ctx context.Context, key, version string) error
	t    afterRetryTuning

	mu    sync.Mutex
	items map[string]*afterRetryItem
	wake  chan struct{} // 1-buffered: the queue changed
}

func newAfterRetryQueue(logger *slog.Logger, action, bucket string, t afterRetryTuning, match doneset.Match,
	stat func(ctx context.Context, key string) (objectstore.Object, error),
	do func(ctx context.Context, key, version string) error) *afterRetryQueue {
	if t.base <= 0 {
		t.base = doneset.RetryBase
	}
	if t.max <= 0 {
		t.max = doneset.RetryMax
	}
	if t.maxRetries <= 0 {
		t.maxRetries = eventAfterMaxRetries
	}
	if t.capacity <= 0 {
		t.capacity = eventAfterQueueMax
	}
	return &afterRetryQueue{
		logger: logger, action: action, bucket: bucket, match: match, stat: stat, do: do, t: t,
		items: make(map[string]*afterRetryItem),
		wake:  make(chan struct{}, 1),
	}
}

// wait is the pause before retry number n (1 is the first): base, doubling,
// capped at max.
func (q *afterRetryQueue) wait(n int) time.Duration {
	w := q.t.base
	for i := 1; i < n && w < q.t.max; i++ {
		w *= 2
	}
	return min(w, q.t.max)
}

func (q *afterRetryQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// failed records that the after-action of key failed right after the publish
// of the object with stamp. A key that is gone counts as done; a refusal that
// cannot change is only logged; anything else is queued. A key already queued
// starts over with the new stamp: it was published again and its new
// after-action failed too.
func (q *afterRetryQueue) failed(key string, stamp doneset.Stamp, err error) {
	switch {
	case errors.Is(err, objectstore.ErrNotFound):
		q.logger.Info("after-action: object already gone; nothing to do", "key", key, "action", q.action)
		q.forget(key)
		return
	case errors.Is(err, errMoveOntoSelf):
		q.logger.Warn("after-action failed", "key", key, "action", q.action, "error", err)
		q.forget(key)
		return
	}
	q.mu.Lock()
	if _, queued := q.items[key]; !queued && len(q.items) >= q.t.capacity {
		q.mu.Unlock()
		q.logger.Error("after-action failed and the retry queue is full; giving up, the object stays in the bucket",
			"bucket", q.bucket, "key", key, "action", q.action, "queue_size", q.t.capacity, "error", err)
		return
	}
	q.items[key] = &afterRetryItem{stamp: stamp, next: time.Now().Add(q.wait(1))}
	q.mu.Unlock()
	q.logger.Warn("after-action failed; will retry in memory", "key", key, "action", q.action,
		"retry_in", q.wait(1), "error", err)
	q.poke()
}

// forget drops key: its after-action worked, or the object was handled again.
func (q *afterRetryQueue) forget(key string) {
	q.mu.Lock()
	delete(q.items, key)
	q.mu.Unlock()
}

func (q *afterRetryQueue) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// next returns the key that is due first and when.
func (q *afterRetryQueue) next() (key string, at time.Time, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for k, it := range q.items {
		if !ok || it.next.Before(at) {
			key, at, ok = k, it.next, true
		}
	}
	return key, at, ok
}

// run retries queued keys until ctx is done. One goroutine per connection;
// the caller waits for it to return.
func (q *afterRetryQueue) run(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	defer func() {
		if n := q.len(); n > 0 {
			q.logger.Info("event loop stopped with after-actions still to retry; they are not kept",
				"bucket", q.bucket, "pending", n)
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		key, at, ok := q.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-q.wake:
			}
			continue
		}
		if d := time.Until(at); d > 0 {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(d)
			select {
			case <-ctx.Done():
				return
			case <-q.wake: // a sooner key may have arrived
			case <-timer.C:
			}
			continue
		}
		q.attempt(ctx, key)
	}
}

// attempt retries a due key once: it stats the object, and runs the
// after-action only when the object is the one that was published. Then it
// removes, reschedules or drops the key. A key handled again while the try ran
// (forgotten, or queued anew with a new stamp) keeps its new state: the result
// belongs to the old item.
func (q *afterRetryQueue) attempt(ctx context.Context, key string) {
	q.mu.Lock()
	it, ok := q.items[key]
	q.mu.Unlock()
	if !ok {
		return
	}

	obj, err := q.stat(ctx, key)
	if ctx.Err() != nil {
		return // stopping: the result may be the cancellation itself
	}
	switch {
	case errors.Is(err, objectstore.ErrNotFound):
		if q.drop(key, it) {
			q.logger.Info("after-action retry: object already gone; done", "key", key, "action", q.action)
		}
		return
	case err != nil:
		q.retryFailed(key, it, err)
		return
	case !q.match.Same(stampOf(obj), it.stamp):
		q.replaced(key, it)
		return
	}

	err = q.do(ctx, key, it.stamp.Version)
	if ctx.Err() != nil {
		return
	}
	switch {
	case errors.Is(err, errReplaced):
		// Written again between the Stat and the delete or copy (#718).
		q.replaced(key, it)
	case err == nil:
		if q.drop(key, it) {
			q.logger.Info("after-action done on retry", "key", key, "action", q.action, "retries", it.retries+1)
		}
	case errors.Is(err, objectstore.ErrNotFound):
		if q.drop(key, it) {
			q.logger.Info("after-action retry: object already gone; done", "key", key, "action", q.action)
		}
	default:
		q.retryFailed(key, it, err)
	}
}

// replaced drops key, whose object is no longer the one published.
func (q *afterRetryQueue) replaced(key string, it *afterRetryItem) {
	if q.drop(key, it) {
		q.logger.Warn("after-action retry: object replaced since published; left for its own notification",
			"bucket", q.bucket, "key", key, "action", q.action)
	}
}

// drop removes key if it still holds it, and reports whether it did.
func (q *afterRetryQueue) drop(key string, it *afterRetryItem) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.items[key] != it {
		return false
	}
	delete(q.items, key)
	return true
}

// retryFailed counts a failed retry of it: it is rescheduled, or dropped at the
// cap. Nothing happens if key no longer holds it.
func (q *afterRetryQueue) retryFailed(key string, it *afterRetryItem, err error) {
	q.mu.Lock()
	if q.items[key] != it {
		q.mu.Unlock()
		return
	}
	it.retries++
	if it.retries >= q.t.maxRetries {
		delete(q.items, key)
		q.mu.Unlock()
		q.logger.Error("after-action still failing; giving up, the object stays in the bucket",
			"bucket", q.bucket, "key", key, "action", q.action, "retries", it.retries, "error", err)
		return
	}
	w := q.wait(it.retries + 1)
	it.next = time.Now().Add(w)
	q.mu.Unlock()
	q.logger.Warn("after-action failed again; will retry in memory", "key", key, "action", q.action,
		"retries", it.retries, "retry_in", w, "error", err)
}
