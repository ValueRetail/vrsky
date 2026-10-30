package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #594: in event mode the consumer applies prefix and file_pattern
// to the keys a notification names, as poll mode does, and S3 ingests only
// object-created records (Azure keeps BlobCreated, GCS OBJECT_FINALIZE).
// A key outside the selection is acked and skipped: not published, not moved
// or deleted, not left for redelivery.

// efProviders turns object keys into the keys the provider's real parser yields
// for a "created" notification of them, so the table runs the parser and the
// loop together.
var efProviders = []struct {
	name     string
	provider string
	keys     func(keys ...string) []string
}{
	{"s3", objectstore.ProviderS3, func(keys ...string) []string {
		var recs []string
		for _, k := range keys {
			recs = append(recs, `{"eventName":"ObjectCreated:Put","s3":{"object":{"key":`+jsonQuote(k)+`}}}`)
		}
		return parseS3Notification([]byte(`{"Records":[` + strings.Join(recs, ",") + `]}`))
	}},
	{"azure", objectstore.ProviderAzure, func(keys ...string) []string {
		var evs []string
		for _, k := range keys {
			evs = append(evs, `{"eventType":"Microsoft.Storage.BlobCreated","subject":"/blobServices/default/containers/c/blobs/`+k+`"}`)
		}
		return parseAzureNotification([]byte(`[` + strings.Join(evs, ",") + `]`))
	}},
	{"gcs", objectstore.ProviderGCS, func(keys ...string) []string {
		var out []string
		for _, k := range keys {
			out = append(out, gcsObjectKeys(map[string]string{"eventType": "OBJECT_FINALIZE", "objectId": k})...)
		}
		return out
	}},
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type efResult struct {
	published []string // payload (= key) of each envelope
	acked     []string
	copied    map[string]string
	deleted   []string
	logs      string
}

// efRun runs the event loop on one message naming keys, over a store holding
// every key, until the message is acked or, when failKey makes one publish
// fail, until the other key has been ingested; then stops it.
func efRun(t *testing.T, cfg *cloudConfig, keys []string, failKey string) efResult {
	t.Helper()
	fs := &fakeStore{objects: map[string][]byte{}}
	for _, k := range keys {
		fs.objects[k] = []byte(k)
	}
	fe := &fakeEvents{keys: keys}
	var mu sync.Mutex
	var published []string
	logs := &mpLog{}
	c := &cloudConsumer{
		logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		active: make(map[string]*activeConn),
		publish: func(_ context.Context, e *envelope.Envelope) error {
			mu.Lock()
			defer mu.Unlock()
			if failKey != "" && string(e.Payload) == failKey {
				return io.ErrClosedPipe
			}
			published = append(published, string(e.Payload))
			return nil
		},
		newStore:  func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return fs, nil },
		newEvents: func(context.Context, *cloudConfig) (eventSource, error) { return fe, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		c.runEventLoop(ctx, &activeConn{cancel: cancel}, "conn", "tenant", cfg)
	}()
	if failKey == "" {
		harness.Eventually(t, srWait, "event message acked", func() bool {
			fe.mu.Lock()
			defer fe.mu.Unlock()
			return len(fe.acked) == 1
		})
	} else {
		// The loop does not ack a message with a failed key, so wait for the
		// last ingest of it instead: the one good key being deleted.
		harness.Eventually(t, srWait, "message handled", func() bool {
			fs.mu.Lock()
			defer fs.mu.Unlock()
			return len(fs.deleted) >= 1
		})
	}
	cancel()
	srAwait(t, stopped, "event loop to stop")

	fe.mu.Lock()
	defer fe.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return efResult{
		published: append([]string(nil), published...),
		acked:     append([]string(nil), fe.acked...),
		copied:    fs.copied,
		deleted:   append([]string(nil), fs.deleted...),
		logs:      logs.String(),
	}
}

func TestEventMode_AppliesPrefixAndFilePattern(t *testing.T) {
	tests := []struct {
		name        string
		prefix      string
		pattern     string
		keys        []string
		wantIngest  []string
		wantSkipped []string
	}{
		{name: "no prefix, no pattern ingests all", keys: []string{"a.csv", "x/b.json"}, wantIngest: []string{"a.csv", "x/b.json"}},
		{name: "prefix selects", prefix: "in/", keys: []string{"in/a.csv", "out/b.csv"}, wantIngest: []string{"in/a.csv"}, wantSkipped: []string{"out/b.csv"}},
		{name: "prefix is text, not a folder", prefix: "in", keys: []string{"in/a.csv", "in-done/b.csv", "other/in/c.csv"}, wantIngest: []string{"in/a.csv", "in-done/b.csv"}, wantSkipped: []string{"other/in/c.csv"}},
		{name: "prefix is case sensitive", prefix: "in/", keys: []string{"IN/a.csv"}, wantSkipped: []string{"IN/a.csv"}},
		{name: "pattern matches the base name", pattern: "*.csv", keys: []string{"a.csv", "deep/dir/b.csv", "c.json"}, wantIngest: []string{"a.csv", "deep/dir/b.csv"}, wantSkipped: []string{"c.json"}},
		{name: "pattern is not applied to the folder part", pattern: "in*", keys: []string{"in/a.csv", "x/in.csv"}, wantIngest: []string{"x/in.csv"}, wantSkipped: []string{"in/a.csv"}},
		{name: "prefix and pattern both apply", prefix: "in/", pattern: "*.csv", keys: []string{"in/a.csv", "in/b.json", "out/c.csv"}, wantIngest: []string{"in/a.csv"}, wantSkipped: []string{"in/b.json", "out/c.csv"}},
		{name: "malformed pattern matches nothing, as in poll mode", pattern: "[", keys: []string{"a.csv"}, wantSkipped: []string{"a.csv"}},
		{name: "everything skipped still acks", prefix: "in/", keys: []string{"out/a.csv", "out/b.csv"}, wantSkipped: []string{"out/a.csv", "out/b.csv"}},
		// The S3 console's "Create folder" writes a key ending in "/"; poll
		// mode's listing skips it, so event mode does too.
		{name: "folder placeholder skipped", prefix: "in/", keys: []string{"in/", "in/sub/", "in/a.csv"}, wantIngest: []string{"in/a.csv"}, wantSkipped: []string{"in/", "in/sub/"}},
		{name: "folder placeholder skipped without prefix", keys: []string{"new folder/", "a.csv"}, wantIngest: []string{"a.csv"}, wantSkipped: []string{"new folder/"}},
	}
	for _, p := range efProviders {
		for _, tt := range tests {
			t.Run(p.name+"/"+tt.name, func(t *testing.T) {
				cfg := &cloudConfig{
					Config:        objectstore.Config{Provider: p.provider, Bucket: "b", Prefix: tt.prefix},
					Mode:          "event",
					FilePattern:   tt.pattern,
					EventQueueURL: "http://sqs.local/q",
					AfterAction:   "delete",
				}
				res := efRun(t, cfg, p.keys(tt.keys...), "")

				got := append([]string(nil), res.published...)
				sort.Strings(got)
				want := append([]string(nil), tt.wantIngest...)
				sort.Strings(want)
				if strings.Join(got, ",") != strings.Join(want, ",") {
					t.Errorf("published %v, want %v", got, want)
				}
				// Only what was ingested is after-actioned: a skipped key is
				// neither deleted nor moved.
				del := append([]string(nil), res.deleted...)
				sort.Strings(del)
				if strings.Join(del, ",") != strings.Join(want, ",") {
					t.Errorf("deleted %v, want %v (skipped keys must be left alone)", del, want)
				}
				if len(res.acked) != 1 || res.acked[0] != "h1" {
					t.Errorf("acked %v, want [h1] (a skipped key must not leave the message for redelivery)", res.acked)
				}
				efCheckSkipLines(t, res.logs, tt.wantSkipped)
				if len(tt.wantSkipped) == 0 && strings.Contains(res.logs, "skipped") {
					t.Errorf("unexpected skip logged:\n%s", res.logs)
				}
				if strings.Contains(res.logs, "level=ERROR") || strings.Contains(res.logs, "level=WARN") {
					t.Errorf("skipping is not an error:\n%s", res.logs)
				}
			})
		}
	}
}

// efSkipLines returns the skip log lines in order.
func efSkipLines(logs string) []string {
	var out []string
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, "outside prefix or file_pattern; skipped") {
			out = append(out, l)
		}
	}
	return out
}

