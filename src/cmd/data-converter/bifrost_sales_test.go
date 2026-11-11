package main

// Tests for the Bifrost sales → Retailhero template (#817): the converter
// config the wizard deploys (ui/src/onboarding/testdata/bifrost-sales-to-retailhero.json)
// run on Basket event fixtures copied from ValueRetail/Bifrost
// basket/docs/events/. The output is held to test/fixtures/bifrost/
// intake-sale-batch.json, the same file cmd/http-producer sends to a stubbed
// Retailhero intake, so the two halves of the template meet in one golden.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
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

// The lines are not touched here: Retailhero reads the money from the event
// (line_ex_vat, line_vat, line_inc_vat). A converter that rounded or renamed
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
		case "operationId", "journalPosition", "event":
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

// Only copy and each may name the record with "."; rename and to_string with it
// are skipped like a missing source, and leave the record alone.
func TestApplyMappings_DotSourceOnOtherTypesIsSkipped(t *testing.T) {
	for _, typ := range []string{"rename", "to_string", "to_number", ""} {
		cfg := &ConverterNodeConfig{Mappings: []FieldMapping{{Type: typ, Source: ".", Target: "t"}}}
		got := mapObj(t, `{"a":1}`, cfg)
		assertJSONEqual(t, "type "+typ, got, mustJSON(t, `{"a":1}`))
	}
}

// A Basket return (#852) goes out the same way as a sale, with the return's
// operation ID: bifrost-return-<event_id>-r1.
func TestTemplate_BifrostReturn_WrapsTheEventAsOneBatchEntry(t *testing.T) {
	event := bifrostFixture(t, "basket-return-recorded.v1.json")
	got := throughJetStream(t, bifrostConfig(t), inputEnvelope(event, 1), 1)
	if len(got) != 1 {
		t.Fatalf("want one batch for one return, got %d", len(got))
	}
	assertJSONEqual(t, "batch", decodeJSON(t, got[0].Payload), decodeJSON(t, bifrostFixture(t, "intake-return-batch.json")))
}

// journalPosition is the till's journal_sequence as a decimal string (#851),
// for a sale and a return alike; sales and returns share one series per till.
func TestTemplate_Bifrost_JournalPositionIsTheJournalSequenceAsText(t *testing.T) {
	for name, want := range map[string]string{"basket-sale-recorded.v1.json": "42", "basket-return-recorded.v1.json": "43"} {
		got := throughJetStream(t, bifrostConfig(t), inputEnvelope(bifrostFixture(t, name), 1), 1)
		entry := decodeJSON(t, got[0].Payload).(map[string]interface{})["entries"].([]interface{})[0].(map[string]interface{})
		if jp, ok := entry["journalPosition"].(string); !ok || jp != want {
			t.Errorf("%s: journalPosition = %#v, want the string %q", name, entry["journalPosition"], want)
		}
	}
}

