package records

import (
	"reflect"
	"strings"
	"testing"
)

func mustRead(t *testing.T, format, payload string, opts Options) []Record {
	t.Helper()
	r, err := New(format, strings.NewReader(payload), opts)
	if err != nil {
		t.Fatalf("New(%s): %v", format, err)
	}
	recs, err := ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll(%s): %v", format, err)
	}
	return recs
}

// --- CSV / TSV ---

func TestCSV_HeaderAndRows(t *testing.T) {
	recs := mustRead(t, FormatCSV, "name,qty\nwidget,3\ngadget,7\n", Options{})
	want := []Record{
		{"name": "widget", "qty": "3"},
		{"name": "gadget", "qty": "7"},
	}
	if !reflect.DeepEqual(recs, want) {
		t.Errorf("got %v, want %v", recs, want)
	}
}

// Values stay strings — the accepted ADR decision. A leading-zero SKU must not
// become the number 123.
func TestCSV_ValuesStayStrings(t *testing.T) {
	recs := mustRead(t, FormatCSV, "sku,price\n0123,4.50\n", Options{})
	if recs[0]["sku"] != "0123" {
		t.Errorf("sku = %#v, want the string \"0123\"", recs[0]["sku"])
	}
	if recs[0]["price"] != "4.50" {
		t.Errorf("price = %#v, want the string \"4.50\"", recs[0]["price"])
	}
}

func TestCSV_QuotedFieldsWithDelimitersAndNewlines(t *testing.T) {
	recs := mustRead(t, FormatCSV, "name,note\n\"Doe, John\",\"line1\nline2\"\n", Options{})
	if recs[0]["name"] != "Doe, John" {
		t.Errorf("quoted delimiter mishandled: %#v", recs[0]["name"])
	}
	if recs[0]["note"] != "line1\nline2" {
		t.Errorf("embedded newline mishandled: %#v", recs[0]["note"])
	}
}

func TestCSV_RaggedRowsAndWideRows(t *testing.T) {
	recs := mustRead(t, FormatCSV, "a,b,c\n1,2\n1,2,3,4\n", Options{})
	if recs[0]["c"] != "" {
		t.Errorf("short row should pad with empty, got %#v", recs[0]["c"])
	}
	if recs[1]["column_4"] != "4" {
		t.Errorf("wide row should keep the extra cell, got %v", recs[1])
	}
}

func TestCSV_BlankAndDuplicateHeaders(t *testing.T) {
	recs := mustRead(t, FormatCSV, "id,,id\n1,2,3\n", Options{})
	if recs[0]["id"] != "1" || recs[0]["column_2"] != "2" || recs[0]["id_2"] != "3" {
		t.Errorf("header normalisation wrong: %v", recs[0])
	}
}

func TestCSV_SniffsSemicolonAndTab(t *testing.T) {
	semi := mustRead(t, FormatCSV, "a;b\n1;2\n", Options{})
	if semi[0]["a"] != "1" || semi[0]["b"] != "2" {
		t.Errorf("semicolon sniff failed: %v", semi[0])
	}
	tsv := mustRead(t, FormatTSV, "a\tb\n1\t2\n", Options{})
	if tsv[0]["a"] != "1" || tsv[0]["b"] != "2" {
		t.Errorf("tsv failed: %v", tsv[0])
	}
}

func TestCSV_ExplicitDelimiterBeatsSniff(t *testing.T) {
	// Commas inside the values would win a sniff; the explicit pipe must hold.
	recs := mustRead(t, FormatCSV, "a|b\n1,5|2,5\n", Options{Delimiter: '|'})
	if recs[0]["a"] != "1,5" || recs[0]["b"] != "2,5" {
		t.Errorf("explicit delimiter ignored: %v", recs[0])
	}
}

func TestCSV_NoHeaderSynthesisesColumns(t *testing.T) {
	recs := mustRead(t, FormatCSV, "1,2\n3,4\n", Options{NoHeader: true, Delimiter: ','})
	if len(recs) != 2 || recs[0]["column_1"] != "1" || recs[1]["column_2"] != "4" {
		t.Errorf("no-header mode wrong: %v", recs)
	}
}

