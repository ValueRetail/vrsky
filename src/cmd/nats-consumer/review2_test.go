package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for the second review of #844.

// A user that may not publish acks would have every message come back each
// AckWait and published again once VRSky's dedup window passed. The
// connection stops with status error instead.
func TestAckDenied_EndsWithStatusError(t *testing.T) {
	ext := startExternal(t, func(o *natsserver.Options) {
		o.Users = []*natsserver.User{
			{Username: "admin", Password: "admin-pw"},
			{Username: "rh", Password: "rh-pw", Permissions: &natsserver.Permissions{
				Publish: &natsserver.SubjectPermission{Deny: []string{"$JS.ACK.>"}},
			}},
		}
	})
	_, js := ext.admin(nats.UserInfo("admin", "admin-pw"))
	addStream(t, js)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-1", `{"n":1}`)

	v := startVRSky(t)
	c := newTestConsumer(v.publish)
	rec := withStatusDB(t, c)
	cfg := baseConfig(ext.url())
	cfg.AuthType, cfg.Username, cfg.Password = authUserPassword, "rh", "rh-pw"
	cancel, done := c.runLoop(cfg)
	defer stop(t, cancel, done)
	select {
	case <-done:
	case <-time.After(3 * wait):
		t.Fatal("the loop kept going without being able to ack")
	}
	reason, ok := rec.errorWrite()
	if !ok || !strings.Contains(reason, "may not ack") {
		t.Fatalf("status error reason = %q (written %v)", reason, ok)
	}
	if got := len(v.published()); got != 1 {
		t.Errorf("publish calls = %d, want 1", got)
	}
}

// A restore at pod start never creates the durable: one that is missing was
// deleted while the pipeline ran. Status error, no publish, no durable.
func TestRestore_MissingDurableIsAnError(t *testing.T) {
	ext := startExternal(t, nil)
	_, js := ext.admin()
	addStream(t, js)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-1", `{"n":1}`)

	v := startVRSky(t)
	c := newTestConsumer(v.publish)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	c.db = db
	mock.MatchExpectationsInOrder(false)
	nodes, _ := json.Marshal([]map[string]any{{
		"id": "src", "type": "consumer",
		"config": map[string]any{"type": "nats", "nats": map[string]any{
			"servers": []string{ext.url()}, "stream": tStream, "durable": tDur, "filter_subject": tSubj,
		}},
	}})
	mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(tConn, tTenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow(nodes))
	mock.ExpectExec(`UPDATE connections SET status = \$1, started_at`).WithArgs("running", tConn, tTenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
	rec := &statusRec{}
	mock.ExpectExec(`UPDATE connections SET status = \$1, last_error = \$2`).
		WithArgs(argCapture{rec, 0}, argCapture{rec, 1}, argCapture{rec, 2}, argCapture{rec, 3}).
		WillReturnResult(sqlmock.NewResult(0, 1))

	c.restoreConnection(context.Background(), tConn, tTenant)
	harness.Eventually(t, wait, "connection ended", func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		_, ok := c.active[tConn]
		return !ok
	})
	harness.Eventually(t, wait, "status written", func() bool { _, ok := rec.errorWrite(); return ok })
	reason, _ := rec.errorWrite()
	if !strings.Contains(reason, "no longer exists") {
		t.Errorf("reason = %q", reason)
	}
	if n := len(v.published()); n != 0 {
		t.Errorf("a restore published %d messages", n)
	}
	if _, err := js.ConsumerInfo(tStream, tDur); err == nil {
		t.Error("a restore created the durable")
	}
}

// While a publish is retried, the held messages are kept in progress: a
// second puller on the same durable gets none of them, though the outage
// lasts longer than AckWait.
func TestRetry_KeepsHeldMessagesInProgress(t *testing.T) {
	ext := startExternal(t, nil)
	_, js := ext.admin()
	addStream(t, js)
	addDurable(t, js, time.Second)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-1", `{"n":1}`)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-2", `{"n":2}`)

	v := startVRSky(t)
	held := make(chan struct{})
	release := make(chan struct{})
	v.hook = func(n int, _ *envelope.Envelope) error {
		if n == 1 {
			close(held)
		}
		select {
		case <-release:
			return nil
		default:
			return fmt.Errorf("VRSky NATS unavailable")
		}
	}
	c := newTestConsumer(v.publish)
	cancel, done := c.runLoop(baseConfig(ext.url()))
	defer stop(t, cancel, done)
	<-held

	sub, err := js.PullSubscribe(tSubj, tDur, nats.Bind(tStream, tDur))
	if err != nil {
		t.Fatalf("second puller: %v", err)
	}
	deadline := time.Now().Add(2500 * time.Millisecond) // > 2 × AckWait
	for time.Now().Before(deadline) {
		msgs, _ := sub.Fetch(10, nats.MaxWait(300*time.Millisecond))
		if len(msgs) > 0 {
			t.Fatalf("the second puller got %d held message(s): their AckWait ran out", len(msgs))
		}
	}
	close(release)
	harness.Eventually(t, wait, "both acked", func() bool {
		info, err := js.ConsumerInfo(tStream, tDur)
		return err == nil && info.AckFloor.Stream == 2
	})
}

