package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

// fakePicture is one record's picture on the fake Business Central.
type fakePicture struct {
	id, contentType string
	content         []byte
	contentStatus   int    // 0 = 200
	readLinkHost    string // host to put in mediaReadLink ("" = the server's own)
}

// fakeBC stands in for Business Central in pictures mode: one page of
// records, a picture (or 404) per record, and the content behind it. It
// records every request path and the page request's query.
type fakeBC struct {
	mu        sync.Mutex
	records   string // the page's "value" array
	pictures  map[string]*fakePicture
	paths     []string
	pageQuery string
	srv       *httptest.Server
}

func newFakeBC(t *testing.T, records string, pictures map[string]*fakePicture) *fakeBC {
	t.Helper()
	f := &fakeBC{records: records, pictures: pictures}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBC) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/content"):
		for id, pic := range f.pictures {
			if strings.Contains(p, "("+id+")/picture("+pic.id+")/content") {
				if pic.contentStatus != 0 {
					w.WriteHeader(pic.contentStatus)
					return
				}
				w.Header().Set("Content-Type", pic.contentType)
				_, _ = w.Write(pic.content)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	case strings.HasSuffix(p, "/picture"):
		for id, pic := range f.pictures {
			if strings.HasSuffix(p, "("+id+")/picture") {
				host := pic.readLinkHost
				if host == "" {
					host = f.srv.URL
				}
				link := host + strings.TrimSuffix(p, "/picture") + "/picture(" + pic.id + ")/content"
				fmt.Fprintf(w, `{"id":%q,"parentType":"Item","width":400,"height":300,"contentType":%q,`+
					`"pictureContent@odata.mediaReadLink":%q}`, pic.id, pic.contentType, link)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		f.pageQuery = r.URL.RawQuery
		fmt.Fprintf(w, `{"value":%s}`, f.records)
	}
}

func (f *fakeBC) contentRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.paths {
		if strings.HasSuffix(p, "/content") {
			n++
		}
	}
	return n
}

func picturesConfig(f *fakeBC, tokenURL string) *BCConfig {
	return &BCConfig{
		AADTenantID: "aad", CompanyID: "C1", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: f.srv.URL, TokenURL: tokenURL, Pictures: true,
	}
}

func fetch(t *testing.T, c *bcConsumer, cfg *BCConfig) error {
	t.Helper()
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
		WithHTTPClient(http.DefaultClient)
	return c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger)
}

var jpeg = []byte("\xff\xd8\xff\xe0 fake jpeg bytes")

// split separates the record pages from the pictures a fetch published.
func split(envs []*envelope.Envelope) (pages, pictures []*envelope.Envelope) {
	for _, e := range envs {
		if strings.HasPrefix(e.ContentType, "image/") {
			pictures = append(pictures, e)
		} else {
			pages = append(pages, e)
		}
	}
	return pages, pictures
}