func TestCSV_EmptyInput(t *testing.T) {
	if recs := mustRead(t, FormatCSV, "", Options{}); len(recs) != 0 {
		t.Errorf("empty input should yield no records, got %v", recs)
	}
}

// --- JSON / NDJSON ---

func TestJSON_ArrayObjectAndNDJSON(t *testing.T) {
	arr := mustRead(t, FormatJSON, `[{"a":1},{"a":2}]`, Options{})
	if len(arr) != 2 || arr[1]["a"] != float64(2) {
		t.Errorf("array: %v", arr)
	}
	single := mustRead(t, FormatJSON, `{"a":1,"b":{"c":2}}`, Options{})
	if len(single) != 1 || single[0]["a"] != float64(1) {
		t.Errorf("single object: %v", single)
	}
	nd := mustRead(t, FormatNDJSON, "{\"a\":1}\n{\"a\":2}\n", Options{})
	if len(nd) != 2 || nd[1]["a"] != float64(2) {
		t.Errorf("ndjson: %v", nd)
	}
}

// Parity with the buffered transforms: non-object array entries are skipped,
// not fatal.
func TestJSON_SkipsNonObjectEntries(t *testing.T) {
	recs := mustRead(t, FormatJSON, `[{"a":1},42,"x",{"a":2}]`, Options{})
	if len(recs) != 2 {
		t.Errorf("expected the 2 objects, got %v", recs)
	}
}

// --- XML ---

func TestXML_RecordPathRequired(t *testing.T) {
	_, err := New(FormatXML, strings.NewReader("<a/>"), Options{})
	if err == nil {
		t.Fatal("xml without a record path must be a config error, not a guess")
	}
	if !strings.Contains(err.Error(), "input_xml_record_path") {
		t.Errorf("error should name the config field, got: %v", err)
	}
}

func TestXML_AttributesRepeatsAndNesting(t *testing.T) {
	doc := `<Orders>
	  <Order id="7" ns:x="1"><Line>a</Line><Line>b</Line><Ship><City>Oslo</City></Ship></Order>
	  <Order id="8"><Line>c</Line></Order>
	</Orders>`
	recs := mustRead(t, FormatXML, doc, Options{RecordPath: "Orders.Order"})
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d: %v", len(recs), recs)
	}
	if recs[0]["@id"] != "7" {
		t.Errorf("attribute mapping: %v", recs[0])
	}
	lines, ok := recs[0]["Line"].([]interface{})
	if !ok || len(lines) != 2 || lines[0] != "a" {
		t.Errorf("repeated elements should become a slice: %#v", recs[0]["Line"])
	}
	ship, ok := recs[0]["Ship"].(Record)
	if !ok || ship["City"] != "Oslo" {
		t.Errorf("nested element: %#v", recs[0]["Ship"])
	}
	// A single occurrence stays scalar, not a one-element slice.
	if recs[1]["Line"] != "c" {
		t.Errorf("single child should stay scalar: %#v", recs[1]["Line"])
	}
}

func TestXML_MixedContentAndEmptyElements(t *testing.T) {
	recs := mustRead(t, FormatXML, `<r><Item>hi<b/>there</Item></r>`, Options{RecordPath: "Item"})
	if recs[0]["#text"] != "hithere" {
		t.Errorf("mixed text should land under #text: %v", recs[0])
	}
	if recs[0]["b"] != "" {
		t.Errorf("empty element should be an empty string: %#v", recs[0]["b"])
	}
}

func TestXML_SingleSegmentPathMatchesAtAnyDepth(t *testing.T) {
	recs := mustRead(t, FormatXML, `<a><b><Item><v>1</v></Item></b></a>`, Options{RecordPath: "Item"})
	if len(recs) != 1 || recs[0]["v"] != "1" {
		t.Errorf("deep single-segment match failed: %v", recs)
	}
}

func TestXML_NonMatchingPathYieldsNothing(t *testing.T) {
	recs := mustRead(t, FormatXML, `<Orders><Order><a>1</a></Order></Orders>`, Options{RecordPath: "Invoices.Invoice"})
	if len(recs) != 0 {
		t.Errorf("a path that matches nothing should yield no records, got %v", recs)
	}
}

