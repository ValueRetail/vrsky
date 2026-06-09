package main

import "testing"

func TestParseDelimitedSample_CSV(t *testing.T) {
	csv := []byte("id,name,city\n1,Acme,Bicester\n2,Globex,Oslo\n")
	headers, row := parseDelimitedSample(csv, ',')
	if len(headers) != 3 || headers[0] != "id" || headers[2] != "city" {
		t.Fatalf("headers = %v, want [id name city]", headers)
	}
	if row["name"] != "Acme" || row["city"] != "Bicester" {
		t.Errorf("first row = %v, want Acme/Bicester", row)
	}
}

func TestParseDelimitedSample_TSV_HeaderOnly(t *testing.T) {
	tsv := []byte("col_a\tcol_b\n")
	headers, row := parseDelimitedSample(tsv, '\t')
	if len(headers) != 2 || headers[1] != "col_b" {
		t.Fatalf("headers = %v, want [col_a col_b]", headers)
	}
	// No data rows -> empty-string values, keys present.
	if v, ok := row["col_a"]; !ok || v != "" {
		t.Errorf("row[col_a] = %q (ok=%v), want empty present", v, ok)
	}
}

func TestParseDelimitedSample_Empty(t *testing.T) {
	if h, _ := parseDelimitedSample([]byte(""), ','); h != nil {
		t.Errorf("empty input should yield nil headers, got %v", h)
	}
}