// efHasKey reports whether a slog text line carries key=k (quoted when k
// needs it).
func efHasKey(line, k string) bool {
	return strings.Contains(line, "key="+k+" ") || strings.Contains(line, "key="+strconv.Quote(k)+" ")
}

// efCheckSkipLines checks one skip line per skipped key, in order: the first
// at Info, so a filter that drops objects shows in production logs (they run
// at Info), the rest at Debug.
func efCheckSkipLines(t *testing.T, logs string, skipped []string) {
	t.Helper()
	lines := efSkipLines(logs)
	if len(lines) != len(skipped) {
		t.Fatalf("%d skip lines, want %d:\n%s", len(lines), len(skipped), logs)
	}
	for i, k := range skipped {
		want := "level=DEBUG"
		if i == 0 {
			want = "level=INFO"
		}
		if !strings.Contains(lines[i], want) || !efHasKey(lines[i], k) {
			t.Errorf("skip line %d = %q, want %s for key %q", i, lines[i], want, k)
		}
	}
}

// A skipped key never moves. With after_action=move the skipped object is not
// copied and the ingested one is.
func TestEventMode_SkippedKeyIsNotMoved(t *testing.T) {
	cfg := &cloudConfig{
		Config:        objectstore.Config{Provider: objectstore.ProviderS3, Bucket: "b", Prefix: "in/"},
		Mode:          "event",
		EventQueueURL: "http://sqs.local/q",
		AfterAction:   "move",
		MovePrefix:    "in-done",
	}
	res := efRun(t, cfg, efProviders[0].keys("in/a.csv", "out/b.csv"), "")
	if len(res.copied) != 1 || res.copied["in/a.csv"] != "in-done/a.csv" {
		t.Errorf("copied %v, want only in/a.csv -> in-done/a.csv", res.copied)
	}
	if len(res.deleted) != 1 || res.deleted[0] != "in/a.csv" {
		t.Errorf("deleted %v, want only in/a.csv", res.deleted)
	}
}

