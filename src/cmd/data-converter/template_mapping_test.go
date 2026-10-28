package main

// Tests for the Sitoo ⇄ Business Central onboarding templates (#435), written
// from plans/issue-435-sitoo-bc-mapping.md: the converter configs the wizard
// deploys (ui/src/onboarding/testdata) run against the repo's Sitoo and BC fixtures,
// plus the new "each" mapping type, the split_records node flag and the
// to_string decimal fix.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

const (
	tmplSubject      = "vrsky.data.tenant-x.pipeline.conn-1"
	tmplNodeID       = "cv1"
	testCustomerNumb = "C00010"
)

// --- loading the template configs and fixtures from disk ---

func loadTemplateConfig(t *testing.T, name string) *ConverterNodeConfig {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "ui", "src", "onboarding", "testdata", name))
	if err != nil {
		t.Fatalf("read template config %s: %v", name, err)
	}
	var cfg ConverterNodeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal template config %s: %v", name, err)
	}
	return &cfg
}

// sitooToBCConfig is the Sitoo→BC converter config with the wizard's customer
// number filled in, the way the wizard would before deploying.
func sitooToBCConfig(t *testing.T) *ConverterNodeConfig {
	t.Helper()
	cfg := loadTemplateConfig(t, "sitoo-to-business-central.json")
	filled := false
	for i := range cfg.Mappings {
		m := &cfg.Mappings[i]
		if m.Type == "static" && m.Target == "customerNumber" {
			m.Value = testCustomerNumb
			filled = true
		}
	}
	if !filled {
		t.Fatal("sitoo-to-business-central.json has no static customerNumber mapping for the wizard to fill")
	}
	return cfg
}

func bcToSitooConfig(t *testing.T) *ConverterNodeConfig {
	t.Helper()
	return loadTemplateConfig(t, "business-central-to-sitoo.json")
}

// sitooPagePayload is what the Sitoo consumer publishes for the fixture page:
// the page's items array.
func sitooPagePayload(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "sitoo", "transactions_page.json"))
	if err != nil {
		t.Fatalf("read sitoo fixture: %v", err)
	}
	var page struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("unmarshal sitoo fixture: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatal("sitoo fixture has no items")
	}
	return page.Items
}

// bcItemsPayload is the BC consumer's golden items page with the fields a
// real API v2.0 item carries and the golden lacks: inventory, gtin, type.
func bcItemsPayload(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "business-central", "envelopes.golden.json"))
	if err != nil {
		t.Fatalf("read bc golden: %v", err)
	}
	var envs []struct {
		Payload []map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &envs); err != nil {
		t.Fatalf("unmarshal bc golden: %v", err)
	}
	if len(envs) == 0 || len(envs[0].Payload) != 2 {
		t.Fatalf("bc golden: want one envelope with two items, got %+v", envs)
	}
	items := envs[0].Payload
	extra := []map[string]interface{}{
		{"inventory": 12, "gtin": "5701234000017", "type": "Inventory", "baseUnitOfMeasureCode": "PCS", "blocked": false},
		{"inventory": 1000000, "gtin": "5701234000024", "type": "Inventory", "baseUnitOfMeasureCode": "PCS", "blocked": false},
	}
	for i := range items {
		for k, v := range extra[i] {
			items[i][k] = v
		}
	}
	b, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal bc items: %v", err)
	}
	return b
}

// --- running the converter ---

func inputEnvelope(payload []byte, recordCount int) *envelope.Envelope {
	env := envelope.New()
	env.TenantID = "tenant-x"
	env.IntegrationID = "conn-1"
	env.ContentType = "application/json"
	env.Payload = payload
	env.Metadata = map[string]interface{}{"record_count": recordCount}
	return env
}

// collector captures every envelope the converter node republishes.
type collector struct {
	mu   sync.Mutex
	envs []*envelope.Envelope
	ch   chan struct{}
}

func (c *collector) handle(m *nats.Msg) {
	var env envelope.Envelope
	if json.Unmarshal(m.Data, &env) != nil || env.Metadata == nil {
		return
	}
	if v, _ := env.Metadata["_last_processed_by"].(string); v != tmplNodeID {
		return
	}
	c.mu.Lock()
	c.envs = append(c.envs, &env)
	c.mu.Unlock()
	select {
	case c.ch <- struct{}{}:
	default:
	}
}

