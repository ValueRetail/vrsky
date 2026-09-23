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

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

func newTestConsumer() (*bcConsumer, *[]*envelope.Envelope, *sync.Mutex) {
	var mu sync.Mutex
	var got []*envelope.Envelope
	c := &bcConsumer{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient:  http.DefaultClient,
		checkpoints: checkpoint.NewInMemoryStore(),
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
	got := cfg.entityURL("")
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

// --- incremental polling (watermark) ---------------------------------------

// bcTestServer stands in for Business Central: it records the $filter of every
// request and answers each one from the supplied page bodies in order.
func bcTestServer(t *testing.T, pages ...string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var filters []string
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		filters = append(filters, r.URL.Query().Get("$filter"))
		body := `{"value":[]}`
		if n < len(pages) {
			body = pages[n]
		}
		n++
		mu.Unlock()
		fmt.Fprint(w, body)
	}))
	return srv, &filters
}

func bcTestToken(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
	}))
}

func recordsIn(t *testing.T, envs []*envelope.Envelope) int {
	t.Helper()
	total := 0
	for _, env := range envs {
		var recs []map[string]any
		if err := json.Unmarshal(env.Payload, &recs); err != nil {
			t.Fatalf("payload: %v", err)
		}
		total += len(recs)
	}
	return total
}

// TestIncrementalSecondPollFetchesOnlyWhatChanged is the point of the feature:
// a restart or a second tick must not replay the whole entity. It asserts what
// was delivered, not merely that a filter string was built.
func TestIncrementalSecondPollFetchesOnlyWhatChanged(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	apiSrv, filters := bcTestServer(t,
		`{"value":[{"number":"A","lastModifiedDateTime":"2026-01-01T10:00:00Z"},`+
			`{"number":"B","lastModifiedDateTime":"2026-01-02T10:00:00Z"}]}`,
		`{"value":[{"number":"C","lastModifiedDateTime":"2026-01-03T10:00:00Z"}]}`,
	)
	defer apiSrv.Close()

	c, got, mu := newTestConsumer()
	cfg := &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: apiSrv.URL, TokenURL: tokenSrv.URL,
		Incremental: true, NodeID: "src",
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
		WithHTTPClient(http.DefaultClient)

	for i := 0; i < 2; i++ {
		if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
			t.Fatalf("poll %d: %v", i+1, err)
		}
	}

	if len(*filters) != 2 {
		t.Fatalf("made %d requests, want 2", len(*filters))
	}
	if (*filters)[0] != "" {
		t.Errorf("first poll sent $filter=%q, want none — there is no watermark yet", (*filters)[0])
	}
	if want := "lastModifiedDateTime gt 2026-01-02T10:00:00Z"; (*filters)[1] != want {
		t.Errorf("second poll $filter = %q, want %q", (*filters)[1], want)
	}

	mu.Lock()
	defer mu.Unlock()
	if n := recordsIn(t, *got); n != 3 {
		t.Errorf("delivered %d records across both polls, want 3 (2 then 1) — "+
			"the second poll replayed records the first already delivered", n)
	}
}

// TestIncrementalIsOffByDefault — an existing connection keeps re-reading the
// whole entity until its owner opts in. Turning this on silently would change
// what a running pipeline delivers.
func TestIncrementalIsOffByDefault(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	page := `{"value":[{"number":"A","lastModifiedDateTime":"2026-01-01T10:00:00Z"}]}`
	apiSrv, filters := bcTestServer(t, page, page)
	defer apiSrv.Close()

	c, got, mu := newTestConsumer()
	cfg := &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: apiSrv.URL, TokenURL: tokenSrv.URL, NodeID: "src",
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
		WithHTTPClient(http.DefaultClient)

	for i := 0; i < 2; i++ {
		if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
			t.Fatalf("poll %d: %v", i+1, err)
		}
	}
	for i, f := range *filters {
		if f != "" {
			t.Errorf("poll %d sent $filter=%q with incremental off", i+1, f)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if n := recordsIn(t, *got); n != 2 {
		t.Errorf("delivered %d records, want 2 (the same record twice)", n)
	}
}

// TestWatermarkHoldsWhenAPageFails — a fetch that dies after page 1 must not
// bank the records it did read. Resuming early would skip everything on the
// pages it never got to.
func TestWatermarkHoldsWhenAPageFails(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()

	var mu sync.Mutex
	var filters []string
	call := 0
	var apiURL string
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.URL.Query().Get("page") == "" {
			filters = append(filters, r.URL.Query().Get("$filter"))
		}
		call++
		n := call
		mu.Unlock()

		switch {
		case r.URL.Query().Get("page") == "2":
			http.Error(w, "boom", http.StatusInternalServerError)
		case n <= 2: // first poll: page 1 then the failing page 2
			fmt.Fprintf(w, `{"value":[{"number":"A","lastModifiedDateTime":"2026-01-01T10:00:00Z"}],`+
				`"@odata.nextLink":"%s?page=2"}`, apiURL)
		default: // second poll, single page
			fmt.Fprint(w, `{"value":[{"number":"B","lastModifiedDateTime":"2026-01-02T10:00:00Z"}]}`)
		}
	}))
	defer apiSrv.Close()
	apiURL = apiSrv.URL + "/companies(GUID)/items"

	c, _, _ := newTestConsumer()
	cfg := &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: apiSrv.URL, TokenURL: tokenSrv.URL,
		Incremental: true, NodeID: "src",
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
		WithHTTPClient(http.DefaultClient)

	if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err == nil {
		t.Fatal("first poll: want the page-2 failure to surface, got nil")
	}
	if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
		t.Fatalf("second poll: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(filters) != 2 {
		t.Fatalf("made %d first-page requests, want 2", len(filters))
	}
	if filters[1] != "" {
		t.Errorf("second poll resumed from %q after a failed fetch; want a full re-read", filters[1])
	}
}

