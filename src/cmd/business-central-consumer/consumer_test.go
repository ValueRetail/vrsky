package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

func newTestConsumer() (*bcConsumer, *[]*envelope.Envelope, *sync.Mutex) {
	var mu sync.Mutex
	var got []*envelope.Envelope
	c := &bcConsumer{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: http.DefaultClient,
		publish: func(_ context.Context, env *envelope.Envelope) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, env)
			return nil
		},
	}
	return c, &got, &mu
}

// TestFetchAndPublish_ODataPaginationWithBearer verifies the Bearer token is
// sent and @odata.nextLink pagination is followed across pages.
func TestFetchAndPublish_ODataPaginationWithBearer(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"tok-abc","expires_in":3600}`)
	}))
	defer tokenSrv.Close()

	var apiURL string
	var authSeen string
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSeen = r.Header.Get("Authorization")
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"value":[{"number":"C1"}]}`) // last page, no nextLink
			return
		}
		// first page → one record + a nextLink to page 2
		fmt.Fprintf(w, `{"value":[{"number":"A1"},{"number":"B1"}],"@odata.nextLink":"%s?page=2"}`, apiURL)
	}))
	defer apiSrv.Close()
	apiURL = apiSrv.URL + "/companies(comp-guid)/items"

	c, got, mu := newTestConsumer()
	cfg := &BCConfig{
		AADTenantID: "tenant", CompanyID: "comp-guid", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: apiSrv.URL, TokenURL: tokenSrv.URL,
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(http.DefaultClient)

	if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if authSeen != "Bearer tok-abc" {
		t.Errorf("Authorization = %q, want Bearer tok-abc", authSeen)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 2 {
		t.Fatalf("published %d pages, want 2", len(*got))
	}
	total := 0
	for _, env := range *got {
		var recs []map[string]any
		if err := json.Unmarshal(env.Payload, &recs); err != nil {
			t.Fatalf("payload: %v", err)
		}
		total += len(recs)
		if env.Source != "business-central-consumer" || env.TenantID != "tenant-1" {
			t.Errorf("bad envelope: %+v", env)
		}
	}
	if total != 3 {
		t.Errorf("published %d records, want 3", total)
	}
}

// TestEntityURL builds the API v2.0 company-scoped URL with an optional $filter.
func TestEntityURL(t *testing.T) {
	cfg := &BCConfig{APIBaseURL: "https://host/api/v2.0", CompanyID: "GUID", Entity: "salesOrders", Filter: "status eq 'Open'"}
	got := cfg.entityURL()
	want := "https://host/api/v2.0/companies(GUID)/salesOrders?$filter=status+eq+%27Open%27"
	if got != want {
		t.Errorf("entityURL = %q\nwant %q", got, want)
	}
}

// TestSampleData_BC exercises the pre-deploy /sample-data aux endpoint: it must
// acquire a token, GET the first OData page, and return the records as {ok,data}.
func TestSampleData_BC(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"tok-abc","expires_in":3600}`)
	}))
	defer tokenSrv.Close()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"value":[{"number":"C1","displayName":"Acme"},{"number":"C2","displayName":"Beta"}]}`)
	}))
	defer apiSrv.Close()

	c := &bcConsumer{httpClient: http.DefaultClient}
	body := fmt.Sprintf(`{"client_id":"id","client_secret":"sec","api_base_url":%q,"token_url":%q,"company_id":"GUID","entity":"customers"}`, apiSrv.URL, tokenSrv.URL)
	req := httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body))
	w := httptest.NewRecorder()
	c.handleSampleData()(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		OK    bool          `json:"ok"`
		Data  []interface{} `json:"data"`
		Error string        `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Fatalf("ok=false error=%q", resp.Error)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("want 2 records, got %d: %s", len(resp.Data), w.Body.String())
	}
}
