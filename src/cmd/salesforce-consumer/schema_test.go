package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestObjectFromSOQL(t *testing.T) {
	cases := map[string]string{
		"SELECT Id, Name FROM Account":                     "Account",
		"select id from contact where x=1":                 "contact",
		"SELECT Id FROM My_Custom__c ORDER BY CreatedDate": "My_Custom__c",
		"SELECT count() FROM Opportunity LIMIT 10":         "Opportunity",
		"not a query": "",
		"":            "",
		// Child-relationship subquery in SELECT — outer object is Account.
		"SELECT Name, (SELECT LastName FROM Contacts) FROM Account": "Account",
		// Semi-join subquery in WHERE — outer object is Account.
		"SELECT Id FROM Account WHERE Id IN (SELECT AccountId FROM Contact)": "Account",
	}
	for soql, want := range cases {
		if got := objectFromSOQL(soql); got != want {
			t.Errorf("objectFromSOQL(%q) = %q, want %q", soql, got, want)
		}
	}
}

// TestHandleSchema drives the /schema/ handler against a fake Salesforce describe
// endpoint (token resolution stubbed) and checks the fields are mapped.
func TestHandleSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/sobjects/Account/describe") {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fake-token" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"Account","fields":[
			{"name":"Id","type":"id","nillable":false},
			{"name":"Name","type":"string","nillable":false},
			{"name":"AnnualRevenue","type":"currency","nillable":true},
			{"name":"IsDeleted","type":"boolean","nillable":false}
		]}`))
	}))
	defer srv.Close()

	c := &salesforceConsumer{
		httpClient:   srv.Client(),
		logger:       slog.Default(),
		resolveToken: func(context.Context, string, string, bool) (string, error) { return "fake-token", nil },
	}

	body, _ := json.Marshal(map[string]string{
		"tenant_id": "tenant-x", "instance_url": srv.URL, "oauth_grant_id": "g1",
		"soql": "SELECT Id, Name FROM Account",
	})
	req := httptest.NewRequest(http.MethodPost, "/schema/", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	c.handleSchema()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK     bool   `json:"ok"`
		Object string `json:"object"`
		Fields []struct {
			Name     string `json:"name"`
			Type     string `json:"type"`
			Nullable bool   `json:"nullable"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || resp.Object != "Account" || len(resp.Fields) != 4 {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Fields[2].Name != "AnnualRevenue" || resp.Fields[2].Type != "currency" || !resp.Fields[2].Nullable {
		t.Errorf("AnnualRevenue field = %+v", resp.Fields[2])
	}
}

func TestHandleSchema_MissingObject(t *testing.T) {
	c := &salesforceConsumer{logger: slog.Default()}
	body, _ := json.Marshal(map[string]string{
		"tenant_id": "t", "instance_url": "https://x", "oauth_grant_id": "g1", "soql": "not a query",
	})
	req := httptest.NewRequest(http.MethodPost, "/schema/", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	c.handleSchema()(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