// TestWatermarkComparesInstantsNotText — "…:00.5Z" sorts before "…:00Z" as a
// string while being the later instant. Picking the watermark by string order
// would park it in the past and redeliver everything after it, every poll.
func TestWatermarkComparesInstantsNotText(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	apiSrv, filters := bcTestServer(t,
		`{"value":[{"number":"A","lastModifiedDateTime":"2026-01-01T10:00:00.500Z"},`+
			`{"number":"B","lastModifiedDateTime":"2026-01-01T10:00:00Z"}]}`,
		`{"value":[]}`,
	)
	defer apiSrv.Close()

	c, _, _ := newTestConsumer()
	cfg := &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: apiSrv.URL, TokenURL: tokenSrv.URL,
		Incremental: true, NodeID: "src",
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
		WithHTTPClient(http.DefaultClient)

	for i := 0; i < 2; i++ {
		if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
			t.Fatalf("poll %d: %v", i+1, err)
		}
	}
	if want := "lastModifiedDateTime gt 2026-01-01T10:00:00.500Z"; (*filters)[1] != want {
		t.Errorf("watermark = %q, want %q", (*filters)[1], want)
	}
}

// TestWatermarkSkipsRecordsWithoutAUsableTimestamp — an entity with no
// lastModifiedDateTime (or a custom API page naming it something else) must
// keep polling rather than build a filter out of a missing field.
func TestWatermarkSkipsRecordsWithoutAUsableTimestamp(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	apiSrv, filters := bcTestServer(t,
		`{"value":[{"number":"A"},{"number":"B","lastModifiedDateTime":"not-a-time"}]}`,
		`{"value":[]}`,
	)
	defer apiSrv.Close()

	c, got, mu := newTestConsumer()
	cfg := &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: apiSrv.URL, TokenURL: tokenSrv.URL,
		Incremental: true, NodeID: "src",
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
		WithHTTPClient(http.DefaultClient)

	for i := 0; i < 2; i++ {
		if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
			t.Fatalf("poll %d: %v", i+1, err)
		}
	}
	if (*filters)[1] != "" {
		t.Errorf("second poll $filter = %q, want none — nothing gave a usable watermark", (*filters)[1])
	}
	mu.Lock()
	defer mu.Unlock()
	if n := recordsIn(t, *got); n != 2 {
		t.Errorf("delivered %d records, want 2 — records without a timestamp must still flow", n)
	}
}

// TestEntityURLCursorDoesNotEscapeTheConfiguredFilter — `a or b` plus a cursor
// must not bind as `a or (b and cursor)`, which would re-read everything
// matching `a` on every poll.
func TestEntityURLCursorDoesNotEscapeTheConfiguredFilter(t *testing.T) {
	cfg := &BCConfig{
		APIBaseURL: "https://host/api/v2.0", CompanyID: "GUID", Entity: "salesOrders",
		Filter: "status eq 'Open' or status eq 'Draft'", Incremental: true,
	}
	got := cfg.filterWithCursor("2026-01-02T10:00:00Z")
	want := "(status eq 'Open' or status eq 'Draft') and lastModifiedDateTime gt 2026-01-02T10:00:00Z"
	if got != want {
		t.Errorf("filterWithCursor = %q\nwant %q", got, want)
	}
}

// TestCursorFieldIsConfigurable — a custom API page may name its watermark
// something other than BC's standard field.
func TestCursorFieldIsConfigurable(t *testing.T) {
	cfg := &BCConfig{Filter: "", CursorField: "systemModifiedAt", Incremental: true}
	if got, want := cfg.filterWithCursor("2026-01-02T10:00:00Z"), "systemModifiedAt gt 2026-01-02T10:00:00Z"; got != want {
		t.Errorf("filterWithCursor = %q, want %q", got, want)
	}
}

// --- server-driven paging -------------------------------------------------

