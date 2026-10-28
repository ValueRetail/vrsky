package messaging

import (
	"reflect"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// Tests for issue #353: ensure() reconciles an existing stream only when its
// config differs from the wanted one, so a boot-time EnsureStreams does not
// send a stream update every time a service starts.

// wantMainConfig is the VRSKY_DATA config EnsureStreams asks for.
func wantMainConfig() nats.StreamConfig {
	return nats.StreamConfig{
		Name:       MainStreamName,
		Subjects:   []string{MainSubjectAll},
		Retention:  nats.LimitsPolicy,
		Storage:    nats.FileStorage,
		MaxAge:     MainRetention,
		MaxBytes:   MainMaxBytes,
		MaxMsgs:    MainMaxMsgs,
		Discard:    nats.DiscardOld,
		Duplicates: 5 * time.Minute,
	}
}

// wantDLQConfig is the VRSKY_DLQ config EnsureStreams asks for. It leaves
// max bytes, max msgs, discard and the duplicates window unset, so the server
// fills in -1, -1, old and 2m.
func wantDLQConfig() nats.StreamConfig {
	return nats.StreamConfig{
		Name:      DLQStreamName,
		Subjects:  []string{DLQSubjectAll},
		Retention: nats.LimitsPolicy,
		Storage:   nats.FileStorage,
		MaxAge:    DLQRetention,
	}
}

func wantConfig(t *testing.T, stream string) nats.StreamConfig {
	t.Helper()
	switch stream {
	case MainStreamName:
		return wantMainConfig()
	case DLQStreamName:
		return wantDLQConfig()
	}
	t.Fatalf("unknown stream %q", stream)
	return nats.StreamConfig{}
}

func updateSubject(stream string) string { return "$JS.API.STREAM.UPDATE." + stream }
func createSubject(stream string) string { return "$JS.API.STREAM.CREATE." + stream }

// streamAPIWatch counts the JetStream stream API requests (create, update,
// info, ...) that a connection sends, keyed by request subject.
//
// Why the counts are exact and need no sleep: js.UpdateStream / AddStream
// are plain core-NATS requests on "$JS.API.STREAM.UPDATE.<name>" /
// "$JS.API.STREAM.CREATE.<name>" in the client's own account (nats.go
// v1.53.1 jsm.go:1138, js.go:195). A second connection subscribed to
// "$JS.API.STREAM.>" in that account is matched by the same sublist lookup
// that finds the JetStream API handler, and processMsgResults queues the
// message to every matching subscriber while handling the PUB (nats-server
// v2.15.1 client.go:4544 acc.sl.Match, client.go:4578 processMsgResults).
// take() then puts two barriers in order:
//  1. pub.Flush(): the server's read loop for a connection handles its
//     protocol ops one after another (client.go:1498 readLoop ->
//     client.go:1611 c.parse), so the PONG for this PING is sent only after
//     every earlier PUB on pub has been fanned out, i.e. queued to obs.
//  2. obs.Flush(): obs's outbound buffer is FIFO, so its PONG arrives after
//     any request queued in step 1, and nats.go's read loop puts a sync
//     subscription's message in its channel and bumps Pending() before it
//     reads the next op (nats.go processMsg, nats.go:3903-3924).
//
// After both flushes every request the code under test sent is in Pending().
// Tests still pair each "no update" check with a positive control that
// proves the watch does see a real update.
type streamAPIWatch struct {
	t   *testing.T
	pub *nats.Conn
	obs *nats.Conn
	sub *nats.Subscription
}

func watchStreamAPI(t *testing.T, pub *nats.Conn) *streamAPIWatch {
	t.Helper()
	obs, err := nats.Connect(pub.ConnectedUrl())
	if err != nil {
		t.Fatalf("observer connect: %v", err)
	}
	t.Cleanup(obs.Close)
	sub, err := obs.SubscribeSync("$JS.API.STREAM.>")
	if err != nil {
		t.Fatalf("observer subscribe: %v", err)
	}
	// The SUB is registered on the server before this PONG comes back, so
	// nothing published after this line can be missed.
	if err := obs.Flush(); err != nil {
		t.Fatalf("observer flush: %v", err)
	}
	return &streamAPIWatch{t: t, pub: pub, obs: obs, sub: sub}
}

// take returns the requests seen since the previous take, by subject.
func (w *streamAPIWatch) take() map[string]int {
	w.t.Helper()
	if err := w.pub.Flush(); err != nil {
		w.t.Fatalf("flush publisher: %v", err)
	}
	if err := w.obs.Flush(); err != nil {
		w.t.Fatalf("flush observer: %v", err)
	}
	n, _, err := w.sub.Pending()
	if err != nil {
		w.t.Fatalf("pending: %v", err)
	}
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		msg, err := w.sub.NextMsg(time.Second)
		if err != nil {
			w.t.Fatalf("read observed request %d of %d: %v", i+1, n, err)
		}
		counts[msg.Subject]++
	}
	return counts
}

