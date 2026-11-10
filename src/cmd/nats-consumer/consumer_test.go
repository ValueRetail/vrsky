package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

const wait = 10 * time.Second

// stop cancels a loop and waits for it to return.
func stop(t *testing.T, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("consume loop did not return after cancel")
	}
}

// Each message reaches the pipeline once, with an envelope id derived from
// its Nats-Msg-Id (or stream and sequence without one), and is acked on the
// source. VRSky creates the durable when it is missing.
func TestForward_PublishesThenAcks(t *testing.T) {
	ext := startExternal(t, nil)
	_, js := ext.admin()
	addStream(t, js)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-1", `{"sku":"A","price":10}`)
	publishEvent(t, js, "rh.evt.v1.tenant-a.master.article_updated", "", `plain text`)
	publishEvent(t, js, "rh.evt.v1.tenant-b.pricing.price_changed", "evt-b", `{"other":"tenant"}`) // outside the filter

	v := startVRSky(t)
	c := newTestConsumer(v.publish)
	cancel, done := c.runLoop(baseConfig(ext.url()))
	defer stop(t, cancel, done)

	harness.Eventually(t, wait, "both events acked on the source", func() bool {
		info, err := js.ConsumerInfo(tStream, tDur)
		return err == nil && info.AckFloor.Stream == 2 && info.NumAckPending == 0
	})
	if got := v.stored(t); got != 2 {
		t.Fatalf("VRSKY_DATA holds %d messages, want 2", got)
	}

	calls := v.published()
	if len(calls) != 2 {
		t.Fatalf("publish calls = %d, want 2", len(calls))
	}
	first, second := calls[0], calls[1]
	if first.ID != envelopeID(tConn, "evt-1") {
		t.Errorf("first envelope id = %s, want the id derived from Nats-Msg-Id evt-1", first.ID)
	}
	if second.ID != envelopeID(tConn, tStream+":2") {
		t.Errorf("second envelope id = %s, want the id derived from %s:2", second.ID, tStream)
	}
	if first.TenantID != tTenant || first.IntegrationID != tConn || first.Source != "nats-consumer" {
		t.Errorf("envelope routing = %q/%q/%q", first.TenantID, first.IntegrationID, first.Source)
	}
	if string(first.Payload) != `{"sku":"A","price":10}` || first.ContentType != "application/json" {
		t.Errorf("first payload %q content type %q", first.Payload, first.ContentType)
	}
	if second.ContentType != "application/octet-stream" {
		t.Errorf("second content type = %q", second.ContentType)
	}
	if first.Metadata["nats_subject"] != "rh.evt.v1.tenant-a.pricing.price_changed" || first.Metadata["nats_msg_id"] != "evt-1" {
		t.Errorf("first metadata = %v", first.Metadata)
	}
	if _, ok := second.Metadata["nats_msg_id"]; ok {
		t.Errorf("second metadata has a nats_msg_id: %v", second.Metadata)
	}

	// The durable VRSky created: pull, explicit ack, the configured filter.
	info := consumerInfo(t, js)
	if info.Config.DeliverSubject != "" || info.Config.AckPolicy != nats.AckExplicitPolicy || info.Config.FilterSubject != tSubj {
		t.Errorf("created durable config = %+v", info.Config)
	}
	if info.Config.MaxDeliver != -1 {
		t.Errorf("created durable MaxDeliver = %d, want -1", info.Config.MaxDeliver)
	}
}