// The feature: the records go out as always — every field, so the CSV the
// converter makes is unchanged — and each picture follows as its own message,
// the image as the payload, the record's identity and a file name beside it.
func TestPictures_RecordsAndPictures(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	f := newFakeBC(t,
		`[{"id":"i1","number":"1896-S","displayName":"ATHENS Desk","unitPrice":1000.8},`+
			`{"id":"i2","number":"1900-S","displayName":"PARIS Chair","unitPrice":193.7}]`,
		map[string]*fakePicture{"i1": {id: "p1", contentType: "image/jpeg", content: jpeg}})
	c, got, mu := newTestConsumer()

	if err := fetch(t, c, picturesConfig(f, tokenSrv.URL)); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	pages, pics := split(*got)
	if len(pages) != 1 || recordsIn(t, pages) != 2 {
		t.Fatalf("record pages %d, want 1 page with both records", len(pages))
	}
	if !strings.Contains(string(pages[0].Payload), `"unitPrice":193.7`) {
		t.Errorf("records lost fields: %s", pages[0].Payload)
	}
	if len(pics) != 1 {
		t.Fatalf("published %d pictures, want 1 (i2 has none)", len(pics))
	}
	if (*got)[0] != pages[0] {
		t.Error("the picture was published before the records it belongs to")
	}
	env := pics[0]
	if env.ContentType != "image/jpeg" || !bytes.Equal(env.Payload, jpeg) || env.PayloadSize != int64(len(jpeg)) {
		t.Errorf("payload = %q (%s, %d bytes), want the picture bytes as image/jpeg", env.Payload, env.ContentType, env.PayloadSize)
	}
	want := map[string]interface{}{
		"entity": "items", "record_id": "i1", "number": "1896-S", "display_name": "ATHENS Desk",
		"picture_id": "p1", "width": 400, "height": 300, "filename": "1896-S.jpg",
	}
	for k, v := range want {
		if fmt.Sprint(env.Metadata[k]) != fmt.Sprint(v) {
			t.Errorf("metadata[%s] = %v, want %v", k, env.Metadata[k], v)
		}
	}
	if env.TenantID != "tenant-1" || env.IntegrationID != "conn-1" || env.Source != "business-central-consumer" {
		t.Errorf("routing fields = %q/%q/%q", env.TenantID, env.IntegrationID, env.Source)
	}
}

