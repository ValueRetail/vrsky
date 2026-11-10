package sdk_test

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// #811: what happens to a message whose fifth and last delivery fails, when
// copying it to the DLQ fails too, or when the handler panics on it.
//
// nats-server makes no delivery past MaxDeliver: a message NAK'd on its last
// delivery is dropped from the durable's pending set on the next pull
// (getNextMsg, dc > maxdc), and one left unacked when its ack wait runs out
// (checkPending → hasMaxDeliveries). Before the fix the subscriber NAK'd in
// both cases, so the message was in neither the durable nor the DLQ: it sat
// in VRSKY_DATA, which nothing reads by sequence, until it aged out.
//
// The DLQ is broken by lowering VRSKY_DLQ's MaxMsgSize, which makes
// JetStream refuse the publish at once. EnsureStreams does not compare that
// field, so the subscriber's own first EnsureStreams (lazily, on its first
// DLQ publish) leaves it alone. The harness's core subscription on vrsky.dlq.>
// sees every publish, refused or not, so it counts the attempts; what reached
// the DLQ is read from the stream.
func TestProducer_LastDeliveryReachesTheDLQ(t *testing.T) {
	ms := time.Millisecond
	// Delivery 5 comes 600 ms after delivery 4, and its ack wait is the last
	// step, 600 ms, which the heartbeat keeps open while the DLQ is retried.
	backoff := []time.Duration{100 * ms, 100 * ms, 100 * ms, 600 * ms}
	tests := []struct {
		name string
		// fail answers delivery n (from 1).
		fail func(n int) error
		// dlqDown breaks the DLQ before the message is published, and
		// restoreAfter mends it that long after delivery 5 started (0: never).
		dlqDown      bool
		restoreAfter time.Duration
		wantInDLQ    bool
		wantLastErr  string // substring of X-Vrsky-Last-Error
		// wantPublishes is how many DLQ publishes the subscriber makes.
		wantPublishes int
		// wantGaveUp: vrsky_dlq_publish_failed_total went up by one.
		wantGaveUp bool
	}{
		{
			name:          "handler error, DLQ up",
			fail:          func(int) error { return errTarget },
			wantInDLQ:     true,
			wantLastErr:   "target down",
			wantPublishes: 1,
		},
		{
			// Before: no DLQ copy; the panic took the retry path, whose NAK
			// nats-server never answers on the last delivery.
			name:          "handler panics on every delivery",
			fail:          func(int) error { panic("connector bug") },
			wantInDLQ:     true,
			wantLastErr:   "handler panicked: connector bug",
			wantPublishes: 1,
		},
		{
			name: "handler panics on the last delivery only",
			fail: func(n int) error {
				if n == messaging.MaxDeliveryAttempts {
					panic("connector bug on the last try")
				}
				return errTarget
			},
			wantInDLQ:     true,
			wantLastErr:   "handler panicked: connector bug on the last try",
			wantPublishes: 1,
		},
		{
			// Before: one refused publish, a NAK, and the message was gone.
			// Now the publish is retried in place and the second or third
			// try gets through.
			name:          "DLQ refuses, then recovers",
			fail:          func(int) error { return errTarget },
			dlqDown:       true,
			restoreAfter:  300 * ms,
			wantInDLQ:     true,
			wantLastErr:   "target down",
			wantPublishes: 2,
		},
		{
			name:          "DLQ refuses throughout",
			fail:          func(int) error { return errTarget },
			dlqDown:       true,
			wantInDLQ:     false,
			wantPublishes: messaging.DLQPublishAttempts,
			wantGaveUp:    true,
		},
		{
			name:          "panic and the DLQ refuses throughout",
			fail:          func(int) error { panic("connector bug") },
			dlqDown:       true,
			wantInDLQ:     false,
			wantPublishes: messaging.DLQPublishAttempts,
			wantGaveUp:    true,
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			durable := "test-producer-last-delivery-" + string(rune('a'+i))
			var (
				js       nats.JetStreamContext
				restored atomic.Bool
				attempts atomic.Int32
				lastAt   atomic.Int64
			)
			p := &failingProducer{fail: func(string) error {
				n := int(attempts.Add(1))
				if n == messaging.MaxDeliveryAttempts {
					lastAt.Store(time.Now().UnixNano())
					if tt.dlqDown && tt.restoreAfter > 0 {
						time.AfterFunc(tt.restoreAfter, func() {
							setDLQMaxMsgSize(t, js, -1)
							restored.Store(true)
						})
					}
				}
				return tt.fail(n)
			}}
			h := harness.NewProducerHarness(t, p, harness.Options{Name: durable, Backoff: backoff})
			var err error
			if js, err = h.NATS().JetStream(); err != nil {
				t.Fatalf("jetstream: %v", err)
			}
			var publishes atomic.Int32
			sub, err := h.NATS().Subscribe("vrsky.dlq.tenant-a.>", func(*nats.Msg) { publishes.Add(1) })
			if err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			t.Cleanup(func() { _ = sub.Unsubscribe() })
			if tt.dlqDown {
				setDLQMaxMsgSize(t, js, 1)
			}
			gaveUpBefore := dlqPublishFailed(t, durable)

			env := envelope.New()
			env.TenantID, env.IntegrationID = "tenant-a", "conn-a"
			env.Payload = []byte(`{"n":1}`)
			h.Publish(t, env)

			// Five attempts take about a second; the DLQ retries up to a few
			// more. Then give the ack wait time to run out, and the server
			// time to show a sixth delivery if it were ever going to.
			deadline := time.Now().Add(20 * time.Second)
			for attempts.Load() < messaging.MaxDeliveryAttempts && time.Now().Before(deadline) {
				time.Sleep(20 * ms)
			}
			if n := attempts.Load(); n < messaging.MaxDeliveryAttempts {
				t.Fatalf("delivered %d times within 20 s, want %d", n, messaging.MaxDeliveryAttempts)
			}
			settle := time.Unix(0, lastAt.Load()).Add(messaging.DLQPublishTimeout + 5*time.Second)
			for time.Now().Before(settle) && !done(tt.wantGaveUp, gaveUpBefore, durable, t, js, tt.wantInDLQ) {
				time.Sleep(50 * ms)
			}
			if tt.wantGaveUp {
				// Given up on, the message is left unacked, not NAK'd: the
				// durable still counts it until its ack wait (600 ms here,
				// from the last heartbeat) runs out. A NAK would have it
				// dropped on the next pull, within a poll.
				if ci, err := js.ConsumerInfo(messaging.MainStreamName, durable); err != nil {
					t.Errorf("consumer info: %v", err)
				} else if ci.NumAckPending != 1 {
					t.Errorf("right after giving up the durable has %d unacked, want 1 (left unacked)", ci.NumAckPending)
				}
			}
			time.Sleep(backoff[len(backoff)-1] + 500*ms)

			if n := attempts.Load(); n != messaging.MaxDeliveryAttempts {
				t.Errorf("delivered %d times, want %d", n, messaging.MaxDeliveryAttempts)
			}
			if tt.restoreAfter > 0 && !restored.Load() {
				t.Fatal("the DLQ was never restored")
			}
			copies, last := dlqCopies(t, js, "tenant-a", "conn-a")
			where := whereIsIt(js, durable)
			t.Logf("DLQ copies: %d, DLQ publishes: %d; %s", copies, publishes.Load(), where)
			if tt.wantInDLQ {
				if copies != 1 {
					t.Fatalf("%d copies in the DLQ, want 1 (%s)", copies, where)
				}
				if got := last.Header.Get("X-Vrsky-Last-Error"); !strings.Contains(got, tt.wantLastErr) {
					t.Errorf("X-Vrsky-Last-Error = %q, want it to contain %q", got, tt.wantLastErr)
				}
				if got, err := envelope.Unmarshal(last.Data); err != nil || got.ID != env.ID {
					t.Errorf("the DLQ copy is not envelope %s (%v)", env.ID, err)
				}
			} else if copies != 0 {
				t.Errorf("%d copies in the DLQ, want none (it refused them)", copies)
			}
			if got := int(publishes.Load()); got != tt.wantPublishes {
				t.Errorf("%d DLQ publishes, want %d", got, tt.wantPublishes)
			}
			if got := dlqPublishFailed(t, durable) - gaveUpBefore; got != b2f(tt.wantGaveUp) {
				t.Errorf("vrsky_dlq_publish_failed_total went up by %v, want %v", got, b2f(tt.wantGaveUp))
			}
			// Whatever happened, the payload is still in the data stream at
			// its sequence: that is what an operator recovers it from.
			if _, err := js.GetMsg(messaging.MainStreamName, 1); err != nil {
				t.Errorf("the message is gone from %s: %v", messaging.MainStreamName, err)
			}
		})
	}
}

