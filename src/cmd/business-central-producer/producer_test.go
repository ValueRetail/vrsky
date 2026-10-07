package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/oauthcc"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func testProducer() *bcProducer {
	return &bcProducer{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: http.DefaultClient,
	}
}

func tokenServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"tok-xyz","expires_in":3600}`)
	}))
}

func cfgFor(apiURL, tokenURL string) *BCProducerConfig {
	return &BCProducerConfig{
		AADTenantID: "t", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: apiURL, TokenURL: tokenURL,
	}
}

// TestWrite_SendsBearerAndBody verifies the POST carries the Bearer token, the
// body, the company-scoped path, and that a 2xx acks.
func TestWrite_Success(t *testing.T) {
	tok := tokenServer(t)
	defer tok.Close()

	var auth, path, body string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer api.Close()

	cfg := cfgFor(api.URL, tok.URL)
	c := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(http.DefaultClient)
	if err := testProducer().write(context.Background(), cfg, c, "conn-1", []byte(`{"number":"IT-1"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if auth != "Bearer tok-xyz" {
		t.Errorf("auth = %q, want Bearer tok-xyz", auth)
	}
	if path != "/companies(GUID)/items" {
		t.Errorf("path = %q", path)
	}
	if body != `{"number":"IT-1"}` {
		t.Errorf("body = %q", body)
	}
}

// TestWrite_Classification maps HTTP status → SDK retry semantics.
func TestWrite_Classification(t *testing.T) {
	tok := tokenServer(t)
	defer tok.Close()

	cases := []struct {
		status    int
		permanent bool
		wantErr   bool
	}{
		{http.StatusCreated, false, false},
		{http.StatusBadRequest, true, true},
		{http.StatusUnauthorized, true, true},
		{http.StatusInternalServerError, false, true},
		{http.StatusServiceUnavailable, false, true},
	}
	for _, tc := range cases {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		cfg := cfgFor(api.URL, tok.URL)
		c := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(http.DefaultClient)
		err := testProducer().write(context.Background(), cfg, c, "conn-1", []byte(`{}`))
		api.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("status %d: err=%v wantErr=%v", tc.status, err, tc.wantErr)
			continue
		}
		if tc.wantErr && sdk.IsPermanent(err) != tc.permanent {
			t.Errorf("status %d: IsPermanent=%v want %v", tc.status, sdk.IsPermanent(err), tc.permanent)
		}
	}
}

// --- dedupe_fields (plans/webhook-idempotency.md) ---

// bcFake is a Business Central that answers the dedupe GET and records POSTs.
type bcFake struct {
	srv     *httptest.Server
	gets    []string // raw query strings of the GET lookups
	posts   int
	lookup  func(w http.ResponseWriter) // what the GET answers
	postRes int
}