func (c *collector) snapshot() []*envelope.Envelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*envelope.Envelope(nil), c.envs...)
}

// startConverter boots the real converter on embedded JetStream with one node
// and a subscriber that captures its output.
func startConverter(t *testing.T, cfg *ConverterNodeConfig) (*ConverterService, nats.JetStreamContext, *collector) {
	t.Helper()
	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	t.Cleanup(cleanup)

	s := newTestConverterService(newMemStore(), &ConverterEntry{NodeID: tmplNodeID, Config: cfg, PredIsConsumer: true})
	s.nc = nc
	s.pub = messaging.NewPublisher(js)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(s.Stop)

	c := &collector{ch: make(chan struct{}, 64)}
	sub, err := nc.Subscribe(tmplSubject, c.handle)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return s, js, c
}

// throughJetStream publishes the input envelope onto the stream (the path a
// consumer's message takes) and returns what the node republished: it waits
// for `want` envelopes, then a little longer to catch any extra ones.
func throughJetStream(t *testing.T, cfg *ConverterNodeConfig, in *envelope.Envelope, want int) []*envelope.Envelope {
	t.Helper()
	_, js, c := startConverter(t, cfg)
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if _, err := js.Publish(tmplSubject, data); err != nil {
		t.Fatalf("publish: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for len(c.snapshot()) < want {
		select {
		case <-c.ch:
		case <-deadline:
			t.Fatalf("got %d republished envelopes, want %d", len(c.snapshot()), want)
		}
	}
	time.Sleep(500 * time.Millisecond) // anything beyond `want` would show up here
	return c.snapshot()
}

// throughHandler calls the converter's message handler directly (it returns
// once every output is published) and returns the outputs and its error.
func throughHandler(t *testing.T, cfg *ConverterNodeConfig, in *envelope.Envelope) ([]*envelope.Envelope, error) {
	t.Helper()
	s, _, c := startConverter(t, cfg)
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	herr := s.handleMessage(context.Background(), &nats.Msg{Subject: tmplSubject, Data: data})
	if err := s.nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	return c.snapshot(), herr
}

// --- assertion helpers ---

func decodeJSON(t *testing.T, b []byte) interface{} {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return v
}

func mustJSON(t *testing.T, s string) interface{} {
	t.Helper()
	return decodeJSON(t, []byte(s))
}

func assertJSONEqual(t *testing.T, label string, got, want interface{}) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		t.Errorf("%s:\n got: %s\nwant: %s", label, g, w)
	}
}

func metaNumber(t *testing.T, env *envelope.Envelope, key string) (float64, bool) {
	t.Helper()
	v, ok := env.Metadata[key]
	if !ok {
		return 0, false
	}
	f, isNum := v.(float64)
	if !isNum {
		t.Errorf("metadata %s = %#v (%T), want a number", key, v, v)
		return 0, false
	}
	return f, true
}

// mapObj runs applyMappings on a fresh decode of the JSON object in `in`.
func mapObj(t *testing.T, in string, cfg *ConverterNodeConfig) map[string]interface{} {
	t.Helper()
	obj, ok := mustJSON(t, in).(map[string]interface{})
	if !ok {
		t.Fatalf("test input is not an object: %s", in)
	}
	out, _ := applyMappings(obj, cfg)
	return out
}

// Fixture transaction 90210 and 90211 as the plan says BC must receive them.
const (
	wantOrder90210 = `{
		"customerNumber": "C00010",
		"externalDocumentNumber": "S1-2026-000412",
		"salesInvoiceLines": [
			{"lineType": "Item", "lineObjectNumber": "VR-TSHIRT-M-BLK", "quantity": 2, "unitPrice": 499.5},
			{"lineType": "Item", "lineObjectNumber": "VR-CAP-OS-RED", "quantity": 1, "unitPrice": 250}
		]
	}`
	wantOrder90211 = `{
		"customerNumber": "C00010",
		"externalDocumentNumber": "S1-2026-000413",
		"salesInvoiceLines": [
			{"lineType": "Item", "lineObjectNumber": "VR-SOCKS-42-GRY", "quantity": 1, "unitPrice": 199}
		]
	}`
)