// TestPageSizeAsksBusinessCentralToPage is the reason page_size exists: BC
// returns most entities whole, so @odata.nextLink — and therefore the
// pagination path — never runs. `Prefer: odata.maxpagesize` is what makes it.
func TestPageSizeAsksBusinessCentralToPage(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()

	var mu sync.Mutex
	var prefers []string
	var apiURL string
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		prefers = append(prefers, r.Header.Get("Prefer"))
		mu.Unlock()

		// Page only when asked to, exactly as BC does.
		if r.Header.Get("Prefer") != "odata.maxpagesize=2" {
			fmt.Fprint(w, `{"value":[{"number":"A"},{"number":"B"},{"number":"C"}]}`)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"value":[{"number":"C"}]}`)
			return
		}
		fmt.Fprintf(w, `{"value":[{"number":"A"},{"number":"B"}],"@odata.nextLink":"%s?page=2"}`, apiURL)
	}))
	defer apiSrv.Close()
	apiURL = apiSrv.URL + "/companies(GUID)/items"

	c, got, mu2 := newTestConsumer()
	cfg := &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: apiSrv.URL, TokenURL: tokenSrv.URL,
		PageSize: 2, NodeID: "src",
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
		WithHTTPClient(http.DefaultClient)

	if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}

	mu.Lock()
	seen := append([]string(nil), prefers...)
	mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("made %d requests, want 2 — the nextLink page was not followed", len(seen))
	}
	for i, p := range seen {
		if p != "odata.maxpagesize=2" {
			t.Errorf("request %d sent Prefer=%q, want odata.maxpagesize=2 — "+
				"the header must be on the nextLink request too, or page 2 comes back whole", i+1, p)
		}
	}

	mu2.Lock()
	defer mu2.Unlock()
	if n := recordsIn(t, *got); n != 3 {
		t.Errorf("delivered %d records across the pages, want 3", n)
	}
}

// TestNoPageSizeSendsNoPreferHeader — an unset page size must leave BC on its
// own default rather than inventing one.
func TestNoPageSizeSendsNoPreferHeader(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()

	var mu sync.Mutex
	var prefer string
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		prefer = r.Header.Get("Prefer")
		mu.Unlock()
		fmt.Fprint(w, `{"value":[{"number":"A"}]}`)
	}))
	defer apiSrv.Close()

	c, _, _ := newTestConsumer()
	cfg := &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: apiSrv.URL, TokenURL: tokenSrv.URL, NodeID: "src",
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
		WithHTTPClient(http.DefaultClient)
	if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if prefer != "" {
		t.Errorf("sent Prefer=%q with no page_size configured", prefer)
	}
}

// TestPastedWhitespaceDoesNotReachTheURL — a GUID copied out of the Azure
// portal arrives with a trailing space more often than not, and the tenant id
// goes straight into the API path. Entra accepted it; Business Central
// answered `400 RequestDataInvalid`, which says nothing about a space you
// cannot see. Cost an afternoon on 2026-09-23.
func TestPastedWhitespaceDoesNotReachTheURL(t *testing.T) {
	cfg := &BCConfig{
		AADTenantID: "  093542aa-492d-4505-8c37-80d4b0eeaef0  ",
		Environment: " Production\n",
		CompanyID:   "\tbb45ab19-8ea5-f111-90e5-70a8a5790457 ",
		ClientID:    " 6e1daad1-f3dc-4d76-ab50-8ee8c9b62e40 ",
		Entity:      " items ",
		Filter:      "  status eq 'Open'  ",
		CursorField: " lastModifiedDateTime ",
	}
	cfg.normalise()

	got := cfg.entityURL("")
	want := "https://api.businesscentral.dynamics.com/v2.0/093542aa-492d-4505-8c37-80d4b0eeaef0/" +
		"Production/api/v2.0/companies(bb45ab19-8ea5-f111-90e5-70a8a5790457)/items?" +
		"$filter=status+eq+%27Open%27"
	if got != want {
		t.Errorf("entityURL = %q\nwant %q", got, want)
	}
	if want := "https://login.microsoftonline.com/093542aa-492d-4505-8c37-80d4b0eeaef0/oauth2/v2.0/token"; cfg.effectiveTokenURL() != want {
		t.Errorf("tokenURL = %q, want %q", cfg.effectiveTokenURL(), want)
	}
	if cfg.effectiveCursorField() != "lastModifiedDateTime" {
		t.Errorf("cursorField = %q", cfg.effectiveCursorField())
	}
}

// The client secret is opaque: trimming a credential guesses about its
// contents rather than its shape, so it is passed through untouched.
func TestClientSecretIsNotTrimmed(t *testing.T) {
	cfg := &BCConfig{ClientSecret: " s3cret "}
	cfg.normalise()
	if cfg.ClientSecret != " s3cret " {
		t.Errorf("ClientSecret = %q, want it untouched", cfg.ClientSecret)
	}
}
