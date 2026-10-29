package main

// Tests for #525, written from plans/issue-525-dotted-source.md: rename and
// each move a dotted source (the field at the path is removed, not a top-level
// key with dots in its name), the input object is never mutated, and remove
// keeps its exact-top-level-key behaviour.

import (
	"encoding/json"
	"testing"
)

// mustCfg decodes a converter node config from JSON, the shape the UI stores.
func mustCfg(t *testing.T, s string) *ConverterNodeConfig {
	t.Helper()
	var cfg ConverterNodeConfig
	if err := json.Unmarshal([]byte(s), &cfg); err != nil {
		t.Fatalf("bad test config: %v\n%s", err, s)
	}
	return &cfg
}

type dottedCase struct {
	name string
	in   string
	cfg  string
	want string
}

func runDottedCases(t *testing.T, cases []dottedCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapObj(t, tc.in, mustCfg(t, tc.cfg))
			assertJSONEqual(t, tc.name, got, mustJSON(t, tc.want))
		})
	}
}

// Criteria 1, 3, 4, 6, 9: rename.
func TestDottedSourceRename(t *testing.T) {
	runDottedCases(t, []dottedCase{
		{
			name: "dotted source moved, siblings kept",
			in:   `{"id":1,"order":{"items":[1,2],"id":"o1","note":"n"}}`,
			cfg:  `{"mappings":[{"type":"rename","source":"order.items","target":"lines"}]}`,
			want: `{"id":1,"order":{"id":"o1","note":"n"},"lines":[1,2]}`,
		},
		{
			name: "empty type means rename",
			in:   `{"order":{"items":[1],"id":"o1"}}`,
			cfg:  `{"mappings":[{"source":"order.items","target":"lines"}]}`,
			want: `{"order":{"id":"o1"},"lines":[1]}`,
		},
		{
			name: "dotted source into dotted target elsewhere",
			in:   `{"order":{"customer":"C1","id":"o1"}}`,
			cfg:  `{"mappings":[{"type":"rename","source":"order.customer","target":"header.customerNumber"}]}`,
			want: `{"order":{"id":"o1"},"header":{"customerNumber":"C1"}}`,
		},
		{
			name: "top-level source as before",
			in:   `{"name":"x","keep":true}`,
			cfg:  `{"mappings":[{"type":"rename","source":"name","target":"full_name"}]}`,
			want: `{"full_name":"x","keep":true}`,
		},
		{
			name: "top-level source into dotted target",
			in:   `{"name":"x","keep":true}`,
			cfg:  `{"mappings":[{"type":"rename","source":"name","target":"customer.name"}]}`,
			want: `{"customer":{"name":"x"},"keep":true}`,
		},
		{
			name: "drop_unmapped on dotted source",
			in:   `{"id":1,"order":{"items":[1,2],"id":"o1"}}`,
			cfg:  `{"drop_unmapped":true,"mappings":[{"type":"rename","source":"order.items","target":"lines"}]}`,
			want: `{"lines":[1,2]}`,
		},
		{
			name: "drop_unmapped on top-level source",
			in:   `{"name":"x","keep":true}`,
			cfg:  `{"drop_unmapped":true,"mappings":[{"type":"rename","source":"name","target":"full_name"}]}`,
			want: `{"full_name":"x"}`,
		},
		{
			name: "parent left empty stays as {}",
			in:   `{"id":1,"order":{"items":[1]}}`,
			cfg:  `{"mappings":[{"type":"rename","source":"order.items","target":"lines"}]}`,
			want: `{"id":1,"order":{},"lines":[1]}`,
		},
		{
			name: "deep path a.b.c, siblings at every level kept",
			in:   `{"a":{"x":1,"b":{"c":"v","y":2}},"z":3}`,
			cfg:  `{"mappings":[{"type":"rename","source":"a.b.c","target":"c"}]}`,
			want: `{"a":{"x":1,"b":{"y":2}},"z":3,"c":"v"}`,
		},
		{
			name: "deep path leaves empty innermost parent",
			in:   `{"a":{"x":1,"b":{"c":"v"}}}`,
			cfg:  `{"mappings":[{"type":"rename","source":"a.b.c","target":"c"}]}`,
			want: `{"a":{"x":1,"b":{}},"c":"v"}`,
		},
		{
			name: "dotted source equal to target keeps value",
			in:   `{"order":{"id":"o1","note":"n"}}`,
			cfg:  `{"mappings":[{"type":"rename","source":"order.id","target":"order.id"}]}`,
			want: `{"order":{"id":"o1","note":"n"}}`,
		},
		{
			name: "top-level source equal to target keeps value",
			in:   `{"id":"o1"}`,
			cfg:  `{"mappings":[{"type":"rename","source":"id","target":"id"}]}`,
			want: `{"id":"o1"}`,
		},
		{
			name: "target inside source parent",
			in:   `{"order":{"id":"o1"}}`,
			cfg:  `{"mappings":[{"type":"rename","source":"order.id","target":"order.ref"}]}`,
			want: `{"order":{"ref":"o1"}}`,
		},
		{
			name: "literal top-level dotted key kept, nested source moved",
			in:   `{"order.items":"literal","order":{"items":[1],"id":"o1"}}`,
			cfg:  `{"mappings":[{"type":"rename","source":"order.items","target":"lines"}]}`,
			want: `{"order.items":"literal","order":{"id":"o1"},"lines":[1]}`,
		},
		{
			name: "two renames out of the same parent",
			in:   `{"order":{"id":"o1","customer":"C1","note":"n"}}`,
			cfg: `{"mappings":[
				{"type":"rename","source":"order.id","target":"ref"},
				{"type":"rename","source":"order.customer","target":"customer"}]}`,
			want: `{"order":{"note":"n"},"ref":"o1","customer":"C1"}`,
		},
	})
}