// --- POS sales → ERP (Sitoo → Business Central) ---

// The Sitoo page (two receipts in one message) through the deployed config on
// embedded JetStream comes out as two messages, one salesInvoice object each,
// exactly the plan's body: lines renamed, numbers as JSON numbers, everything
// else dropped.
func TestTemplate_SitooToBC_OneSalesInvoicePerTransaction(t *testing.T) {
	cfg := sitooToBCConfig(t)
	got := throughJetStream(t, cfg, inputEnvelope(sitooPagePayload(t), 2), 2)
	if len(got) != 2 {
		t.Fatalf("want exactly one envelope per transaction (2), got %d", len(got))
	}

	want := map[string]interface{}{
		"S1-2026-000412": mustJSON(t, wantOrder90210),
		"S1-2026-000413": mustJSON(t, wantOrder90211),
	}
	indexByDoc := map[string]float64{}
	for _, env := range got {
		if !bytes.HasPrefix(bytes.TrimSpace(env.Payload), []byte("{")) {
			t.Fatalf("each part must be one JSON object, not an array: %s", env.Payload)
		}
		// Plain decimal in the wire bytes, not the string "499.50".
		if bytes.Contains(env.Payload, []byte(`"499.50"`)) || bytes.Contains(env.Payload, []byte(`"250.00"`)) {
			t.Errorf("unitPrice must be a JSON number: %s", env.Payload)
		}
		order, ok := decodeJSON(t, env.Payload).(map[string]interface{})
		if !ok {
			t.Fatalf("payload is not an object: %s", env.Payload)
		}
		doc, _ := order["externalDocumentNumber"].(string)
		w, known := want[doc]
		if !known {
			t.Fatalf("unexpected order (externalDocumentNumber %q): %s", doc, env.Payload)
		}
		assertJSONEqual(t, "salesInvoice "+doc, order, w)
		delete(want, doc)

		if rc, ok := metaNumber(t, env, "record_count"); !ok || rc != 1 {
			t.Errorf("order %s: record_count = %v (present %v), want 1", doc, rc, ok)
		}
		if n, ok := metaNumber(t, env, "_split_count"); !ok || n != 2 {
			t.Errorf("order %s: _split_count = %v (present %v), want 2", doc, n, ok)
		}
		idx, ok := metaNumber(t, env, "_split_index")
		if !ok {
			t.Errorf("order %s: no _split_index in metadata", doc)
		}
		indexByDoc[doc] = idx
	}
	if len(want) != 0 {
		t.Errorf("orders never published: %v", want)
	}

	// The plan names _split_index but not its base; the indices must be two
	// consecutive positions in input order.
	i0, i1 := indexByDoc["S1-2026-000412"], indexByDoc["S1-2026-000413"]
	if !(i0 == 0 && i1 == 1) && !(i0 == 1 && i1 == 2) {
		t.Errorf("_split_index: receipt 412 = %v, 413 = %v; want consecutive positions in input order", i0, i1)
	}
}

// split_records off is the existing behaviour: one message with the whole
// mapped array.
func TestTemplate_SitooToBC_SplitOffPublishesOneArray(t *testing.T) {
	cfg := sitooToBCConfig(t)
	cfg.SplitRecords = false
	got := throughJetStream(t, cfg, inputEnvelope(sitooPagePayload(t), 2), 1)
	if len(got) != 1 {
		t.Fatalf("split off: want one envelope, got %d", len(got))
	}
	want := []interface{}{mustJSON(t, wantOrder90210), mustJSON(t, wantOrder90211)}
	assertJSONEqual(t, "split off payload", decodeJSON(t, got[0].Payload), want)
	for _, k := range []string{"_split_index", "_split_count"} {
		if _, has := got[0].Metadata[k]; has {
			t.Errorf("split off must not set %s: %v", k, got[0].Metadata)
		}
	}
}

// --- split_records edge cases ---

