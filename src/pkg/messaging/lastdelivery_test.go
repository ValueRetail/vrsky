package messaging

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// flakyJS is a JetStream context whose StreamInfo fails while infoDown is
// set, and whose first DLQ publish is stored but answered with a timeout when
// loseFirstDLQAck is set (the PubAck lost on the way back).
type flakyJS struct {
	nats.JetStreamContext
	infoDown        atomic.Bool
	infoFailed      atomic.Int32
	loseFirstDLQAck bool
	dlqPublishes    atomic.Int32
}

var errBlip = errors.New("jetstream blip")

func (f *flakyJS) StreamInfo(stream string, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	if f.infoDown.Load() {
		f.infoFailed.Add(1)
		return nil, errBlip
	}
	return f.JetStreamContext.StreamInfo(stream, opts...)
}

func (f *flakyJS) PublishMsg(m *nats.Msg, opts ...nats.PubOpt) (*nats.PubAck, error) {
	if !strings.HasPrefix(m.Subject, "vrsky.dlq.") {
		return f.JetStreamContext.PublishMsg(m, opts...)
	}
	n := f.dlqPublishes.Add(1)
	ack, err := f.JetStreamContext.PublishMsg(m, opts...)
	if err == nil && f.loseFirstDLQAck && n == 1 {
		return nil, nats.ErrTimeout
	}
	return ack, err
}

// #811 review of #830: the DLQ copy on the last delivery, against a JetStream
// that misbehaves in two ways the stream-level tests in pkg/sdk cannot show.
func TestDeadLetterSurvivesJetStreamTrouble(t *testing.T) {
	ms := time.Millisecond
	tests := []struct {
		name string
		// onLast runs when delivery 5 starts.
		onLast       func(f *flakyJS)
		loseFirstAck bool
		wantPublish  int32
	}{
		{
			// The subscriber's Publisher used to run EnsureStreams on its
			// first DLQ copy and keep the error for good: a blip of 300 ms
			// failed all three tries, without them reaching NATS, and every
			// later copy until a restart. Now Subscribe's own EnsureStreams
			// counts, so the copy does not probe the streams at all.
			name: "stream info fails for 300 ms",
			onLast: func(f *flakyJS) {
				f.infoDown.Store(true)
				time.AfterFunc(300*ms, func() { f.infoDown.Store(false) })
			},
			wantPublish: 1,
		},
		{
			// Stored, but the answer is lost: the second try must not make a
			// second copy. The Msg-Id is the same on every try, so the DLQ's
			// duplicate window collapses it.
			name:         "first copy stored, its PubAck lost",
			onLast:       func(*flakyJS) {},
			loseFirstAck: true,
			wantPublish:  2,
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, js, cleanup := startServer(t)
			defer cleanup()
			f := &flakyJS{JetStreamContext: js, loseFirstDLQAck: tt.loseFirstAck}
			var attempts atomic.Int32
			sub, err := Subscribe(f, SubscriberOpts{
				DurableName: "dead-letter-trouble-" + string(rune('a'+i)),
				Backoff:     []time.Duration{50 * ms, 50 * ms, 50 * ms, 50 * ms, time.Minute},
			}, func(context.Context, *nats.Msg) error {
				if attempts.Add(1) == MaxDeliveryAttempts {
					tt.onLast(f)
				}
				return errors.New("target down")
			})
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			defer sub.Stop()
			if err := NewPublisher(js).Publish(context.Background(), "tenant-a", "conn-a", "m-1", []byte("x")); err != nil {
				t.Fatalf("publish: %v", err)
			}
			waitFor(t, func() bool { return attempts.Load() >= MaxDeliveryAttempts }, 10*time.Second)
			// The tries take 2.5 s at most when each fails at once.
			deadline := time.Now().Add(4 * time.Second)
			for time.Now().Before(deadline) && f.dlqPublishes.Load() < tt.wantPublish {
				time.Sleep(20 * ms)
			}
			time.Sleep(700 * ms) // a further try would come 500 ms or 2 s later

			info, err := js.StreamInfo(DLQStreamName)
			if err != nil {
				t.Fatalf("DLQ info: %v", err)
			}
			if info.State.Msgs != 1 {
				t.Errorf("the DLQ holds %d copies, want 1", info.State.Msgs)
			}
			if got := f.dlqPublishes.Load(); got != tt.wantPublish {
				t.Errorf("%d DLQ publishes, want %d", got, tt.wantPublish)
			}
			if n := f.infoFailed.Load(); n != 0 {
				t.Errorf("the dead-letter path probed the streams %d times during the blip, want 0", n)
			}
		})
	}
}

// A Publisher whose first ensure failed tries again on the next publish
// instead of failing every publish for its lifetime (#811 review of #830).
func TestPublisherRetriesAFailedEnsure(t *testing.T) {
	_, js, cleanup := startServer(t)
	defer cleanup()
	f := &flakyJS{JetStreamContext: js}
	pub := NewPublisher(f)

	f.infoDown.Store(true)
	if err := pub.Publish(context.Background(), "tenant-a", "conn-a", "m-1", []byte("x")); err == nil {
		t.Fatal("publish during the blip succeeded, want the ensure error")
	}
	f.infoDown.Store(false)
	if err := pub.Publish(context.Background(), "tenant-a", "conn-a", "m-2", []byte("x")); err != nil {
		t.Fatalf("publish after the blip: %v", err)
	}
	if err := pub.PublishToDLQ(context.Background(), "tenant-a", "conn-a", "d-1", []byte("x"), nil); err != nil {
		t.Fatalf("DLQ publish after the blip: %v", err)
	}
	// Ensured now: a later blip does not touch publishing.
	f.infoDown.Store(true)
	if err := pub.Publish(context.Background(), "tenant-a", "conn-a", "m-3", []byte("x")); err != nil {
		t.Fatalf("publish once ensured: %v", err)
	}
}