func streamConfig(t *testing.T, js nats.JetStreamContext, stream string) nats.StreamConfig {
	t.Helper()
	info, err := js.StreamInfo(stream)
	if err != nil {
		t.Fatalf("stream info %s: %v", stream, err)
	}
	return info.Config
}

// assertWantedConfig checks the fields ensure() sets. A field the wanted
// config leaves unset is not checked: the server fills it with its default.
func assertWantedConfig(t *testing.T, got, want nats.StreamConfig) {
	t.Helper()
	if !reflect.DeepEqual(got.Subjects, want.Subjects) {
		t.Errorf("%s subjects = %v, want %v", want.Name, got.Subjects, want.Subjects)
	}
	if got.Retention != want.Retention {
		t.Errorf("%s retention = %v, want %v", want.Name, got.Retention, want.Retention)
	}
	if got.Storage != want.Storage {
		t.Errorf("%s storage = %v, want %v", want.Name, got.Storage, want.Storage)
	}
	if got.MaxAge != want.MaxAge {
		t.Errorf("%s max age = %v, want %v", want.Name, got.MaxAge, want.MaxAge)
	}
	if want.MaxBytes != 0 && got.MaxBytes != want.MaxBytes {
		t.Errorf("%s max bytes = %d, want %d", want.Name, got.MaxBytes, want.MaxBytes)
	}
	if want.MaxMsgs != 0 && got.MaxMsgs != want.MaxMsgs {
		t.Errorf("%s max msgs = %d, want %d", want.Name, got.MaxMsgs, want.MaxMsgs)
	}
	if got.Discard != want.Discard {
		t.Errorf("%s discard = %v, want %v", want.Name, got.Discard, want.Discard)
	}
	if want.Duplicates != 0 && got.Duplicates != want.Duplicates {
		t.Errorf("%s duplicates = %v, want %v", want.Name, got.Duplicates, want.Duplicates)
	}
}

// TestEnsureStreams_UnchangedStreamIsNotUpdated proves that ensuring a stream
// whose config already matches sends no update: a second EnsureStreams after
// the first one created both streams leaves them alone. The DLQ row also
// proves that server-filled defaults (max bytes/msgs -1, discard old,
// duplicates 2m) compare equal to the fields its config leaves unset. Each
// row ends with a positive control: a changed MaxAge does produce an update
// the watch sees, so a watch that saw nothing cannot pass the test.
func TestEnsureStreams_UnchangedStreamIsNotUpdated(t *testing.T) {
	tests := []struct {
		name   string
		stream string
	}{
		{"main stream", MainStreamName},
		{"dlq stream with server-filled defaults", DLQStreamName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nc, js, cleanup := startServer(t)
			defer cleanup()

			if err := EnsureStreams(js); err != nil {
				t.Fatalf("first EnsureStreams: %v", err)
			}
			watch := watchStreamAPI(t, nc)

			if err := EnsureStreams(js); err != nil {
				t.Fatalf("second EnsureStreams: %v", err)
			}
			seen := watch.take()
			if n := seen[updateSubject(tt.stream)]; n != 0 {
				t.Errorf("second EnsureStreams sent %d update(s) to unchanged %s, want 0", n, tt.stream)
			}
			if n := seen[createSubject(tt.stream)]; n != 0 {
				t.Errorf("second EnsureStreams sent %d create(s) for existing %s, want 0", n, tt.stream)
			}
			assertWantedConfig(t, streamConfig(t, js, tt.stream), wantConfig(t, tt.stream))

			// Positive control: a real difference is updated and observed.
			changed := wantConfig(t, tt.stream)
			changed.MaxAge = time.Hour
			if err := ensure(js, &changed); err != nil {
				t.Fatalf("ensure changed config: %v", err)
			}
			if n := watch.take()[updateSubject(tt.stream)]; n != 1 {
				t.Fatalf("control: ensure with a changed MaxAge sent %d update(s) to %s, want 1", n, tt.stream)
			}
			if got := streamConfig(t, js, tt.stream).MaxAge; got != time.Hour {
				t.Errorf("control: %s max age = %v after update, want 1h", tt.stream, got)
			}
		})
	}
}