func TestSplitRecords_EmptyArrayPublishesNothing(t *testing.T) {
	got, err := throughHandler(t, sitooToBCConfig(t), inputEnvelope([]byte(`[]`), 0))
	if err != nil {
		t.Fatalf("an empty page is not an infrastructure failure, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an empty page must publish nothing, got %d envelopes: %s", len(got), got[0].Payload)
	}
}

// A single object (not an array) still goes out as one message, as an object.
func TestSplitRecords_SingleObjectPublishesOneObject(t *testing.T) {
	in := `{"transactionid": 90210, "receiptnumber": "S1-2026-000412", "currency": "NOK",
		"items": [{"itemid": 55031, "sku": "VR-TSHIRT-M-BLK", "quantity": 2, "priceeach": "499.50"},
		          {"itemid": 55118, "sku": "VR-CAP-OS-RED", "quantity": 1, "priceeach": "250.00"}]}`
	got, err := throughHandler(t, sitooToBCConfig(t), inputEnvelope([]byte(in), 1))
	if err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("a single object must publish one envelope, got %d", len(got))
	}
	if !bytes.HasPrefix(bytes.TrimSpace(got[0].Payload), []byte("{")) {
		t.Fatalf("a single object must stay an object: %s", got[0].Payload)
	}
	assertJSONEqual(t, "single object", decodeJSON(t, got[0].Payload), mustJSON(t, wantOrder90210))
}

// A one-element page splits into exactly one object (not a one-element array).
func TestSplitRecords_OneElementArrayPublishesOneObject(t *testing.T) {
	in := `[{"receiptnumber": "S1-2026-000413", "items": [{"itemid": 55204, "sku": "VR-SOCKS-42-GRY", "quantity": 1, "priceeach": "199.00"}]}]`
	got, err := throughHandler(t, sitooToBCConfig(t), inputEnvelope([]byte(in), 1))
	if err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want one envelope, got %d", len(got))
	}
	assertJSONEqual(t, "one-element page", decodeJSON(t, got[0].Payload), mustJSON(t, wantOrder90211))
	if n, ok := metaNumber(t, got[0], "_split_count"); !ok || n != 1 {
		t.Errorf("_split_count = %v (present %v), want 1", n, ok)
	}
}

// Over the rehydrate cap the converter streams; the plan says the streaming
// path declines split_records with an error, like the formats it can't stream.
func TestSplitRecords_StreamingPathDeclines(t *testing.T) {
	store := newMemStore()
	s := newTestConverterService(store, &ConverterEntry{NodeID: tmplNodeID, Config: sitooToBCConfig(t), PredIsConsumer: true})
	s.rehydrateMax = 100 // the fixture page is far bigger → streaming path

	msg := refEnvelopeMsg(t, store, sitooPagePayload(t))
	err := s.handleMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("split_records over the rehydrate cap must be declined with an error, not streamed")
	}
	if !strings.Contains(err.Error(), "split_records") {
		t.Errorf("declined for another reason than split_records: %v", err)
	}
}

// A node that only splits (no mappings, no format, JSON in) is not a no-op:
// it must still publish one message per record.
func TestSplitRecords_SplitOnlyNodeSplits(t *testing.T) {
	in := `[{"a":1},{"a":2},{"a":3}]`
	got, err := throughHandler(t, &ConverterNodeConfig{SplitRecords: true}, inputEnvelope([]byte(in), 3))
	if err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("a split-only node published %d envelopes, want 3", len(got))
	}
	for _, env := range got {
		if !bytes.HasPrefix(bytes.TrimSpace(env.Payload), []byte("{")) {
			t.Errorf("part is not an object: %s", env.Payload)
		}
	}
}