// Skipping keys changes nothing about redelivery of the ones that are
// ingested: a failed publish still leaves the message unacked.
func TestEventMode_FailedPublishStillLeavesMessageUnacked(t *testing.T) {
	cfg := &cloudConfig{
		Config:        objectstore.Config{Provider: objectstore.ProviderS3, Bucket: "b", Prefix: "in/"},
		Mode:          "event",
		EventQueueURL: "http://sqs.local/q",
		AfterAction:   "delete",
	}
	res := efRun(t, cfg, efProviders[0].keys("out/skip.csv", "in/bad.csv", "in/ok.csv"), "in/bad.csv")
	if len(res.acked) != 0 {
		t.Errorf("acked %v, want none: a key of the message failed", res.acked)
	}
	for _, d := range res.deleted {
		if d == "out/skip.csv" || d == "in/bad.csv" {
			t.Errorf("deleted %q", d)
		}
	}
}

// TestParseS3Notification_EventNames: only object-created records yield keys.
func TestParseS3Notification_EventNames(t *testing.T) {
	tests := []struct {
		eventName string
		want      bool
	}{
		{"ObjectCreated:Put", true},
		{"ObjectCreated:Post", true},
		{"ObjectCreated:Copy", true}, // how a move lands
		{"ObjectCreated:CompleteMultipartUpload", true},
		{"ObjectCreated:*", true},
		{"s3:ObjectCreated:Put", true}, // MinIO writes the s3: prefix
		{"", true},                     // no eventName: like GCS without eventType
		{"ObjectRemoved:Delete", false},
		{"ObjectRemoved:DeleteMarkerCreated", false}, // "Created" in the name, but a removal
		{"s3:ObjectRemoved:Delete", false},
		{"LifecycleExpiration:Delete", false},
		{"LifecycleTransition", false},
		{"ObjectRestore:Completed", false},
		{"ObjectTagging:Put", false},
		{"ObjectAcl:Put", false},
		{"Replication:OperationFailedReplication", false},
		{"IntelligentTiering", false},
		{"ReducedRedundancyLostObject", false},
		{"objectcreated:put", false}, // names are case sensitive
		// MinIO sends metadata changes under ObjectCreated; they would
		// republish an object whose content did not change.
		{"s3:ObjectCreated:PutTagging", false},
		{"s3:ObjectCreated:DeleteTagging", false},
		{"s3:ObjectCreated:PutRetention", false},
		{"s3:ObjectCreated:PutLegalHold", false},
		{"ObjectCreated:PutTagging", false},
		{"ObjectCreated:PutLegalHold", false},
		{"s3:ObjectCreated:CompleteMultipartUpload", true},
	}
	for _, tt := range tests {
		t.Run(tt.eventName, func(t *testing.T) {
			body := `{"Records":[{"eventName":` + jsonQuote(tt.eventName) + `,"s3":{"object":{"key":"in/a.csv"}}}]}`
			got := parseS3Notification([]byte(body))
			if tt.want && (len(got) != 1 || got[0] != "in/a.csv") {
				t.Errorf("keys = %v, want [in/a.csv]", got)
			}
			if !tt.want && len(got) != 0 {
				t.Errorf("keys = %v, want none", got)
			}
		})
	}

	t.Run("mixed records", func(t *testing.T) {
		body := `{"Records":[
			{"eventName":"ObjectCreated:Put","s3":{"object":{"key":"a.csv"}}},
			{"eventName":"ObjectRemoved:Delete","s3":{"object":{"key":"b.csv"}}},
			{"eventName":"ObjectCreated:CompleteMultipartUpload","s3":{"object":{"key":"c+d.csv"}}}]}`
		got := parseS3Notification([]byte(body))
		if strings.Join(got, ",") != "a.csv,c d.csv" {
			t.Errorf("keys = %v, want [a.csv c d.csv]", got)
		}
	})
}

