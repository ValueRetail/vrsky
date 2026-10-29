package messaging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// Tests for #494: Halt and Stop end a waiting fetch at once, and what happens
// to messages does not change.

// stopBound is how long Stop on an idle subscriber may take. It used to wait
// out the 2 s fetch.
const stopBound = 200 * time.Millisecond

// strandBound is how many messages per race subtest may have a delivery
// spent in the moment a halt withdraws its pull request, and requeueBound how
// soon such a message must reach the next subscriber. See
// TestHaltRaceWithPublish.
const (
	strandBound  = 2
	requeueBound = 500 * time.Millisecond
)

// stopOpts is a subscriber that only sees its own pipeline's messages, so
// every durable in a test is independent of the others on the same server.
func stopOpts(durable, conn string, maxAckPending int) SubscriberOpts {
	return SubscriberOpts{
		DurableName:   durable,
		FilterSubject: DataSubject("tenant-stop", conn),
		MaxAckPending: maxAckPending,
	}
}

// publishStop publishes one message to the pipeline conn. msgID must be unique
// per server: the main stream drops a repeated ID as a duplicate.
func publishStop(js nats.JetStreamContext, conn, msgID, body string) error {
	return NewPublisher(js).Publish(context.Background(), "tenant-stop", conn, msgID, []byte(body))
}

// waitFetching blocks until the durable has a pull request waiting on the
// server, i.e. the subscriber's loop is inside a fetch with nothing to return.
func waitFetching(t *testing.T, js nats.JetStreamContext, durable string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ci, err := js.ConsumerInfo(MainStreamName, durable)
		if err == nil && ci.NumWaiting > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s: no fetch waiting on the server within 5s", durable)
}

func consumerInfo(t *testing.T, js nats.JetStreamContext, durable string) *nats.ConsumerInfo {
	t.Helper()
	ci, err := js.ConsumerInfo(MainStreamName, durable)
	if err != nil {
		t.Fatalf("ConsumerInfo %s: %v", durable, err)
	}
	return ci
}

// waitSettled polls until the durable has nothing pending and nothing
// unacked (acks are sent asynchronously), and returns the final info.
func waitSettled(t *testing.T, js nats.JetStreamContext, durable string) *nats.ConsumerInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ci := consumerInfo(t, js, durable)
		if ci.NumAckPending == 0 && ci.NumPending == 0 {
			return ci
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not settle within 5s: NumAckPending=%d NumPending=%d NumRedelivered=%d",
				durable, ci.NumAckPending, ci.NumPending, ci.NumRedelivered)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func dlqMsgs(t *testing.T, js nats.JetStreamContext) uint64 {
	t.Helper()
	info, err := js.StreamInfo(DLQStreamName)
	if err != nil {
		t.Fatalf("DLQ info: %v", err)
	}
	return info.State.Msgs
}

// delivery is what a handler saw of one message.
type delivery struct {
	data         string
	numDelivered uint64
}

// receiveOne subscribes on the durable, waits up to max for one message, acks
// it, and stops. ok is false when nothing arrived.
func receiveOne(t *testing.T, js nats.JetStreamContext, opts SubscriberOpts, max time.Duration) (d delivery, ok bool) {
	t.Helper()
	got := make(chan delivery, 1)
	sub, err := Subscribe(js, opts, func(_ context.Context, m *nats.Msg) error {
		var n uint64
		if md, err := m.Metadata(); err == nil {
			n = md.NumDelivered
		}
		select {
		case got <- delivery{data: string(m.Data), numDelivered: n}:
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe %s: %v", opts.DurableName, err)
	}
	defer sub.Stop()
	select {
	case d = <-got:
		return d, true
	case <-time.After(max):
		return delivery{}, false
	}
}

// TestStopIdleSubscriberIsPrompt: Stop, and Halt followed by Stop, on a
// subscriber whose loop is waiting in a fetch return well inside stopBound.
// The rounds stop at different points of the 2 s fetch so one lucky timing
// can't pass it.
func TestStopIdleSubscriberIsPrompt(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	cases := []struct {
		name string
		stop func(*Subscriber)
	}{
		{"Stop", func(s *Subscriber) { s.Stop() }},
		{"HaltThenStop", func(s *Subscriber) { s.Halt(); s.Stop() }},
	}
	// How long after the fetch is seen waiting on the server to stop.
	intoFetch := []time.Duration{20 * time.Millisecond, 20 * time.Millisecond, 300 * time.Millisecond, time.Second}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for r, wait := range intoFetch {
				durable := fmt.Sprintf("stop-idle-%s-%d", tc.name, r)
				sub, err := Subscribe(js, stopOpts(durable, durable, 0),
					func(context.Context, *nats.Msg) error {
						t.Error("idle subscriber got a message")
						return nil
					})
				if err != nil {
					t.Fatalf("Subscribe: %v", err)
				}
				waitFetching(t, js, durable)
				time.Sleep(wait)

				start := time.Now()
				tc.stop(sub)
				took := time.Since(start)
				t.Logf("round %d (%v into the fetch): %v", r, wait, took)
				if took > stopBound {
					t.Errorf("round %d (%v into the fetch): stop took %v, want <= %v", r, wait, took, stopBound)
				}
			}
		})
	}
}

