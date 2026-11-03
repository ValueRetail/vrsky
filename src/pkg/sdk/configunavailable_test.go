package sdk_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// configProbe reads its connection config through read, the way the real
// producers do: ServesConnection answers true on ErrConfigUnavailable, and
// Deliver returns it.
type configProbe struct {
	sdk.BaseProducer
	read      func() error
	attempts  atomic.Int32 // Deliver calls
	delivered atomic.Int32 // Deliver calls that got a config and "sent"
}

func (p *configProbe) Configure(context.Context, *sdk.Resources) error { return nil }

func (p *configProbe) ServesConnection(context.Context, string, string) bool {
	err := p.read()
	return err == nil || sdk.IsConfigUnavailable(err)
}

func (p *configProbe) Deliver(context.Context, *envelope.Envelope) error {
	p.attempts.Add(1)
	if err := p.read(); err != nil {
		return err
	}
	p.delivered.Add(1)
	return nil
}

// syncBuffer is a bytes.Buffer safe for the runner's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func fastRetries(t *testing.T) {
	d := 50 * time.Millisecond
	sdk.SetDispatchBackoffForTest(t, []time.Duration{d, d, d, d})
}

// A config read that keeps failing must never ack: the message is redelivered
// MaxDeliveryAttempts times, then lands in the DLQ, and each failure is logged
// at Error with the connection id (#704).
func TestConfigUnavailable_RetriedThenDeadLettered(t *testing.T) {
	fastRetries(t)
	var logs syncBuffer
	sdk.SetLoggerForTest(t, slog.New(slog.NewJSONHandler(&logs, nil)))

	p := &configProbe{read: func() error {
		return sdk.ConfigUnavailable(errors.New("pq: the database system is shutting down"))
	}}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "config-probe-dlq"})

	env := envelope.New()
	env.TenantID = "tenant-x"
	env.IntegrationID = "conn-db-down"
	env.Payload = []byte(`{"order":1}`)
	h.Publish(t, env)

	got := h.ExpectDLQ(t, 15*time.Second)
	if got.ID != env.ID {
		t.Errorf("DLQ holds %s, want %s", got.ID, env.ID)
	}
	if n := p.attempts.Load(); n != int32(messaging.MaxDeliveryAttempts) {
		t.Errorf("Deliver ran %d times, want %d (one per delivery attempt)", n, messaging.MaxDeliveryAttempts)
	}
	if n := p.delivered.Load(); n != 0 {
		t.Errorf("delivered %d times with no config", n)
	}

	var errorLines int
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil || rec["level"] != "ERROR" {
			continue
		}
		if rec["connection_id"] == env.IntegrationID && strings.Contains(rec["msg"].(string), "config unavailable") {
			errorLines++
		}
	}
	if errorLines != messaging.MaxDeliveryAttempts {
		t.Errorf("got %d Error lines naming the connection, want %d\n%s", errorLines, messaging.MaxDeliveryAttempts, logs.String())
	}
}

// A blip that clears before the retries run out delivers the message once and
// leaves nothing in the DLQ.
func TestConfigUnavailable_RecoversOnRedelivery(t *testing.T) {
	fastRetries(t)
	var failures atomic.Int32
	failures.Store(3) // ServesConnection + Deliver of the first attempt, then one more read
	p := &configProbe{read: func() error {
		if failures.Add(-1) >= 0 {
			return sdk.ConfigUnavailable(errors.New("connection reset by peer"))
		}
		return nil
	}}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "config-probe-recover"})

	env := envelope.New()
	env.Payload = []byte(`{"order":2}`)
	h.Publish(t, env)

	harness.Eventually(t, 10*time.Second, "delivered after the read recovered", func() bool {
		return p.delivered.Load() == 1
	})
	h.ExpectNoDLQ(t, time.Second)
	if n := p.delivered.Load(); n != 1 {
		t.Errorf("delivered %d times, want 1", n)
	}
}

// "Not mine" still acks quietly: no Deliver, no retries, no DLQ.
func TestConfigUnavailable_NotMineStillAcks(t *testing.T) {
	fastRetries(t)
	p := &configProbe{read: func() error { return sdk.ConfigReadError(sql.ErrNoRows) }}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "config-probe-foreign"})

	env := envelope.New()
	env.Payload = []byte(`{"order":3}`)
	h.Publish(t, env)

	h.ExpectNoDLQ(t, 2*time.Second)
	if n := p.attempts.Load(); n != 0 {
		t.Errorf("Deliver ran %d times for a connection that isn't ours", n)
	}
}