// Poll mode is unchanged: its file_pattern is the shared helper, and prefix
// stays with the listing.
func TestPollMode_PrefixAndPatternUnchanged(t *testing.T) {
	fs := &fakeStore{objects: map[string][]byte{
		"in/a.csv": []byte("in/a.csv"), "in/b.json": []byte("in/b.json"), "out/c.csv": []byte("out/c.csv"),
	}}
	var mu sync.Mutex
	var got []string
	c := &cloudConsumer{
		publish: func(_ context.Context, e *envelope.Envelope) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, string(e.Payload))
			return nil
		},
	}
	cfg := &cloudConfig{
		Config:      objectstore.Config{Bucket: "b", Prefix: "in/"},
		FilePattern: "*.csv",
		AfterAction: "none",
	}
	done := c.doneSet("conn", "tenant", cfg)
	c.pollOnce(context.Background(), "conn", "tenant", fs, cfg, done, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(got) != 1 || got[0] != "in/a.csv" {
		t.Errorf("published %v, want [in/a.csv]", got)
	}
}

// S3 notifications URL-encode keys (spaces as '+'). prefix and file_pattern
// apply to the decoded key, as the listing returns it in poll mode.
func TestEventMode_EncodedS3KeyMatchesDecoded(t *testing.T) {
	const raw = `in+folder/%C3%A4/my+file%2B1.csv`
	const decoded = "in folder/ä/my file+1.csv"
	tests := []struct {
		name    string
		prefix  string
		pattern string
		ingest  bool
	}{
		{name: "decoded prefix and pattern", prefix: "in folder/ä/", pattern: "my file+*.csv", ingest: true},
		{name: "decoded prefix", prefix: "in folder/", ingest: true},
		{name: "encoded prefix does not match", prefix: "in+folder/", ingest: false},
		{name: "encoded pattern does not match", pattern: "my+file%2B*.csv", ingest: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := parseS3Notification([]byte(`{"Records":[{"eventName":"ObjectCreated:Put","s3":{"object":{"key":"` + raw + `"}}}]}`))
			if len(keys) != 1 || keys[0] != decoded {
				t.Fatalf("parsed keys %q, want [%q]", keys, decoded)
			}
			cfg := &cloudConfig{
				Config:        objectstore.Config{Provider: objectstore.ProviderS3, Bucket: "b", Prefix: tt.prefix},
				Mode:          "event",
				FilePattern:   tt.pattern,
				EventQueueURL: "http://sqs.local/q",
			}
			res := efRun(t, cfg, keys, "")
			if got := len(res.published) == 1 && res.published[0] == decoded; got != tt.ingest {
				t.Errorf("published %q, want ingest=%v", res.published, tt.ingest)
			}
			if len(res.acked) != 1 {
				t.Errorf("acked %v, want one", res.acked)
			}
		})
	}
}

// nqEvents is a queue of event messages that a store copy can add to, as a
// real bucket notification does for the object a move writes.
type nqEvents struct {
	mu    sync.Mutex
	queue []eventMessage
	n     int
	acked []string
	wake  chan struct{}
}

func newNQEvents(msgs ...[]string) *nqEvents {
	q := &nqEvents{wake: make(chan struct{}, 1)}
	for _, keys := range msgs {
		q.push(keys...)
	}
	return q
}

func (q *nqEvents) push(keys ...string) {
	q.mu.Lock()
	q.n++
	q.queue = append(q.queue, eventMessage{objectKeys: keys, ackHandle: fmt.Sprintf("h%d", q.n)})
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *nqEvents) Receive(ctx context.Context) ([]eventMessage, error) {
	for {
		q.mu.Lock()
		if len(q.queue) > 0 {
			out := q.queue
			q.queue = nil
			q.mu.Unlock()
			return out, nil
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-q.wake:
		}
	}
}

func (q *nqEvents) Ack(_ context.Context, handle string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.acked = append(q.acked, handle)
	return nil
}

// notifyingStore is a fakeStore whose Copy notifies the copy's key, as S3,
// Azure and GCS do when a move writes the object to move_prefix.
type notifyingStore struct {
	*fakeStore
	events *nqEvents
}