func TestXML_CustomAttrPrefixAndTextKey(t *testing.T) {
	recs := mustRead(t, FormatXML, `<r><Item id="1">txt<c/></Item></r>`,
		Options{RecordPath: "Item", AttrPrefix: "attr_", TextKey: "value"})
	if recs[0]["attr_id"] != "1" || recs[0]["value"] != "txt" {
		t.Errorf("custom conventions ignored: %v", recs[0])
	}
}

// --- YAML ---

func TestYAML_MappingSequenceAndMultiDoc(t *testing.T) {
	one := mustRead(t, FormatYAML, "a: 1\nb: x\n", Options{})
	if len(one) != 1 || one[0]["a"] != 1 || one[0]["b"] != "x" {
		t.Errorf("mapping doc: %v", one)
	}
	seq := mustRead(t, FormatYAML, "- a: 1\n- a: 2\n", Options{})
	if len(seq) != 2 || seq[1]["a"] != 2 {
		t.Errorf("sequence doc: %v", seq)
	}
	multi := mustRead(t, FormatYAML, "a: 1\n---\na: 2\n", Options{})
	if len(multi) != 2 || multi[1]["a"] != 2 {
		t.Errorf("multi-doc: %v", multi)
	}
}

func TestYAML_NonStringKeysStringified(t *testing.T) {
	recs := mustRead(t, FormatYAML, "1: one\ntrue: yes\n", Options{})
	if recs[0]["1"] != "one" {
		t.Errorf("numeric key should stringify: %v", recs[0])
	}
}

// --- format resolution ---

func TestDetect_Precedence(t *testing.T) {
	// Explicit config wins over a contradicting content type and body.
	if got := Detect("csv", "application/json", []byte(`{"a":1}`)); got != FormatCSV {
		t.Errorf("explicit should win, got %s", got)
	}
	// ContentType wins over the body sniff.
	if got := Detect("", "text/csv", []byte(`{"a":1}`)); got != FormatCSV {
		t.Errorf("content type should win over sniff, got %s", got)
	}
	// charset parameters are ignored.
	if got := Detect("", "application/xml; charset=utf-8", nil); got != FormatXML {
		t.Errorf("charset param broke detection, got %s", got)
	}
	// Uninformative content types fall through to the sniff.
	if got := Detect("", "application/octet-stream", []byte("a,b\n1,2")); got != FormatCSV {
		t.Errorf("sniff should resolve csv, got %s", got)
	}
	if got := Detect("", "text/plain", []byte("<?xml version=\"1.0\"?><a/>")); got != FormatXML {
		t.Errorf("sniff should resolve xml, got %s", got)
	}
	// Nothing to go on: json, the historical default.
	if got := Detect("", "", nil); got != FormatJSON {
		t.Errorf("default should be json, got %s", got)
	}
	// "auto" is treated as unset.
	if got := Detect("auto", "text/csv", nil); got != FormatCSV {
		t.Errorf("auto should defer to content type, got %s", got)
	}
}

func TestNormaliseAliases(t *testing.T) {
	for in, want := range map[string]string{
		"": FormatJSON, "JSON": FormatJSON, "jsonl": FormatNDJSON,
		"yml": FormatYAML, "TSV": FormatTSV, "  csv  ": FormatCSV,
	} {
		if got := Normalise(in); got != want {
			t.Errorf("Normalise(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStreams(t *testing.T) {
	for _, f := range []string{FormatJSON, FormatNDJSON, FormatCSV, FormatTSV, FormatXML} {
		if !Streams(f) {
			t.Errorf("%s should stream", f)
		}
	}
	if Streams(FormatYAML) {
		t.Error("yaml buffers each document and must not claim to stream")
	}
}

func TestNew_UnsupportedFormat(t *testing.T) {
	if _, err := New("parquet", strings.NewReader(""), Options{}); err == nil {
		t.Fatal("unsupported format must error, not silently fall back to json")
	}
}
