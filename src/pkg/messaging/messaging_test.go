package messaging

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// startServer brings up an in-process NATS with JetStream enabled and
// returns a connected client + cleanup. JS state lives under t.TempDir
// so each test starts with a clean slate.
func startServer(t *testing.T) (*nats.Conn, nats.JetStreamContext, func()) {
	t.Helper()
	dir := t.TempDir()
	opts := &server.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		NoLog:     true,
		NoSigs:    true,
		JetStream: true,
		StoreDir:  filepath.Join(dir, "js"),
	}
	srv, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server did not come up")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	cleanup := func() {
		_ = nc.Drain()
		srv.Shutdown()
		_ = os.RemoveAll(dir)
	}
	return nc, js, cleanup
}

func TestEnsureStreams(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	if err := EnsureStreams(js); err != nil {
		t.Fatalf("EnsureStreams: %v", err)
	}

	for _, name := range []string{MainStreamName, DLQStreamName} {
		info, err := js.StreamInfo(name)
		if err != nil {
			t.Fatalf("StreamInfo %s: %v", name, err)
		}
		if info.Config.Name != name {
			t.Errorf("expected stream name %q, got %q", name, info.Config.Name)
		}
	}

	// The data stream must be size-bounded with DiscardOld so a runaway producer
	// or stuck consumer sheds stale messages instead of OOM-killing NATS.
	main, err := js.StreamInfo(MainStreamName)
	if err != nil {
		t.Fatalf("StreamInfo %s: %v", MainStreamName, err)
	}
	if main.Config.MaxBytes != MainMaxBytes {
		t.Errorf("MaxBytes = %d, want %d", main.Config.MaxBytes, MainMaxBytes)
	}
	if main.Config.MaxMsgs != MainMaxMsgs {
		t.Errorf("MaxMsgs = %d, want %d", main.Config.MaxMsgs, MainMaxMsgs)
	}
	if main.Config.Discard != nats.DiscardOld {
		t.Errorf("Discard = %v, want DiscardOld", main.Config.Discard)
	}

	// Idempotent.
	if err := EnsureStreams(js); err != nil {
		t.Fatalf("second EnsureStreams: %v", err)
	}
}

func TestPublishHandlerAckHappy(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	const tenant, conn = "tenant-A", "pipeline-1"
	pub := NewPublisher(js)

	var got [][]byte
	var mu sync.Mutex
	sub, err := Subscribe(js, SubscriberOpts{
		DurableName: "test-happy",
		AckWait:     2 * time.Second,
	}, func(ctx context.Context, m *nats.Msg) error {
		mu.Lock()
		got = append(got, append([]byte(nil), m.Data...))
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Stop()

	for i := 0; i < 3; i++ {
		if err := pub.Publish(context.Background(), tenant, conn, fmt.Sprintf("m-%d", i), []byte(fmt.Sprintf("hello-%d", i))); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 3
	}, 5*time.Second)
}

func TestHandlerErrorRetriesWithBackoff(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	pub := NewPublisher(js)

	var attempts int32
	sub, err := Subscribe(js, SubscriberOpts{
		DurableName: "test-retry",
		AckWait:     500 * time.Millisecond,
		Backoff:     []time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond},
	}, func(ctx context.Context, m *nats.Msg) error {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Stop()

	if err := pub.Publish(context.Background(), "tenant-B", "pipeline-flaky", "m-1", []byte("payload")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&attempts) >= 3 }, 10*time.Second)
}

