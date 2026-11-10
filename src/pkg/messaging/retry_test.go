package messaging

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

type delayErr struct{ d time.Duration }

func (e delayErr) Error() string             { return "rate limited" }
func (e delayErr) RetryDelay() time.Duration { return e.d }

// #794: how long a failed delivery waits, and the delay to NAK it with so
// nats-server (which adds Backoff[n-1]-AckWait to a NAK delay) waits that.
func TestRetryWait(t *testing.T) {
	const s = time.Second
	def := DefaultBackoff
	tests := []struct {
		name      string
		backoff   []time.Duration
		delivered uint64
		err       error
		wantWait  time.Duration
	}{
		{"attempt 1 waits the first step", def, 1, errors.New("boom"), 1 * s},
		{"attempt 2 waits the second step", def, 2, errors.New("boom"), 5 * s},
		{"attempt 3 waits the third step", def, 3, errors.New("boom"), 30 * s},
		{"attempt 4 waits the fourth step", def, 4, errors.New("boom"), 2 * time.Minute},
		{"past the schedule: the last step", def, 7, errors.New("boom"), 2 * time.Minute},
		{"no delivery count: the last step", def, 0, errors.New("boom"), 2 * time.Minute},
		{"a hint longer than the step wins", def, 1, delayErr{7 * s}, 7 * s},
		{"a step longer than the hint wins", def, 3, delayErr{7 * s}, 30 * s},
		{"wrapped hint", def, 1, fmt.Errorf("deliver: %w", delayErr{7 * s}), 7 * s},
		{"hint over the cap is cut to the largest step", def, 1, delayErr{time.Hour}, 2 * time.Minute},
		{"cap is the largest step, not the last", []time.Duration{s, 3 * time.Minute, time.Minute}, 1, delayErr{time.Hour}, 3 * time.Minute},
		{"zero hint raised to the floor", []time.Duration{100 * time.Millisecond, 2 * s}, 1, delayErr{0}, 1 * s},
		{"negative hint raised to the floor", []time.Duration{100 * time.Millisecond, 2 * s}, 1, delayErr{-s}, 1 * s},
		{"a schedule shorter than the floor wins", []time.Duration{50 * time.Millisecond, 50 * time.Millisecond}, 1, delayErr{5 * s}, 50 * time.Millisecond},
		{"errors.Join: the largest hint wins", def, 1, errors.Join(delayErr{2 * s}, delayErr{9 * s}, delayErr{4 * s}), 9 * s},
		{"errors.Join: first found is not the answer", def, 1, errors.Join(fmt.Errorf("a: %w", delayErr{3 * s}), fmt.Errorf("b: %w", delayErr{8 * s})), 8 * s},
		{"errors.Join with no hint: the step", def, 2, errors.Join(errors.New("a"), errors.New("b")), 5 * s},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ackWait := tt.backoff[0]
			wait, nak := retryWait(tt.backoff, ackWait, tt.delivered, tt.err)
			if wait != tt.wantWait {
				t.Errorf("wait = %v, want %v", wait, tt.wantWait)
			}
			// What nats-server will actually wait for a NAK with delay nak.
			i := len(tt.backoff) - 1
			if tt.delivered >= 1 && int(tt.delivered) <= len(tt.backoff) {
				i = int(tt.delivered) - 1
			}
			if served := nak + tt.backoff[i] - ackWait; served != wait {
				t.Errorf("nak %v makes the server wait %v, want %v", nak, served, wait)
			}
			if nak < ackWait {
				t.Errorf("nak = %v, below AckWait %v", nak, ackWait)
			}
		})
	}
}

