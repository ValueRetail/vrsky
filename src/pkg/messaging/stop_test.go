package messaging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// Tests for #494: Halt and Stop wait out at most one short poll, and a stop
// never strands a message.

// stopBound is how long Stop on an idle subscriber may take: one poll
// (pollWait, 150 ms) and room for a loaded machine. It used to wait out a 2 s
// fetch.
const stopBound = 400 * time.Millisecond

// stopHang is how long a stop may take before a test calls it hung.
const stopHang = 5 * time.Second

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

// timedStop runs stop and returns how long it took. A stop still running after
// stopHang fails the test at once instead of hanging it.
func timedStop(t *testing.T, stop func()) time.Duration {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		stop()
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(stopHang):
		t.Fatalf("stop did not return within %v", stopHang)
		return 0
	}
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
		time.Sleep(2 * time.Millisecond)
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

// receiveN subscribes on the durable, collects (and acks) up to n messages
// within max, and stops.
func receiveN(t *testing.T, js nats.JetStreamContext, opts SubscriberOpts, n int, max time.Duration) []delivery {
	t.Helper()
	var mu sync.Mutex
	var got []delivery
	all := make(chan struct{})
	sub, err := Subscribe(js, opts, func(_ context.Context, m *nats.Msg) error {
		var nd uint64
		if md, err := m.Metadata(); err == nil {
			nd = md.NumDelivered
		}
		mu.Lock()
		defer mu.Unlock()
		got = append(got, delivery{data: string(m.Data), numDelivered: nd})
		if len(got) == n {
			close(all)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe %s: %v", opts.DurableName, err)
	}
	select {
	case <-all:
	case <-time.After(max):
	}
	timedStop(t, sub.Stop)
	mu.Lock()
	defer mu.Unlock()
	return append([]delivery(nil), got...)
}

// receiveOne is receiveN for one message. ok is false when nothing arrived.
func receiveOne(t *testing.T, js nats.JetStreamContext, opts SubscriberOpts, max time.Duration) (d delivery, ok bool) {
	t.Helper()
	got := receiveN(t, js, opts, 1, max)
	if len(got) == 0 {
		return delivery{}, false
	}
	return got[0], true
}

// TestStopIdleSubscriberIsPrompt: Stop, and Halt followed by Stop, on a
// subscriber whose loop is waiting in a fetch return within one poll and a
// margin. The rounds stop at different points of the poll, and one several
// polls in, so one lucky timing can't pass it.
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
	// How long after a fetch is seen waiting on the server to stop.
	intoFetch := []time.Duration{0, 20 * time.Millisecond, 75 * time.Millisecond, 140 * time.Millisecond, time.Second}
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

				took := timedStop(t, func() { tc.stop(sub) })
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
			timedStop(t, sub.Stop)

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

// recorder is a handler that remembers what it saw, per message body.
type recorder struct {
	mu   sync.Mutex
	seen map[string][]uint64 // body -> NumDelivered of each call
}

func newRecorder() *recorder { return &recorder{seen: map[string][]uint64{}} }

func (r *recorder) handle(_ context.Context, m *nats.Msg) error {
	var nd uint64
	if md, err := m.Metadata(); err == nil {
		nd = md.NumDelivered
	}
	r.mu.Lock()
	r.seen[string(m.Data)] = append(r.seen[string(m.Data)], nd)
	r.mu.Unlock()
	return nil
}

func (r *recorder) snapshot() map[string][]uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string][]uint64, len(r.seen))
	for k, v := range r.seen {
		out[k] = append([]uint64(nil), v...)
	}
	return out
}

