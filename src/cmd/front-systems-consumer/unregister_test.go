package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #503: Front Systems unregisters its webhooks on stop, reuses
// registrations on start, and publishes inbound events only for connections
// this process started, for the tenant they were started for.
//
// The fake Front Systems below keeps registrations in memory per API key (as
// the real API scopes them to the account), assigns ids, records every call,
// and can be told to fail or hold DELETE. Waits are bounded (urWait); slow
// parts are held through the fake's handlers or the guard's hooks, never
// ordered with sleeps.

const (
	urTenantA = "tenant-fs-a"
	urTenantB = "tenant-fs-b"
	urWait    = 5 * time.Second
)

var urEvents = []string{"SaleCreated", "StockMovementCreated"}

// --- fake Front Systems ---

type fsHook struct {
	ID      string `json:"id"`
	Event   string `json:"event"`
	URL     string `json:"url"`
	StoreID int    `json:"storeId"`
	apiKey  string
}

// fsCall is one request seen by the fake. A DELETE also logs a "DELETE-done"
// entry once the fake has answered or abandoned it.
type fsCall struct {
	Method, Path, ID, SubKey, APIKey string
}

type fakeFS struct {
	srv *httptest.Server

	mu           sync.Mutex
	hooks        []fsHook
	created      []string // ids of every successful create, in order
	seq          int
	log          []fsCall
	lists        int
	posts        int
	deleteStatus int    // non-zero: answer every DELETE with this status
	deleteHang   bool   // hold every DELETE until its request context ends or release is called
	listStatus   int    // non-zero: answer every list with this status
	listHang     bool   // hold every list until its request context ends or release is called
	postNoID     bool   // answer a create with an empty body
	postFail     string // answer the create for this event 500
	postErrLeft  int    // answer the next postErrLeft creates with postErr (retry_test.go)
	postErr      int    // their status; 0 drops the connection without an answer
	onPost       func(n int, event string)
	onList       func(n int)
	unhang       chan struct{}
	released     bool

	deletesSeen chan string
	listsSeen   chan struct{}
}

func newFakeFS(t *testing.T) *fakeFS {
	t.Helper()
	f := &fakeFS{unhang: make(chan struct{}), deletesSeen: make(chan string, 64), listsSeen: make(chan struct{}, 64)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() {
		f.release()
		f.srv.Close()
	})
	return f
}

// release lets held DELETEs go; they are then answered 504.
func (f *fakeFS) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.released {
		f.released = true
		close(f.unhang)
	}
}

