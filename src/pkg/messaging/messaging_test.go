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