// checkNoStrand is the criterion of the race tests, checked after the halting
// subscriber's Stop. Each of the published bodies was either handled by it
// exactly once, on its first delivery, and acked; or left untouched, with no
// delivery counted, and the next subscriber on the durable gets it on its
// first delivery. A delivery the halting client received and never handled
// fails, however rare: that message would only come back after AckWait with
// an attempt spent, and on its last attempt never. mustLeave says the halting
// subscriber may not have handled any of them. It returns how many it
// handled.
func checkNoStrand(t *testing.T, js nats.JetStreamContext, opts SubscriberOpts, rec *recorder, bodies []string, mustLeave bool, desc string) int {
	t.Helper()
	seen := rec.snapshot()
	var handled int
	var left []string
	for _, b := range bodies {
		switch calls := seen[b]; {
		case len(calls) == 0:
			left = append(left, b)
		case len(calls) > 1:
			t.Errorf("%s: %s handled %d times by the halting subscriber, want at most 1", desc, b, len(calls))
		case calls[0] != 1:
			t.Errorf("%s: halting subscriber got %s with NumDelivered=%d, want 1", desc, b, calls[0])
		default:
			handled++
		}
	}
	if mustLeave && handled > 0 {
		t.Errorf("%s: a halted subscriber handled %d message(s) published after its last poll", desc, handled)
	}

	// The acks of what was handled are asynchronous; once they are in, the
	// durable must show exactly those deliveries and nothing else. A message
	// delivered into a fetch nobody reads shows up as an extra delivery,
	// ack-pending.
	want := func(ci *nats.ConsumerInfo) bool {
		return ci.Delivered.Consumer == uint64(handled) && ci.NumAckPending == 0 &&
			ci.NumRedelivered == 0 && ci.NumPending == uint64(len(left))
	}
	deadline := time.Now().Add(2 * time.Second)
	ci := consumerInfo(t, js, opts.DurableName)
	for !want(ci) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		ci = consumerInfo(t, js, opts.DurableName)
	}
	if !want(ci) {
		t.Errorf("%s: after stop Delivered.Consumer=%d NumAckPending=%d NumRedelivered=%d NumPending=%d, want %d, 0, 0, %d: a delivery was stranded",
			desc, ci.Delivered.Consumer, ci.NumAckPending, ci.NumRedelivered, ci.NumPending, handled, len(left))
	}

	if len(left) == 0 {
		return handled
	}
	got := receiveN(t, js, opts, len(left), 3*time.Second)
	byBody := map[string]uint64{}
	for _, d := range got {
		if _, dup := byBody[d.data]; dup {
			t.Errorf("%s: next subscriber got %s twice", desc, d.data)
		}
		byBody[d.data] = d.numDelivered
	}
	for _, b := range left {
		nd, ok := byBody[b]
		switch {
		case !ok:
			t.Errorf("%s: %s never reached the next subscriber", desc, b)
		case nd != 1:
			t.Errorf("%s: next subscriber got %s with NumDelivered=%d, want 1", desc, b, nd)
		}
	}
	return handled
}

// TestHaltRaceWithPublish: a message published around Halt is never stranded
// (see checkNoStrand): handled once by the halting subscriber's last poll, or
// left untouched for the next subscriber.
//
// "before" publishes from the test goroutine and halts a moment later (zero
// included), so the message is on its way into the open poll when Halt comes.
// "after" publishes after Halt, with a short delay (zero included).
// "concurrent" publishes from another goroutine released together with Halt,
// with a little jitter on one side or the other. "gap" halts, publishes 20 ms
// later and stops 100 ms after that, the way the remote-agent gateway halts
// early and stops once its handlers are done; the last poll may still be open
// then. "late" publishes once the server holds no pull request of the halted
// subscriber's any more: a halted loop sends no more, so the message must be
// left untouched.
func TestHaltRaceWithPublish(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	delays := []time.Duration{
		0, 0, 0, 0,
		50 * time.Microsecond, 100 * time.Microsecond, 250 * time.Microsecond,
		500 * time.Microsecond, time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond,
	}

	// MaxAckPending 1 and the default 32: the subscriber's fetch batch
	// follows it, and the server answers a batch of 1 and a batch of 32
	// differently (a batch of 32 keeps waiting for more until expiry).
	for _, batch := range []int{1, 32} {
		for _, mode := range []string{"before", "after", "concurrent", "gap", "late"} {
			t.Run(fmt.Sprintf("%s/batch%d", mode, batch), func(t *testing.T) {
				iterations := 16
				switch mode {
				case "gap":
					iterations = 8
				case "late":
					iterations = 4
				}
				var handled int
				for i := 0; i < iterations; i++ {
					durable := fmt.Sprintf("stop-race-%s-%d-%d", mode, batch, i)
					opts := stopOpts(durable, durable, batch)

					rec := newRecorder()
					sub, err := Subscribe(js, opts, rec.handle)
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
					case "late":
						desc = "publish after the last poll ended, then Stop"
						sub.Halt()
						// A fetch that started just as Halt came has
						// reached the server by now; wait for it to end.
						time.Sleep(20 * time.Millisecond)
						waitFor(t, func() bool {
							ci, err := js.ConsumerInfo(MainStreamName, durable)
							return err == nil && ci.NumWaiting == 0
						}, 5*time.Second)
						if err := publishStop(js, durable, durable, durable); err != nil {
							sub.Stop()
							t.Fatalf("iter %d: publish: %v", i, err)
						}
						time.Sleep(50 * time.Millisecond)
					}
					timedStop(t, sub.Stop)

					handled += checkNoStrand(t, js, opts, rec, []string{durable}, mode == "late",
						fmt.Sprintf("iter %d (%s)", i, desc))
				}
				t.Logf("handled by the halting subscriber: %d of %d, the rest left untouched", handled, iterations)
				if n := dlqMsgs(t, js); n != 0 {
					t.Errorf("DLQ has %d messages, want 0", n)
				}
			})
		}
	}
}

