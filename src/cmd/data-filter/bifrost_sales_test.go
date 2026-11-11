package main

// The filter of the Bifrost sales → Retailhero template (#817), read from
// ui/src/onboarding/testdata/bifrost-sales-filter.json and run on the Basket
// event fixtures. Retailhero refuses a whole batch on an event type its
// profile does not map and on a sale-recorded from a channel that is not a
// POS, so only a till sale may pass.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readJSONFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return raw
}

func TestBifrostSalesFilter_OnlyATillSalePasses(t *testing.T) {
	var cfg FilterNodeConfig
	if err := json.Unmarshal(readJSONFile(t, "..", "..", "..", "ui", "src", "onboarding", "testdata", "bifrost-sales-filter.json"), &cfg); err != nil {
		t.Fatalf("filter config: %v", err)
	}
	fixture := func(name string) map[string]interface{} {
		var m map[string]interface{}
		if err := json.Unmarshal(readJSONFile(t, "..", "..", "test", "fixtures", "bifrost", name), &m); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return m
	}

	webSale := fixture("basket-sale-recorded.v1.json")
	webSale["source"] = map[string]interface{}{"kind": "ecommerce", "instance_id": "web-no", "location_id": "web"}

	cases := []struct {
		name string
		obj  map[string]interface{}
		want bool
	}{
		{"sale-recorded from a till", fixture("basket-sale-recorded.v1.json"), true},
		{"sale-recorded from the web shop", webSale, false},
		{"completed", fixture("basket-completed.v1.json"), false},
		{"cancelled", fixture("basket-cancelled.v1.json"), false},
		{"created", map[string]interface{}{"event_type": "basket.created", "source": map[string]interface{}{"kind": "pos"}}, false},
	}
	for _, c := range cases {
		if got := evaluateRules(c.obj, cfg.Rules, cfg.Logic); got != c.want {
			t.Errorf("%s: passes = %v, want %v", c.name, got, c.want)
		}
	}
}