// #794: the caps on messages waiting out a delay, in all and per connection.
func TestDelayedSet(t *testing.T) {
	t0 := time.Now()
	later := t0.Add(time.Minute)
	type op struct {
		kind   string // "add", "remove", "at"
		seq    uint64
		conn   string
		now    time.Time
		wantOK bool
	}
	tests := []struct {
		name      string
		maxTotal  int
		maxConn   int
		ops       []op
		wantLen   int
		wantConnA int
	}{
		{
			name: "per-connection cap", maxTotal: 8, maxConn: 2,
			ops: []op{
				{kind: "add", seq: 1, conn: "a", now: t0, wantOK: true},
				{kind: "add", seq: 2, conn: "a", now: t0, wantOK: true},
				{kind: "add", seq: 3, conn: "a", now: t0, wantOK: false},
				{kind: "add", seq: 4, conn: "b", now: t0, wantOK: true},
			},
			wantLen: 3, wantConnA: 2,
		},
		{
			name: "total cap across connections", maxTotal: 3, maxConn: 2,
			ops: []op{
				{kind: "add", seq: 1, conn: "a", now: t0, wantOK: true},
				{kind: "add", seq: 2, conn: "b", now: t0, wantOK: true},
				{kind: "add", seq: 3, conn: "c", now: t0, wantOK: true},
				{kind: "add", seq: 4, conn: "d", now: t0, wantOK: false},
			},
			wantLen: 3, wantConnA: 1,
		},
		{
			name: "a message that comes back frees its place", maxTotal: 8, maxConn: 1,
			ops: []op{
				{kind: "add", seq: 1, conn: "a", now: t0, wantOK: true},
				{kind: "add", seq: 2, conn: "a", now: t0, wantOK: false},
				{kind: "remove", seq: 1},
				{kind: "add", seq: 2, conn: "a", now: t0, wantOK: true},
			},
			wantLen: 1, wantConnA: 1,
		},
		{
			name: "a due entry frees its place (redelivered elsewhere)", maxTotal: 8, maxConn: 1,
			ops: []op{
				{kind: "add", seq: 1, conn: "a", now: t0, wantOK: true},
				{kind: "add", seq: 2, conn: "a", now: later, wantOK: true},
			},
			wantLen: 1, wantConnA: 1,
		},
		{
			name: "the same message twice is counted once", maxTotal: 8, maxConn: 1,
			ops: []op{
				{kind: "add", seq: 1, conn: "a", now: t0, wantOK: true},
				{kind: "add", seq: 1, conn: "a", now: t0, wantOK: true},
			},
			wantLen: 1, wantConnA: 1,
		},
		{
			// Re-added at +20 s, it is due at +50 s, not at the first +30 s:
			// at +40 s it still holds the connection's only place.
			name: "adding the same message again refreshes its due time", maxTotal: 8, maxConn: 1,
			ops: []op{
				{kind: "add", seq: 1, conn: "a", now: t0, wantOK: true},
				{kind: "add", seq: 1, conn: "a", now: t0.Add(20 * time.Second), wantOK: true},
				{kind: "add", seq: 2, conn: "a", now: t0.Add(40 * time.Second), wantOK: false},
			},
			wantLen: 1, wantConnA: 1,
		},
		{
			name: "removing an unknown message is a no-op", maxTotal: 8, maxConn: 4,
			ops: []op{
				{kind: "remove", seq: 9},
				{kind: "add", seq: 1, conn: "a", now: t0, wantOK: true},
				{kind: "remove", seq: 9},
			},
			wantLen: 1, wantConnA: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d delayedSet
			for i, o := range tt.ops {
				switch o.kind {
				case "add":
					// Due 30 s after "now": t0 entries are due before `later`.
					ok, _ := d.add(o.seq, o.conn, o.now.Add(30*time.Second), o.now, tt.maxTotal, tt.maxConn)
					if ok != o.wantOK {
						t.Fatalf("op %d: add(%d, %q) = %v, want %v", i, o.seq, o.conn, ok, o.wantOK)
					}
				case "remove":
					d.remove(o.seq)
				}
			}
			if d.len() != tt.wantLen {
				t.Errorf("len = %d, want %d", d.len(), tt.wantLen)
			}
			if d.byConn["a"] != tt.wantConnA {
				t.Errorf("waiting for a = %d, want %d", d.byConn["a"], tt.wantConnA)
			}
		})
	}
}