// Turning pictures on must not change a single byte of the records, nor the
// request that fetches them: the till's importer reads that CSV as it is.
func TestPictures_RecordsAreUnchanged(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	f := newFakeBC(t, `[{"id":"i1","number":"1896-S","displayName":"ATHENS Desk","unitPrice":1000.8}]`,
		map[string]*fakePicture{"i1": {id: "p1", contentType: "image/jpeg", content: jpeg}})

	run := func(pictures bool) (string, string) {
		c, got, mu := newTestConsumer()
		cfg := picturesConfig(f, tokenSrv.URL)
		cfg.Pictures = pictures
		if err := fetch(t, c, cfg); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		pages, _ := split(*got)
		if len(pages) != 1 {
			t.Fatalf("pictures=%v: %d record pages", pictures, len(pages))
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		return string(pages[0].Payload), f.pageQuery
	}
	offPayload, offQuery := run(false)
	onPayload, onQuery := run(true)
	if onPayload != offPayload {
		t.Errorf("records differ with pictures on:\n on: %s\noff: %s", onPayload, offPayload)
	}
	if onQuery != offQuery {
		t.Errorf("page request differs with pictures on: %q vs %q", onQuery, offQuery)
	}
}

func TestPictures_RequiresAnEntityWithPictures(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	f := newFakeBC(t, `[{"id":"s1"}]`, nil)
	c, got, _ := newTestConsumer()

	cfg := picturesConfig(f, tokenSrv.URL)
	cfg.Entity = "salesOrders"
	err := fetch(t, c, cfg)
	if err == nil || !strings.Contains(err.Error(), "items, customers, vendors, employees and contacts") {
		t.Fatalf("err = %v, want the list of entities that have pictures", err)
	}
	if len(*got) != 0 || len(f.paths) != 0 {
		t.Errorf("published %d / requested %v for an entity without pictures", len(*got), f.paths)
	}
	for _, e := range []string{"items", "customers", "vendors", "employees", "contacts"} {
		if err := (&BCConfig{Entity: e, Pictures: true}).validatePictures(); err != nil {
			t.Errorf("%s: %v", e, err)
		}
	}
}

// A running poller does not download an unchanged picture again; a replaced
// one (new picture id) is sent.
func TestPictures_UnchangedPictureIsNotResent(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	pic := &fakePicture{id: "p1", contentType: "image/png", content: []byte("png-1")}
	f := newFakeBC(t, `[{"id":"i1","number":"1896-S"}]`, map[string]*fakePicture{"i1": pic})
	c, got, mu := newTestConsumer()
	cfg := picturesConfig(f, tokenSrv.URL)

	for i := 0; i < 2; i++ {
		if err := fetch(t, c, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if _, pics := split(*got); len(pics) != 1 {
		t.Fatalf("two polls of an unchanged picture published %d pictures, want 1", len(pics))
	}
	if n := f.contentRequests(); n != 1 {
		t.Errorf("content downloaded %d times, want 1", n)
	}

	f.mu.Lock()
	pic.id, pic.content = "p2", []byte("png-2")
	f.mu.Unlock()
	if err := fetch(t, c, cfg); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, pics := split(*got); len(pics) != 2 || string(pics[1].Payload) != "png-2" {
		t.Errorf("replaced picture not sent: %d pictures", len(pics))
	}
}

// A failed download fails the fetch, so the watermark holds and the next poll
// retries — the same bargain a failed page makes.
func TestPictures_ContentFailureHoldsTheWatermark(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	f := newFakeBC(t, `[{"id":"i1","number":"1896-S","lastModifiedDateTime":"2026-09-28T10:00:00Z"}]`,
		map[string]*fakePicture{"i1": {id: "p1", contentType: "image/jpeg", contentStatus: http.StatusInternalServerError}})
	c, got, _ := newTestConsumer()
	cfg := picturesConfig(f, tokenSrv.URL)
	cfg.Incremental, cfg.NodeID = true, "n1"

	if err := fetch(t, c, cfg); err == nil {
		t.Fatal("a 500 on the picture content did not fail the fetch")
	}
	if _, pics := split(*got); len(pics) != 0 {
		t.Errorf("published %d pictures from a failed download", len(pics))
	}
	if cursor := c.loadCursor(context.Background(), "tenant-1", "conn-1", "n1", c.logger); cursor != "" {
		t.Errorf("watermark advanced to %q past a picture that was never delivered", cursor)
	}
}

// On-prem BC reports its media links with an internal hostname; the content
// must be fetched from the configured API host.
func TestPictures_MediaLinkHostIsReplacedByTheAPIHost(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	f := newFakeBC(t, `[{"id":"i1","number":"1896-S"}]`,
		map[string]*fakePicture{"i1": {id: "p1", contentType: "image/jpeg", content: jpeg, readLinkHost: "http://bcserver:7048"}})
	c, got, _ := newTestConsumer()

	if err := fetch(t, c, picturesConfig(f, tokenSrv.URL)); err != nil {
		t.Fatalf("fetch: %v (the content request went to the unreachable media-link host?)", err)
	}
	if _, pics := split(*got); len(pics) != 1 || f.contentRequests() != 1 {
		t.Errorf("pictures %d, content requests to the API host %d; want 1 and 1", len(pics), f.contentRequests())
	}
}

// Large pictures stream into the payload store when there is one; small ones
// are published inline.
func TestPictures_LargeContentGoesThroughTheClaimCheck(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	big := bytes.Repeat([]byte("x"), 2048)
	f := newFakeBC(t, `[{"id":"i1","number":"BIG"},{"id":"i2","number":"SMALL"}]`, map[string]*fakePicture{
		"i1": {id: "p1", contentType: "image/jpeg", content: big},
		"i2": {id: "p2", contentType: "image/jpeg", content: jpeg},
	})
	c, inline, mu := newTestConsumer()
	c.inlineMax = 1024
	var streamed []*envelope.Envelope
	var streamedBytes [][]byte
	c.publishStream = func(_ context.Context, env *envelope.Envelope, body io.Reader) error {
		b, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		streamed = append(streamed, env)
		streamedBytes = append(streamedBytes, b)
		return nil
	}

	if err := fetch(t, c, picturesConfig(f, tokenSrv.URL)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(streamed) != 1 || streamed[0].Metadata["number"] != "BIG" || !bytes.Equal(streamedBytes[0], big) {
		t.Errorf("the 2 KiB picture was not streamed whole (streamed %d)", len(streamed))
	}
	if _, pics := split(*inline); len(pics) != 1 || pics[0].Metadata["number"] != "SMALL" || !bytes.Equal(pics[0].Payload, jpeg) {
		t.Errorf("the small picture was not published inline (inline pictures %d)", len(pics))
	}
}

func TestPictures_FilenameIsSafe(t *testing.T) {
	for _, tc := range []struct {
		rec         pictureRecord
		contentType string
		want        string
	}{
		{pictureRecord{ID: "i1", Number: "1896-S"}, "image/jpeg", "1896-S.jpg"},
		{pictureRecord{ID: "i1", Number: `A/B\C:D`}, "image/png", "A_B_C_D.png"},
		{pictureRecord{ID: "i1", Number: "trailing. "}, "image/gif", "trailing.gif"},
		{pictureRecord{ID: "i1"}, "image/png", "i1.png"},
		{pictureRecord{ID: "i1", Number: "CON"}, "image/jpeg", "i1.jpg"}, // reserved on Windows
		{pictureRecord{ID: "i1", Number: "X"}, "image/jpeg; charset=binary", "X.jpg"},
		{pictureRecord{ID: "i1", Number: "X"}, "application/x-unknown-thing", "X.bin"},
	} {
		if got := pictureFilename(tc.rec, tc.contentType); got != tc.want {
			t.Errorf("pictureFilename(%+v, %q) = %q, want %q", tc.rec, tc.contentType, got, tc.want)
		}
	}
}

// The "show data structure" preview shows the records for a node with
// pictures on, as for any other: the records are what a converter maps.
func TestPictures_PreviewShowsTheRecords(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	f := newFakeBC(t, `[{"id":"i1","number":"1896-S","unitPrice":1000.8}]`, nil)
	c, _, _ := newTestConsumer()
	body := fmt.Sprintf(`{"client_id":"cid","client_secret":"sec","company_id":"C1","entity":"items","pictures":true,`+
		`"api_base_url":%q,"token_url":%q}`, f.srv.URL, tokenSrv.URL)
	rec := httptest.NewRecorder()
	c.handleSampleData()(rec, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body)))
	if !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), `"unitPrice":1000.8`) {
		t.Errorf("preview for a node with pictures on = %s", rec.Body.String())
	}
}