// A publish that never answers is cut at publishTimeout and retried.
func TestPublish_HangingPublishTimesOut(t *testing.T) {
	ext := startExternal(t, nil)
	_, js := ext.admin()
	addStream(t, js)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-1", `{"n":1}`)

	v := startVRSky(t)
	c := newTestConsumer(nil)
	c.publishTimeout = 300 * time.Millisecond
	calls := 0
	c.publish = func(ctx context.Context, env *envelope.Envelope) error {
		calls++
		if calls == 1 {
			<-ctx.Done() // a leaderless stream: no answer
			return ctx.Err()
		}
		return v.publish(ctx, env)
	}
	cancel, done := c.runLoop(baseConfig(ext.url()))
	defer stop(t, cancel, done)
	harness.Eventually(t, wait, "stored after the timeout", func() bool { return v.stored(t) == 1 })
}

// deliver_policy new: a durable VRSky creates skips what the stream held
// before it existed.
func TestDeliverPolicy_New(t *testing.T) {
	ext := startExternal(t, nil)
	_, js := ext.admin()
	addStream(t, js)
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "old", `{"n":0}`)

	v := startVRSky(t)
	c := newTestConsumer(v.publish)
	cfg := baseConfig(ext.url())
	cfg.DeliverPolicy = deliverNew
	cancel, done := c.runLoop(cfg)
	defer stop(t, cancel, done)
	harness.Eventually(t, wait, "durable created", func() bool {
		info, err := js.ConsumerInfo(tStream, tDur)
		return err == nil && info.Config.DeliverPolicy == nats.DeliverNewPolicy
	})
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "new", `{"n":1}`)
	harness.Eventually(t, wait, "new event stored", func() bool { return v.stored(t) == 1 })
	time.Sleep(300 * time.Millisecond)
	calls := v.published()
	if len(calls) != 1 || calls[0].ID != idFor("new") {
		t.Fatalf("published %d message(s); want only the new one", len(calls))
	}
}

// A durable that limits pull requests (batch and expiry) is served within its
// limits instead of being refused with a 409 on every pull.
func TestPullLimits_DurableLimitsRespected(t *testing.T) {
	ext := startExternal(t, nil)
	_, js := ext.admin()
	addStream(t, js)
	if _, err := js.AddConsumer(tStream, &nats.ConsumerConfig{
		Durable: tDur, FilterSubject: tSubj, AckPolicy: nats.AckExplicitPolicy, AckWait: 5 * time.Second,
		MaxRequestBatch: 2, MaxRequestExpires: 500 * time.Millisecond,
	}); err != nil {
		t.Fatalf("add durable: %v", err)
	}
	for i := 1; i <= 5; i++ {
		publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", fmt.Sprintf("evt-%d", i), `{}`)
	}
	v := startVRSky(t)
	c := newTestConsumer(v.publish)
	c.fetchWait = 2 * time.Second // above the durable's MaxRequestExpires
	rec := withStatusDB(t, c)
	defer func() {
		if reason, ok := rec.errorWrite(); ok {
			t.Errorf("the connection ended: %s", reason)
		}
	}()
	cancel, done := c.runLoop(baseConfig(ext.url()))
	defer stop(t, cancel, done)
	harness.Eventually(t, wait, "all five stored", func() bool { return v.stored(t) == 5 })
	publishEvent(t, js, "rh.evt.v1.tenant-a.pricing.price_changed", "evt-6", `{}`)
	harness.Eventually(t, wait, "a waiting pull within MaxRequestExpires", func() bool { return v.stored(t) == 6 })
}

func TestPullLimitsTable(t *testing.T) {
	info := func(mrb int, exp time.Duration) *jetstream.ConsumerInfo {
		return &jetstream.ConsumerInfo{Config: jetstream.ConsumerConfig{MaxRequestBatch: mrb, MaxRequestExpires: exp}}
	}
	tests := []struct {
		name       string
		want       int
		maxPayload int64
		info       *jetstream.ConsumerInfo
		batch      int
		wait       time.Duration
	}{
		{"default server, 1 MB", 10, 1 << 20, nil, 10, 0},
		{"256 at 1 MB is cut to the 32 MiB budget", 256, 1 << 20, nil, 32, 0},
		{"64 MB server: one at a time", 256, 64 << 20, nil, 1, 0},
		{"durable MaxRequestBatch", 10, 1 << 20, info(3, 0), 3, 0},
		{"durable MaxRequestExpires", 10, 1 << 20, info(0, time.Second), 10, time.Second},
	}
	for _, tt := range tests {
		b, w := pullLimits(tt.want, tt.maxPayload, tt.info)
		if b != tt.batch || w != tt.wait {
			t.Errorf("%s: got %d, %s; want %d, %s", tt.name, b, w, tt.batch, tt.wait)
		}
	}
}
