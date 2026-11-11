package main

// Tests for the Bifrost sales → Retailhero template (#817): the converter
// config the wizard deploys (ui/src/onboarding/testdata/bifrost-sales-to-retailhero.json)
// run on Basket event fixtures copied from ValueRetail/Bifrost
// basket/docs/events/. The output is held to test/fixtures/bifrost/
// intake-sale-batch.json, the same file cmd/http-producer sends to a stubbed
// Retailhero intake, so the two halves of the template meet in one golden.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const bifrostJournal = "J-TEST-01"

func bifrostFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "bifrost", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

// bifrostConfig is the template's converter config with the wizard's journal
// ID filled in.
func bifrostConfig(t *testing.T) *ConverterNodeConfig {
	t.Helper()
	cfg := loadTemplateConfig(t, "bifrost-sales-to-retailhero.json")
	filled := false
	for i := range cfg.Mappings {
		if m := &cfg.Mappings[i]; m.Type == "static" && m.Target == "journalId" {
			m.Value = bifrostJournal
			filled = true
		}
	}
	if !filled {
		t.Fatal("the template has no static journalId mapping for the wizard to fill")
	}
	return cfg
}

// A Basket sale-recorded event through the deployed config comes out as one
// BIFROST-BASKET-v1 batch: the event unchanged as the only entry's event, the
// source instance lifted to the batch, and an operation ID from the event ID.
func TestTemplate_BifrostSale_WrapsTheEventAsOneBatchEntry(t *testing.T) {
	event := bifrostFixture(t, "basket-sale-recorded.v1.json")
	got := throughJetStream(t, bifrostConfig(t), inputEnvelope(event, 1), 1)
	if len(got) != 1 {
		t.Fatalf("want one batch for one event, got %d", len(got))
	}
	assertJSONEqual(t, "batch", decodeJSON(t, got[0].Payload), decodeJSON(t, bifrostFixture(t, "intake-sale-batch.json")))
}

// The lines are not touched here: Retailhero derives net, tax and gross from
// unit_price, vat and quantity, exactly. A converter that rounded or renamed
// them would change what Retailhero reconciles.
func TestTemplate_BifrostSale_LeavesTheLinesAsBifrostSentThem(t *testing.T) {
	event := bifrostFixture(t, "basket-sale-recorded.v1.json")
	got := throughJetStream(t, bifrostConfig(t), inputEnvelope(event, 1), 1)
	batch := decodeJSON(t, got[0].Payload).(map[string]interface{})
	entries := batch["entries"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("want one entry, got %d", len(entries))
	}
	gotEvent := entries[0].(map[string]interface{})["event"]
	assertJSONEqual(t, "event", gotEvent, decodeJSON(t, event))

	// The wrapper members the profile names, and nothing else: an unknown
	// member of the wrapper or of an entry refuses the whole batch.
	for k := range batch {
		switch k {
		case "profileVersion", "schemaVersion", "sourceInstanceId", "journalId", "entries":
		default:
			t.Errorf("unexpected batch member %q", k)
		}
	}
	for k := range entries[0].(map[string]interface{}) {
		switch k {
		case "operationId", "event":
		default:
			t.Errorf("unexpected entry member %q", k)
		}
	}
}

// The operation ID must be a function of the Basket event ID alone: a
// redelivery (new transport envelope, same event) has to give Retailhero the
// same ID, and another event a different one. This is what stops a redelivered
// sale being counted twice.
func TestTemplate_BifrostSale_OperationIDFollowsTheEventID(t *testing.T) {
	cfg := bifrostConfig(t)
	event := bifrostFixture(t, "basket-sale-recorded.v1.json")

	opID := func(payload []byte) string {
		t.Helper()
		got := throughJetStream(t, cfg, inputEnvelope(payload, 1), 1)
		batch := decodeJSON(t, got[0].Payload).(map[string]interface{})
		return batch["entries"].([]interface{})[0].(map[string]interface{})["operationId"].(string)
	}

	first := opID(event)
	again := opID(event) // fresh envelope ID, fresh run: a redelivery
	if first != again {
		t.Errorf("the same event gave %q and then %q", first, again)
	}
	if want := "bifrost-sale-4f6b2d1e-8a3c-5b7d-9e0f-1a2b3c4d5e6f-r1"; first != want {
		t.Errorf("operation ID = %q, want %q", first, want)
	}

	var ev map[string]interface{}
	if err := json.Unmarshal(event, &ev); err != nil {
		t.Fatal(err)
	}
	ev["event_id"] = "4f6b2d1e-8a3c-5b7d-9e0f-ffffffffffff"
	other, _ := json.Marshal(ev)
	if second := opID(other); second == first || !strings.Contains(second, "ffffffffffff") {
		t.Errorf("another event ID gave %q (first was %q)", second, first)
	}
	if len(first) > 128 {
		t.Errorf("operation ID is %d bytes; Retailhero identifiers are at most 128", len(first))
	}
}

// "." as a source is the record itself: `each` over it builds a one-element
// array from the whole record, and `copy` of it nests the record unchanged.
func TestApplyMappings_DotSourceIsTheWholeRecord(t *testing.T) {
	cfg := &ConverterNodeConfig{DropUnmapped: true, Mappings: []FieldMapping{
		{Type: "each", Source: ".", Target: "items", DropUnmapped: true, Mappings: []FieldMapping{
			{Type: "copy", Source: ".", Target: "whole"},
			{Type: "rename", Source: "a", Target: "b"},
		}},
	}}
	got := mapObj(t, `{"a":1,"n":{"x":2}}`, cfg)
	want := mustJSON(t, `{"items":[{"whole":{"a":1,"n":{"x":2}},"b":1}]}`)
	assertJSONEqual(t, "dot source", got, want)
}

// Without "." a missing source is still skipped, not treated as the record.
func TestApplyMappings_EachOnAMissingSourceIsStillSkipped(t *testing.T) {
	cfg := &ConverterNodeConfig{DropUnmapped: true, Mappings: []FieldMapping{
		{Type: "each", Source: "nothing", Target: "items", Mappings: []FieldMapping{{Type: "copy", Source: ".", Target: "whole"}}},
	}}
	got := mapObj(t, `{"a":1}`, cfg)
	assertJSONEqual(t, "missing source", got, mustJSON(t, `{}`))
}