// TestHaltDuringHandlerKeepsDeliverySemantics: a message being handled when
// Halt comes is acked or NAK'd exactly as it would be without the halt. One
// handler call, no DLQ, and the durable's state afterwards is what that one
// ack or NAK leaves behind.
func TestHaltDuringHandlerKeepsDeliverySemantics(t *testing.T) {
	cases := []struct {
		name   string
		result error
		acked  bool
	}{
		{"ack", nil, true},
		{"nak", errors.New("handler failed"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, js, cleanup := startServer(t)
			defer cleanup()

			durable := "stop-inflight-" + tc.name
			opts := stopOpts(durable, durable, 1)

			holding := make(chan struct{}, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			doRelease := func() { releaseOnce.Do(func() { close(release) }) }
			defer doRelease() // never leave the handler blocked if the test bails

			var calls int32
			sub, err := Subscribe(js, opts, func(context.Context, *nats.Msg) error {
				atomic.AddInt32(&calls, 1)
				select {
				case holding <- struct{}{}:
				default:
				}
				<-release
				return tc.result
			})
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			if err := publishStop(js, durable, "m-1", "held"); err != nil {
				t.Fatalf("publish: %v", err)
			}
			select {
			case <-holding:
			case <-time.After(5 * time.Second):
				doRelease()
				sub.Stop()
				t.Fatal("handler never got the message")
			}

			sub.Halt()
			doRelease()
			// Room for a loop that ignored the halt to fetch again.
			time.Sleep(100 * time.Millisecond)
			sub.Stop()

			if got := atomic.LoadInt32(&calls); got != 1 {
				t.Errorf("handler ran %d times, want 1", got)
			}
			if n := dlqMsgs(t, js); n != 0 {
				t.Errorf("DLQ has %d messages, want 0", n)
			}

			if tc.acked {
				ci := waitSettled(t, js, durable)
				if ci.NumRedelivered != 0 || ci.Delivered.Consumer != 1 {
					t.Errorf("after ack: NumRedelivered=%d Delivered.Consumer=%d, want 0 and 1",
						ci.NumRedelivered, ci.Delivered.Consumer)
				}
				// Nothing comes back to the next subscriber, not even after
				// AckWait (1s with the default backoff).
				if d, ok := receiveOne(t, js, opts, 1500*time.Millisecond); ok {
					t.Errorf("acked message redelivered to the next subscriber: %+v", d)
				}
				return
			}

			// NAK'd: the next subscriber gets it on its second delivery. A
			// third would mean the stop itself spent one.
			d, ok := receiveOne(t, js, opts, 5*time.Second)
			if !ok {
				t.Fatal("NAK'd message never reached the next subscriber")
			}
			if d.data != "held" || d.numDelivered != 2 {
				t.Errorf("next subscriber got %q with NumDelivered=%d, want %q with 2",
					d.data, d.numDelivered, "held")
			}
			if n := dlqMsgs(t, js); n != 0 {
				t.Errorf("DLQ has %d messages after redelivery, want 0", n)
			}
		})
	}
}

// TestHaltRaceWithPublish: a message published around Halt, while the
// subscriber's fetch is still waiting on the server, is never lost and never
// dead-lettered by the stop.
//
// "before" publishes from the test goroutine and halts a moment later (zero
// included), so the message is on its way into the open fetch when Halt cuts
// it. "after" publishes after Halt, with a short delay (zero included).
// "concurrent" publishes from another goroutine released together with Halt,
// with a little jitter on one side or the other. "gap" halts, publishes 20 ms
// later and stops 100 ms after that, the way the remote-agent gateway halts
// early and stops once its handlers are done.
//
// In "gap" the halt withdrew the pull request long before the publish, so the
// message must be left untouched: no delivery spent on it. Without that, a
// halted subscriber would keep receiving into a fetch nobody reads until Stop.
//
// In the other modes the publish lands within about a millisecond of the
// withdrawal, and there the server can still count a delivery to the request
// and then find nobody to publish it to (nats-server queues a delivery after
// counting it; see plans/issue-494-stop-cuts-fetch.md). On this single server
// it notices and redelivers at once, with NumDelivered 2 on a first delivery.
// That is allowed here, rarely: at most strandBound per subtest. What fails is
// a message that comes back only after AckWait, which means the client got it
// and dropped it (the halt's withdrawal or the reclaim is broken), and
// anything worse: lost, a second wasted delivery, the DLQ. In a cluster with
// the consumer leader on another node the same race does cost an AckWait;
// that is why #494's PR waits for a human.
func TestHaltRaceWithPublish(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	delays := []time.Duration{
		0, 0, 0, 0,
		50 * time.Microsecond, 100 * time.Microsecond, 250 * time.Microsecond,
		500 * time.Microsecond, time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond,
	}
	const iterations = 16

	// MaxAckPending 1 and the default 32: the subscriber's fetch batch
	// follows it, and the server answers a batch of 1 and a batch of 32
	// differently (a batch of 32 keeps waiting for more until expiry).
	for _, batch := range []int{1, 32} {
		for _, mode := range []string{"before", "after", "concurrent", "gap"} {
			t.Run(fmt.Sprintf("%s/batch%d", mode, batch), func(t *testing.T) {
				var handled, left, stranded int
				iterations := iterations
				if mode == "gap" {
					iterations = 4
				}
				for i := 0; i < iterations; i++ {
					durable := fmt.Sprintf("stop-race-%s-%d-%d", mode, batch, i)
					opts := stopOpts(durable, durable, batch)

					var calls int32
					var firstDelivered atomic.Uint64
					sub, err := Subscribe(js, opts, func(_ context.Context, m *nats.Msg) error {
						if atomic.AddInt32(&calls, 1) == 1 {
							if md, err := m.Metadata(); err == nil {
								firstDelivered.Store(md.NumDelivered)
							}
						}
						return nil
					})
					if err != nil {
						t.Fatalf("Subscribe: %v", err)
					}
					waitFetching(t, js, durable)

					delay := delays[i%len(delays)]
					if i >= len(delays) && delay > 0 {
						delay += rand.N(200 * time.Microsecond)
					}
					var desc string
					switch mode {
					case "before":
						desc = fmt.Sprintf("Halt %v after publish", delay)
						if err := publishStop(js, durable, durable, durable); err != nil {
							sub.Stop()
							t.Fatalf("iter %d: publish: %v", i, err)
						}
						if delay > 0 {
							time.Sleep(delay)
						}
						sub.Halt()
					case "after":
						desc = fmt.Sprintf("publish %v after Halt", delay)
						sub.Halt()
						if delay > 0 {
							time.Sleep(delay)
						}
						if err := publishStop(js, durable, durable, durable); err != nil {
							sub.Stop()
							t.Fatalf("iter %d: publish: %v", i, err)
						}
					case "concurrent":
						// Even iterations: no jitter at all. Odd: up to
						// 1ms on the publisher's side or on Halt's.
						var pubJitter, haltJitter time.Duration
						if i%2 == 1 {
							j := rand.N(time.Millisecond)
							if i%4 == 1 {
								pubJitter = j
							} else {
								haltJitter = j
							}
						}
						desc = fmt.Sprintf("concurrent, publish jitter %v, halt jitter %v", pubJitter, haltJitter)
						start := make(chan struct{})
						pubErr := make(chan error, 1)
						go func() {
							<-start
							if pubJitter > 0 {
								time.Sleep(pubJitter)
							}
							pubErr <- publishStop(js, durable, durable, durable)
						}()
						close(start)
						if haltJitter > 0 {
							time.Sleep(haltJitter)
						}
						sub.Halt()
						if err := <-pubErr; err != nil {
							sub.Stop()
							t.Fatalf("iter %d: publish: %v", i, err)
						}
					case "gap":
						desc = "publish 20ms after Halt, Stop 100ms later"
						sub.Halt()
						time.Sleep(20 * time.Millisecond)
						if err := publishStop(js, durable, durable, durable); err != nil {
							sub.Stop()
							t.Fatalf("iter %d: publish: %v", i, err)
						}
						time.Sleep(100 * time.Millisecond)
					}
					sub.Stop()

					switch n := atomic.LoadInt32(&calls); n {
					case 1:
						handled++
						if mode == "gap" {
							t.Errorf("iter %d (%s): a subscriber halted 20ms before the publish handled the message", i, desc)
						}
						if d := firstDelivered.Load(); d != 1 {
							t.Errorf("iter %d (%s): halting subscriber got it with NumDelivered=%d, want 1", i, desc, d)
						}
						ci := waitSettled(t, js, durable)
						if ci.NumRedelivered != 0 || ci.Delivered.Consumer != 1 {
							t.Errorf("iter %d (%s): after ack NumRedelivered=%d Delivered.Consumer=%d, want 0 and 1",
								i, desc, ci.NumRedelivered, ci.Delivered.Consumer)
						}
					case 0:
						left++
						// A delivery into the dead fetch shows up here as
						// ack-pending instead of pending.
						time.Sleep(50 * time.Millisecond)
						ci := consumerInfo(t, js, durable)
						untouched := ci.NumAckPending == 0 && ci.NumPending == 1 && ci.Delivered.Consumer == 0
						if !untouched {
							stranded++
							if mode == "gap" {
								t.Errorf("iter %d (%s): after stop NumAckPending=%d NumPending=%d Delivered.Consumer=%d, want 0, 1, 0: a halted subscriber still took a delivery",
									i, desc, ci.NumAckPending, ci.NumPending, ci.Delivered.Consumer)
							}
						}
						// The next subscriber gets it at once: on its first
						// delivery if untouched, on its second if the server
						// counted one into the withdrawn request. Only a
						// message the client received and dropped waits for
						// AckWait (1s).
						want := uint64(1)
						if !untouched {
							want = 2
						}
						start := time.Now()
						d, ok := receiveOne(t, js, opts, 3*time.Second)
						took := time.Since(start)
						switch {
						case !ok:
							t.Errorf("iter %d (%s): message not delivered to the next subscriber within 3s", i, desc)
						case !untouched && took >= requeueBound:
							t.Errorf("iter %d (%s): next subscriber got it after %v, want under %v: it waited for AckWait, so the halting client received it and dropped it",
								i, desc, took, requeueBound)
						case d.numDelivered != want:
							t.Errorf("iter %d (%s): next subscriber got it with NumDelivered=%d, want %d", i, desc, d.numDelivered, want)
						case d.data != durable:
							t.Errorf("iter %d (%s): next subscriber got %q, want %q", i, desc, d.data, durable)
						}
					default:
						t.Errorf("iter %d (%s): halting subscriber handled it %d times, want at most 1", i, desc, n)
					}
				}
				t.Logf("handled by the halting subscriber: %d, left for the next: %d (of which a delivery was spent in the cut fetch: %d)",
					handled, left, stranded)
				// On this single server the window is about a millisecond
				// wide and, measured, catches a few messages in a thousand
				// (in a cluster it is the UNSUB's trip over the route).
				// More means the halt no longer withdraws the request, or
				// what a cut fetch returned is being dropped.
				if stranded > strandBound {
					t.Errorf("%d of %d messages had a delivery spent in a cut fetch, want at most %d",
						stranded, iterations, strandBound)
				}
				if n := dlqMsgs(t, js); n != 0 {
					t.Errorf("DLQ has %d messages, want 0", n)
				}
			})
		}
	}
}

// settledGoroutines waits until the goroutine count holds still and returns it.
func settledGoroutines() int {
	n := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		m := runtime.NumGoroutine()
		if m == n {
			return n
		}
		n = m
	}
	return n
}