func TestMaxDeliverRoutesToDLQ(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	pub := NewPublisher(js)

	var attempts int32
	sub, err := Subscribe(js, SubscriberOpts{
		DurableName: "test-dlq",
		AckWait:     300 * time.Millisecond,
		Backoff:     []time.Duration{50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond},
	}, func(ctx context.Context, m *nats.Msg) error {
		atomic.AddInt32(&attempts, 1)
		return errors.New("always fail")
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Stop()

	if err := pub.Publish(context.Background(), "tenant-C", "pipeline-fail", "m-1", []byte("doomed")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	waitFor(t, func() bool { return atomic.LoadInt32(&attempts) >= int32(MaxDeliveryAttempts) }, 15*time.Second)

	waitFor(t, func() bool {
		info, err := js.StreamInfo(DLQStreamName)
		return err == nil && info.State.Msgs == 1
	}, 5*time.Second)

	// No further delivery after MaxDeliver.
	time.Sleep(500 * time.Millisecond)
	if got := atomic.LoadInt32(&attempts); got != int32(MaxDeliveryAttempts) {
		t.Fatalf("expected exactly %d attempts, got %d", MaxDeliveryAttempts, got)
	}
}

func TestDLQSurvivesNATSRestart(t *testing.T) {
	dir := t.TempDir()
	startOne := func() (*server.Server, *nats.Conn, nats.JetStreamContext) {
		opts := &server.Options{
			Host: "127.0.0.1", Port: -1,
			NoLog: true, NoSigs: true,
			JetStream: true, StoreDir: filepath.Join(dir, "js"),
		}
		srv, err := server.NewServer(opts)
		if err != nil {
			t.Fatalf("new server: %v", err)
		}
		go srv.Start()
		if !srv.ReadyForConnections(5 * time.Second) {
			t.Fatal("nats not ready")
		}
		nc, err := nats.Connect(srv.ClientURL())
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		js, _ := nc.JetStream()
		return srv, nc, js
	}

	srv, nc, js := startOne()
	pub := NewPublisher(js)
	hdr := nats.Header{"X-Test": []string{"yes"}}
	if err := pub.PublishToDLQ(context.Background(), "tenant-D", "pipeline-survives", "msg-1", []byte("preserved"), hdr); err != nil {
		t.Fatalf("publish to dlq: %v", err)
	}
	_ = nc.Drain()
	srv.Shutdown()

	srv2, nc2, js2 := startOne()
	defer func() {
		_ = nc2.Drain()
		srv2.Shutdown()
	}()
	info, err := js2.StreamInfo(DLQStreamName)
	if err != nil {
		t.Fatalf("post-restart stream info: %v", err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("expected 1 DLQ msg post-restart, got %d", info.State.Msgs)
	}
}

func TestDeduplicationByMsgID(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	pub := NewPublisher(js)

	var got int32
	sub, err := Subscribe(js, SubscriberOpts{DurableName: "test-dedup"},
		func(ctx context.Context, m *nats.Msg) error {
			atomic.AddInt32(&got, 1)
			return nil
		})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Stop()

	for i := 0; i < 5; i++ {
		if err := pub.Publish(context.Background(), "tenant-E", "pipeline-dedup", "same-id", []byte("dup")); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	time.Sleep(500 * time.Millisecond)
	if n := atomic.LoadInt32(&got); n != 1 {
		t.Fatalf("expected 1 delivery (deduped by MsgID), got %d", n)
	}
}

func TestParseSubject(t *testing.T) {
	tenant, conn := parseSubject("vrsky.data.tenant-X.pipeline.conn-Y")
	if tenant != "tenant-X" || conn != "conn-Y" {
		t.Fatalf("parseSubject = (%q, %q)", tenant, conn)
	}
	if t2, c2 := parseSubject("not a subject"); t2 != "" || c2 != "" {
		t.Fatalf("parseSubject should return empty on malformed input, got (%q, %q)", t2, c2)
	}
}

func TestMultipleWorkersBothReceive(t *testing.T) {
	// Two durable consumers on the same stream both see every message.
	// This is the model that lets data-filter, data-converter, and the
	// producers run independently.
	_, js, cleanup := startServer(t)
	defer cleanup()

	pub := NewPublisher(js)

	var aCount, bCount int32
	subA, err := Subscribe(js, SubscriberOpts{DurableName: "worker-A"},
		func(ctx context.Context, m *nats.Msg) error {
			atomic.AddInt32(&aCount, 1)
			return nil
		})
	if err != nil {
		t.Fatalf("subA: %v", err)
	}
	defer subA.Stop()
	subB, err := Subscribe(js, SubscriberOpts{DurableName: "worker-B"},
		func(ctx context.Context, m *nats.Msg) error {
			atomic.AddInt32(&bCount, 1)
			return nil
		})
	if err != nil {
		t.Fatalf("subB: %v", err)
	}
	defer subB.Stop()

	for i := 0; i < 4; i++ {
		if err := pub.Publish(context.Background(), "tenant-X", "pipeline-multi", fmt.Sprintf("m%d", i), []byte("hi")); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	waitFor(t, func() bool {
		return atomic.LoadInt32(&aCount) == 4 && atomic.LoadInt32(&bCount) == 4
	}, 5*time.Second)
}

// TestSlowHandlerNoRedelivery covers the #139 in-progress heartbeat: a handler
// that runs far longer than AckWait is still delivered exactly once, because the
// subscriber keeps the message in-flight with periodic InProgress() calls. With
// a 1s AckWait and a 3s handler, the pre-#139 behavior would redeliver the
// message (NumDelivered>1) and re-invoke the handler after the first ack.
func TestSlowHandlerNoRedelivery(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	pub := NewPublisher(js)

	const ackWait = 1 * time.Second
	const handlerDur = 3 * time.Second // 3x AckWait

	var invocations int32
	sub, err := Subscribe(js, SubscriberOpts{
		DurableName: "test-slow-handler",
		AckWait:     ackWait,
		// Tight backoff so a (wrongly) redelivered message would re-invoke fast.
		Backoff: []time.Duration{ackWait, ackWait, ackWait, ackWait},
	}, func(ctx context.Context, m *nats.Msg) error {
		atomic.AddInt32(&invocations, 1)
		time.Sleep(handlerDur)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Stop()

	if err := pub.Publish(context.Background(), "tenant-A", "pipeline-1", "slow-1", []byte("payload")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Wait past handler completion plus a couple of AckWait windows, during
	// which a non-heartbeated message would have been redelivered.
	time.Sleep(handlerDur + 2*ackWait)

	if n := atomic.LoadInt32(&invocations); n != 1 {
		t.Fatalf("handler invoked %d times; want exactly 1 (heartbeat should prevent redelivery while in-flight)", n)
	}
}

// TestRaisedAckWaitRebinds covers #139 acceptance #1: a durable created at one
// AckWait can be re-subscribed with a higher AckWait without the #99 crash-loop
// — reconcileAckWait updates the consumer in place so the bind succeeds and the
// new ack-wait is in effect.
func TestRaisedAckWaitRebinds(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()

	const durable = "test-ackwait-rebind"

	// First bind: default schedule → effective AckWait = DefaultBackoff[0] (1s).
	sub1, err := Subscribe(js, SubscriberOpts{DurableName: durable}, func(context.Context, *nats.Msg) error { return nil })
	if err != nil {
		t.Fatalf("first Subscribe: %v", err)
	}
	sub1.Stop()
	if ci, err := js.ConsumerInfo(MainStreamName, durable); err != nil {
		t.Fatalf("ConsumerInfo: %v", err)
	} else if ci.Config.AckWait != DefaultBackoff[0] {
		t.Fatalf("initial AckWait = %v, want %v", ci.Config.AckWait, DefaultBackoff[0])
	}

	// Re-bind the SAME durable with a raised AckWait. Pre-#139 this errored with
	// an ack-wait mismatch; now it reconciles and binds.
	const raised = 30 * time.Second
	sub2, err := Subscribe(js, SubscriberOpts{DurableName: durable, AckWait: raised}, func(context.Context, *nats.Msg) error { return nil })
	if err != nil {
		t.Fatalf("re-Subscribe with raised AckWait: %v", err)
	}
	defer sub2.Stop()

	ci, err := js.ConsumerInfo(MainStreamName, durable)
	if err != nil {
		t.Fatalf("ConsumerInfo after raise: %v", err)
	}
	if ci.Config.AckWait != raised {
		t.Fatalf("AckWait after raise = %v, want %v", ci.Config.AckWait, raised)
	}
}

func waitFor(t *testing.T, ok func() bool, max time.Duration) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", max)
}
