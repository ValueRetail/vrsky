package main

// Test for the Retailhero -> Bifrost onboarding template (#816, #855): the
// converter config the wizard deploys
// (ui/src/onboarding/testdata/retailhero-to-bifrost.json) runs on embedded
// JetStream against Retailhero's bifrost-catalogue.batch events
// (src/test/fixtures/retailhero) and must produce the item lists in the
// *.items.golden.json files. bifrost-batch-full-1.json is the "event" of
// ValueRetail/Retailhero tests/fixtures/pos-publication/bifrost-catalogue/v1/event-batch.json
// (Retailhero PR #2746), the body the partner durable delivers;
// bifrost-batch-delta.json is the delta batch of expected-delta.json in the
// same envelope, as the dispatch numbers it after the full package's three
// batches. The producer side of the same files is checked in
// bifrost-catalogue-producer/retailhero_template_test.go.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func retailheroFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "retailhero", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

func TestTemplate_RetailheroToBifrost_BatchBecomesItemList(t *testing.T) {
	cfg := loadTemplateConfig(t, "retailhero-to-bifrost.json")
	for _, name := range []string{"bifrost-batch-full-1", "bifrost-batch-delta"} {
		t.Run(name, func(t *testing.T) {
			in := retailheroFixture(t, name+".json")
			got := throughJetStream(t, cfg, inputEnvelope(in, 1), 1)
			if len(got) != 1 {
				t.Fatalf("want one message per batch, got %d", len(got))
			}
			want := decodeJSON(t, retailheroFixture(t, name+".items.golden.json"))
			assertJSONEqual(t, name, decodeJSON(t, got[0].Payload), want)

			// Money stays text: every price is a JSON string with its two
			// decimals, never a number that went through a float.
			var out struct {
				Items []map[string]json.RawMessage `json:"items"`
			}
			if err := json.Unmarshal(got[0].Payload, &out); err != nil {
				t.Fatalf("the converter's output is not a JSON object: %v", err)
			}
			price := regexp.MustCompile(`^"\d+\.\d{2}"$`)
			for _, it := range out.Items {
				if !price.Match(it["price_incl_vat"]) {
					t.Errorf("price_incl_vat = %s, want a two-decimal string", it["price_incl_vat"])
				}
			}
		})
	}
}

// Fields the event carries beside fact.rows[].importItem (sourceItemId, the
// envelope, the rest of fact) and a price_ex_vat nobody sent must not reach
// Bifrost; absent optional fields (gtin, category, min_age) stay absent rather
// than becoming null.
func TestTemplate_RetailheroToBifrost_OnlyImportItemFieldsPass(t *testing.T) {
	cfg := loadTemplateConfig(t, "retailhero-to-bifrost.json")
	in := `{"contractVersion":"pos-publication-events.v1","eventType":"bifrost-catalogue.batch","recordedAt":"2026-11-11T12:00:00Z",
	  "fact":{"kind":"delta","effectiveFrom":"2026-12-01T00:00:00Z","sequence":7,"storeId":"Store-1","rows":[
	  {"sourceItemId":"S-1","catalogueItem":{"price_ex_vat":"19.92"},
	   "importItem":{"article_no":"D-400","name":"Vare D","price_incl_vat":"15.00","vat_code":"N","unit":"pcs","alcohol":false,"effective_from":"2026-12-01T00:00:00Z"}}]}}`
	got := throughJetStream(t, cfg, inputEnvelope([]byte(in), 1), 1)
	want := mustJSON(t, `{"sequence":7,"items":[{"article_no":"D-400","name":"Vare D","price_incl_vat":"15.00","vat_code":"N","unit":"pcs","alcohol":false,"effective_from":"2026-12-01T00:00:00Z"}]}`)
	assertJSONEqual(t, "item", decodeJSON(t, got[0].Payload), want)
	if bytes.Contains(got[0].Payload, []byte("null")) {
		t.Errorf("absent fields became null: %s", got[0].Payload)
	}
}

// fact.sequence, Retailhero's per-store batch counter, goes to the producer as
// "sequence", a whole number written as one (not 4.0 or 4e+00, which the
// producer would refuse). recordedAt is not used: every batch of one
// publication shares it, so Bifrost would refuse all but the first with 409.
func TestTemplate_RetailheroToBifrost_FactSequenceBecomesSequence(t *testing.T) {
	cfg := loadTemplateConfig(t, "retailhero-to-bifrost.json")
	for _, seq := range []string{"1", "4", "1000000", "9007199254740991"} {
		t.Run(seq, func(t *testing.T) {
			in := `{"recordedAt":"2026-11-11T12:00:00.123456Z","stream":{"sequence":"999"},"fact":{"kind":"delta","sequence":` + seq + `,"rows":[
			  {"sourceItemId":"S-1","importItem":{"article_no":"D-400","name":"Vare D","price_incl_vat":"15.00","vat_code":"N","unit":"pcs","alcohol":false,"effective_from":"2026-12-01T00:00:00Z"}}]}}`
			got := throughJetStream(t, cfg, inputEnvelope([]byte(in), 1), 1)
			var out map[string]json.RawMessage
			if err := json.Unmarshal(got[0].Payload, &out); err != nil {
				t.Fatalf("the converter's output is not a JSON object: %v", err)
			}
			if string(out["sequence"]) != seq {
				t.Errorf("sequence = %s, want %s from fact.sequence (not recordedAt or stream.sequence)", out["sequence"], seq)
			}
		})
	}
}

// A batch without fact.sequence is not converted: sent unnumbered, Bifrost
// would apply it whatever it already holds, so a late retry could put an older
// price back on the tills. The message fails, is retried and then
// dead-lettered with the field named.
func TestTemplate_RetailheroToBifrost_BatchWithoutSequenceFails(t *testing.T) {
	cfg := loadTemplateConfig(t, "retailhero-to-bifrost.json")
	for name, in := range map[string]string{
		"no sequence":   `{"recordedAt":"2026-11-11T12:00:00Z","fact":{"kind":"delta","rows":[{"importItem":{"article_no":"D-400"}}]}}`,
		"null sequence": `{"recordedAt":"2026-11-11T12:00:00Z","fact":{"kind":"delta","sequence":null,"rows":[{"importItem":{"article_no":"D-400"}}]}}`,
		// The shape the template assumed before Retailhero published the event.
		"bare batch without fact": `{"kind":"delta","recordedAt":"2026-11-11T12:00:00Z","rows":[{"importItem":{"article_no":"D-400"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := throughHandler(t, cfg, inputEnvelope([]byte(in), 1))
			if err == nil {
				t.Fatal("the batch was acked; without fact.sequence it must fail so it is retried and dead-lettered")
			}
			if !strings.Contains(err.Error(), `"fact.sequence"`) || !strings.Contains(err.Error(), "required field missing") {
				t.Errorf("error %q does not name fact.sequence as a missing required field", err)
			}
			if len(out) != 0 {
				t.Errorf("an unnumbered batch was published: %s", out[0].Payload)
			}
		})
	}
}