// Split parts get IDs derived from the input, so a redelivered input split
// again inside the JetStream dedup window publishes the same IDs (and the
// stream drops the repeats). Different positions get different IDs.
func TestSplitRecords_PartIDsAreStableAcrossRedelivery(t *testing.T) {
	in := inputEnvelope(sitooPagePayload(t), 2)
	ids := func() []string {
		got, err := throughHandler(t, sitooToBCConfig(t), in)
		if err != nil {
			t.Fatalf("handleMessage: %v", err)
		}
		out := make([]string, 0, len(got))
		for _, env := range got {
			out = append(out, env.ID)
		}
		sort.Strings(out)
		return out
	}
	first, second := ids(), ids()
	if len(first) != 2 || first[0] == first[1] {
		t.Fatalf("want two distinct part IDs, got %v", first)
	}
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Errorf("the same input split twice gave different part IDs: %v vs %v", first, second)
	}
	for _, id := range first {
		if id == in.ID {
			t.Errorf("a part reuses the input's ID %s; JetStream would drop it as the input's duplicate", id)
		}
	}
}

// --- ERP inventory → POS (Business Central → Sitoo) ---

// The BC items page through the deployed config: one message, an array of
// Sitoo warehouse items with sku and a plain-decimal string decimaltotal.
func TestTemplate_BCToSitoo_WarehouseItems(t *testing.T) {
	got := throughJetStream(t, bcToSitooConfig(t), inputEnvelope(bcItemsPayload(t), 2), 1)
	if len(got) != 1 {
		t.Fatalf("no split: want one envelope for the page, got %d", len(got))
	}
	payload := got[0].Payload
	if bytes.Contains(payload, []byte("e+")) {
		t.Errorf("decimaltotal must be plain decimal, not exponent notation: %s", payload)
	}
	want := mustJSON(t, `[
		{"sku": "1896-S", "decimaltotal": "12"},
		{"sku": "1900-S", "decimaltotal": "1000000"}
	]`)
	assertJSONEqual(t, "warehouse items", decodeJSON(t, payload), want)
	for _, k := range []string{"_split_index", "_split_count"} {
		if _, has := got[0].Metadata[k]; has {
			t.Errorf("no split configured, yet %s is set: %v", k, got[0].Metadata)
		}
	}
}

// to_string writes numbers as plain decimals (the 1e+06 fix), through the
// deployed BC→Sitoo mapping.
func TestTemplate_BCToSitoo_ToStringPlainDecimal(t *testing.T) {
	cfg := bcToSitooConfig(t)
	cases := []struct {
		inventory string // JSON literal
		want      string
	}{
		{"0", "0"},
		{"12", "12"},
		{"1000000", "1000000"},
		{"25000000", "25000000"},
		{"12.5", "12.5"},
		{"1234567.25", "1234567.25"},
		{"-3", "-3"},
		{`"7"`, "7"},
	}
	for _, tc := range cases {
		t.Run(tc.inventory, func(t *testing.T) {
			out := mapObj(t, `{"number": "1896-S", "inventory": `+tc.inventory+`}`, cfg)
			assertJSONEqual(t, "warehouse item", out, map[string]interface{}{"sku": "1896-S", "decimaltotal": tc.want})
		})
	}
}

// --- missing-data rows of the mapping tables ---