// TestStopLeavesNoGoroutines: after Stop the process is back to the goroutine
// count it had before Subscribe: for an idle stop, for one that cut a fetch
// short after Halt, and for one halted while a handler held a message.
func TestStopLeavesNoGoroutines(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	noop := func(context.Context, *nats.Msg) error { return nil }

	// Each scenario runs one subscriber from Subscribe to Stop. round makes
	// the message IDs unique across the warm-up and the measured run.
	cases := []struct {
		name string
		run  func(t *testing.T, opts SubscriberOpts, round int)
	}{
		{"idle", func(t *testing.T, opts SubscriberOpts, _ int) {
			sub, err := Subscribe(js, opts, noop)
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			waitFetching(t, js, opts.DurableName)
			sub.Stop()
		}},
		{"halted-fetch", func(t *testing.T, opts SubscriberOpts, _ int) {
			sub, err := Subscribe(js, opts, noop)
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			waitFetching(t, js, opts.DurableName)
			sub.Halt()
			time.Sleep(20 * time.Millisecond)
			sub.Stop()
		}},
		{"halted-in-flight", func(t *testing.T, opts SubscriberOpts, round int) {
			holding := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			doRelease := func() { once.Do(func() { close(release) }) }
			defer doRelease()
			sub, err := Subscribe(js, opts, func(context.Context, *nats.Msg) error {
				select {
				case holding <- struct{}{}:
				default:
				}
				<-release
				return nil
			})
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			id := fmt.Sprintf("%s-%d", opts.DurableName, round)
			if err := publishStop(js, opts.DurableName, id, id); err != nil {
				doRelease()
				sub.Stop()
				t.Fatalf("publish: %v", err)
			}
			select {
			case <-holding:
			case <-time.After(5 * time.Second):
				doRelease()
				sub.Stop()
				t.Fatal("handler never got the message")
			}
			sub.Halt()
			doRelease()
			sub.Stop()
			waitSettled(t, js, opts.DurableName)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			durable := "stop-leak-" + tc.name
			opts := stopOpts(durable, durable, 1)

			// The embedded server runs goroutines per consumer for as long as
			// the durable exists, and the connection may start some on first
			// use. Run the scenario once to get all of those into the
			// baseline, then wait for any pull request it left behind.
			tc.run(t, opts, 0)
			waitFor(t, func() bool {
				ci, err := js.ConsumerInfo(MainStreamName, durable)
				return err == nil && ci.NumWaiting == 0
			}, 5*time.Second)

			base := settledGoroutines()
			tc.run(t, opts, 1)

			deadline := time.Now().Add(time.Second)
			for runtime.NumGoroutine() > base {
				if time.Now().After(deadline) {
					var buf bytes.Buffer
					_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
					t.Fatalf("goroutines after Stop: %d, before Subscribe: %d\n%s",
						runtime.NumGoroutine(), base, buf.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

// warnRecorder keeps every record at Warn or above.
type warnRecorder struct {
	mu   sync.Mutex
	recs []string
}

func (h *warnRecorder) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }

func (h *warnRecorder) Handle(_ context.Context, r slog.Record) error {
	line := r.Level.String() + " " + r.Message
	r.Attrs(func(a slog.Attr) bool {
		line += " " + a.String()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, line)
	h.mu.Unlock()
	return nil
}

func (h *warnRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *warnRecorder) WithGroup(string) slog.Handler      { return h }

func (h *warnRecorder) records() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.recs...)
}

// TestHaltCutFetchLogsNothing: a fetch ended by Halt or Stop is a normal
// shutdown, not a failure. Nothing at Warn or Error.
func TestHaltCutFetchLogsNothing(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	cases := []struct {
		name string
		stop func(*Subscriber)
	}{
		{"Stop", func(s *Subscriber) { s.Stop() }},
		{"HaltThenStop", func(s *Subscriber) { s.Halt(); s.Stop() }},
		// Long enough for a loop that treats the cut fetch as a failure to
		// log it and start its back-off before Stop is reached.
		{"HaltWaitStop", func(s *Subscriber) { s.Halt(); time.Sleep(100 * time.Millisecond); s.Stop() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &warnRecorder{}
			durable := "stop-log-" + tc.name
			opts := stopOpts(durable, durable, 0)
			opts.Logger = slog.New(rec)
			sub, err := Subscribe(js, opts, func(context.Context, *nats.Msg) error { return nil })
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			waitFetching(t, js, durable)
			time.Sleep(20 * time.Millisecond)
			tc.stop(sub)
			if recs := rec.records(); len(recs) != 0 {
				t.Errorf("stop logged %d record(s) at Warn or above:\n%v", len(recs), recs)
			}
		})
	}
}