// Criteria 2, 3, 4, 6, 8: each.
func TestDottedSourceEach(t *testing.T) {
	runDottedCases(t, []dottedCase{
		{
			name: "dotted source moved, siblings kept",
			in:   `{"id":1,"order":{"items":[{"sku":"A"},{"sku":"B"}],"id":"o1"}}`,
			cfg: `{"mappings":[{"type":"each","source":"order.items","target":"lines",
				"mappings":[{"type":"rename","source":"sku","target":"code"}]}]}`,
			want: `{"id":1,"order":{"id":"o1"},"lines":[{"code":"A"},{"code":"B"}]}`,
		},
		{
			name: "top-level source as before",
			in:   `{"items":[{"sku":"A"}],"keep":true}`,
			cfg: `{"mappings":[{"type":"each","source":"items","target":"lines",
				"mappings":[{"type":"rename","source":"sku","target":"code"}]}]}`,
			want: `{"lines":[{"code":"A"}],"keep":true}`,
		},
		{
			name: "drop_unmapped on dotted source",
			in:   `{"id":1,"order":{"items":[{"sku":"A","qty":2}],"id":"o1"}}`,
			cfg: `{"drop_unmapped":true,"mappings":[{"type":"each","source":"order.items","target":"lines",
				"mappings":[{"type":"rename","source":"sku","target":"code"}]}]}`,
			want: `{"lines":[{"code":"A","qty":2}]}`,
		},
		{
			name: "drop_unmapped on top-level source",
			in:   `{"items":[{"sku":"A"}],"keep":true}`,
			cfg: `{"drop_unmapped":true,"mappings":[{"type":"each","source":"items","target":"lines",
				"mappings":[{"type":"rename","source":"sku","target":"code"}]}]}`,
			want: `{"lines":[{"code":"A"}]}`,
		},
		{
			name: "parent left empty stays as {}",
			in:   `{"order":{"items":[{"sku":"A"}]}}`,
			cfg:  `{"mappings":[{"type":"each","source":"order.items","target":"lines","mappings":[]}]}`,
			want: `{"order":{},"lines":[{"sku":"A"}]}`,
		},
		{
			name: "deep dotted source a.b.c",
			in:   `{"a":{"x":1,"b":{"c":[{"v":1}],"y":2}}}`,
			cfg:  `{"mappings":[{"type":"each","source":"a.b.c","target":"out","mappings":[]}]}`,
			want: `{"a":{"x":1,"b":{"y":2}},"out":[{"v":1}]}`,
		},
		{
			name: "item rename of a dotted field inside each element",
			in: `{"items":[
				{"product":{"sku":"A","name":"x"},"qty":1},
				{"product":{"sku":"B","name":"y"},"qty":2}]}`,
			cfg: `{"mappings":[{"type":"each","source":"items","target":"items",
				"mappings":[{"type":"rename","source":"product.sku","target":"sku"}]}]}`,
			want: `{"items":[
				{"product":{"name":"x"},"qty":1,"sku":"A"},
				{"product":{"name":"y"},"qty":2,"sku":"B"}]}`,
		},
		{
			name: "dotted each source with dotted item rename",
			in:   `{"order":{"id":"o1","items":[{"product":{"sku":"A","name":"x"},"qty":1}]}}`,
			cfg: `{"mappings":[{"type":"each","source":"order.items","target":"lines",
				"mappings":[{"type":"rename","source":"product.sku","target":"itemNumber"}]}]}`,
			want: `{"order":{"id":"o1"},"lines":[{"product":{"name":"x"},"qty":1,"itemNumber":"A"}]}`,
		},
		{
			name: "nested item each over a dotted source",
			in:   `{"orders":[{"meta":{"lines":[{"sku":"A"}],"n":1}}]}`,
			cfg: `{"mappings":[{"type":"each","source":"orders","target":"orders","mappings":[
				{"type":"each","source":"meta.lines","target":"lines",
				 "mappings":[{"type":"rename","source":"sku","target":"code"}]}]}]}`,
			want: `{"orders":[{"meta":{"n":1},"lines":[{"code":"A"}]}]}`,
		},
		{
			name: "dotted source equal to target keeps value",
			in:   `{"order":{"items":[{"sku":"A"}],"id":"o1"}}`,
			cfg: `{"mappings":[{"type":"each","source":"order.items","target":"order.items",
				"mappings":[{"type":"rename","source":"sku","target":"code"}]}]}`,
			want: `{"order":{"items":[{"code":"A"}],"id":"o1"}}`,
		},
		{
			name: "target inside source parent",
			in:   `{"order":{"items":[{"sku":"A"}]}}`,
			cfg:  `{"mappings":[{"type":"each","source":"order.items","target":"order.lines","mappings":[]}]}`,
			want: `{"order":{"lines":[{"sku":"A"}]}}`,
		},
		{
			name: "literal top-level dotted key kept, nested source moved",
			in:   `{"order.items":"literal","order":{"items":[{"sku":"A"}]}}`,
			cfg:  `{"mappings":[{"type":"each","source":"order.items","target":"lines","mappings":[]}]}`,
			want: `{"order.items":"literal","order":{},"lines":[{"sku":"A"}]}`,
		},
	})
}