func TestTemplate_MissingData(t *testing.T) {
	s2b := sitooToBCConfig(t)
	b2s := bcToSitooConfig(t)
	cases := []struct {
		name string
		cfg  *ConverterNodeConfig
		in   string
		want string
	}{
		{
			name: "no receiptnumber: order has no external number",
			cfg:  s2b,
			in:   `{"transactionid": 1, "items": [{"sku": "A", "quantity": 1, "priceeach": "10.00"}]}`,
			want: `{"customerNumber": "C00010", "salesInvoiceLines": [{"lineType": "Item", "lineObjectNumber": "A", "quantity": 1, "unitPrice": 10}]}`,
		},
		{
			name: "no items: order without lines",
			cfg:  s2b,
			in:   `{"transactionid": 1, "receiptnumber": "R1", "total": "0.00"}`,
			want: `{"customerNumber": "C00010", "externalDocumentNumber": "R1"}`,
		},
		{
			name: "empty items: empty line array",
			cfg:  s2b,
			in:   `{"receiptnumber": "R1", "items": []}`,
			want: `{"customerNumber": "C00010", "externalDocumentNumber": "R1", "salesInvoiceLines": []}`,
		},
		{
			name: "comma decimal price: unitPrice left out",
			cfg:  s2b,
			in:   `{"receiptnumber": "R1", "items": [{"itemid": 9, "sku": "A", "quantity": 2, "priceeach": "499,50"}]}`,
			want: `{"customerNumber": "C00010", "externalDocumentNumber": "R1", "salesInvoiceLines": [{"lineType": "Item", "lineObjectNumber": "A", "quantity": 2}]}`,
		},
		{
			name: "no price: unitPrice left out",
			cfg:  s2b,
			in:   `{"receiptnumber": "R1", "items": [{"sku": "A", "quantity": 2}]}`,
			want: `{"customerNumber": "C00010", "externalDocumentNumber": "R1", "salesInvoiceLines": [{"lineType": "Item", "lineObjectNumber": "A", "quantity": 2}]}`,
		},
		{
			name: "no sku: line without item",
			cfg:  s2b,
			in:   `{"receiptnumber": "R1", "items": [{"itemid": 9, "quantity": 1, "priceeach": "5.00"}]}`,
			want: `{"customerNumber": "C00010", "externalDocumentNumber": "R1", "salesInvoiceLines": [{"lineType": "Item", "quantity": 1, "unitPrice": 5}]}`,
		},
		{
			name: "no quantity: quantity left out",
			cfg:  s2b,
			in:   `{"receiptnumber": "R1", "items": [{"sku": "A", "priceeach": "5.00"}]}`,
			want: `{"customerNumber": "C00010", "externalDocumentNumber": "R1", "salesInvoiceLines": [{"lineType": "Item", "lineObjectNumber": "A", "unitPrice": 5}]}`,
		},
		{
			name: "unparseable quantity: quantity left out",
			cfg:  s2b,
			in:   `{"receiptnumber": "R1", "items": [{"sku": "A", "quantity": "two", "priceeach": "5.00"}]}`,
			want: `{"customerNumber": "C00010", "externalDocumentNumber": "R1", "salesInvoiceLines": [{"lineType": "Item", "lineObjectNumber": "A", "unitPrice": 5}]}`,
		},
		{
			name: "string quantity becomes a number",
			cfg:  s2b,
			in:   `{"receiptnumber": "R1", "items": [{"sku": "A", "quantity": "3", "priceeach": "5.00"}]}`,
			want: `{"customerNumber": "C00010", "externalDocumentNumber": "R1", "salesInvoiceLines": [{"lineType": "Item", "lineObjectNumber": "A", "quantity": 3, "unitPrice": 5}]}`,
		},
		{
			name: "bc item without inventory: no quantity",
			cfg:  b2s,
			in:   `{"id": "x", "number": "1896-S", "displayName": "ATHENS Desk"}`,
			want: `{"sku": "1896-S"}`,
		},
		{
			name: "bc item without number: no sku",
			cfg:  b2s,
			in:   `{"id": "x", "inventory": 4, "gtin": "123"}`,
			want: `{"decimaltotal": "4"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertJSONEqual(t, tc.name, mapObj(t, tc.in, tc.cfg), mustJSON(t, tc.want))
		})
	}
}

// The comma price row end to end: the converter publishes the order without a
// unitPrice instead of failing it.
func TestTemplate_SitooToBC_CommaPriceThroughConverter(t *testing.T) {
	in := `[{"transactionid": 1, "receiptnumber": "R1", "items": [{"itemid": 9, "sku": "A", "quantity": 2, "priceeach": "499,50"}]}]`
	got, err := throughHandler(t, sitooToBCConfig(t), inputEnvelope([]byte(in), 1))
	if err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want one order, got %d", len(got))
	}
	want := `{"customerNumber": "C00010", "externalDocumentNumber": "R1", "salesInvoiceLines": [{"lineType": "Item", "lineObjectNumber": "A", "quantity": 2}]}`
	assertJSONEqual(t, "order", decodeJSON(t, got[0].Payload), mustJSON(t, want))
}

// --- the "each" mapping type ---

func eachConfig(topDrop, nestedDrop bool) *ConverterNodeConfig {
	return &ConverterNodeConfig{
		DropUnmapped: topDrop,
		Mappings: []FieldMapping{{
			Type:         "each",
			Source:       "items",
			Target:       "lines",
			DropUnmapped: nestedDrop,
			Mappings: []FieldMapping{
				{Type: "rename", Source: "sku", Target: "code"},
				{Type: "static", Target: "kind", Value: "L"},
			},
		}},
	}
}

// Cases where the plan pins down the whole result object.
func TestEach_WholeResult(t *testing.T) {
	cases := []struct {
		name       string
		topDrop    bool
		nestedDrop bool
		in         string
		want       string
	}{
		{
			name: "missing source is left alone",
			in:   `{"id": 1}`,
			want: `{"id": 1}`,
		},
		{
			name: "string source is left alone",
			in:   `{"id": 1, "items": "oops"}`,
			want: `{"id": 1, "items": "oops"}`,
		},
		{
			name: "object source is left alone",
			in:   `{"id": 1, "items": {"sku": "A"}}`,
			want: `{"id": 1, "items": {"sku": "A"}}`,
		},
		{
			name: "null source is left alone",
			in:   `{"id": 1, "items": null}`,
			want: `{"id": 1, "items": null}`,
		},
		{
			name:       "top-level drop_unmapped keeps only the mapped target",
			topDrop:    true,
			nestedDrop: true,
			in:         `{"id": 1, "items": [{"sku": "A", "x": 1}, {"sku": "B"}]}`,
			want:       `{"lines": [{"code": "A", "kind": "L"}, {"code": "B", "kind": "L"}]}`,
		},
		{
			name:       "top-level drop_unmapped, missing source: nothing written",
			topDrop:    true,
			nestedDrop: true,
			in:         `{"id": 1}`,
			want:       `{}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mapObj(t, tc.in, eachConfig(tc.topDrop, tc.nestedDrop))
			assertJSONEqual(t, tc.name, out, mustJSON(t, tc.want))
		})
	}
}

