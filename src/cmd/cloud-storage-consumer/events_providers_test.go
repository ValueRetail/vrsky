package main

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// TestParseAzureNotification covers Event Grid Blob notifications delivered into
// a Storage Queue: base64-encoded arrays, raw single events, the data.url
// fallback, URL-decoding, and filtering out non-created events.
func TestParseAzureNotification(t *testing.T) {
	// Array of events, base64-encoded (Event Grid's default queue encoding); the
	// key has a URL-encoded space and a prefix path.
	arr := `[{"eventType":"Microsoft.Storage.BlobCreated","subject":"/blobServices/default/containers/c/blobs/incoming/orders%20list.json","data":{"url":"https://acct.blob.core.windows.net/c/incoming/orders%20list.json"}}]`
	b64 := base64.StdEncoding.EncodeToString([]byte(arr))
	if got := parseAzureNotification([]byte(b64)); len(got) != 1 || got[0] != "incoming/orders list.json" {
		t.Errorf("base64 array: keys = %v, want [incoming/orders list.json]", got)
	}

	// Raw (non-base64) single event.
	raw := `{"eventType":"Microsoft.Storage.BlobCreated","subject":"/blobServices/default/containers/c/blobs/a/b.csv"}`
	if got := parseAzureNotification([]byte(raw)); len(got) != 1 || got[0] != "a/b.csv" {
		t.Errorf("raw single: keys = %v, want [a/b.csv]", got)
	}

	// data.url fallback when there is no subject.
	urlOnly := `{"eventType":"Microsoft.Storage.BlobCreated","data":{"url":"https://acct.blob.core.windows.net/cont/path/to/file.json"}}`
	if got := parseAzureNotification([]byte(urlOnly)); len(got) != 1 || got[0] != "path/to/file.json" {
		t.Errorf("data.url fallback: keys = %v, want [path/to/file.json]", got)
	}

	// Non-created events (delete) yield no keys so the message drains.
	del := `[{"eventType":"Microsoft.Storage.BlobDeleted","subject":"/blobServices/default/containers/c/blobs/x"}]`
	if got := parseAzureNotification([]byte(del)); len(got) != 0 {
		t.Errorf("BlobDeleted should yield no keys, got %v", got)
	}

	// A message with a subject/url but no eventType (e.g. an Event Grid
	// subscription-validation event) must NOT be ingested.
	noType := `{"subject":"/blobServices/default/containers/c/blobs/x","data":{"url":"https://acct.blob.core.windows.net/c/x"}}`
	if got := parseAzureNotification([]byte(noType)); len(got) != 0 {
		t.Errorf("missing eventType should yield no keys, got %v", got)
	}

	// Garbage yields no keys (and does not panic).
	if got := parseAzureNotification([]byte("not json")); len(got) != 0 {
		t.Errorf("garbage should yield no keys, got %v", got)
	}
}

// TestGCSObjectKeys covers the Pub/Sub attribute extraction + event-type filter.
func TestGCSObjectKeys(t *testing.T) {
	if got := gcsObjectKeys(map[string]string{"eventType": "OBJECT_FINALIZE", "objectId": "in/x.json"}); len(got) != 1 || got[0] != "in/x.json" {
		t.Errorf("finalize: keys = %v, want [in/x.json]", got)
	}
	if got := gcsObjectKeys(map[string]string{"eventType": "OBJECT_DELETE", "objectId": "in/x.json"}); len(got) != 0 {
		t.Errorf("delete should yield no keys, got %v", got)
	}
	// No eventType attribute: still ingest (treat as a finalize-style event).
	if got := gcsObjectKeys(map[string]string{"objectId": "in/y.json"}); len(got) != 1 || got[0] != "in/y.json" {
		t.Errorf("no eventType: keys = %v, want [in/y.json]", got)
	}
	if got := gcsObjectKeys(nil); got != nil {
		t.Errorf("nil attrs should yield nil, got %v", got)
	}
}

// TestNewEventSource_Dispatch verifies the provider dispatch + the
// provider-specific required-identifier errors (no network / client build for
// the error paths).
func TestNewEventSource_Dispatch(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		cfg     *cloudConfig
		wantErr string // substring; "" means expect success
	}{
		{
			name:    "s3 missing queue url",
			cfg:     &cloudConfig{Config: objectstore.Config{Provider: objectstore.ProviderS3}, Mode: "event"},
			wantErr: "event_queue_url",
		},
		{
			name:    "azure missing queue name",
			cfg:     &cloudConfig{Config: objectstore.Config{Provider: objectstore.ProviderAzure}, Mode: "event"},
			wantErr: "event_queue_name",
		},
		{
			name:    "gcs missing subscription",
			cfg:     &cloudConfig{Config: objectstore.Config{Provider: objectstore.ProviderGCS}, Mode: "event"},
			wantErr: "event_subscription",
		},
		{
			name:    "unknown provider",
			cfg:     &cloudConfig{Config: objectstore.Config{Provider: "wasabi"}, Mode: "event"},
			wantErr: "not implemented",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newEventSource(ctx, tc.cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestValidateEventConfig mirrors the provider-aware start-command gate.
func TestValidateEventConfig(t *testing.T) {
	ok := []*cloudConfig{
		{Config: objectstore.Config{Provider: objectstore.ProviderS3}, EventQueueURL: "http://q"},
		{Config: objectstore.Config{Provider: objectstore.ProviderAzure}, EventQueueName: "q"},
		{Config: objectstore.Config{Provider: objectstore.ProviderGCS}, EventSubscription: "s", EventProject: "p"},
		{Config: objectstore.Config{Provider: objectstore.ProviderGCS}, EventSubscription: "projects/p/subscriptions/s"}, // full path needs no project
		{Config: objectstore.Config{}, EventQueueURL: "http://q"},                                                        // empty provider defaults to s3
	}
	for i, c := range ok {
		if err := c.validateEventConfig(); err != nil {
			t.Errorf("ok[%d]: unexpected error: %v", i, err)
		}
	}
	bad := []*cloudConfig{
		{Config: objectstore.Config{Provider: objectstore.ProviderS3}},
		{Config: objectstore.Config{Provider: objectstore.ProviderAzure}},
		{Config: objectstore.Config{Provider: objectstore.ProviderGCS}},
		{Config: objectstore.Config{Provider: objectstore.ProviderGCS}, EventSubscription: "s"}, // bare name, no project
	}
	for i, c := range bad {
		if err := c.validateEventConfig(); err == nil {
			t.Errorf("bad[%d]: expected error, got nil", i)
		}
	}
}