// The source ack comes only after VRSky's publish was acknowledged: while the
// publish is held, the message is still pending on the source.
func TestForward_AcksOnlyAfterPublish(t *testing.T) {
	ext := startExternal(t, nil)
	_, js := ext.admin()
	addStream(t, js)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-1", `{"sku":"A"}`)

	v := startVRSky(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	v.hook = func(n int, _ *envelope.Envelope) error {
		if n == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	c := newTestConsumer(v.publish)
	cancel, done := c.runLoop(baseConfig(ext.url()))
	defer stop(t, cancel, done)

	select {
	case <-entered:
	case <-time.After(wait):
		t.Fatal("publish never called")
	}
	info := consumerInfo(t, js)
	if info.AckFloor.Stream != 0 || info.NumAckPending != 1 {
		t.Fatalf("while the publish is held: ack floor %d, pending %d; want 0 and 1", info.AckFloor.Stream, info.NumAckPending)
	}
	close(release)
	harness.Eventually(t, wait, "acked after the publish", func() bool {
		info, err := js.ConsumerInfo(tStream, tDur)
		return err == nil && info.AckFloor.Stream == 1 && info.NumAckPending == 0
	})
}

// A failed publish leaves the message on the source; it and the ones behind
// it come back in order, and each reaches the pipeline once.
func TestForward_FailedPublishStaysOnSource(t *testing.T) {
	ext := startExternal(t, nil)
	_, js := ext.admin()
	addStream(t, js)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-1", `{"n":1}`)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-2", `{"n":2}`)

	v := startVRSky(t)
	v.hook = func(n int, _ *envelope.Envelope) error {
		if n <= 2 {
			return errors.New("VRSky NATS unavailable")
		}
		return nil
	}
	c := newTestConsumer(v.publish)
	cancel, done := c.runLoop(baseConfig(ext.url()))
	defer stop(t, cancel, done)

	harness.Eventually(t, wait, "both acked after the retries", func() bool {
		info, err := js.ConsumerInfo(tStream, tDur)
		return err == nil && info.AckFloor.Stream == 2 && info.NumAckPending == 0
	})
	if got := v.stored(t); got != 2 {
		t.Fatalf("VRSKY_DATA holds %d messages, want 2", got)
	}
	// Order kept: evt-1 is the first message in VRSKY_DATA.
	for seq, key := range map[uint64]string{1: "evt-1", 2: "evt-2"} {
		raw, err := v.js.GetMsg(messaging.MainStreamName, seq)
		if err != nil {
			t.Fatalf("get VRSKY_DATA %d: %v", seq, err)
		}
		if id := raw.Header.Get(nats.MsgIdHdr); id != envelopeID(tConn, key) {
			t.Errorf("VRSKY_DATA seq %d has id %s, want the one for %s", seq, id, key)
		}
	}
	// The first two attempts were both evt-1: nothing behind it went ahead.
	calls := v.published()
	if calls[0].ID != envelopeID(tConn, "evt-1") || calls[1].ID != envelopeID(tConn, "evt-1") {
		t.Errorf("failed attempts were %s, %s; want evt-1 twice", calls[0].ID, calls[1].ID)
	}
}

// A crash after VRSky's publish and before the source ack: the ack never
// reaches the server, which redelivers after AckWait to the next consumer
// process. That one publishes the same envelope id, VRSky's JetStream drops
// the repeat, and the source is acked.
func TestCrashBeforeAck_RedeliveredWithoutDuplicate(t *testing.T) {
	ext := startExternal(t, nil)
	_, js := ext.admin()
	addStream(t, js)
	addDurable(t, js, time.Second)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-1", `{"sku":"A","price":10}`)

	v := startVRSky(t)
	px := startProxy(t, ext.srv.Addr().String())

	// Process A: publishes, then dies with its connection before it can ack.
	var mu sync.Mutex
	var cancelA context.CancelFunc
	a := newTestConsumer(func(ctx context.Context, env *envelope.Envelope) error {
		err := v.publish(ctx, env)
		px.kill()
		mu.Lock()
		cancelA()
		mu.Unlock()
		return err
	})
	mu.Lock()
	cancelA, doneA := a.runLoop(baseConfig(px.url()))
	mu.Unlock()
	select {
	case <-doneA:
	case <-time.After(wait):
		t.Fatal("process A did not stop")
	}
	if got := v.stored(t); got != 1 {
		t.Fatalf("after A: VRSKY_DATA holds %d, want 1", got)
	}
	if info := consumerInfo(t, js); info.AckFloor.Stream != 0 {
		t.Fatalf("A's ack reached the source (ack floor %d)", info.AckFloor.Stream)
	}

	// Process B, after the restart.
	b := newTestConsumer(v.publish)
	cancelB, doneB := b.runLoop(baseConfig(ext.url()))
	defer stop(t, cancelB, doneB)

	harness.Eventually(t, wait, "redelivered and acked", func() bool {
		info, err := js.ConsumerInfo(tStream, tDur)
		return err == nil && info.AckFloor.Stream == 1 && info.NumAckPending == 0
	})
	calls := v.published()
	if len(calls) != 2 || calls[0].ID != calls[1].ID {
		t.Fatalf("publish calls = %d (want 2 with one envelope id)", len(calls))
	}
	if got := v.stored(t); got != 1 {
		t.Fatalf("VRSKY_DATA holds %d messages after the redelivery, want 1", got)
	}
}

// The connection drops (the server restarts); the loop redials and goes on.
func TestReconnect_AfterServerRestart(t *testing.T) {
	ext := startExternal(t, nil)
	_, js := ext.admin()
	addStream(t, js)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-1", `{"n":1}`)

	v := startVRSky(t)
	c := newTestConsumer(v.publish)
	cancel, done := c.runLoop(baseConfig(ext.url()))
	defer stop(t, cancel, done)

	harness.Eventually(t, wait, "first event stored", func() bool { return v.stored(t) == 1 })
	ext.restart()

	_, js2 := ext.admin()
	publishEvent(t, js2, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-2", `{"n":2}`)
	harness.Eventually(t, wait, "second event stored after the reconnect", func() bool { return v.stored(t) == 2 })

	c.mu.RLock()
	_, active := c.active[tConn]
	c.mu.RUnlock()
	if !active {
		t.Error("a connection loss ended the connection; it should only reconnect")
	}
}

// JWT/NKey credentials (a .creds file) authenticate against an operator-mode
// server, as a partner account would.
func TestAuth_CredsFile(t *testing.T) {
	configure, creds := operatorServer(t)
	ext := startExternal(t, configure)
	_, js := ext.admin(nats.UserCredentialBytes([]byte(creds)))
	addStream(t, js)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-1", `{"n":1}`)

	v := startVRSky(t)
	c := newTestConsumer(v.publish)
	cfg := baseConfig(ext.url())
	cfg.AuthType, cfg.Creds = authCreds, creds
	if err := cfg.validate(true); err != nil {
		t.Fatalf("validate: %v", err)
	}
	cancel, done := c.runLoop(cfg)
	defer stop(t, cancel, done)
	harness.Eventually(t, wait, "event stored", func() bool { return v.stored(t) == 1 })
}

// VRSky is down for longer than the durable's AckWait. The head message is
// retried in place and kept in progress, so nothing behind it goes first and
// no delivery is spent: each message reaches the pipeline once, in order,
// and the server delivered each once.
func TestForward_LongOutageKeepsOrderAndDeliveries(t *testing.T) {
	ext := startExternal(t, nil)
	_, js := ext.admin()
	addStream(t, js)
	addDurable(t, js, time.Second)
	for i := 1; i <= 3; i++ {
		publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", fmt.Sprintf("evt-%d", i), fmt.Sprintf(`{"n":%d}`, i))
	}

	v := startVRSky(t)
	down := time.Now().Add(2500 * time.Millisecond) // > 2 × AckWait
	v.hook = func(int, *envelope.Envelope) error {
		if time.Now().Before(down) {
			return errors.New("VRSky NATS unavailable")
		}
		return nil
	}
	c := newTestConsumer(v.publish)
	cancel, done := c.runLoop(baseConfig(ext.url()))
	defer stop(t, cancel, done)

	harness.Eventually(t, wait, "all three acked", func() bool {
		info, err := js.ConsumerInfo(tStream, tDur)
		return err == nil && info.AckFloor.Stream == 3 && info.NumAckPending == 0
	})
	for seq := uint64(1); seq <= 3; seq++ {
		raw, err := v.js.GetMsg(messaging.MainStreamName, seq)
		if err != nil {
			t.Fatalf("get VRSKY_DATA %d: %v", seq, err)
		}
		if id := raw.Header.Get(nats.MsgIdHdr); id != envelopeID(tConn, fmt.Sprintf("evt-%d", seq)) {
			t.Errorf("VRSKY_DATA seq %d holds the wrong event", seq)
		}
	}
	if got := v.stored(t); got != 3 {
		t.Fatalf("VRSKY_DATA holds %d, want 3", got)
	}
	calls := v.published()
	ids := map[string]int{}
	for _, env := range calls {
		ids[env.ID]++
	}
	if ids[envelopeID(tConn, "evt-2")] != 1 || ids[envelopeID(tConn, "evt-3")] != 1 {
		t.Errorf("messages behind the head were published more than once: %v", ids)
	}
	if ids[envelopeID(tConn, "evt-1")] < 2 {
		t.Errorf("the head message was not retried: %v", ids)
	}
	if info := consumerInfo(t, js); info.Delivered.Consumer != 3 {
		t.Errorf("the server made %d deliveries for 3 messages; retries spent deliveries", info.Delivered.Consumer)
	}
}
