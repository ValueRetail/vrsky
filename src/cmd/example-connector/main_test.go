package main

import (
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Lock in the Consumer interface shape at compile time.
var _ sdk.Consumer = (*exampleConnector)(nil)

// TestExampleConnector_Publishes drives the reference connector through the SDK
// runner against an embedded JetStream (no Docker) and asserts it emits a
// well-formed envelope onto the data stream.
func TestExampleConnector_Publishes(t *testing.T) {
	t.Setenv("EXAMPLE_INTERVAL", "100ms")
	t.Setenv("EXAMPLE_TENANT", "tenant-x")
	t.Setenv("EXAMPLE_CONNECTION", "conn-x")

	h := harness.NewConsumerHarness(t, &exampleConnector{}, harness.Options{Name: "example-connector"})

	got := h.ExpectEnvelope(t, harness.MatchTenant("tenant-x"), 3*time.Second)
	if got.IntegrationID != "conn-x" {
		t.Errorf("integration id = %q, want conn-x", got.IntegrationID)
	}
	if got.ContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", got.ContentType)
	}
	if got.Source != "example-connector" {
		t.Errorf("source = %q, want example-connector", got.Source)
	}
	if len(got.Payload) == 0 {
		t.Error("payload is empty")
	}
}