func (s *notifyingStore) Copy(ctx context.Context, src, dst string) error {
	if err := s.fakeStore.Copy(ctx, src, dst); err != nil {
		return err
	}
	s.events.push(dst)
	return nil
}

// nqRun runs the event loop over q and store until want messages are acked
// and the queue is empty, then stops it. It returns what was published and
// the log.
func nqRun(t *testing.T, cfg *cloudConfig, q *nqEvents, store objectstore.ObjectStore, want int) ([]string, string) {
	t.Helper()
	var mu sync.Mutex
	var published []string
	logs := &mpLog{}
	c := &cloudConsumer{
		logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		active: make(map[string]*activeConn),
		publish: func(_ context.Context, e *envelope.Envelope) error {
			mu.Lock()
			defer mu.Unlock()
			published = append(published, string(e.Payload))
			return nil
		},
		newStore:  func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return store, nil },
		newEvents: func(context.Context, *cloudConfig) (eventSource, error) { return q, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		c.runEventLoop(ctx, &activeConn{cancel: cancel}, "conn", "tenant", cfg)
	}()
	harness.Eventually(t, srWait, fmt.Sprintf("%d messages acked", want), func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return len(q.acked) >= want && len(q.queue) == 0
	})
	cancel()
	srAwait(t, stopped, "event loop to stop")
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), published...), logs.String()
}

// The setup the docs used to recommend: event mode, after_action move, empty
// prefix, the notification scoped to a folder only by the bucket config (here
// it covers everything, the worst case). The start goes ahead with a Warn,
// and the moved object that comes back is published once more and left in
// move_prefix: the own-key guard refuses the move, so nothing loops and
// nothing is lost.
func TestEventMode_MoveInsidePrefix_MovedKeyComesBackOnce(t *testing.T) {
	// The reviewer's probe: the config is still reported (poll mode refuses
	// it), but the event-mode start only warns, as the start table checks.
	cfg := &cloudConfig{
		Config:        objectstore.Config{Provider: objectstore.ProviderS3, Bucket: "b", Prefix: ""},
		Mode:          "event",
		EventQueueURL: "http://sqs.local/q",
		AfterAction:   "move",
		MovePrefix:    "processed",
	}
	if err := cfg.validateMoveOutsideListing(); err == nil {
		t.Fatal("validateMoveOutsideListing: want the overlap reported")
	}

	q := newNQEvents([]string{"in/a.csv"})
	fs := &fakeStore{objects: map[string][]byte{"in/a.csv": []byte("in/a.csv")}}
	published, logs := nqRun(t, cfg, q, &notifyingStore{fakeStore: fs, events: q}, 2)

	// One original publish, one duplicate from the moved key's notification.
	if len(published) != 2 {
		t.Fatalf("published %d times (%v), want 2: the original and one duplicate", len(published), published)
	}
	q.mu.Lock()
	n, acked := q.n, append([]string(nil), q.acked...)
	q.mu.Unlock()
	if n != 2 || len(acked) != 2 {
		t.Fatalf("messages %d, acked %v; want 2 and 2 (the moved key's message must not loop)", n, acked)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, ok := fs.objects["processed/a.csv"]; !ok {
		t.Fatalf("object not in move_prefix: %v", fs.objects)
	}
	if _, ok := fs.objects["in/a.csv"]; ok {
		t.Fatal("source still there after the move")
	}
	if len(fs.copied) != 1 || fs.copied["in/a.csv"] != "processed/a.csv" {
		t.Fatalf("copied %v, want only in/a.csv -> processed/a.csv", fs.copied)
	}
	if !strings.Contains(logs, "already in move_prefix") {
		t.Errorf("own-key guard did not refuse the second move:\n%s", logs)
	}
}

// The first skip of a connection's run logs at Info, later ones at Debug,
// across messages too.
func TestEventMode_FirstSkipInfoAcrossMessages(t *testing.T) {
	cfg := &cloudConfig{
		Config:        objectstore.Config{Provider: objectstore.ProviderS3, Bucket: "b", Prefix: "in/"},
		Mode:          "event",
		EventQueueURL: "http://sqs.local/q",
	}
	q := newNQEvents([]string{"out/a.csv"}, []string{"out/b.csv"}, []string{"in/c.csv"})
	fs := &fakeStore{objects: map[string][]byte{"in/c.csv": []byte("in/c.csv")}}
	published, logs := nqRun(t, cfg, q, fs, 3)
	if len(published) != 1 || published[0] != "in/c.csv" {
		t.Errorf("published %v, want [in/c.csv]", published)
	}
	efCheckSkipLines(t, logs, []string{"out/a.csv", "out/b.csv"})
}