var errTarget = errorString("target down")

type errorString string

func (e errorString) Error() string { return string(e) }

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// done reports whether the outcome a case waits for has happened.
func done(wantGaveUp bool, before float64, durable string, t *testing.T, js nats.JetStreamContext, wantInDLQ bool) bool {
	if wantGaveUp {
		return dlqPublishFailed(t, durable) > before
	}
	if wantInDLQ {
		n, _ := dlqCopies(t, js, "tenant-a", "conn-a")
		return n > 0
	}
	return false
}

var dlqConfigMu sync.Mutex

// setDLQMaxMsgSize sets VRSKY_DLQ's MaxMsgSize: 1 refuses every publish, -1
// is unlimited again.
func setDLQMaxMsgSize(t *testing.T, js nats.JetStreamContext, size int32) {
	dlqConfigMu.Lock()
	defer dlqConfigMu.Unlock()
	info, err := js.StreamInfo(messaging.DLQStreamName)
	if err != nil {
		t.Errorf("DLQ info: %v", err)
		return
	}
	cfg := info.Config
	cfg.MaxMsgSize = size
	if _, err := js.UpdateStream(&cfg); err != nil {
		t.Errorf("DLQ update: %v", err)
	}
}

// dlqCopies counts the messages VRSKY_DLQ holds for a connection, and
// returns the last.
func dlqCopies(t *testing.T, js nats.JetStreamContext, tenant, conn string) (int, *nats.RawStreamMsg) {
	subj := messaging.DLQSubject(tenant, conn)
	info, err := js.StreamInfo(messaging.DLQStreamName, &nats.StreamInfoRequest{SubjectsFilter: subj})
	if err != nil {
		t.Errorf("DLQ info: %v", err)
		return 0, nil
	}
	n := int(info.State.Subjects[subj])
	if n == 0 {
		return 0, nil
	}
	m, err := js.GetLastMsg(messaging.DLQStreamName, subj)
	if err != nil {
		t.Errorf("DLQ last message: %v", err)
	}
	return n, m
}

// whereIsIt describes the durable's view of the message, for the log.
func whereIsIt(js nats.JetStreamContext, durable string) string {
	ci, err := js.ConsumerInfo(messaging.MainStreamName, durable)
	if err != nil {
		return "consumer info: " + err.Error()
	}
	return fmt.Sprintf("durable: ack floor at stream seq %d, %d unacked, %d redelivered, %d not yet delivered",
		ci.AckFloor.Stream, ci.NumAckPending, ci.NumRedelivered, ci.NumPending)
}

// dlqPublishFailed reads vrsky_dlq_publish_failed_total for one worker.
func dlqPublishFailed(t *testing.T, worker string) float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Errorf("gather: %v", err)
		return 0
	}
	var sum float64
	for _, mf := range mfs {
		if mf.GetName() != "vrsky_dlq_publish_failed_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "worker" && l.GetValue() == worker {
					sum += m.GetCounter().GetValue()
				}
			}
		}
	}
	return sum
}
