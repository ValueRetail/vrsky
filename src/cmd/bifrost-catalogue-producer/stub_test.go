package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// stubToken is the tenant credential the stub accepts.
const stubToken = "tok-bifrost-0123456789"

// fault makes the stub answer a call differently. drop closes the connection
// without an answer; dropAfter does the work first (a commit that landed whose
// answer was lost).
type fault struct {
	status    int
	body      string
	header    map[string]string
	drop      bool
	dropAfter bool
}

// bifrostStub is an httptest stand-in for Bifrost's catalogue-api that models
// what matters for the contract (crates/catalogue-api/src/routes.rs and
// sessions.rs): imports live in memory, nothing reaches the live catalogue
// before commit, commit takes the import out whatever its outcome, a full
// commit smaller than half the live catalogue is refused with 422 (the shrink
// guard), DELETE of an unknown import is 404, an Idempotency-Key repeated
// inside one import is a replay, and a commit whose sequence is not above the
// highest one the tenant has is refused with 409 (crates/catalogue/src/
// import.rs; an unnumbered import leaves the highest in place).
type bifrostStub struct {
	t     *testing.T
	srv   *httptest.Server
	token string // the credential the stub accepts

	mu      sync.Mutex
	live    map[string]json.RawMessage // article_no -> item as received
	imports map[string]*stubImport
	nextID  int
	calls   []string // "METHOD path", in arrival order
	begins  []stubBegin
	headers []http.Header

	// faults, keyed by call number of that kind (1 = first). A fault applies
	// once. begin/items/commit/abort are the four kinds.
	faults  map[string]fault
	counts  map[string]int
	newest  *uint64     // highest sequence committed
	onItems func(n int) // called with the batch number as it arrives
}

type stubBegin struct {
	Kind        string  `json:"kind"`
	AllowShrink string  `json:"allow_shrink"`
	Sequence    *uint64 `json:"sequence"`
	raw         string
}

type stubImport struct {
	kind    string
	seq     *uint64
	items   []json.RawMessage
	seen    map[string]bool
	batches int
}

func newBifrostStub(t *testing.T) *bifrostStub {
	t.Helper()
	s := &bifrostStub{
		t:       t,
		token:   stubToken,
		live:    map[string]json.RawMessage{},
		imports: map[string]*stubImport{},
		faults:  map[string]fault{},
		counts:  map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/catalogue/imports", s.handle("begin", s.begin))
	mux.HandleFunc("POST /v1/catalogue/imports/{id}/items", s.handle("items", s.items))
	mux.HandleFunc("POST /v1/catalogue/imports/{id}/commit", s.handle("commit", s.commit))
	mux.HandleFunc("DELETE /v1/catalogue/imports/{id}", s.handle("abort", s.abort))
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *bifrostStub) URL() string { return s.srv.URL }

// failOnce makes the n-th call of kind (begin, items, commit, abort) misbehave.
func (s *bifrostStub) failOnce(kind string, n int, f fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults[fmt.Sprintf("%s#%d", kind, n)] = f
}

func (s *bifrostStub) seed(items ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range items {
		s.live[articleNo(json.RawMessage(it))] = json.RawMessage(it)
	}
}

// seedN puts n generated articles in the live catalogue.
func (s *bifrostStub) seedN(n int) {
	for i := 0; i < n; i++ {
		s.seed(fmt.Sprintf(`{"article_no":"old-%d","name":"Old","price_ex_vat":"1.00","vat_code":"F"}`, i))
	}
}

func (s *bifrostStub) liveSnapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.live))
	for k, v := range s.live {
		out[k] = string(v)
	}
	return out
}

func (s *bifrostStub) openImports() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.imports)
}

func (s *bifrostStub) callLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// kinds returns the sequence of call kinds, e.g. "begin items items commit".
func (s *bifrostStub) kinds() string {
	var out []string
	for _, c := range s.callLog() {
		switch {
		case c == "POST /v1/catalogue/imports":
			out = append(out, "begin")
		case strings.HasSuffix(c, "/items"):
			out = append(out, "items")
		case strings.HasSuffix(c, "/commit"):
			out = append(out, "commit")
		case strings.HasPrefix(c, "DELETE "):
			out = append(out, "abort")
		}
	}
	return strings.Join(out, " ")
}

func (s *bifrostStub) beginBodies() []stubBegin {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubBegin(nil), s.begins...)
}

func (s *bifrostStub) requestHeaders() []http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]http.Header(nil), s.headers...)
}

func articleNo(raw json.RawMessage) string {
	var a struct {
		ArticleNo string `json:"article_no"`
	}
	_ = json.Unmarshal(raw, &a)
	return a.ArticleNo
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	b, _ := json.Marshal(map[string]string{"error": msg})
	writeJSON(w, status, string(b))
}