// A source that cannot be read leaves the object unchanged and does not panic.
func TestDottedSourceUnreadable(t *testing.T) {
	const in = `{"order":{"items":[1,2],"id":"o1","name":"s"},"flag":"x"}`
	sources := []string{
		"customer.name",       // parent missing
		"order.missing",       // leaf missing
		"order.items.0",       // intermediate is an array
		"order.name.first",    // intermediate is a string
		"flag.items",          // top-level intermediate is a string
		"order.missing.deep",  // intermediate missing
		"order..items",        // empty segment
		"",                    // empty path
		"order.id.x.y.z.deep", // walks off a leaf
	}
	for _, typ := range []string{"rename", "each"} {
		for _, src := range sources {
			t.Run(typ+" "+src, func(t *testing.T) {
				cfg := &ConverterNodeConfig{Mappings: []FieldMapping{{Type: typ, Source: src, Target: "out"}}}
				got := mapObj(t, in, cfg)
				assertJSONEqual(t, typ+" "+src, got, mustJSON(t, in))
			})
		}
	}
	// each whose dotted source is not an array is left alone too.
	t.Run("each non-array dotted source", func(t *testing.T) {
		cfg := mustCfg(t, `{"mappings":[{"type":"each","source":"order.id","target":"out","mappings":[]}]}`)
		got := mapObj(t, in, cfg)
		assertJSONEqual(t, "each order.id", got, mustJSON(t, in))
	})
}

// Criterion 5: remove still deletes only an exact top-level key.
func TestDottedSourceRemoveUnchanged(t *testing.T) {
	runDottedCases(t, []dottedCase{
		{
			name: "remove a.b leaves nested a.b",
			in:   `{"a":{"b":1,"c":2}}`,
			cfg:  `{"mappings":[{"type":"remove","source":"a.b"}]}`,
			want: `{"a":{"b":1,"c":2}}`,
		},
		{
			name: "remove a.b deletes literal top-level key a.b only",
			in:   `{"a.b":"lit","a":{"b":1}}`,
			cfg:  `{"mappings":[{"type":"remove","source":"a.b"}]}`,
			want: `{"a":{"b":1}}`,
		},
		{
			name: "remove top-level key",
			in:   `{"a":{"b":1},"c":2}`,
			cfg:  `{"mappings":[{"type":"remove","source":"a"}]}`,
			want: `{"c":2}`,
		},
	})
}

// Criterion 7: the input is not mutated, and a later mapping reading the same
// dotted source still finds it.
func TestDottedSourceInputNotMutated(t *testing.T) {
	const in = `{
		"order":{"id":"o1","customer":{"no":"C1","name":"n"},
			"items":[{"product":{"sku":"A","name":"x"},"qty":1}]},
		"keep":true}`
	cfg := mustCfg(t, `{"mappings":[
		{"type":"rename","source":"order.customer.no","target":"customerNumber"},
		{"type":"each","source":"order.items","target":"lines",
		 "mappings":[{"type":"rename","source":"product.sku","target":"itemNumber"}]},
		{"type":"rename","source":"order.id","target":"order.ref"},
		{"type":"copy","source":"order.id","target":"externalId"},
		{"type":"copy","source":"order.customer.no","target":"customerCopy"},
		{"type":"copy","source":"order.items","target":"rawItems"}]}`)

	obj, ok := mustJSON(t, in).(map[string]interface{})
	if !ok {
		t.Fatal("input is not an object")
	}
	got, _ := applyMappings(obj, cfg)

	assertJSONEqual(t, "input after applyMappings", obj, mustJSON(t, in))
	assertJSONEqual(t, "output", got, mustJSON(t, `{
		"order":{"ref":"o1","customer":{"name":"n"}},
		"keep":true,
		"customerNumber":"C1",
		"lines":[{"product":{"name":"x"},"qty":1,"itemNumber":"A"}],
		"externalId":"o1",
		"customerCopy":"C1",
		"rawItems":[{"product":{"sku":"A","name":"x"},"qty":1}]}`))

	// A second run over the same input gives the same output.
	again, _ := applyMappings(obj, cfg)
	assertJSONEqual(t, "second run", again, got)
}