func (f *fakeFS) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.ToLower(strings.TrimRight(r.URL.Path, "/"))
	call := fsCall{
		Method: r.Method, Path: r.URL.Path,
		SubKey: r.Header.Get("Ocp-Apim-Subscription-Key"), APIKey: r.Header.Get("x-api-key"),
	}
	switch {
	case r.Method == http.MethodGet && path == "/api/webhooks":
		f.mu.Lock()
		f.log = append(f.log, call)
		f.lists++
		n, hook, status, hang, unhang := f.lists, f.onList, f.listStatus, f.listHang, f.unhang
		f.mu.Unlock()
		select {
		case f.listsSeen <- struct{}{}:
		default:
		}
		if hook != nil {
			hook(n)
		}
		if hang {
			select {
			case <-r.Context().Done():
				f.mu.Lock()
				f.log = append(f.log, fsCall{Method: "GET-cancelled"})
				f.mu.Unlock()
				return
			case <-unhang:
				http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
				return
			}
		}
		if status != 0 {
			http.Error(w, `{"error":"list failed"}`, status)
			return
		}
		f.mu.Lock()
		out := []fsHook{}
		for _, h := range f.hooks {
			if h.apiKey == call.APIKey {
				out = append(out, h)
			}
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(out)

	case r.Method == http.MethodPost && path == "/api/webhooks":
		var body struct{ Event, URL string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Event == "" || body.URL == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.log = append(f.log, call)
		f.posts++
		n, hook, failEvent := f.posts, f.onPost, f.postFail
		errStatus, failThis := f.postErr, f.postErrLeft > 0
		if failThis {
			f.postErrLeft--
		}
		f.mu.Unlock()
		if hook != nil {
			hook(n, body.Event)
		}
		if failThis {
			if errStatus == 0 {
				if hj, ok := w.(http.Hijacker); ok {
					if conn, _, err := hj.Hijack(); err == nil {
						_ = conn.Close()
						return
					}
				}
				errStatus = http.StatusBadGateway
			}
			http.Error(w, `{"error":"create failed","detail":"body-must-not-reach-last_error"}`, errStatus)
			return
		}
		if failEvent != "" && body.Event == failEvent {
			http.Error(w, `{"error":"create failed"}`, http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.seq++
		h := fsHook{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", f.seq), Event: body.Event, URL: body.URL, StoreID: 7, apiKey: call.APIKey}
		f.hooks = append(f.hooks, h)
		f.created = append(f.created, h.ID)
		noID := f.postNoID
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if !noID {
			_ = json.NewEncoder(w).Encode(h)
		}

	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/api/webhooks/"):
		call.ID = r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		f.mu.Lock()
		f.log = append(f.log, call)
		status, hang, unhang := f.deleteStatus, f.deleteHang, f.unhang
		f.mu.Unlock()
		defer func() {
			f.mu.Lock()
			f.log = append(f.log, fsCall{Method: "DELETE-done", ID: call.ID})
			f.mu.Unlock()
		}()
		select {
		case f.deletesSeen <- call.ID:
		default:
		}
		if hang {
			select {
			case <-r.Context().Done():
				return
			case <-unhang:
				http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
				return
			}
		}
		if status != 0 {
			http.Error(w, `{"error":"delete failed"}`, status)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for i, h := range f.hooks {
			if h.ID == call.ID && h.apiKey == call.APIKey {
				f.hooks = append(f.hooks[:i], f.hooks[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.NotFound(w, r)

	default:
		http.NotFound(w, r)
	}
}

func (f *fakeFS) set(fn func(f *fakeFS)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// seed adds a registration that exists before the test's start.
func (f *fakeFS) seed(apiKey, event, url string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	h := fsHook{ID: fmt.Sprintf("00000000-0000-4000-9000-%012d", f.seq), Event: event, URL: url, apiKey: apiKey}
	f.hooks = append(f.hooks, h)
	return h.ID
}

// registered returns the registrations under apiKey.
func (f *fakeFS) registered(apiKey string) []fsHook {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fsHook
	for _, h := range f.hooks {
		if h.apiKey == apiKey {
			out = append(out, h)
		}
	}
	return out
}

func (f *fakeFS) registeredIDs(apiKey string) []string {
	var ids []string
	for _, h := range f.registered(apiKey) {
		ids = append(ids, h.ID)
	}
	sort.Strings(ids)
	return ids
}

func (f *fakeFS) calls(method string) []fsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fsCall
	for _, c := range f.log {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeFS) logCopy() []fsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fsCall(nil), f.log...)
}

// awaitDelete waits for the fake to see a DELETE.
func (f *fakeFS) awaitDelete(t *testing.T) string {
	t.Helper()
	select {
	case id := <-f.deletesSeen:
		return id
	case <-time.After(urWait):
		t.Fatal("timed out waiting for a DELETE at Front Systems")
		return ""
	}
}

// drainLists forgets the lists seen so far.
func (f *fakeFS) drainLists() {
	for {
		select {
		case <-f.listsSeen:
		default:
			return
		}
	}
}

// noListWhileHeld checks that no list arrives within a short window. It is
// called while a stop's DELETE is held and a start has claimed the
// connection: that start must wait for the delete, not look the webhooks up
// (AC4). A start that doesn't wait lists within microseconds, so the window
// is generous; it only bounds the test when the code is right.
func (f *fakeFS) noListWhileHeld(t *testing.T) {
	t.Helper()
	select {
	case <-f.listsSeen:
		t.Error("a start listed the webhooks while the stop's DELETE was still running")
	case <-time.After(200 * time.Millisecond):
	}
}

// --- connection config ---

type urConn struct {
	id, tenant string
	events     []string // configured events; urEvents when nil
}

func (u urConn) evs() []string {
	if u.events != nil {
		return u.events
	}
	return urEvents
}

func (u urConn) subKey() string   { return "sub-" + u.id }
func (u urConn) apiKey() string   { return "api-" + u.id }
func (u urConn) callback() string { return "https://vrsky.example/frontsystems/events/" + u.id }

func (u urConn) nodes(baseURL string) []byte {
	return mustJSON([]any{map[string]any{"id": "n1", "type": "consumer", "config": map[string]any{
		"type": "front_systems",
		"front_systems": map[string]any{
			"base_url":         baseURL,
			"subscription_key": u.subKey(),
			"api_key":          u.apiKey(),
			"events":           u.evs(),
			"callback_url":     u.callback(),
		},
	}}})
}

// configLookupSQL is getConfig's lookup; configRows answers it with nodes and
// the row version cfgVersion.
const configLookupSQL = `SELECT nodes, updated_at FROM connections WHERE id = \$1 AND tenant_id = \$2`

var cfgVersion = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

func configRows(nodes []byte) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"nodes", "updated_at"}).AddRow(nodes, cfgVersion)
}

func expectConfig(mock sqlmock.Sqlmock, fs *fakeFS, u urConn, n int) {
	for i := 0; i < n; i++ {
		mock.ExpectQuery(configLookupSQL).
			WithArgs(u.id, u.tenant).
			WillReturnRows(configRows(u.nodes(fs.srv.URL)))
	}
}

func cmdMsg(kind, connID, tenantID string) *nats.Msg {
	return &nats.Msg{
		Subject: "vrsky.commands." + tenantID + ".connection." + kind,
		Data:    mustJSON(map[string]string{"connection_id": connID, "tenant_id": tenantID}),
	}
}

// owners is the stub behind resolveTenant: the tenant each connection row
// belongs to.
type owners struct {
	mu sync.Mutex
	m  map[string]string
}

func (o *owners) set(conn, tenant string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[conn] = tenant
}

func (o *owners) resolve(conn string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if t, ok := o.m[conn]; ok {
		return t, nil
	}
	return "", errUnknown
}

func postEvent(c *frontSystemsConsumer, connID, body string) int {
	rec := httptest.NewRecorder()
	c.handleWebhook()(rec, httptest.NewRequest(http.MethodPost, "/frontsystems/events/"+connID, strings.NewReader(body)))
	return rec.Code
}

func activeTenant(c *frontSystemsConsumer, connID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ac, ok := c.active[connID]
	if !ok {
		return "", false
	}
	return ac.tenantID, true
}

func sameIDs(a, b []string) bool {
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, ",") == strings.Join(b, ",")
}

// --- direct fixture (handlers called on the test goroutine) ---

type urFixture struct {
	fs     *fakeFS
	c      *frontSystemsConsumer
	mock   sqlmock.Sqlmock
	owners *owners

	pubMu     sync.Mutex
	published []*envelope.Envelope
}

func newURFixture(t *testing.T) *urFixture {
	t.Helper()
	fs := newFakeFS(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	mock.MatchExpectationsInOrder(false)
	t.Cleanup(func() { _ = db.Close() })
	f := &urFixture{fs: fs, mock: mock, owners: &owners{m: map[string]string{}}}
	f.c = &frontSystemsConsumer{
		db:            db,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient:    fs.srv.Client(),
		resolveTenant: f.owners.resolve,
		publish: func(_ context.Context, env *envelope.Envelope) error {
			f.pubMu.Lock()
			defer f.pubMu.Unlock()
			f.published = append(f.published, env)
			return nil
		},
	}
	// Stop the consumer at the end so no unregister goroutine outlives the test.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = f.c.Stop(ctx)
	})
	return f
}

// conn sets up a connection whose row belongs to tenant, with n config lookups.
func (f *urFixture) conn(id, tenant string, lookups int) urConn {
	u := urConn{id: id, tenant: tenant}
	f.owners.set(id, tenant)
	expectConfig(f.mock, f.fs, u, lookups)
	return u
}

func (f *urFixture) start(u urConn) { f.c.startConnection(context.Background(), u.id, u.tenant) }

func (f *urFixture) stop(u urConn) { f.c.handleStopCommand(cmdMsg("stop", u.id, u.tenant)) }

func (f *urFixture) publishedFor(connID string) int {
	f.pubMu.Lock()
	defer f.pubMu.Unlock()
	n := 0
	for _, env := range f.published {
		if env.IntegrationID == connID {
			n++
		}
	}
	return n
}

// mustStart starts u and checks it ended active with one registration per
// event at Front Systems.
func (f *urFixture) mustStart(t *testing.T, u urConn) {
	t.Helper()
	fgAwait(t, goDone(func() { f.start(u) }), "start")
	if _, ok := activeTenant(f.c, u.id); !ok {
		t.Fatalf("%s not active after its start", u.id)
	}
	wantOnePerEvent(t, f.fs, u, "after start")
}

func wantOnePerEvent(t *testing.T, fs *fakeFS, u urConn, when string) {
	t.Helper()
	got := map[string]int{}
	for _, h := range fs.registered(u.apiKey()) {
		if h.URL == u.callback() {
			got[h.Event]++
		}
	}
	for _, ev := range u.evs() {
		if got[ev] != 1 {
			t.Errorf("%s: %s registered %d time(s) for %s, want 1 (all: %v)", when, ev, got[ev], u.id, got)
		}
	}
}

// wantDeletes checks the DELETEs sent with u's keys are exactly ids, each
// once, with both auth headers.
func wantDeletes(t *testing.T, fs *fakeFS, u urConn, ids []string) {
	t.Helper()
	var got []string
	for _, c := range fs.calls(http.MethodDelete) {
		if c.APIKey != u.apiKey() {
			continue
		}
		if c.SubKey != u.subKey() {
			t.Errorf("DELETE %s: Ocp-Apim-Subscription-Key = %q, want %q", c.ID, c.SubKey, u.subKey())
		}
		got = append(got, c.ID)
	}
	if !sameIDs(got, ids) {
		t.Errorf("DELETEs for %s = %v, want %v", u.id, got, ids)
	}
}

// --- tests ---

// AC1, AC6 through the real runner and NATS: boot restore starts two
// connections, an event for one is published, a stop command on the bus
// deletes exactly that connection's webhooks with its own keys, its events
// are refused afterwards and nothing is published for them, a start command
// brings it back, and shutting the consumer down deletes nothing (AC8).
func TestFrontSystemsUnregister_HarnessStopDeletesAndRefuses(t *testing.T) {
	fs := newFakeFS(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	mock.MatchExpectationsInOrder(false)
	t.Cleanup(func() { _ = db.Close() })

	x := urConn{id: "conn-fs-x", tenant: urTenantA}
	s := urConn{id: "conn-fs-sentinel", tenant: urTenantA}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id::text, tenant_id FROM connections`)).
		WithArgs("front_systems").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id"}).AddRow(x.id, x.tenant).AddRow(s.id, s.tenant))
	expectConfig(mock, fs, x, 2) // restore, the start command later
	expectConfig(mock, fs, s, 1) // restore

	own := &owners{m: map[string]string{x.id: x.tenant, s.id: s.tenant}}
	c := &frontSystemsConsumer{httpClient: fs.srv.Client(), resolveTenant: own.resolve}

	// Registered before the harness so it runs after the harness has shut the
	// consumer down (cleanups run last in, first out).
	var deletesBeforeShutdown atomic.Int64
	deletesBeforeShutdown.Store(-1)
	t.Cleanup(func() {
		n := deletesBeforeShutdown.Load()
		if n < 0 {
			return
		}
		if got := int64(len(fs.calls(http.MethodDelete))); got != n {
			t.Errorf("shutdown sent %d DELETE(s) at Front Systems, want none", got-n)
		}
		wantOnePerEvent(t, fs, x, "after shutdown")
		wantOnePerEvent(t, fs, s, "after shutdown")
	})

	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "front-systems-consumer", DB: db})

	var dataMu sync.Mutex
	var data []*envelope.Envelope
	sub, err := h.NATS().Subscribe("vrsky.data.>", func(m *nats.Msg) {
		if env, err := envelope.Unmarshal(m.Data); err == nil {
			dataMu.Lock()
			data = append(data, env)
			dataMu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("subscribe data: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := h.NATS().Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	countFor := func(connID string) int {
		dataMu.Lock()
		defer dataMu.Unlock()
		n := 0
		for _, env := range data {
			if env.IntegrationID == connID {
				n++
			}
		}
		return n
	}

	harness.Eventually(t, urWait, "boot restore to start both connections", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, okX := c.active[x.id]
		_, okS := c.active[s.id]
		return okX && okS && !c.restoring
	})
	wantOnePerEvent(t, fs, x, "after restore")
	wantOnePerEvent(t, fs, s, "after restore")
	xIDs := fs.registeredIDs(x.apiKey())
	sIDs := fs.registeredIDs(s.apiKey())

	// AC6: an event for a started connection is published.
	if code := postEvent(c, x.id, `{"event":"SaleCreated","saleId":1}`); code != http.StatusAccepted {
		t.Fatalf("event for a running connection: status %d, want 202", code)
	}
	env := h.ExpectEnvelope(t, func(e *envelope.Envelope) bool { return e.IntegrationID == x.id }, urWait)
	if env.TenantID != x.tenant {
		t.Errorf("published tenant = %q, want %q", env.TenantID, x.tenant)
	}
	if env.Metadata["event_type"] != "SaleCreated" {
		t.Errorf("event_type = %v, want SaleCreated", env.Metadata["event_type"])
	}

	// AC1: the stop command on the bus deletes x's webhooks, and only x's.
	if err := h.NATS().Publish("vrsky.commands."+x.tenant+".connection.stop",
		mustJSON(map[string]string{"connection_id": x.id, "tenant_id": x.tenant})); err != nil {
		t.Fatalf("publish stop: %v", err)
	}
	harness.Eventually(t, urWait, "x's webhooks to be deleted at Front Systems", func() bool {
		return len(fs.registered(x.apiKey())) == 0 && len(fs.calls("DELETE-done")) == len(xIDs)
	})
	if _, ok := activeTenant(c, x.id); ok {
		t.Error("x still active after its stop")
	}
	wantDeletes(t, fs, x, xIDs)
	wantDeletes(t, fs, s, nil)
	if got := fs.registeredIDs(s.apiKey()); !sameIDs(got, sIDs) {
		t.Errorf("the other connection's webhooks = %v, want %v untouched", got, sIDs)
	}

	// AC6: an event for the stopped connection is refused and not published.
	// The sentinel's event goes through the same connection afterwards; once
	// it has arrived, anything published for x before it would have too.
	before := countFor(x.id)
	if code := postEvent(c, x.id, `{"event":"SaleCreated","saleId":2}`); code != http.StatusGone {
		t.Errorf("event for a stopped connection: status %d, want 410", code)
	}
	if code := postEvent(c, s.id, `{"event":"SaleCreated","saleId":3}`); code != http.StatusAccepted {
		t.Fatalf("sentinel event: status %d, want 202", code)
	}
	harness.Eventually(t, urWait, "the sentinel's envelope", func() bool { return countFor(s.id) == 1 })
	if got := countFor(x.id); got != before {
		t.Errorf("envelopes for the stopped connection: %d, want %d (nothing new)", got, before)
	}

	// A start command brings x back with fresh registrations, and its events
	// are published again.
	if err := h.NATS().Publish("vrsky.commands."+x.tenant+".connection.start",
		mustJSON(map[string]string{"connection_id": x.id, "tenant_id": x.tenant})); err != nil {
		t.Fatalf("publish start: %v", err)
	}
	harness.Eventually(t, urWait, "x to be started again", func() bool {
		_, ok := activeTenant(c, x.id)
		return ok
	})
	wantOnePerEvent(t, fs, x, "after the start command")
	if code := postEvent(c, x.id, `{"event":"SaleCreated","saleId":4}`); code != http.StatusAccepted {
		t.Fatalf("event after the restart: status %d, want 202", code)
	}
	h.ExpectEnvelope(t, func(e *envelope.Envelope) bool { return e.IntegrationID == x.id && e.TenantID == x.tenant }, urWait)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}

	deletesBeforeShutdown.Store(int64(len(fs.calls(http.MethodDelete))))
}

// AC1: a stop deletes every id the start registered, whether the create
// response carried the id or it had to be read back from the list, with both
// auth headers, and Front Systems ends with nothing registered.
func TestFrontSystemsUnregister_StopDeletesEveryRegisteredID(t *testing.T) {
	for _, tc := range []struct {
		name     string
		postNoID bool
	}{
		{"ids from the create response", false},
		{"ids read back from the list", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newURFixture(t)
			f.fs.set(func(fs *fakeFS) { fs.postNoID = tc.postNoID })
			u := f.conn("conn-fs-1", urTenantA, 1)

			f.mustStart(t, u)
			ids := f.fs.registeredIDs(u.apiKey())

			fgAwait(t, goDone(func() { f.stop(u) }), "stop handler")
			harness.Eventually(t, urWait, "every webhook deleted", func() bool {
				return len(f.fs.registered(u.apiKey())) == 0
			})
			wantDeletes(t, f.fs, u, ids)
			if _, ok := activeTenant(f.c, u.id); ok {
				t.Error("connection still active after stop")
			}
		})
	}
}

// AC2, AC4: an unregister that fails or hangs does not hold up the stop: the
// handler returns, the connection is stopped and refuses events at once, and
// a later start works. That start does not look the webhooks up until the
// failed deletes are over, and reuses the registrations they left behind.
func TestFrontSystemsUnregister_FailureDoesNotBlockStop(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(fs *fakeFS)
		held  bool // the DELETE is still running when the later start claims
	}{
		{"delete answers 500", func(fs *fakeFS) { fs.deleteStatus = http.StatusInternalServerError }, false},
		{"delete hangs", func(fs *fakeFS) { fs.deleteHang = true }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newURFixture(t)
			u := f.conn("conn-fs-fail", urTenantA, 2)
			f.mustStart(t, u)
			ids := f.fs.registeredIDs(u.apiKey())
			f.fs.set(tc.setup)

			fgAwait(t, goDone(func() { f.stop(u) }), "stop handler to return while the unregister fails")
			f.fs.awaitDelete(t)
			if _, ok := activeTenant(f.c, u.id); ok {
				t.Fatal("connection still active after a stop whose unregister fails")
			}
			if code := postEvent(f.c, u.id, `{"event":"SaleCreated"}`); code != http.StatusGone {
				t.Errorf("event after the stop: status %d, want 410", code)
			}
			if n := f.publishedFor(u.id); n != 0 {
				t.Errorf("published %d envelope(s) for a stopped connection, want 0", n)
			}

			// A later start, while the deletes may still be running.
			reserved := make(chan struct{})
			var once sync.Once
			f.c.starts.AfterReserve = func(id string) {
				if id == u.id {
					once.Do(func() { close(reserved) })
				}
			}
			listsBefore := len(f.fs.calls(http.MethodGet))
			f.fs.drainLists()
			started := goDone(func() { f.start(u) })
			fgAwait(t, reserved, "later start to claim the connection")
			if tc.held {
				f.fs.noListWhileHeld(t)
			}
			f.fs.release() // a held DELETE is now answered 504
			fgAwait(t, started, "later start")

			if _, ok := activeTenant(f.c, u.id); !ok {
				t.Fatal("later start did not activate the connection")
			}
			// AC4: its lookup came after every delete the stop sent was over.
			log := f.fs.logCopy()
			lastDeleteDone, firstLaterList := -1, -1
			lists := 0
			for i, c := range log {
				switch c.Method {
				case "DELETE-done":
					lastDeleteDone = i
				case http.MethodGet:
					lists++
					if lists == listsBefore+1 && firstLaterList < 0 {
						firstLaterList = i
					}
				}
			}
			if n := len(f.fs.calls("DELETE-done")); n != len(ids) {
				t.Errorf("deletes finished = %d, want %d", n, len(ids))
			}
			if firstLaterList < 0 || firstLaterList < lastDeleteDone {
				t.Errorf("the later start listed webhooks (log #%d) before the stop's deletes were over (log #%d): %+v",
					firstLaterList, lastDeleteDone, log)
			}
			// The failed deletes left the registrations; the start reuses them.
			if n := len(f.fs.calls(http.MethodPost)); n != len(urEvents) {
				t.Errorf("registration POSTs = %d, want %d (the later start reuses)", n, len(urEvents))
			}
			wantOnePerEvent(t, f.fs, u, "after the later start")
			if !sameIDs(f.fs.registeredIDs(u.apiKey()), ids) {
				t.Errorf("registrations = %v, want the original %v", f.fs.registeredIDs(u.apiKey()), ids)
			}
			if code := postEvent(f.c, u.id, `{"event":"SaleCreated"}`); code != http.StatusAccepted {
				t.Errorf("event after the later start: status %d, want 202", code)
			}
			if n := f.publishedFor(u.id); n != 1 {
				t.Errorf("published %d envelope(s), want 1", n)
			}
		})
	}
}

// AC6: what the inbound route does with an event in each state, and that it
// publishes only in the one it accepts.
func TestFrontSystemsRefuse_EventsOnlyForActiveConnection(t *testing.T) {
	const conn = "conn-fs-route"
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *urFixture, u urConn)
		want  int
	}{
		{"unknown connection", func(t *testing.T, f *urFixture, u urConn) {
			f.owners.set(u.id+"-other", u.tenant) // only some other row exists
		}, http.StatusNotFound},
		{"known, never started", func(t *testing.T, f *urFixture, u urConn) {
			f.owners.set(u.id, u.tenant)
		}, http.StatusGone},
		{"started", func(t *testing.T, f *urFixture, u urConn) {
			expectConfig(f.mock, f.fs, u, 1)
			f.owners.set(u.id, u.tenant)
			f.mustStart(t, u)
		}, http.StatusAccepted},
		{"stopped", func(t *testing.T, f *urFixture, u urConn) {
			expectConfig(f.mock, f.fs, u, 1)
			f.owners.set(u.id, u.tenant)
			f.mustStart(t, u)
			f.stop(u)
		}, http.StatusGone},
		{"started, row now owned by another tenant", func(t *testing.T, f *urFixture, u urConn) {
			expectConfig(f.mock, f.fs, u, 1)
			f.owners.set(u.id, u.tenant)
			f.mustStart(t, u)
			f.owners.set(u.id, urTenantB)
		}, http.StatusGone},
		{"boot restore running, not restored yet", func(t *testing.T, f *urFixture, u urConn) {
			f.owners.set(u.id, u.tenant)
			f.c.mu.Lock()
			f.c.restoring = true
			f.c.mu.Unlock()
		}, http.StatusServiceUnavailable},
		{"shutting down", func(t *testing.T, f *urFixture, u urConn) {
			expectConfig(f.mock, f.fs, u, 1)
			f.owners.set(u.id, u.tenant)
			f.mustStart(t, u)
			_ = f.c.Stop(context.Background())
		}, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newURFixture(t)
			u := urConn{id: conn, tenant: urTenantA}
			tc.setup(t, f, u)

			if code := postEvent(f.c, u.id, `{"event":"SaleCreated","saleId":9}`); code != tc.want {
				t.Fatalf("status %d, want %d", code, tc.want)
			}
			wantPub := 0
			if tc.want == http.StatusAccepted {
				wantPub = 1
			}
			f.pubMu.Lock()
			defer f.pubMu.Unlock()
			if len(f.published) != wantPub {
				t.Fatalf("published %d envelope(s), want %d", len(f.published), wantPub)
			}
			if wantPub == 1 {
				env := f.published[0]
				if env.TenantID != u.tenant || env.IntegrationID != u.id {
					t.Errorf("envelope tenant/integration = %q/%q, want %q/%q", env.TenantID, env.IntegrationID, u.tenant, u.id)
				}
			}
		})
	}
}

// AC6, AC7: another tenant can neither push events through a connection nor
// stop it. A stop naming tenant B leaves tenant A's connection running and
// deletes nothing; A's own stop then deletes each webhook once.
func TestFrontSystemsTenant_OtherTenantCannotUseOrStop(t *testing.T) {
	f := newURFixture(t)
	u := f.conn("conn-fs-tenant", urTenantA, 1)
	f.mustStart(t, u)
	ids := f.fs.registeredIDs(u.apiKey())

	// The row says tenant B owns it: refused, nothing published.
	f.owners.set(u.id, urTenantB)
	if code := postEvent(f.c, u.id, `{"event":"SaleCreated"}`); code != http.StatusGone {
		t.Errorf("event resolved to another tenant: status %d, want 410", code)
	}
	if n := f.publishedFor(u.id); n != 0 {
		t.Errorf("published %d envelope(s) for another tenant, want 0", n)
	}
	f.owners.set(u.id, urTenantA)

	// A stop naming tenant B.
	f.c.handleStopCommand(cmdMsg("stop", u.id, urTenantB))
	if tenant, ok := activeTenant(f.c, u.id); !ok || tenant != urTenantA {
		t.Fatalf("after another tenant's stop: active=%v tenant=%q, want still active for %q", ok, tenant, urTenantA)
	}
	if code := postEvent(f.c, u.id, `{"event":"SaleCreated"}`); code != http.StatusAccepted {
		t.Errorf("event after another tenant's stop: status %d, want 202", code)
	}

	// A's own stop: if B's stop had queued deletes too, each id would be
	// deleted twice.
	f.stop(u)
	harness.Eventually(t, urWait, "A's stop to delete the webhooks", func() bool {
		return len(f.fs.calls("DELETE-done")) >= len(ids) && len(f.fs.registered(u.apiKey())) == 0
	})
	// Shut down so no unregister can still be on its way, then count.
	_ = f.c.Stop(context.Background())
	wantDeletes(t, f.fs, u, ids)
}

// AC3: a start reuses registrations that already match its event and
// callback URL instead of posting duplicates, posts the rest, leaves other
// callbacks alone, and falls back to posting when the list fails. The stop
// then deletes exactly what the start recorded.
func TestFrontSystemsReuse_StartReusesExistingRegistrations(t *testing.T) {
	u := urConn{id: "conn-fs-reuse", tenant: urTenantA}
	other := "https://vrsky.example/frontsystems/events/conn-fs-someone-else"
	for _, tc := range []struct {
		name      string
		seed      func(fs *fakeFS) (reused, untouched []string)
		listFails bool
		wantPosts int
	}{
		{"both events registered already", func(fs *fakeFS) ([]string, []string) {
			return []string{fs.seed(u.apiKey(), "SaleCreated", u.callback()), fs.seed(u.apiKey(), "StockMovementCreated", u.callback())}, nil
		}, false, 0},
		{"one event registered already", func(fs *fakeFS) ([]string, []string) {
			return []string{fs.seed(u.apiKey(), "SaleCreated", u.callback())}, nil
		}, false, 1},
		{"same event for another callback", func(fs *fakeFS) ([]string, []string) {
			return nil, []string{fs.seed(u.apiKey(), "SaleCreated", other)}
		}, false, 2},
		{"list fails", func(fs *fakeFS) ([]string, []string) { return nil, nil }, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newURFixture(t)
			f.conn(u.id, u.tenant, 1)
			reused, untouched := tc.seed(f.fs)
			if tc.listFails {
				f.fs.set(func(fs *fakeFS) { fs.listStatus = http.StatusInternalServerError })
			}

			fgAwait(t, goDone(func() { f.start(u) }), "start")
			if _, ok := activeTenant(f.c, u.id); !ok {
				t.Fatal("connection not active after start")
			}
			if n := len(f.fs.calls(http.MethodPost)); n != tc.wantPosts {
				t.Errorf("registration POSTs = %d, want %d", n, tc.wantPosts)
			}
			wantOnePerEvent(t, f.fs, u, "after start")
			var mine []string
			for _, h := range f.fs.registered(u.apiKey()) {
				if h.URL == u.callback() {
					mine = append(mine, h.ID)
				}
			}
			for _, id := range reused {
				found := false
				for _, m := range mine {
					found = found || m == id
				}
				if !found {
					t.Errorf("existing registration %s was not reused (registered: %v)", id, mine)
				}
			}

			f.stop(u)
			harness.Eventually(t, urWait, "the stop's deletes", func() bool {
				return len(f.fs.calls("DELETE-done")) == len(mine)
			})
			wantDeletes(t, f.fs, u, mine)
			if got := f.fs.registeredIDs(u.apiKey()); !sameIDs(got, untouched) {
				t.Errorf("left at Front Systems: %v, want %v", got, untouched)
			}
		})
	}
}

// AC3, AC4: stop and start cycles end with exactly one registration per
// event, whether the next start comes after the deletes, straight after the
// stop, or while a delete is still held at Front Systems.
func TestFrontSystemsReuse_StopStartCyclesKeepOneRegistration(t *testing.T) {
	const cycles = 3
	for _, tc := range []struct {
		name string
		// holdDelete holds the stop's first DELETE at Front Systems.
		holdDelete bool
		// between runs after each stop and makes the next start.
		between func(t *testing.T, f *urFixture, u urConn)
	}{
		{"start after the deletes", false, func(t *testing.T, f *urFixture, u urConn) {
			harness.Eventually(t, urWait, "deletes", func() bool { return len(f.fs.registered(u.apiKey())) == 0 })
			f.start(u)
		}},
		{"start straight after the stop", false, func(t *testing.T, f *urFixture, u urConn) {
			f.start(u)
		}},
		{"start while a delete is held", true, func(t *testing.T, f *urFixture, u urConn) {
			f.fs.awaitDelete(t)
			reserved := make(chan struct{})
			var once sync.Once
			f.c.starts.AfterReserve = func(string) { once.Do(func() { close(reserved) }) }
			f.fs.drainLists()
			started := goDone(func() { f.start(u) })
			fgAwait(t, reserved, "start to claim the connection")
			f.fs.noListWhileHeld(t)
			// The held delete is answered (504) and the stop goes on to the
			// next id; let that one through.
			f.fs.set(func(fs *fakeFS) { fs.deleteHang = false })
			f.fs.release()
			fgAwait(t, started, "start")
			f.c.starts.AfterReserve = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newURFixture(t)
			u := f.conn("conn-fs-cycle", urTenantA, cycles+1)
			f.mustStart(t, u)
			for i := 0; i < cycles; i++ {
				f.fs.set(func(fs *fakeFS) {
					for len(fs.deletesSeen) > 0 {
						<-fs.deletesSeen
					}
					if tc.holdDelete {
						fs.deleteHang, fs.unhang, fs.released = true, make(chan struct{}), false
					}
				})
				f.stop(u)
				tc.between(t, f, u)
				if _, ok := activeTenant(f.c, u.id); !ok {
					t.Fatalf("cycle %d: not active after the start", i+1)
				}
				wantOnePerEvent(t, f.fs, u, fmt.Sprintf("cycle %d", i+1))
				// What the connection holds is what is registered: the next
				// stop must be able to delete all of it.
				f.c.mu.Lock()
				held := append([]string(nil), f.c.active[u.id].hooks.ids...)
				f.c.mu.Unlock()
				if got := f.fs.registeredIDs(u.apiKey()); !sameIDs(held, got) {
					t.Errorf("cycle %d: connection holds %v, Front Systems has %v", i+1, held, got)
				}
			}
			f.fs.set(func(fs *fakeFS) { fs.deleteHang = false })
			f.stop(u)
			harness.Eventually(t, urWait, "final deletes", func() bool { return len(f.fs.registered(u.apiKey())) == 0 })
		})
	}
}

// AC4, the other order: the stop has handed its webhooks over but its
// goroutine has not taken the hook lock yet, and the next start gets there
// first. That start deletes them before it lists, so it does not reuse ids
// about to be deleted. The handover is set up by hand (what handleStopCommand
// does under mu before it starts its goroutine), since which of the two takes
// the lock first can't be steered from outside.
func TestFrontSystemsReuse_StartDeletesPendingBeforeListing(t *testing.T) {
	f := newURFixture(t)
	u := f.conn("conn-fs-pending", urTenantA, 2)
	f.mustStart(t, u)
	old := f.fs.registeredIDs(u.apiKey())

	f.c.mu.Lock()
	ac := f.c.active[u.id]
	delete(f.c.active, u.id)
	if f.c.pendingUnregister == nil {
		f.c.pendingUnregister = make(map[string][]hookSet)
	}
	f.c.pendingUnregister[u.id] = append(f.c.pendingUnregister[u.id], ac.hooks)
	f.c.mu.Unlock()

	listsBefore := len(f.fs.calls(http.MethodGet))
	fgAwait(t, goDone(func() { f.start(u) }), "start")

	wantDeletes(t, f.fs, u, old)
	log := f.fs.logCopy()
	lastDeleteDone, firstLaterList, lists := -1, -1, 0
	for i, c := range log {
		switch c.Method {
		case "DELETE-done":
			lastDeleteDone = i
		case http.MethodGet:
			lists++
			if lists == listsBefore+1 {
				firstLaterList = i
			}
		}
	}
	if firstLaterList < lastDeleteDone {
		t.Errorf("the start listed (log #%d) before the pending deletes were over (log #%d)", firstLaterList, lastDeleteDone)
	}
	wantOnePerEvent(t, f.fs, u, "after the start")
	f.c.mu.Lock()
	held := append([]string(nil), f.c.active[u.id].hooks.ids...)
	pending := len(f.c.pendingUnregister[u.id])
	f.c.mu.Unlock()
	if got := f.fs.registeredIDs(u.apiKey()); !sameIDs(held, got) {
		t.Errorf("connection holds %v, Front Systems has %v", held, got)
	}
	if pending != 0 {
		t.Errorf("%d pending unregister batch(es) left after the start", pending)
	}
}

// AC5: a start that a stop abandons mid-registration deletes what it had
// registered. A start that arrives right after the stop ends up with exactly
// one registration per event.
func TestFrontSystemsUnregister_StartAbandonedByStopDeletesWhatItRegistered(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stopAtPost int
		restart    bool
	}{
		{"stop during the first registration", 1, false},
		{"stop during the second registration", 2, false},
		{"stop during registration, then a new start", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newURFixture(t)
			lookups := 1
			if tc.restart {
				lookups = 2
			}
			u := f.conn("conn-fs-abandon", urTenantA, lookups)

			var fired atomic.Bool
			restarted := make(chan struct{})
			f.fs.set(func(fs *fakeFS) {
				fs.onPost = func(n int, _ string) {
					if n != tc.stopAtPost || !fired.CompareAndSwap(false, true) {
						return
					}
					f.stop(u)
					if tc.restart {
						go func() {
							defer close(restarted)
							f.start(u)
						}()
					}
				}
			})

			// The undo runs on the start's own goroutine, so it is done when
			// the start returns: no polling.
			fgAwait(t, goDone(func() { f.start(u) }), "abandoned start")
			if !fired.Load() {
				t.Fatal("the stop never landed mid-start")
			}

			if !tc.restart {
				if _, ok := activeTenant(f.c, u.id); ok {
					t.Fatal("connection active although a stop arrived mid-start")
				}
				if got := f.fs.registered(u.apiKey()); len(got) != 0 {
					t.Errorf("left registered at Front Systems after the abandoned start: %+v", got)
				}
				// Everything it registered, including what it posted after
				// the stop landed, is deleted.
				var created []string
				for i := 1; i <= len(f.fs.calls(http.MethodPost)); i++ {
					created = append(created, fmt.Sprintf("00000000-0000-4000-8000-%012d", i))
				}
				wantDeletes(t, f.fs, u, created)
				return
			}

			fgAwait(t, restarted, "start after the stop")
			if _, ok := activeTenant(f.c, u.id); !ok {
				t.Fatal("the start after the stop did not activate the connection")
			}
			wantOnePerEvent(t, f.fs, u, "after abandon and restart")
			// The abandoned start's two ids were deleted; the new ones are live.
			wantDeletes(t, f.fs, u, []string{
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000002",
			})
		})
	}
}

// AC8: shutting the consumer down deletes nothing at Front Systems, for
// running connections or for a start still in flight. Unregisters already
// started by stop commands are cancelled once Stop's context ends.
func TestFrontSystemsShutdown_StopKeepsRegistrations(t *testing.T) {
	t.Run("running connections", func(t *testing.T) {
		f := newURFixture(t)
		a := f.conn("conn-fs-down-a", urTenantA, 1)
		b := f.conn("conn-fs-down-b", urTenantB, 1)
		f.mustStart(t, a)
		f.mustStart(t, b)

		fgAwait(t, goDone(func() { _ = f.c.Stop(context.Background()) }), "Stop")
		if n := len(f.fs.calls(http.MethodDelete)); n != 0 {
			t.Errorf("Stop sent %d DELETE(s), want 0", n)
		}
		wantOnePerEvent(t, f.fs, a, "after Stop")
		wantOnePerEvent(t, f.fs, b, "after Stop")
		if code := postEvent(f.c, a.id, `{}`); code != http.StatusServiceUnavailable {
			t.Errorf("event while shut down: status %d, want 503", code)
		}
	})

	t.Run("start in flight", func(t *testing.T) {
		f := newURFixture(t)
		u := f.conn("conn-fs-down-start", urTenantA, 1)
		var fired atomic.Bool
		f.fs.set(func(fs *fakeFS) {
			fs.onPost = func(n int, _ string) {
				if fired.CompareAndSwap(false, true) {
					_ = f.c.Stop(context.Background())
				}
			}
		})
		fgAwait(t, goDone(func() { f.start(u) }), "start")
		if !fired.Load() {
			t.Fatal("Stop never ran mid-start")
		}
		if _, ok := activeTenant(f.c, u.id); ok {
			t.Error("connection active after Stop during its start")
		}
		if n := len(f.fs.calls(http.MethodDelete)); n != 0 {
			t.Errorf("Stop during a start sent %d DELETE(s), want 0", n)
		}
	})

	t.Run("held unregister is cancelled", func(t *testing.T) {
		f := newURFixture(t)
		u := f.conn("conn-fs-down-held", urTenantA, 1)
		f.mustStart(t, u)
		f.fs.set(func(fs *fakeFS) { fs.deleteHang = true })
		f.stop(u)
		f.fs.awaitDelete(t)

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		begin := time.Now()
		// Well under stopGrace: Stop must give up when its context ends.
		fgAwait(t, goDone(func() { _ = f.c.Stop(ctx) }), "Stop with a held unregister")
		if d := time.Since(begin); d >= stopGrace {
			t.Errorf("Stop took %s, want it to end with its context", d)
		}
		harness.Eventually(t, urWait, "the held DELETE's request to end", func() bool {
			return len(f.fs.calls("DELETE-done")) >= 1
		})
	})
}

// AC6: while boot restore runs, a connection not restored yet gets 503, not
// 410; once restore is done, a restored connection publishes and one that
// is not running gets 410. Driven through the real runner.
func TestFrontSystemsRefuse_RestoreAnswers503(t *testing.T) {
	fs := newFakeFS(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	mock.MatchExpectationsInOrder(false)
	t.Cleanup(func() { _ = db.Close() })

	x := urConn{id: "conn-fs-restore", tenant: urTenantA}
	idle := urConn{id: "conn-fs-idle", tenant: urTenantA}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id::text, tenant_id FROM connections`)).
		WithArgs("front_systems").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id"}).AddRow(x.id, x.tenant))
	expectConfig(mock, fs, x, 1)

	own := &owners{m: map[string]string{x.id: x.tenant, idle.id: idle.tenant}}
	c := &frontSystemsConsumer{httpClient: fs.srv.Client(), resolveTenant: own.resolve}

	reached, release := make(chan struct{}), make(chan struct{})
	fs.set(func(fs *fakeFS) {
		fs.onList = func(n int) {
			if n == 1 {
				close(reached)
				waitOrTimeout(release)
			}
		}
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "front-systems-consumer", DB: db})
	fgAwait(t, reached, "boot restore to be registering")

	c.mu.Lock()
	restoring := c.restoring
	c.mu.Unlock()
	if !restoring {
		t.Fatal("restoring is false while boot restore is still starting a connection")
	}
	for _, conn := range []string{x.id, idle.id} {
		if code := postEvent(c, conn, `{"event":"SaleCreated"}`); code != http.StatusServiceUnavailable {
			t.Errorf("%s during restore: status %d, want 503", conn, code)
		}
	}
	if code := postEvent(c, "conn-fs-nobody", `{}`); code != http.StatusNotFound {
		t.Errorf("unknown connection during restore: status %d, want 404", code)
	}

	close(release)
	harness.Eventually(t, urWait, "boot restore to finish", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, ok := c.active[x.id]
		return ok && !c.restoring
	})
	if code := postEvent(c, idle.id, `{}`); code != http.StatusGone {
		t.Errorf("connection not running, after restore: status %d, want 410", code)
	}
	if code := postEvent(c, x.id, `{"event":"SaleCreated"}`); code != http.StatusAccepted {
		t.Fatalf("restored connection: status %d, want 202", code)
	}
	env := h.ExpectEnvelope(t, func(e *envelope.Envelope) bool { return e.IntegrationID == x.id }, urWait)
	if env.TenantID != x.tenant {
		t.Errorf("published tenant = %q, want %q", env.TenantID, x.tenant)
	}
}

// AC6: the runner serves the webhook route as soon as Configure returns,
// before Run has restored anything. In that window an event for a connection
// not started yet gets 503 (it may be running), not 410, and nothing is
// published. Run is never called here, so only Configure can have set that.
func TestFrontSystemsRefuse_AfterConfigureBeforeRestore503(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	u := urConn{id: "conn-fs-configured", tenant: urTenantA}
	own := &owners{m: map[string]string{u.id: u.tenant}}
	var published atomic.Int32
	c := &frontSystemsConsumer{
		httpClient:    http.DefaultClient,
		resolveTenant: own.resolve,
		publish: func(context.Context, *envelope.Envelope) error {
			published.Add(1)
			return nil
		},
	}
	if err := c.Configure(context.Background(), &sdk.Resources{
		DB:     db,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	if code := postEvent(c, u.id, `{"event":"SaleCreated"}`); code != http.StatusServiceUnavailable {
		t.Errorf("event between Configure and restore: status %d, want 503", code)
	}
	if code := postEvent(c, "conn-fs-nobody", `{}`); code != http.StatusNotFound {
		t.Errorf("unknown connection between Configure and restore: status %d, want 404", code)
	}
	if n := published.Load(); n != 0 {
		t.Errorf("published %d envelope(s) before restore, want 0", n)
	}
}

// AC2, AC8: Stop returns once its context ends even when a stop's unregister
// is stuck behind a start that holds the connection's hook lock in a hanging
// Front Systems call, and Stop's cancel ends that call.
//
// The two can't be put in that order from outside: a start can only claim a
// connection that is not active, and a stop only unregisters an active one,
// so its goroutine is normally first in line for the lock. The start is held
// in its list call first, then the connection is marked active by hand (with
// one webhook id), so the real stop command hands that id to an unregister
// goroutine that waits behind the start.
func TestFrontSystemsShutdown_StopCancelsStartHoldingHookLock(t *testing.T) {
	f := newURFixture(t)
	u := f.conn("conn-fs-down-locked", urTenantA, 1)
	seeded := f.fs.seed(u.apiKey(), "SaleCreated", u.callback())
	f.fs.set(func(fs *fakeFS) { fs.listHang = true })
	f.fs.drainLists()

	started := goDone(func() { f.start(u) })
	select {
	case <-f.fs.listsSeen:
	case <-time.After(urWait):
		t.Fatal("timed out waiting for the start's list call")
	}

	f.c.mu.Lock()
	if f.c.active == nil {
		f.c.active = make(map[string]*activeConn)
	}
	f.c.active[u.id] = &activeConn{tenantID: u.tenant, hooks: hookSet{
		cfg: &FrontSystemsConfig{BaseURL: f.fs.srv.URL, SubscriptionKey: u.subKey(), APIKey: u.apiKey()},
		ids: []string{seeded},
	}}
	f.c.mu.Unlock()
	f.stop(u)
	// The unregister waits for the hook lock: no DELETE while the start's
	// list hangs.
	select {
	case id := <-f.fs.deletesSeen:
		t.Fatalf("DELETE %s sent while the start held the hook lock", id)
	case <-time.After(100 * time.Millisecond):
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	begin := time.Now()
	fgAwait(t, goDone(func() { _ = f.c.Stop(ctx) }), "Stop with an unregister behind a hanging start")
	if d := time.Since(begin); d >= 2*time.Second {
		t.Errorf("Stop took %s, want it to end around its 300ms context", d)
	}
	harness.Eventually(t, urWait, "Stop to cancel the start's hanging list call", func() bool {
		return len(f.fs.calls("GET-cancelled")) == 1
	})
	fgAwait(t, started, "the cancelled start to return")
	if _, ok := activeTenant(f.c, u.id); ok {
		t.Error("connection active after Stop")
	}
}

// AC3, AC5: a registration that fails part-way deletes what that start
// created, so nothing is left registered that no stop would ever delete; a
// matching registration it found and reused is left alone. The connection
// stays stopped, and a later start registers what is missing.
func TestFrontSystemsUnregister_PartialRegistrationFailureUndoesCreated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
		seeded string // event registered before the start (reused)
		fails  string // event whose create fails
	}{
		{"second of three fails", []string{"SaleCreated", "ReturnCreated", "StockMovementCreated"}, "StockMovementCreated", "ReturnCreated"},
		{"third of three fails, after a reused one", []string{"SaleCreated", "ReturnCreated", "StockMovementCreated"}, "SaleCreated", "StockMovementCreated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newURFixture(t)
			u := urConn{id: "conn-fs-partial", tenant: urTenantA, events: tc.events}
			f.owners.set(u.id, u.tenant)
			expectConfig(f.mock, f.fs, u, 2)
			reused := f.fs.seed(u.apiKey(), tc.seeded, u.callback())
			f.fs.set(func(fs *fakeFS) { fs.postFail = tc.fails })

			fgAwait(t, goDone(func() { f.start(u) }), "failing start")
			if _, ok := activeTenant(f.c, u.id); ok {
				t.Fatal("connection active although a registration failed")
			}
			// Exactly the ids this start created before the failure are
			// deleted; the reused one is not.
			var created []string
			for _, h := range f.fs.registered(u.apiKey()) {
				if h.ID != reused {
					created = append(created, h.ID)
				}
			}
			if len(created) != 0 {
				t.Errorf("left registered after the failed start: %v (only %s, which it reused, should stay)", created, reused)
			}
			f.fs.mu.Lock()
			wantDeleted := append([]string(nil), f.fs.created...)
			f.fs.mu.Unlock()
			if len(wantDeleted) != 1 {
				t.Fatalf("setup: the start created %d webhook(s) before failing, want 1", len(wantDeleted))
			}
			wantDeletes(t, f.fs, u, wantDeleted)
			if got := f.fs.registeredIDs(u.apiKey()); !sameIDs(got, []string{reused}) {
				t.Errorf("registered after the failed start = %v, want only the reused %s", got, reused)
			}
			// A 500 is transient: the start waits to retry, so the route says
			// "try later" (503), not "gone" (410). retry_test.go covers the retry.
			if code := postEvent(f.c, u.id, `{}`); code != http.StatusServiceUnavailable {
				t.Errorf("event after the failed start: status %d, want 503 (start waiting to retry)", code)
			}

			// Front Systems recovers; the next start reuses and fills in.
			f.fs.set(func(fs *fakeFS) { fs.postFail = "" })
			f.mustStart(t, u)
			if got := f.fs.registeredIDs(u.apiKey()); !containsID(got, reused) {
				t.Errorf("the later start did not reuse %s: %v", reused, got)
			}
		})
	}
}

func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