// --- Resend everything ---

// A resend makes the next poll send everything: records regardless of the
// incremental watermark, pictures regardless of what was already sent. The
// poll after that is incremental again.
func TestResend_NextPollSendsEverythingOnce(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	f := newFakeBC(t, `[{"id":"i1","number":"1896-S","lastModifiedDateTime":"2026-09-28T10:00:00Z"}]`,
		map[string]*fakePicture{"i1": {id: "p1", contentType: "image/jpeg", content: jpeg}})
	c, got, mu := newTestConsumer()
	cfg := picturesConfig(f, tokenSrv.URL)
	cfg.Incremental, cfg.NodeID = true, "n1"

	if err := fetch(t, c, cfg); err != nil { // first poll: everything, watermark saved
		t.Fatal(err)
	}
	if err := fetch(t, c, cfg); err != nil { // second: incremental, picture unchanged
		t.Fatal(err)
	}
	f.mu.Lock()
	q := f.pageQuery
	f.mu.Unlock()
	if !strings.Contains(q, "lastModifiedDateTime") {
		t.Fatalf("second poll was not incremental: %q", q)
	}
	mu.Lock()
	_, pics := split(*got)
	before := len(pics)
	mu.Unlock()
	if before != 1 {
		t.Fatalf("pictures before resend = %d, want 1", before)
	}

	cfg.resendAll = true
	if err := fetch(t, c, cfg); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	q = f.pageQuery
	f.mu.Unlock()
	if strings.Contains(q, "lastModifiedDateTime") {
		t.Errorf("the resend poll still carried the watermark: %q", q)
	}
	mu.Lock()
	_, pics = split(*got)
	after := len(pics)
	mu.Unlock()
	if after != 2 {
		t.Errorf("pictures after resend = %d, want 2 (the unchanged picture sent again)", after)
	}
	if cfg.resendAll {
		t.Error("resendAll still set after a complete fetch; every poll would resend")
	}

	if err := fetch(t, c, cfg); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	q = f.pageQuery
	f.mu.Unlock()
	if !strings.Contains(q, "lastModifiedDateTime") {
		t.Errorf("the poll after a resend was not incremental again: %q", q)
	}
}