// handle wraps a call: records it, checks the credential the way
// catalogue-api does (401 bad key, 403 conflicting tenant header), and applies
// a queued fault.
func (s *bifrostStub) handle(kind string, h func(w http.ResponseWriter, r *http.Request, body []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.counts[kind]++
		n := s.counts[kind]
		s.calls = append(s.calls, r.Method+" "+r.URL.Path)
		s.headers = append(s.headers, r.Header.Clone())
		f, faulty := s.faults[fmt.Sprintf("%s#%d", kind, n)]
		s.mu.Unlock()

		if r.Header.Get("Authorization") != "Bearer "+s.token {
			apiError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if t := r.Header.Get("X-Tenant-Id"); t != "" && t != "nordic" {
			apiError(w, http.StatusForbidden, "forbidden")
			return
		}
		if kind == "items" && s.onItems != nil {
			s.onItems(n)
		}
		if faulty && f.drop {
			s.dropConn(w)
			return
		}
		if faulty && !f.dropAfter {
			for k, v := range f.header {
				w.Header().Set(k, v)
			}
			writeJSON(w, f.status, f.body)
			return
		}
		rec := httptest.NewRecorder()
		h(rec, r, body)
		if faulty && f.dropAfter {
			s.dropConn(w)
			return
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}
}

func (s *bifrostStub) dropConn(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		s.t.Error("stub cannot hijack")
		return
	}
	conn, _, err := hj.Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

func (s *bifrostStub) begin(w http.ResponseWriter, _ *http.Request, body []byte) {
	var b stubBegin
	if len(body) > 0 {
		_ = json.Unmarshal(body, &b)
	}
	if b.Kind != "full" && b.Kind != "delta" {
		apiError(w, http.StatusBadRequest, fmt.Sprintf("kind '%s' is not 'full' or 'delta'", b.Kind))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b.raw = string(body)
	s.begins = append(s.begins, b)
	s.nextID++
	id := fmt.Sprintf("imp_%04d", s.nextID)
	s.imports[id] = &stubImport{kind: b.Kind, seq: b.Sequence, seen: map[string]bool{}}
	// Remembered for the shrink override.
	if b.AllowShrink != "" {
		s.imports[id].seen["allow_shrink"] = true
	}
	writeJSON(w, http.StatusCreated, fmt.Sprintf(`{"import_id":%q,"kind":%q}`, id, b.Kind))
}

func (s *bifrostStub) items(w http.ResponseWriter, r *http.Request, body []byte) {
	var b struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		apiError(w, http.StatusUnprocessableEntity, "Failed to deserialize the JSON body")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	imp, ok := s.imports[r.PathValue("id")]
	if !ok {
		apiError(w, http.StatusNotFound, "no such import — it may have expired")
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		if imp.seen["key:"+key] {
			writeJSON(w, http.StatusOK, fmt.Sprintf(`{"received":0,"total_so_far":%d,"replayed":true}`, len(imp.items)))
			return
		}
		imp.seen["key:"+key] = true
	}
	imp.items = append(imp.items, b.Items...)
	imp.batches++
	writeJSON(w, http.StatusOK, fmt.Sprintf(`{"received":%d,"total_so_far":%d,"replayed":false}`, len(b.Items), len(imp.items)))
}

func (s *bifrostStub) commit(w http.ResponseWriter, r *http.Request, _ []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	imp, ok := s.imports[id]
	if !ok {
		apiError(w, http.StatusNotFound, "no such import — it may have expired")
		return
	}
	delete(s.imports, id) // taken whatever the outcome
	if imp.seq != nil && s.newest != nil && *imp.seq <= *s.newest {
		apiError(w, http.StatusConflict, fmt.Sprintf("import sequence %d is not newer than %d, which is already committed", *imp.seq, *s.newest))
		return
	}
	if imp.kind == "full" && !imp.seen["allow_shrink"] && len(s.live) > 0 && float64(len(imp.items)) < 0.5*float64(len(s.live)) {
		apiError(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("refusing a full import of %d articles over a live catalogue of %d: shrink guard", len(imp.items), len(s.live)))
		return
	}
	if imp.kind == "full" {
		s.live = map[string]json.RawMessage{}
	}
	for _, it := range imp.items {
		s.live[articleNo(it)] = it
	}
	if imp.seq != nil && (s.newest == nil || *imp.seq > *s.newest) {
		v := *imp.seq
		s.newest = &v
	}
	writeJSON(w, http.StatusOK, fmt.Sprintf(
		`{"import_id":%q,"version":"v%d","kind":%q,"applied":%d,"catalogue_size":%d,"rejected":0,"errors":[]}`,
		id, s.nextID, imp.kind, len(imp.items), len(s.live)))
}

func (s *bifrostStub) abort(w http.ResponseWriter, r *http.Request, _ []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if _, ok := s.imports[id]; !ok {
		apiError(w, http.StatusNotFound, "no such import — it may have expired")
		return
	}
	delete(s.imports, id)
	w.WriteHeader(http.StatusNoContent)
}