// TestEnsureStreams_ChangedFieldIsUpdated proves that a stream that exists
// with a different value in one of the fields ensure() compares is updated
// exactly once by EnsureStreams, and that the wanted value is then in effect.
func TestEnsureStreams_ChangedFieldIsUpdated(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		drift  func(cfg *nats.StreamConfig)
	}{
		{"main max age", MainStreamName, func(c *nats.StreamConfig) { c.MaxAge = time.Hour }},
		{"main max bytes", MainStreamName, func(c *nats.StreamConfig) { c.MaxBytes = 1024 * 1024 }},
		{"main max msgs unlimited", MainStreamName, func(c *nats.StreamConfig) { c.MaxMsgs = -1 }},
		{"main discard", MainStreamName, func(c *nats.StreamConfig) { c.Discard = nats.DiscardNew }},
		{"main duplicates window", MainStreamName, func(c *nats.StreamConfig) { c.Duplicates = 2 * time.Minute }},
		{"main subjects", MainStreamName, func(c *nats.StreamConfig) { c.Subjects = []string{"vrsky.data.legacy.>"} }},
		{"main retention", MainStreamName, func(c *nats.StreamConfig) { c.Retention = nats.InterestPolicy }},
		{"dlq max age", DLQStreamName, func(c *nats.StreamConfig) { c.MaxAge = 24 * time.Hour }},
		// The DLQ leaves these unset; a value set by hand is drift and the
		// update puts the server default back.
		{"dlq max bytes set by hand", DLQStreamName, func(c *nats.StreamConfig) { c.MaxBytes = 1024 * 1024 }},
		{"dlq max msgs set by hand", DLQStreamName, func(c *nats.StreamConfig) { c.MaxMsgs = 1000 }},
		{"dlq discard set by hand", DLQStreamName, func(c *nats.StreamConfig) { c.Discard = nats.DiscardNew }},
		{"dlq duplicates set by hand", DLQStreamName, func(c *nats.StreamConfig) { c.Duplicates = time.Minute }},
		{"dlq subjects", DLQStreamName, func(c *nats.StreamConfig) {
			c.Subjects = []string{DLQSubjectAll, "vrsky.dlq-legacy.>"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nc, js, cleanup := startServer(t)
			defer cleanup()

			drifted := wantConfig(t, tt.stream)
			tt.drift(&drifted)
			if _, err := js.AddStream(&drifted); err != nil {
				t.Fatalf("create drifted %s: %v", tt.stream, err)
			}
			watch := watchStreamAPI(t, nc)

			if err := EnsureStreams(js); err != nil {
				t.Fatalf("EnsureStreams: %v", err)
			}
			if n := watch.take()[updateSubject(tt.stream)]; n != 1 {
				t.Errorf("EnsureStreams sent %d update(s) to drifted %s, want 1", n, tt.stream)
			}
			got := streamConfig(t, js, tt.stream)
			assertWantedConfig(t, got, wantConfig(t, tt.stream))
			if tt.stream == DLQStreamName {
				if got.MaxBytes != -1 || got.MaxMsgs != -1 || got.Discard != nats.DiscardOld ||
					got.Duplicates != 2*time.Minute {
					t.Errorf("dlq server defaults not restored: max bytes %d, max msgs %d, discard %v, duplicates %v",
						got.MaxBytes, got.MaxMsgs, got.Discard, got.Duplicates)
				}
			}
		})
	}
}

// TestEnsure_UpdateErrorIsIgnored proves that storage is one of the compared
// fields and that a rejected update stays best effort: the server refuses to
// change a stream's storage type, ensure() still returns nil, and the stream
// keeps working as it was.
func TestEnsure_UpdateErrorIsIgnored(t *testing.T) {
	nc, js, cleanup := startServer(t)
	defer cleanup()

	drifted := wantMainConfig()
	drifted.Storage = nats.MemoryStorage
	if _, err := js.AddStream(&drifted); err != nil {
		t.Fatalf("create memory-backed %s: %v", MainStreamName, err)
	}
	watch := watchStreamAPI(t, nc)

	want := wantMainConfig()
	if err := ensure(js, &want); err != nil {
		t.Fatalf("ensure returned %v; a rejected update must be ignored", err)
	}
	if n := watch.take()[updateSubject(MainStreamName)]; n != 1 {
		t.Errorf("ensure sent %d update(s) for a storage difference, want 1", n)
	}
	if got := streamConfig(t, js, MainStreamName).Storage; got != nats.MemoryStorage {
		t.Errorf("storage = %v; the server should have rejected the change", got)
	}
}

// TestEnsureStreams_MissingStreamIsCreated proves that EnsureStreams creates
// a stream that does not exist, with the wanted config, through a create
// request and not an update.
func TestEnsureStreams_MissingStreamIsCreated(t *testing.T) {
	tests := []struct {
		name     string
		existing []string // streams created with the wanted config beforehand
		missing  []string
	}{
		{"neither stream exists", nil, []string{MainStreamName, DLQStreamName}},
		{"only dlq missing", []string{MainStreamName}, []string{DLQStreamName}},
		{"only main missing", []string{DLQStreamName}, []string{MainStreamName}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nc, js, cleanup := startServer(t)
			defer cleanup()

			for _, s := range tt.existing {
				cfg := wantConfig(t, s)
				if _, err := js.AddStream(&cfg); err != nil {
					t.Fatalf("pre-create %s: %v", s, err)
				}
			}
			watch := watchStreamAPI(t, nc)

			if err := EnsureStreams(js); err != nil {
				t.Fatalf("EnsureStreams: %v", err)
			}
			seen := watch.take()
			for _, s := range tt.missing {
				if n := seen[createSubject(s)]; n != 1 {
					t.Errorf("EnsureStreams sent %d create(s) for missing %s, want 1", n, s)
				}
				if n := seen[updateSubject(s)]; n != 0 {
					t.Errorf("EnsureStreams sent %d update(s) to newly created %s, want 0", n, s)
				}
				assertWantedConfig(t, streamConfig(t, js, s), wantConfig(t, s))
			}
		})
	}
}