// Cases about the target array only (the plan does not say whether a
// differently named source is kept when the top level keeps unmapped fields).
func TestEach_TargetArray(t *testing.T) {
	cases := []struct {
		name       string
		nestedDrop bool
		in         string
		want       string
	}{
		{
			name:       "empty array gives an empty array",
			nestedDrop: true,
			in:         `{"items": []}`,
			want:       `[]`,
		},
		{
			name:       "non-object elements are kept as they are",
			nestedDrop: true,
			in:         `{"items": [{"sku": "A", "x": 1}, 5, "s", null, true, [1, 2]]}`,
			want:       `[{"code": "A", "kind": "L"}, 5, "s", null, true, [1, 2]]`,
		},
		{
			name:       "nested drop_unmapped true drops unnamed fields",
			nestedDrop: true,
			in:         `{"items": [{"itemid": 7, "sku": "A", "note": "n"}]}`,
			want:       `[{"code": "A", "kind": "L"}]`,
		},
		{
			name:       "nested drop_unmapped false keeps unnamed fields",
			nestedDrop: false,
			in:         `{"items": [{"itemid": 7, "sku": "A", "note": "n"}]}`,
			want:       `[{"itemid": 7, "code": "A", "kind": "L", "note": "n"}]`,
		},
		{
			name:       "element order is kept",
			nestedDrop: true,
			in:         `{"items": [{"sku": "C"}, {"sku": "A"}, {"sku": "B"}]}`,
			want:       `[{"code": "C", "kind": "L"}, {"code": "A", "kind": "L"}, {"code": "B", "kind": "L"}]`,
		},
	}
	for _, tc := range cases {
		for _, topDrop := range []bool{false, true} {
			name := tc.name
			if topDrop {
				name += " (top drop_unmapped)"
			}
			t.Run(name, func(t *testing.T) {
				out := mapObj(t, tc.in, eachConfig(topDrop, tc.nestedDrop))
				lines, ok := out["lines"]
				if !ok {
					t.Fatalf("target not written: %v", out)
				}
				if _, isArr := lines.([]interface{}); !isArr {
					t.Fatalf("target must be an array, got %T: %v", lines, lines)
				}
				assertJSONEqual(t, name, lines, mustJSON(t, tc.want))
			})
		}
	}
}
