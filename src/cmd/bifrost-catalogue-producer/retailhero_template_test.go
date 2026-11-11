package main

// Producer side of the Retailhero -> Bifrost template (#816). The item lists
// in src/test/fixtures/retailhero/*.items.golden.json are what the template's
// converter produces from Retailhero's Bifrost catalogue fixtures (checked in
// data-converter/retailhero_template_test.go). Here they go through the
// producer in the template's mode (delta) to the stubbed catalogue-api, and
// what the stub holds must be those items, byte for byte per field.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

func retailheroItems(t *testing.T, name string) (payload []byte, items []map[string]json.RawMessage) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "retailhero", name))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil || len(doc.Items) == 0 {
		t.Fatalf("golden %s: %v (%d items)", name, err, len(doc.Items))
	}
	return payload, doc.Items
}

func TestRetailheroTemplate_BatchesReachCatalogueApi(t *testing.T) {
	stub := newBifrostStub(t)
	// Articles the pilot store already has and Retailhero does not touch: a
	// delta must keep them.
	stub.seedN(3)

	var want []map[string]json.RawMessage
	for i, name := range []string{"bifrost-batch-full-1.items.golden.json", "bifrost-batch-delta.items.golden.json"} {
		payload, items := retailheroItems(t, name)
		env := &envelope.Envelope{ID: []string{"rh-1", "rh-2"}[i], Payload: payload}
		if err := deliver(t, testProducer(), env, `[]`, nodeJSON(stub.URL(), `"mode":"delta"`)); err != nil {
			t.Fatalf("Deliver %s: %v", name, err)
		}
		want = append(want, items...)
	}

	for _, b := range stub.beginBodies() {
		if b.Kind != "delta" {
			t.Errorf("import kind = %q, want delta (full would replace the catalogue with one batch)", b.Kind)
		}
	}

	live := stub.liveSnapshot()
	// 3 seeded + A-100, B-200, C-300, D-400 from batch 1; B-200 again in batch 2.
	if len(live) != 3+4 {
		t.Fatalf("catalogue holds %d articles, want 7: %v", len(live), live)
	}
	// B-200 was upserted by the second batch: the later price wins.
	var b200 map[string]json.RawMessage
	if err := json.Unmarshal([]byte(live["B-200"]), &b200); err != nil {
		t.Fatal(err)
	}
	if got := string(b200["price_incl_vat"]); got != `"25.00"` {
		t.Errorf("B-200 price_incl_vat = %s, want \"25.00\" from the later batch", got)
	}
	// Every sent field reached Bifrost as written, prices as strings.
	for _, it := range want {
		var art string
		_ = json.Unmarshal(it["article_no"], &art)
		var got map[string]json.RawMessage
		if err := json.Unmarshal([]byte(live[art]), &got); err != nil {
			t.Fatalf("article %s not live: %v", art, err)
		}
		if art == "B-200" && string(it["price_incl_vat"]) == `"23.00"` {
			continue // superseded by batch 2
		}
		for k, v := range it {
			if string(got[k]) != string(v) {
				t.Errorf("article %s field %s = %s, want %s", art, k, got[k], v)
			}
		}
	}
}

// The template passes each batch's recordedAt through as "sequence" (#858). A
// retried older batch that arrives after a newer one is dropped, so the tills
// keep the newer price.
func TestRetailheroTemplate_OlderBatchRetriedLateIsDropped(t *testing.T) {
	stub := newBifrostStub(t)
	cfg := nodeJSON(stub.URL(), `"mode":"delta"`)
	batch := func(recordedAt, price string) []byte {
		return []byte(`{"sequence":"` + recordedAt + `","items":[{"article_no":"4711","name":"Melk","price_incl_vat":"` + price + `","vat_code":"F","unit":"pcs","alcohol":false,"effective_from":"2026-12-01T00:00:00Z"}]}`)
	}
	if err := deliver(t, testProducer(), &envelope.Envelope{ID: "rh-new", Payload: batch("2026-11-11T12:00:01Z", "12.00")}, `[]`, cfg); err != nil {
		t.Fatalf("newer batch: %v", err)
	}
	if err := deliver(t, testProducer(), &envelope.Envelope{ID: "rh-old", Payload: batch("2026-11-11T12:00:00Z", "11.00")}, `[]`, cfg); err != nil {
		t.Fatalf("older batch returned %v, want it dropped (nil)", err)
	}
	if got := livePrice(t, stub); got != "12.00" {
		t.Errorf("the tills sell 4711 at %s, want 12.00 from the newer batch", got)
	}
	b := stub.beginBodies()
	if len(b) != 2 || b[0].Sequence == nil || *b[0].Sequence != 1794398401000 || b[1].Sequence == nil || *b[1].Sequence != 1794398400000 {
		t.Errorf("begin sequences = %+v, want the recordedAt times in milliseconds", b)
	}
}