// slowLink is a TCP proxy to addr that holds every byte from client to server
// for delay, in order. Server to client is not delayed. It returns the URL to
// connect to.
func slowLink(t *testing.T, addr string, delay time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s, err := net.Dial("tcp", addr)
			if err != nil {
				_ = c.Close()
				continue
			}
			go func() {
				_, _ = io.Copy(c, s)
				_ = c.Close()
				_ = s.Close()
			}()
			go delayCopy(s, c, delay)
		}
	}()
	return "nats://" + ln.Addr().String()
}

// delayCopy copies src to dst, writing each chunk delay after it was read.
func delayCopy(dst, src net.Conn, delay time.Duration) {
	type chunk struct {
		at time.Time
		b  []byte
	}
	q := make(chan chunk, 4096)
	done := make(chan struct{})
	go func() {
		defer close(q)
		for {
			buf := make([]byte, 32*1024)
			n, err := src.Read(buf)
			if n > 0 {
				select {
				case q <- chunk{at: time.Now().Add(delay), b: buf[:n]}:
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		close(done)
		_ = dst.Close()
		_ = src.Close()
	}()
	for c := range q {
		time.Sleep(time.Until(c.at))
		if _, err := dst.Write(c.b); err != nil {
			return
		}
	}
}

// TestHaltOnSlowLinkStrandsNothing: the race criterion holds when the
// subscriber's pull requests reach the server late. Its connection runs
// through a proxy that holds everything it sends for linkDelay; publishing and
// checking use a direct connection. The server starts a pull request's clock
// when the request arrives. A fetch that stopped listening on its own clock,
// a tenth of a poll after it asked (what nats.go does with an honest
// deadline), would stop linkDelay minus that tenth before the server's request
// expired, and a halted loop would leave that request open: a message stored
// in the gap is delivered into a subscription nobody reads. Each round
// publishes a message every 20 ms from the halt to past the last poll, so any
// such gap catches one.
func TestHaltOnSlowLinkStrandsNothing(t *testing.T) {
	nc, js, cleanup := startServer(t)
	defer cleanup()

	const linkDelay = 60 * time.Millisecond
	addr := strings.TrimPrefix(nc.ConnectedUrl(), "nats://")
	slow, err := nats.Connect(slowLink(t, addr, linkDelay))
	if err != nil {
		t.Fatalf("connect through the slow link: %v", err)
	}
	defer slow.Close()
	slowJS, err := slow.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}

	const spacing = 20 * time.Millisecond
	span := pollWait + 2*linkDelay + 3*spacing
	for _, batch := range []int{1, 32} {
		t.Run(fmt.Sprintf("batch%d", batch), func(t *testing.T) {
			var handled, total int
			for i := 0; i < 6; i++ {
				durable := fmt.Sprintf("stop-slow-%d-%d", batch, i)
				opts := stopOpts(durable, durable, batch)
				rec := newRecorder()
				sub, err := Subscribe(slowJS, opts, rec.handle)
				if err != nil {
					t.Fatalf("Subscribe: %v", err)
				}
				waitFetching(t, js, durable)
				// Halt somewhere in a poll, not always at its start.
				time.Sleep(rand.N(pollWait))
				sub.Halt()
				var bodies []string
				for d := time.Duration(0); d <= span; d += spacing {
					body := fmt.Sprintf("%s-%d", durable, d/spacing)
					if err := publishStop(js, durable, body, body); err != nil {
						sub.Stop()
						t.Fatalf("iter %d: publish: %v", i, err)
					}
					bodies = append(bodies, body)
					time.Sleep(spacing)
				}
				timedStop(t, sub.Stop)
				handled += checkNoStrand(t, js, opts, rec, bodies, false, fmt.Sprintf("iter %d", i))
				total += len(bodies)
			}
			t.Logf("handled by the halting subscriber: %d of %d, the rest left untouched", handled, total)
		})
	}
	if n := dlqMsgs(t, js); n != 0 {
		t.Errorf("DLQ has %d messages, want 0", n)
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
// count it had before Subscribe: for an idle stop, for one halted during a
// poll, and for one halted while a handler held a message.
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
			timedStop(t, sub.Stop)
		}},
		{"halted-fetch", func(t *testing.T, opts SubscriberOpts, _ int) {
			sub, err := Subscribe(js, opts, noop)
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			waitFetching(t, js, opts.DurableName)
			sub.Halt()
			time.Sleep(20 * time.Millisecond)
			timedStop(t, sub.Stop)
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
			timedStop(t, sub.Stop)
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

// TestHaltLogsNothing: a stop that comes during a poll is a normal shutdown,
// not a failure. Nothing at Warn or Error.
func TestHaltLogsNothing(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	cases := []struct {
		name string
		stop func(*Subscriber)
	}{
		{"Stop", func(s *Subscriber) { s.Stop() }},
		{"HaltThenStop", func(s *Subscriber) { s.Halt(); s.Stop() }},
		// Long enough for a loop that treats the end of a halted poll as a
		// failure to log it before Stop is reached.
		{"HaltWaitStop", func(s *Subscriber) { s.Halt(); time.Sleep(stopBound); s.Stop() }},
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
			timedStop(t, func() { tc.stop(sub) })
			if recs := rec.records(); len(recs) != 0 {
				t.Errorf("stop logged %d record(s) at Warn or above:\n%v", len(recs), recs)
			}
		})
	}
}

// TestHaltEndsBackoff: after a failed fetch the loop backs off for 500 ms
// before trying again. That wait holds no pull request, so a halt ends it:
// Stop returns at once instead of sitting out the back-off. The fetch is made
// to fail by deleting the durable under the subscriber.
func TestHaltEndsBackoff(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	rec := &warnRecorder{}
	durable := "stop-backoff"
	opts := stopOpts(durable, durable, 0)
	opts.Logger = slog.New(rec)
	sub, err := Subscribe(js, opts, func(context.Context, *nats.Msg) error { return nil })
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	waitFetching(t, js, durable)
	if err := js.DeleteConsumer(MainStreamName, durable); err != nil {
		sub.Stop()
		t.Fatalf("delete consumer: %v", err)
	}
	// The record is logged just before the back-off starts.
	deadline := time.Now().Add(5 * time.Second)
	for len(rec.records()) == 0 {
		if time.Now().After(deadline) {
			sub.Stop()
			t.Fatal("no fetch failure logged within 5s of deleting the durable")
		}
		time.Sleep(time.Millisecond)
	}
	took := timedStop(t, sub.Stop)
	t.Logf("stop during back-off: %v (%v)", took, rec.records()[0])
	if took > 250*time.Millisecond {
		t.Errorf("stop during the back-off took %v, want <= 250ms: the halt did not end it", took)
	}
}

// TestShortAckWaitPollsShorter: what a fetch brings is dispatched when the
// fetch is over, so a message can wait a whole poll for its handler without
// heartbeats. With an AckWait shorter than pollWait it would be redelivered
// into the same fetch and handled twice. The poll shrinks to a quarter of the
// AckWait, so each message is handled once, on its first delivery.
func TestShortAckWaitPollsShorter(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	const ackWait = 80 * time.Millisecond
	for i := 0; i < 4; i++ {
		durable := fmt.Sprintf("stop-short-ackwait-%d", i)
		opts := stopOpts(durable, durable, 32)
		opts.AckWait = ackWait
		opts.Backoff = []time.Duration{ackWait, ackWait, ackWait, ackWait}
		rec := newRecorder()
		sub, err := Subscribe(js, opts, rec.handle)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		waitFetching(t, js, durable)
		// Early in the poll, so it would wait out most of a long one.
		if err := publishStop(js, durable, durable, durable); err != nil {
			sub.Stop()
			t.Fatalf("publish: %v", err)
		}
		waitFor(t, func() bool { return len(rec.snapshot()[durable]) > 0 }, 5*time.Second)
		// Room for a second delivery to show up.
		time.Sleep(3 * ackWait)
		timedStop(t, sub.Stop)
		if got := rec.snapshot()[durable]; len(got) != 1 || got[0] != 1 {
			t.Errorf("iter %d: handled with NumDelivered %v, want once with 1", i, got)
		}
	}
}
