package main

// Test for the Retailhero -> Bifrost onboarding template (#816): the converter
// config the wizard deploys (ui/src/onboarding/testdata/retailhero-to-bifrost.json)
// runs on embedded JetStream against Retailhero's Bifrost catalogue fixtures
// (src/test/fixtures/retailhero, copied from ValueRetail/Retailhero
// tests/fixtures/pos-publication/bifrost-catalogue/v1/expected-*.json) and must
// produce the item lists in the *.items.golden.json files. The producer side of
// the same files is checked in bifrost-catalogue-producer/retailhero_template_test.go.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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
				t.Fatal(err)
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

// Fields the profile adds beside importItem (sourceItemId, the batch header)
// and a price_ex_vat nobody sent must not reach Bifrost; absent optional fields
// (gtin, min_age) stay absent rather than becoming null.
func TestTemplate_RetailheroToBifrost_OnlyImportItemFieldsPass(t *testing.T) {
	cfg := loadTemplateConfig(t, "retailhero-to-bifrost.json")
	in := `{"kind":"delta","effectiveFrom":"2026-12-01T00:00:00Z","rows":[
	  {"sourceItemId":"S-1","catalogueItem":{"price_ex_vat":"19.92"},
	   "importItem":{"article_no":"D-400","name":"Vare D","price_incl_vat":"15.00","vat_code":"N","unit":"pcs","alcohol":false,"effective_from":"2026-12-01T00:00:00Z"}}]}`
	got := throughJetStream(t, cfg, inputEnvelope([]byte(in), 1), 1)
	want := mustJSON(t, `{"items":[{"article_no":"D-400","name":"Vare D","price_incl_vat":"15.00","vat_code":"N","unit":"pcs","alcohol":false,"effective_from":"2026-12-01T00:00:00Z"}]}`)
	assertJSONEqual(t, "item", decodeJSON(t, got[0].Payload), want)
	if bytes.Contains(got[0].Payload, []byte("null")) {
		t.Errorf("absent fields became null: %s", got[0].Payload)
	}
}