// A resend that fails halfway is not done: the flag stays, so the next poll
// resends again rather than quietly going back to incremental.
func TestResend_FailedFetchKeepsTheFlag(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	f := newFakeBC(t, `[{"id":"i1","number":"1896-S"}]`,
		map[string]*fakePicture{"i1": {id: "p1", contentType: "image/jpeg", contentStatus: http.StatusInternalServerError}})
	c, _, _ := newTestConsumer()
	cfg := picturesConfig(f, tokenSrv.URL)
	cfg.resendAll = true
	if err := fetch(t, c, cfg); err == nil {
		t.Fatal("a 500 on the picture did not fail the fetch")
	}
	if !cfg.resendAll {
		t.Error("resendAll cleared by a failed fetch")
	}
}

// The command reaches the poller of the named connection only.
func TestResendCommand_ReachesTheNamedPollerOnly(t *testing.T) {
	c, _, _ := newTestConsumer()
	c.active = map[string]*poller{
		"conn-a": {cancel: func() {}, resend: make(chan struct{}, 1)},
		"conn-b": {cancel: func() {}, resend: make(chan struct{}, 1)},
	}
	c.handleResendCommand(&nats.Msg{Data: []byte(`{"connection_id":"conn-a","tenant_id":"t1"}`)})
	c.handleResendCommand(&nats.Msg{Data: []byte(`{"connection_id":"conn-a","tenant_id":"t1"}`)}) // a second one queues nothing extra
	c.handleResendCommand(&nats.Msg{Data: []byte(`{"connection_id":"unknown","tenant_id":"t1"}`)})
	select {
	case <-c.active["conn-a"].resend:
	default:
		t.Fatal("conn-a's poller was not asked to resend")
	}
	select {
	case <-c.active["conn-b"].resend:
		t.Fatal("conn-b's poller was asked to resend")
	default:
	}
}

