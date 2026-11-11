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

func TestBifrostSalesFilter_OnlyATillSaleOrReturnPasses(t *testing.T) {
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

	webReturn := fixture("basket-return-recorded.v1.json")
	webReturn["source"] = map[string]interface{}{"kind": "ecommerce", "instance_id": "web-no", "location_id": "web"}

	webSale := fixture("basket-sale-recorded.v1.json")
	webSale["source"] = map[string]interface{}{"kind": "ecommerce", "instance_id": "web-no", "location_id": "web"}

	noID := fixture("basket-sale-recorded.v1.json")
	delete(noID, "event_id")
	emptyID := fixture("basket-sale-recorded.v1.json")
	emptyID["event_id"] = ""

	cases := []struct {
		name string
		obj  map[string]interface{}
		want bool
	}{
		{"sale-recorded from a till", fixture("basket-sale-recorded.v1.json"), true},
		{"return-recorded from a till", fixture("basket-return-recorded.v1.json"), true},
		{"return-recorded from the web shop", webReturn, false},
		{"another -recorded type", map[string]interface{}{"event_id": "e1", "event_type": "basket.payment-recorded", "source": map[string]interface{}{"kind": "pos"}}, false},
		{"a type that only contains a listed one", map[string]interface{}{"event_id": "e1", "event_type": "basket.sale-recorded.v2", "source": map[string]interface{}{"kind": "pos"}}, false},
		{"sale-recorded from the web shop", webSale, false},
		{"sale-recorded without an event_id", noID, false},
		{"sale-recorded with an empty event_id", emptyID, false},
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

// "in" passes a field that equals one entry of a comma-separated list, exactly.
func TestEvaluateRule_In(t *testing.T) {
	rule := FilterRule{Field: "t", Operator: "in", Value: "a, b ,c"}
	cases := []struct {
		obj  map[string]interface{}
		want bool
	}{
		{map[string]interface{}{"t": "a"}, true},
		{map[string]interface{}{"t": "b"}, true},
		{map[string]interface{}{"t": "c"}, true},
		{map[string]interface{}{"t": "ab"}, false},
		{map[string]interface{}{"t": ""}, false},
		{map[string]interface{}{"t": nil}, false},
		{map[string]interface{}{}, false},
	}
	for _, c := range cases {
		if got := evaluateRule(c.obj, rule); got != c.want {
			t.Errorf("%v in %q = %v, want %v", c.obj, rule.Value, got, c.want)
		}
	}
	if evaluateRule(map[string]interface{}{"t": ""}, FilterRule{Field: "t", Operator: "in", Value: "a,,b"}) {
		t.Error("an empty entry in the list let an empty value through")
	}
}