// The caps' defaults follow MaxAckPending and never reach zero.
func TestSubscribeDelayedCapDefaults(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()
	tests := []struct {
		name                       string
		maxAckPending              int
		maxDelayed, maxPerConn     int
		wantDelayed, wantPerConnec int
	}{
		{"default MaxAckPending", 0, 0, 0, 8, 4},
		{"one in flight (remote agent)", 1, 0, 0, 1, 1},
		{"explicit caps kept", 32, 5, 2, 5, 2},
		{"per-connection never above the total", 32, 3, 10, 3, 3},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub, err := Subscribe(js, SubscriberOpts{
				DurableName:             fmt.Sprintf("caps-%d", i),
				MaxAckPending:           tt.maxAckPending,
				MaxDelayed:              tt.maxDelayed,
				MaxDelayedPerConnection: tt.maxPerConn,
			}, func(_ context.Context, _ *nats.Msg) error { return nil })
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			defer sub.Stop()
			if sub.opts.MaxDelayed != tt.wantDelayed || sub.opts.MaxDelayedPerConnection != tt.wantPerConnec {
				t.Errorf("caps = (%d, %d), want (%d, %d)", sub.opts.MaxDelayed, sub.opts.MaxDelayedPerConnection,
					tt.wantDelayed, tt.wantPerConnec)
			}
		})
	}
}

// #811 (review of #810): a message whose last delivery fails never comes
// back, so it must not take a place in the delayed set. Before the fix a
// panic on the last delivery went through retry, which added it: it held
// one of the MaxDelayed places until its due time (up to 2 min in
// production). A handler error on the last delivery never did; both now take
// the DLQ path.
func TestLastDeliveryHoldsNoDelayedSlot(t *testing.T) {
	ms := time.Millisecond
	tests := []struct {
		name    string
		handler Handler
	}{
		{"handler error", func(context.Context, *nats.Msg) error { return errors.New("target down") }},
		{"handler panics", func(context.Context, *nats.Msg) error { panic("connector bug") }},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, js, cleanup := startServer(t)
			defer cleanup()
			var attempts atomic.Int32
			// The last step is long, so a leftover entry is still due when
			// the test looks.
			sub, err := Subscribe(js, SubscriberOpts{
				DurableName: fmt.Sprintf("last-delivery-slot-%d", i),
				Backoff:     []time.Duration{50 * ms, 50 * ms, 50 * ms, 50 * ms, time.Minute},
			}, func(ctx context.Context, m *nats.Msg) error {
				attempts.Add(1)
				return tt.handler(ctx, m)
			})
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			if err := NewPublisher(js).Publish(context.Background(), "tenant-a", "conn-a", "m-1", []byte("x")); err != nil {
				t.Fatalf("publish: %v", err)
			}
			waitFor(t, func() bool { return attempts.Load() >= MaxDeliveryAttempts }, 10*time.Second)
			time.Sleep(300 * ms) // the DLQ publish, or a sixth delivery that must not come
			sub.Stop()           // the loop has exited: reading delayed is safe now
			if n := attempts.Load(); n != MaxDeliveryAttempts {
				t.Errorf("delivered %d times, want %d", n, MaxDeliveryAttempts)
			}
			if info, err := js.StreamInfo(DLQStreamName); err != nil {
				t.Errorf("DLQ info: %v", err)
			} else if info.State.Msgs != 1 {
				t.Errorf("the DLQ holds %d messages, want 1", info.State.Msgs)
			}
			if n := sub.delayed.len(); n != 0 {
				t.Errorf("%d messages still counted as waiting out a retry delay, want 0", n)
			}
		})
	}
}
