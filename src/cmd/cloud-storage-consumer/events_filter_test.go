package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sort"
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
				for _, k := range tt.wantSkipped {
					if !strings.Contains(res.logs, "level=DEBUG") || !strings.Contains(res.logs, "key="+k) {
						t.Errorf("no Debug skip line for %q:\n%s", k, res.logs)
					}
				}
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
