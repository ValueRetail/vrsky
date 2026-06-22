package promquery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueryByLabel(t *testing.T) {
	const body = `{
	  "status": "success",
	  "data": {
	    "resultType": "vector",
	    "result": [
	      {"metric": {"tenant_id": "t-1"}, "value": [1718000000, "42"]},
	      {"metric": {"tenant_id": "t-2"}, "value": [1718000000, "7.5"]},
	      {"metric": {"other": "x"},       "value": [1718000000, "99"]},
	      {"metric": {"tenant_id": "t-3"}, "value": [1718000000, "NaN"]}
	    ]
	  }
	}`
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client())
	got, err := c.QueryByLabel(context.Background(), `sum by (tenant_id)(increase(vrsky_messages_published_total[24h]))`, "tenant_id")
	if err != nil {
		t.Fatalf("QueryByLabel: %v", err)
	}
	if gotQuery == "" {
		t.Error("query param was not forwarded")
	}
	if got["t-1"] != 42 {
		t.Errorf("t-1 = %v, want 42", got["t-1"])
	}
	if got["t-2"] != 7.5 {
		t.Errorf("t-2 = %v, want 7.5", got["t-2"])
	}
	// Series without the label is skipped; NaN sample is skipped.
	if _, ok := got["t-3"]; ok {
		t.Error("NaN sample should be skipped")
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2 (label-less + NaN series dropped)", len(got))
	}
}

func TestQueryByLabel_Errors(t *testing.T) {
	// Prometheus-level error.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"parse error"}`))
	}))
	defer bad.Close()
	if _, err := New(bad.URL, bad.Client()).QueryByLabel(context.Background(), "x", "tenant_id"); err == nil {
		t.Error("expected error on status=error response")
	}

	// HTTP-level error.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()
	if _, err := New(down.URL, down.Client()).QueryByLabel(context.Background(), "x", "tenant_id"); err == nil {
		t.Error("expected error on 500 response")
	}

	// Non-vector result (e.g. a matrix from a range query) is rejected loudly
	// rather than silently returning an empty map.
	matrix := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer matrix.Close()
	if _, err := New(matrix.URL, matrix.Client()).QueryByLabel(context.Background(), "x", "tenant_id"); err == nil {
		t.Error("expected error on non-vector (matrix) result")
	}
}