// An event Retailhero would refuse is not sent: one without a journal position
// (#851), or of a type the operation ID has no form for. The converter
// publishes nothing and fails the message with the field named, so it is
// retried and then dead-lettered with that reason in X-Vrsky-Last-Error.
func TestTemplate_Bifrost_AnEventRetailheroWouldRefuseIsNotSent(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		edit    func(ev map[string]interface{})
		want    string
	}{
		{"sale without journal_sequence", "basket-sale-recorded.v1.json",
			func(ev map[string]interface{}) { delete(ev["data"].(map[string]interface{}), "journal_sequence") }, `"data.journal_sequence"`},
		{"sale with a null journal_sequence", "basket-sale-recorded.v1.json",
			func(ev map[string]interface{}) { ev["data"].(map[string]interface{})["journal_sequence"] = nil }, `"data.journal_sequence"`},
		{"return without journal_sequence", "basket-return-recorded.v1.json",
			func(ev map[string]interface{}) { delete(ev["data"].(map[string]interface{}), "journal_sequence") }, `"data.journal_sequence"`},
		{"event without data", "basket-sale-recorded.v1.json",
			func(ev map[string]interface{}) { delete(ev, "data") }, `"data.journal_sequence"`},
		{"an event type with no operation ID", "basket-sale-recorded.v1.json",
			func(ev map[string]interface{}) { ev["event_type"] = "basket.completed" }, `"event_type" is "basket.completed"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ev map[string]interface{}
			if err := json.Unmarshal(bifrostFixture(t, c.fixture), &ev); err != nil {
				t.Fatal(err)
			}
			c.edit(ev)
			payload, _ := json.Marshal(ev)
			out, err := throughHandler(t, bifrostConfig(t), inputEnvelope(payload, 1))
			if err == nil {
				t.Fatal("the message was acked; it must fail so it is retried and dead-lettered")
			}
			if !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "required field missing") {
				t.Errorf("error %q does not name %s as a missing required field", err, c.want)
			}
			if len(out) != 0 {
				t.Errorf("a batch was published for an event Retailhero would refuse: %s", out[0].Payload)
			}
		})
	}
}

// lookup picks a text by the source's value and fills it in like a template.
// A value with no entry writes nothing unless the mapping is required.
func TestApplyMappings_Lookup(t *testing.T) {
	cfg := &ConverterNodeConfig{DropUnmapped: true, Mappings: []FieldMapping{
		{Type: "lookup", Source: "kind", Target: "op", Values: map[string]string{"a": "op-a-{id}", "7": "seven"}},
	}}
	assertJSONEqual(t, "match", mapObj(t, `{"kind":"a","id":"x1"}`, cfg), mustJSON(t, `{"op":"op-a-x1"}`))
	assertJSONEqual(t, "number key", mapObj(t, `{"kind":7}`, cfg), mustJSON(t, `{"op":"seven"}`))
	assertJSONEqual(t, "no entry", mapObj(t, `{"kind":"b","id":"x1"}`, cfg), mustJSON(t, `{}`))
	assertJSONEqual(t, "missing", mapObj(t, `{"id":"x1"}`, cfg), mustJSON(t, `{}`))
	assertJSONEqual(t, "null", mapObj(t, `{"kind":null}`, cfg), mustJSON(t, `{}`))
}

// checkRequired names the first required source a record lacks, inside each
// arrays with the element's index, and passes a record that has them all.
func TestCheckRequired(t *testing.T) {
	maps := []FieldMapping{
		{Type: "to_string", Source: "n", Target: "pos", Required: true},
		{Type: "rename", Source: "opt", Target: "o"},
		{Type: "each", Source: "lines", Target: "l", Mappings: []FieldMapping{{Type: "copy", Source: "sku", Target: "s", Required: true}}},
		{Type: "lookup", Source: "k", Target: "op", Required: true, Values: map[string]string{"x": "y"}},
	}
	cases := []struct{ in, want string }{
		{`{"n":1,"k":"x","lines":[{"sku":"a"}]}`, ""},
		{`{"n":0,"k":"x"}`, ""}, // 0 is a value; Retailhero judges it
		{`{"k":"x"}`, `"n" (mapped to "pos") is missing`},
		{`{"n":"","k":"x"}`, `"n" (mapped to "pos") is missing`},
		{`{"n":1,"k":"x","lines":[{"sku":"a"},{"qty":1}]}`, `lines[1]: required field missing: "sku"`},
		{`{"n":1,"k":"z"}`, `"k" is "z", which has no entry`},
		{`{"n":1}`, `"k" (mapped to "op") is missing`},
	}
	// required means nothing on a type that reads no single field: a static
	// or template row left with the flag after a type change must not
	// dead-letter every message.
	ignored := []FieldMapping{
		{Type: "static", Target: "a", Value: "x", Required: true},
		{Type: "template", Target: "b", Expression: "{n}", Required: true},
		{Type: "remove", Source: "gone", Required: true},
	}
	if err := checkRequired(map[string]interface{}{"n": 1.0}, ignored); err != nil {
		t.Errorf("required on static/template/remove: %v, want it ignored", err)
	}
	// "." is the record only for copy; a required to_string of "." writes
	// nothing, so it fails the check rather than passing it.
	if err := checkRequired(map[string]interface{}{"n": 1.0}, []FieldMapping{{Type: "to_string", Source: ".", Target: "t", Required: true}}); err == nil {
		t.Error(`a required to_string of "." passed the check, then would write nothing`)
	}
	if err := checkRequired(map[string]interface{}{"n": 1.0}, []FieldMapping{{Type: "copy", Source: ".", Target: "t", Required: true}}); err != nil {
		t.Errorf(`a required copy of "." is the record: %v`, err)
	}
	for _, c := range cases {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(c.in), &obj); err != nil {
			t.Fatal(err)
		}
		err := checkRequired(obj, maps)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: unexpected %v", c.in, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: error %v, want one with %q", c.in, err, c.want)
		}
	}
}

// The streamed path (payload over the rehydrate cap) refuses a record without
// a required field the same way: the message fails, nothing is published.
func TestConverter_StreamedRecordWithoutRequiredFieldFails(t *testing.T) {
	store := newMemStore()
	cfg := &ConverterNodeConfig{DropUnmapped: true, Mappings: []FieldMapping{{Type: "to_string", Source: "n", Target: "pos", Required: true}}}
	s := newTestConverterService(store, &ConverterEntry{NodeID: "cv1", Config: cfg, PredIsConsumer: true})
	s.rehydrateMax = 10 // below the payload: the streaming path
	msg := refEnvelopeMsg(t, store, []byte(`[{"n":1},{"other":2}]`))
	err := s.handleMessage(context.Background(), msg)
	if err == nil || !strings.Contains(err.Error(), `"n" (mapped to "pos") is missing`) {
		t.Fatalf("handleMessage = %v, want the missing required field", err)
	}
}

// With two branches, a record one branch requires a field of fails the
// message before either publishes, so the sibling does not publish again on
// every retry and DLQ replay.
func TestConverter_RequiredFailureStopsSiblingBranches(t *testing.T) {
	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	t.Cleanup(cleanup)
	strict := &ConverterNodeConfig{DropUnmapped: true, Mappings: []FieldMapping{{Type: "to_string", Source: "n", Target: "pos", Required: true}}}
	loose := &ConverterNodeConfig{DropUnmapped: true, Mappings: []FieldMapping{{Type: "rename", Source: "x", Target: "y"}}}
	s := newTestConverterService(newMemStore(),
		&ConverterEntry{NodeID: "loose", Config: loose, PredIsConsumer: true},
		&ConverterEntry{NodeID: "strict", Config: strict, PredIsConsumer: true})
	s.nc = nc
	s.pub = messaging.NewPublisher(js)

	var mu sync.Mutex
	published := 0
	sub, err := nc.Subscribe(tmplSubject, func(m *nats.Msg) {
		var env envelope.Envelope
		if json.Unmarshal(m.Data, &env) == nil && env.Metadata["_last_processed_by"] != nil {
			mu.Lock()
			published++
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	data, _ := json.Marshal(inputEnvelope([]byte(`{"x":1}`), 1))
	herr := s.handleMessage(context.Background(), &nats.Msg{Subject: tmplSubject, Data: data})
	if herr == nil || !strings.Contains(herr.Error(), `"n"`) {
		t.Fatalf("handleMessage = %v, want the missing required field", herr)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if published != 0 {
		t.Errorf("%d envelopes published; a sibling branch must not publish for a message that will be retried", published)
	}
}

// The editor's Test Transform says a record would be dead-lettered instead of
// showing a batch the live converter would never send.
func TestPreview_ReportsAMissingRequiredField(t *testing.T) {
	var ev map[string]interface{}
	if err := json.Unmarshal(bifrostFixture(t, "basket-sale-recorded.v1.json"), &ev); err != nil {
		t.Fatal(err)
	}
	delete(ev["data"].(map[string]interface{}), "journal_sequence")
	cfg := bifrostConfig(t)
	body, _ := json.Marshal(map[string]interface{}{"mappings": cfg.Mappings, "drop_unmapped": cfg.DropUnmapped, "sample_data": ev})

	rec := httptest.NewRecorder()
	handlePreview(rec, httptest.NewRequest(http.MethodPost, "/preview/conn-1", bytes.NewReader(body)))
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("preview answer %q: %v", rec.Body.String(), err)
	}
	if out.OK || !strings.Contains(out.Error, "data.journal_sequence") {
		t.Errorf("preview = %+v, want ok false naming data.journal_sequence", out)
	}

	// With the field there, the preview shows the batch.
	ev["data"].(map[string]interface{})["journal_sequence"] = 42
	body, _ = json.Marshal(map[string]interface{}{"mappings": cfg.Mappings, "drop_unmapped": cfg.DropUnmapped, "sample_data": ev})
	rec = httptest.NewRecorder()
	handlePreview(rec, httptest.NewRequest(http.MethodPost, "/preview/conn-1", bytes.NewReader(body)))
	if !strings.Contains(rec.Body.String(), `"journalPosition": "42"`) {
		t.Errorf("preview with the field = %s, want journalPosition \"42\"", rec.Body.String())
	}
}