// markers returns the .no-picture messages a fetch published.
func markers(envs []*envelope.Envelope) []*envelope.Envelope {
	var out []*envelope.Envelope
	for _, e := range envs {
		if e.ContentType == envelope.NoPictureContentType {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeBC) setPicture(id string, pic *fakePicture) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pic == nil {
		delete(f.pictures, id)
		return
	}
	f.pictures[id] = pic
}

func (f *fakeBC) setRecords(records string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = records
}

// TestPictures_RemovedPictureSendsOneMarker — Bifrost's request: when an item
// keeps existing but its picture is removed, exactly one empty
// <number>.no-picture goes out, on the poll that notices it and never again;
// a picture added back later arrives as a picture.
func TestPictures_RemovedPictureSendsOneMarker(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	f := newFakeBC(t, `[{"id":"i1","number":"HBB-1000","displayName":"Desk"}]`,
		map[string]*fakePicture{"i1": {id: "p1", contentType: "image/jpeg", content: jpeg}})
	c, got, mu := newTestConsumer()
	cfg := picturesConfig(f, tokenSrv.URL)

	for _, empty := range []bool{false, true} { // BC's two ways of saying "no picture"
		if err := fetch(t, c, cfg); err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if empty {
			f.setPicture("i1", &fakePicture{contentType: ""})
		} else {
			f.setPicture("i1", nil)
		}
		mu.Lock()
		*got = nil
		mu.Unlock()

		if err := fetch(t, c, cfg); err != nil {
			t.Fatalf("fetch after removal: %v", err)
		}
		mu.Lock()
		ms := markers(*got)
		_, pics := split(*got)
		mu.Unlock()
		if len(pics) != 0 || len(ms) != 1 {
			t.Fatalf("after removal: %d pictures, %d markers; want 0 and 1", len(pics), len(ms))
		}
		m := ms[0]
		if len(m.Payload) != 0 || m.PayloadSize != 0 {
			t.Errorf("marker must be empty, got %d bytes", len(m.Payload))
		}
		if m.Metadata["filename"] != "HBB-1000.no-picture" || m.Metadata["number"] != "HBB-1000" || m.Metadata["record_id"] != "i1" || m.Metadata["marker"] != "no-picture" {
			t.Errorf("marker metadata = %v", m.Metadata)
		}
		if !envelope.IsMedia(m.ContentType) {
			t.Error("the marker must travel like a picture (IsMedia), or the converter will try to parse it")
		}

		mu.Lock()
		*got = nil
		mu.Unlock()
		if err := fetch(t, c, cfg); err != nil {
			t.Fatalf("fetch again: %v", err)
		}
		mu.Lock()
		again := len(markers(*got))
		mu.Unlock()
		if again != 0 {
			t.Fatalf("the marker was sent again on the next poll (%d)", again)
		}

		// Picture back: sent as a picture, and the cycle can repeat.
		f.setPicture("i1", &fakePicture{id: "p2", contentType: "image/jpeg", content: jpeg})
		mu.Lock()
		*got = nil
		mu.Unlock()
		if err := fetch(t, c, cfg); err != nil {
			t.Fatalf("fetch with picture back: %v", err)
		}
		mu.Lock()
		_, pics = split(*got)
		ms = markers(*got)
		mu.Unlock()
		if len(pics) != 1 || len(ms) != 0 || pics[0].Metadata["filename"] != "HBB-1000.jpg" {
			t.Fatalf("picture back: %d pictures %d markers", len(pics), len(ms))
		}
	}
}

// An item with no picture now and none before produces no file, and the
// marker's base name is built exactly like the picture's.
func TestPictures_NeverHadAPictureSendsNoMarker(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	f := newFakeBC(t, `[{"id":"i1","number":"HBB-1000"},{"id":"i2","number":"NO PIC/1"}]`,
		map[string]*fakePicture{"i1": {id: "p1", contentType: "image/png", content: jpeg}})
	c, got, mu := newTestConsumer()
	cfg := picturesConfig(f, tokenSrv.URL)
	for i := 0; i < 3; i++ {
		if err := fetch(t, c, cfg); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if n := len(markers(*got)); n != 0 {
		t.Fatalf("%d markers for an item that never had a picture", n)
	}
	rec := pictureRecord{ID: "x", Number: "NO PIC/1"}
	if got, want := markerFilename(rec), strings.TrimSuffix(pictureFilename(rec, "image/png"), ".png")+".no-picture"; got != want {
		t.Errorf("marker name %q, picture name %q — bases differ", got, pictureFilename(rec, "image/png"))
	}
}

// TestPictures_SweepFindsChangesOnRecordsNotInTheFeed: an incremental poll
// only carries modified records. A picture removed or replaced on an
// untouched record is found by the sweep — one metadata request per
// remembered picture, content fetched only for the replaced one.
func TestPictures_SweepFindsChangesOnRecordsNotInTheFeed(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	f := newFakeBC(t,
		`[{"id":"i1","number":"A-1","lastModifiedDateTime":"2026-10-01T10:00:00Z"},`+
			`{"id":"i2","number":"A-2","lastModifiedDateTime":"2026-10-01T10:00:00Z"},`+
			`{"id":"i3","number":"A-3","lastModifiedDateTime":"2026-10-01T10:00:00Z"}]`,
		map[string]*fakePicture{
			"i1": {id: "p1", contentType: "image/jpeg", content: jpeg},
			"i2": {id: "p2", contentType: "image/jpeg", content: jpeg},
			"i3": {id: "p3", contentType: "image/jpeg", content: jpeg},
		})
	c, got, mu := newTestConsumer()
	cfg := picturesConfig(f, tokenSrv.URL)
	cfg.Incremental = true
	cfg.NodeID = "bc-node"
	if err := fetch(t, c, cfg); err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	// Nothing modified in BC: the incremental feed is empty. Meanwhile i1
	// lost its picture and i2 got a new one; i3 is unchanged.
	f.setRecords(`[]`)
	f.setPicture("i1", nil)
	f.setPicture("i2", &fakePicture{id: "p2b", contentType: "image/jpeg", content: jpeg})
	mu.Lock()
	*got = nil
	mu.Unlock()
	before := f.contentRequests()
	if err := fetch(t, c, cfg); err != nil {
		t.Fatalf("incremental fetch: %v", err)
	}
	mu.Lock()
	ms := markers(*got)
	_, pics := split(*got)
	mu.Unlock()
	if len(ms) != 1 || ms[0].Metadata["filename"] != "A-1.no-picture" {
		t.Fatalf("markers = %d (%v), want one for A-1", len(ms), ms)
	}
	if len(pics) != 1 || pics[0].Metadata["filename"] != "A-2.jpg" || pics[0].Metadata["picture_id"] != "p2b" {
		t.Fatalf("pictures = %d, want the replaced A-2.jpg", len(pics))
	}
	if n := f.contentRequests() - before; n != 1 {
		t.Errorf("content requests during the sweep = %d, want 1 (only the replaced picture is downloaded)", n)
	}

	// A third poll finds nothing to do.
	mu.Lock()
	*got = nil
	mu.Unlock()
	if err := fetch(t, c, cfg); err != nil {
		t.Fatalf("third fetch: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 0 {
		t.Errorf("third poll published %d messages, want 0", len(*got))
	}
}

// TestPictures_SeenPicturesSurviveRestart: the remembered pictures live in
// the node's checkpoint state, so a new consumer (restart, redeploy) on the
// same store neither re-sends an unchanged picture nor forgets a deletion.
func TestPictures_SeenPicturesSurviveRestart(t *testing.T) {
	tokenSrv := bcTestToken(t)
	defer tokenSrv.Close()
	f := newFakeBC(t, `[{"id":"i1","number":"HBB-1000"}]`,
		map[string]*fakePicture{"i1": {id: "p1", contentType: "image/jpeg", content: jpeg}})
	c1, _, _ := newTestConsumer()
	cfg := picturesConfig(f, tokenSrv.URL)
	cfg.NodeID = "bc-node"
	if err := fetch(t, c1, cfg); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	// "Restart": a fresh consumer and a fresh config on the same store.
	c2, got, mu := newTestConsumer()
	c2.checkpoints = c1.checkpoints
	cfg2 := picturesConfig(f, tokenSrv.URL)
	cfg2.NodeID = "bc-node"
	if err := fetch(t, c2, cfg2); err != nil {
		t.Fatalf("fetch after restart: %v", err)
	}
	mu.Lock()
	_, pics := split(*got)
	mu.Unlock()
	if len(pics) != 0 {
		t.Fatalf("an unchanged picture was re-sent after a restart (%d)", len(pics))
	}

	f.setPicture("i1", nil)
	mu.Lock()
	*got = nil
	mu.Unlock()
	if err := fetch(t, c2, cfg2); err != nil {
		t.Fatalf("fetch after removal: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if n := len(markers(*got)); n != 1 {
		t.Fatalf("markers after a removal following a restart = %d, want 1", n)
	}
}
