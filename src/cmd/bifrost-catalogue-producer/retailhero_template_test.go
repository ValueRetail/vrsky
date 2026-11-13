package main

// Producer side of the Retailhero -> Bifrost template (#816, #855). The
// payloads in src/test/fixtures/retailhero/*.items.golden.json are what the
// template's converter produces from Retailhero's bifrost-catalogue.batch
// events (checked in data-converter/retailhero_template_test.go): the items
// and fact.sequence as "sequence". Here they go through the producer in the
// template's mode (delta) to the stubbed catalogue-api, and what the stub
// holds must be those items, byte for byte per field.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	var seqs []uint64
	for _, b := range stub.beginBodies() {
		if b.Kind != "delta" {
			t.Errorf("import kind = %q, want delta (full would replace the catalogue with one batch)", b.Kind)
		}
		if b.Sequence != nil {
			seqs = append(seqs, *b.Sequence)
		}
	}
	// fact.sequence of the two events: 1 (the full package's first batch) and
	// 4 (the delta after its three batches).
	if fmt.Sprint(seqs) != "[1 4]" {
		t.Errorf("begin sequences = %v, want [1 4], Retailhero's fact.sequence", seqs)
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

// rhBatch is the converter's output for one Retailhero batch: its items and
// fact.sequence as "sequence".
func rhBatch(seq int, items ...string) []byte {
	return []byte(fmt.Sprintf(`{"sequence":%d,"items":[%s]}`, seq, strings.Join(items, ",")))
}

func rhItem(article, price string) string {
	return fmt.Sprintf(`{"article_no":%q,"name":"Vare","price_incl_vat":%q,"vat_code":"F","unit":"pcs","alcohol":false,"effective_from":"2026-12-01T00:00:00Z"}`, article, price)
}

// Retailhero cuts a big publication into several events, each with the next
// fact.sequence and all sharing one recordedAt. Every one is its own delta
// import: all of them commit, and none is taken for the whole list, so the
// articles of the earlier batches and those Retailhero never sent stay.
func TestRetailheroTemplate_SplitPublicationCommitsEveryBatch(t *testing.T) {
	stub := newBifrostStub(t)
	stub.seed(`{"article_no":"OLD-1","name":"Gammel","price_incl_vat":"5.00","vat_code":"F"}`)
	cfg := nodeJSON(stub.URL(), `"mode":"delta"`)
	batches := [][]byte{
		rhBatch(11, rhItem("A-100", "10.00"), rhItem("B-200", "20.00")),
		rhBatch(12, rhItem("C-300", "30.00")),
		rhBatch(13, rhItem("B-200", "21.00"), rhItem("D-400", "40.00")),
	}
	for i, b := range batches {
		if err := deliver(t, testProducer(), &envelope.Envelope{ID: fmt.Sprintf("bifrost-Pub-9-%d", i+1), Payload: b}, `[]`, cfg); err != nil {
			t.Fatalf("batch %d of 3: %v", i+1, err)
		}
	}
	if got := stub.kinds(); strings.Count(got, "commit") != 3 {
		t.Fatalf("calls = %q, want three committed imports, one per batch", got)
	}
	for _, b := range stub.beginBodies() {
		if b.Kind != "delta" {
			t.Errorf("batch sent as a %q import; a batch is never the whole catalogue", b.Kind)
		}
	}
	live := stub.liveSnapshot()
	want := map[string]string{"OLD-1": "5.00", "A-100": "10.00", "B-200": "21.00", "C-300": "30.00", "D-400": "40.00"}
	if len(live) != len(want) {
		t.Errorf("the tills hold %d articles, want %d: an earlier batch or an untouched article went missing: %v", len(live), len(want), live)
	}
	for art, price := range want {
		var it struct {
			Price string `json:"price_incl_vat"`
		}
		if err := json.Unmarshal([]byte(live[art]), &it); err != nil || it.Price != price {
			t.Errorf("the tills sell %s at %q, want %s", art, it.Price, price)
		}
	}
}

// A batch retried after the next one committed is dropped (Bifrost's 409 on
// fact.sequence), not retried or dead-lettered, and the newer price stays.
func TestRetailheroTemplate_OlderBatchRetriedLateIsDropped(t *testing.T) {
	stub := newBifrostStub(t)
	cfg := nodeJSON(stub.URL(), `"mode":"delta"`)
	if err := deliver(t, testProducer(), &envelope.Envelope{ID: "bifrost-Pub-1-2", Payload: rhBatch(2, rhItem("4711", "12.00"))}, `[]`, cfg); err != nil {
		t.Fatalf("batch 2: %v", err)
	}
	if err := deliver(t, testProducer(), &envelope.Envelope{ID: "bifrost-Pub-1-1", Payload: rhBatch(1, rhItem("4711", "11.00"))}, `[]`, cfg); err != nil {
		t.Fatalf("batch 1 retried after batch 2 returned %v, want it dropped (nil)", err)
	}
	if got := livePrice(t, stub); got != "12.00" {
		t.Errorf("the tills sell 4711 at %s, want 12.00 from the newer batch", got)
	}
	b := stub.beginBodies()
	if len(b) != 2 || b[0].Sequence == nil || *b[0].Sequence != 2 || b[1].Sequence == nil || *b[1].Sequence != 1 {
		t.Errorf("begin sequences = %+v, want 2 then 1, as fact.sequence", b)
	}
}