func newBCFake(t *testing.T) *bcFake {
	f := &bcFake{postRes: http.StatusCreated}
	f.lookup = func(w http.ResponseWriter) { fmt.Fprint(w, `{"value":[]}`) }
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			f.gets = append(f.gets, r.URL.RawQuery)
			f.lookup(w)
		case http.MethodPost:
			f.posts++
			w.WriteHeader(f.postRes)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

const sale = `{"externalDocumentNumber":"T02-000009","customerNumber":"C'001","totalAmount":12.5}`

func dedupeCfg(apiURL, tokenURL string) *BCProducerConfig {
	cfg := cfgFor(apiURL, tokenURL)
	cfg.Entity = "salesInvoices"
	cfg.DedupeFields = []string{"externalDocumentNumber", "customerNumber"}
	return cfg
}

func TestDedupeFields_SkipsWhenExists(t *testing.T) {
	tok := tokenServer(t)
	defer tok.Close()
	bc := newBCFake(t)
	bc.lookup = func(w http.ResponseWriter) { fmt.Fprint(w, `{"value":[{"id":"inv-1"}]}`) }
	cfg := dedupeCfg(bc.srv.URL, tok.URL)
	c := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(http.DefaultClient)

	if err := testProducer().write(context.Background(), cfg, c, "conn-1", []byte(sale)); err != nil {
		t.Fatalf("write: %v (an existing record is acked, not an error)", err)
	}
	if bc.posts != 0 {
		t.Fatalf("POSTed %d times although the record exists", bc.posts)
	}
	if len(bc.gets) != 1 {
		t.Fatalf("%d lookups, want 1", len(bc.gets))
	}
	q, _ := url.ParseQuery(bc.gets[0])
	// Both fields, equality, the single quote in the customer number doubled.
	if got, want := q.Get("$filter"), "externalDocumentNumber eq 'T02-000009' and customerNumber eq 'C''001'"; got != want {
		t.Errorf("$filter = %q, want %q", got, want)
	}
	if q.Get("$top") != "1" || q.Get("$select") != "id" {
		t.Errorf("lookup query = %q, want $top=1 and $select=id", bc.gets[0])
	}
}

func TestDedupeFields_PostsWhenAbsent(t *testing.T) {
	tok := tokenServer(t)
	defer tok.Close()
	bc := newBCFake(t)
	cfg := dedupeCfg(bc.srv.URL, tok.URL)
	c := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(http.DefaultClient)
	if err := testProducer().write(context.Background(), cfg, c, "conn-1", []byte(sale)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(bc.gets) != 1 || bc.posts != 1 {
		t.Fatalf("lookups=%d posts=%d, want 1 and 1", len(bc.gets), bc.posts)
	}
}

// When BC cannot be asked, the message waits — a POST on an unanswered
// question is how a duplicate would get in.
func TestDedupeFields_LookupFailureRetriesWithoutPosting(t *testing.T) {
	tok := tokenServer(t)
	defer tok.Close()
	for _, tc := range []struct {
		status      int
		permanent   bool
		rateLimited bool
	}{
		{http.StatusInternalServerError, false, false},
		{http.StatusTooManyRequests, false, true},
		{http.StatusUnauthorized, true, false},
	} {
		bc := newBCFake(t)
		status := tc.status
		bc.lookup = func(w http.ResponseWriter) { w.WriteHeader(status) }
		cfg := dedupeCfg(bc.srv.URL, tok.URL)
		c := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(http.DefaultClient)
		err := testProducer().write(context.Background(), cfg, c, "conn-1", []byte(sale))
		if err == nil {
			t.Fatalf("lookup %d: no error", tc.status)
		}
		if sdk.IsPermanent(err) != tc.permanent {
			t.Errorf("lookup %d: IsPermanent=%v, want %v (%v)", tc.status, sdk.IsPermanent(err), tc.permanent, err)
		}
		if _, ok := sdk.RetryAfter(err); ok != tc.rateLimited {
			t.Errorf("lookup %d: rate-limited=%v, want %v", tc.status, ok, tc.rateLimited)
		}
		if bc.posts != 0 {
			t.Errorf("lookup %d: POSTed anyway", tc.status)
		}
	}
}

func TestDedupeFields_MissingFieldPostsAndPatchIgnoresIt(t *testing.T) {
	tok := tokenServer(t)
	defer tok.Close()
	// A payload without one of the fields cannot be checked: write as today.
	bc := newBCFake(t)
	cfg := dedupeCfg(bc.srv.URL, tok.URL)
	c := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(http.DefaultClient)
	if err := testProducer().write(context.Background(), cfg, c, "conn-1", []byte(`{"externalDocumentNumber":"T02-000010"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(bc.gets) != 0 || bc.posts != 1 {
		t.Fatalf("missing field: lookups=%d posts=%d, want 0 and 1", len(bc.gets), bc.posts)
	}
	// PATCH is an update by design: no lookup.
	bc2 := newBCFake(t)
	cfg2 := dedupeCfg(bc2.srv.URL, tok.URL)
	cfg2.Method = "PATCH"
	bc2.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			bc2.gets = append(bc2.gets, r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
	})
	if err := testProducer().write(context.Background(), cfg2, c, "conn-1", []byte(sale)); err != nil {
		t.Fatalf("patch: %v", err)
	}
	if len(bc2.gets) != 0 {
		t.Fatalf("PATCH did a lookup")
	}
}

func TestOdataEqualityFilter(t *testing.T) {
	rec := map[string]any{"s": "a'b", "n": float64(42), "b": true, "e": "", "o": map[string]any{}}
	if f, m := odataEqualityFilter([]string{"s", "n", "b"}, rec); m != "" || f != "s eq 'a''b' and n eq 42 and b eq true" {
		t.Errorf("filter=%q missing=%q", f, m)
	}
	for _, field := range []string{"e", "o", "absent"} {
		if _, m := odataEqualityFilter([]string{"s", field}, rec); m != field {
			t.Errorf("field %s: missing=%q", field, m)
		}
	}
	if _, m := odataEqualityFilter([]string{" ", ""}, rec); m == "" {
		t.Error("no usable fields must read as missing")
	}
}
