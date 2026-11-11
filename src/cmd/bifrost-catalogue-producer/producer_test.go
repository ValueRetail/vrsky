package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

func testProducer() *catalogueProducer {
	return &catalogueProducer{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: safeurl.NewClient(10*time.Second, true),
		// The stub is an httptest server on http://127.0.0.1.
		allowPrivate: true,
	}
}

// node builds a node's config JSON; extra is merged into the bifrost_catalogue
// block, e.g. `"mode":"full"`.
func nodeJSON(baseURL, extra string) string {
	block := fmt.Sprintf(`"base_url":%q,"token_secret_id":%q`, baseURL, harness.TestSecretID)
	if extra != "" {
		block += "," + extra
	}
	return `{"type":"bifrost_catalogue","bifrost_catalogue":{` + block + `}}`
}

// secretCiphertext sets the encryption key for the test and returns stubToken
// encrypted with it, as the secrets table would hold it.
func secretCiphertext(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyHex := hex.EncodeToString(key)
	t.Setenv(crypto.KeyEnvVar, keyHex)
	ct, err := crypto.Encrypt(stubToken, keyHex)
	if err != nil {
		t.Fatal(err)
	}
	return ct
}

// expectNodeRead expects one read of the connection holding the given producer
// node configs, and one secret read for every config that references one.
func expectNodeRead(t *testing.T, mock sqlmock.Sqlmock, ct, edges string, configs ...string) {
	t.Helper()
	parts := make([]string, len(configs))
	for i, c := range configs {
		parts[i] = fmt.Sprintf(`{"id":"n%d","type":"producer","config":%s}`, i+1, c)
	}
	mock.ExpectQuery("SELECT nodes, edges FROM connections").WithArgs("conn-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte("["+strings.Join(parts, ",")+"]"), []byte(edges)))
	for _, c := range configs {
		if strings.Contains(c, "token_secret_id") {
			mock.ExpectQuery(`FROM secrets`).WithArgs(harness.TestSecretID, "tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(ct))
		}
	}
}

// deliver runs Deliver once against a connection holding the given producer
// node configs (id "n1", "n2", …) and edges.
func deliver(t *testing.T, p *catalogueProducer, env *envelope.Envelope, edges string, configs ...string) error {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	expectNodeRead(t, mock, secretCiphertext(t), edges, configs...)
	p.db = db
	if env.TenantID == "" {
		env.TenantID = "tenant-1"
	}
	env.IntegrationID = "conn-1"
	if env.ID == "" {
		env.ID = "env-1"
	}
	return p.Deliver(context.Background(), env)
}

// items returns n generated catalogue items as a JSON array.
func items(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"article_no":"A%05d","gtin":"700%010d","name":"Item %d","price_ex_vat":"%d.90","vat_code":"F"}`, i, i, i, i%90+1)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func liveFromItems(t *testing.T, payload string) map[string]string {
	t.Helper()
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range raw {
		out[articleNo(r)] = string(r)
	}
	return out
}

func sameCatalogue(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("catalogue holds %d articles, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("article %s = %s, want %s", k, got[k], v)
		}
	}
}

func TestFullImport_BeginItemsCommit(t *testing.T) {
	stub := newBifrostStub(t)
	payload := items(2500)

	err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(payload)}, `[]`,
		nodeJSON(stub.URL()+"/", `"mode":"full","batch_size":1000,"tenant_id":"nordic"`))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if got, want := stub.kinds(), "begin items items items commit"; got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if b := stub.beginBodies(); len(b) != 1 || b[0].Kind != "full" || b[0].AllowShrink != "" {
		t.Errorf("begin bodies = %+v, want one full import without a shrink override", b)
	}
	sameCatalogue(t, stub.liveSnapshot(), liveFromItems(t, payload))
	if n := stub.openImports(); n != 0 {
		t.Errorf("%d imports left open", n)
	}

	var keys []string
	for _, h := range stub.requestHeaders() {
		if h.Get("Authorization") != "Bearer "+stubToken {
			t.Errorf("Authorization = %q", h.Get("Authorization"))
		}
		if h.Get("X-Tenant-Id") != "nordic" {
			t.Errorf("X-Tenant-Id = %q, want nordic", h.Get("X-Tenant-Id"))
		}
		if k := h.Get("Idempotency-Key"); k != "" {
			keys = append(keys, k)
		}
	}
	if got, want := strings.Join(keys, ","), "env-1-1,env-1-2,env-1-3"; got != want {
		t.Errorf("Idempotency-Keys = %s, want %s", got, want)
	}
}

// Prices are money: they must arrive as the strings the source wrote, and
// fields this connector does not know (price_incl_vat, vat_rate, a future one)
// must pass through untouched.
func TestItemsPassThroughUnchanged(t *testing.T) {
	stub := newBifrostStub(t)
	payload := `[{"article_no":"1","name":"A","price_incl_vat":"29.95","vat_rate":"0.15","vat_code":"F","future":{"x":[1,2]},"price_ex_vat":"0.10"},` +
		`{"article_no":"2","name":"B","price_ex_vat":"1234567890.12345678","vat_code":"F","effective_from":"2026-12-01T00:00:00Z"}]`
	if err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(payload)}, `[]`, nodeJSON(stub.URL(), `"mode":"full"`)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	sameCatalogue(t, stub.liveSnapshot(), liveFromItems(t, payload))
}

func TestDeltaImport(t *testing.T) {
	stub := newBifrostStub(t)
	stub.seedN(5)
	// A single object is one item; default mode is delta, so the five stay.
	err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(`{"article_no":"new","name":"N","price_ex_vat":"9.90","vat_code":"F"}`)}, `[]`, nodeJSON(stub.URL(), ``))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if b := stub.beginBodies(); len(b) != 1 || b[0].Kind != "delta" {
		t.Errorf("begin bodies = %+v, want one delta import", b)
	}
	if n := len(stub.liveSnapshot()); n != 6 {
		t.Errorf("catalogue holds %d articles, want 6 (5 kept + 1 upserted)", n)
	}
	// {"items": [...]} is read as the list.
	if err := deliver(t, testProducer(), &envelope.Envelope{ID: "env-2", Payload: []byte(`{"items":` + items(3) + `}`)}, `[]`, nodeJSON(stub.URL(), `"mode":"delta"`)); err != nil {
		t.Fatalf("Deliver {items}: %v", err)
	}
	if n := len(stub.liveSnapshot()); n != 9 {
		t.Errorf("catalogue holds %d articles, want 9", n)
	}
}

// The batch window is the envelope: batch_size and the byte cap only cut
// requests, never imports.
func TestSplitBatches(t *testing.T) {
	mk := func(n, size int) []json.RawMessage {
		out := make([]json.RawMessage, n)
		for i := range out {
			out[i] = json.RawMessage(`{"article_no":"` + strings.Repeat("x", size) + `"}`)
		}
		return out
	}
	tests := []struct {
		name      string
		items     []json.RawMessage
		size, max int
		want      []int
	}{
		{"by count", mk(7, 1), 3, 1 << 20, []int{3, 3, 1}},
		{"exact", mk(6, 1), 3, 1 << 20, []int{3, 3}},
		{"by bytes", mk(5, 100), 100, 250, []int{2, 2, 1}},
		{"oversized item alone", mk(3, 500), 100, 100, []int{1, 1, 1}},
		{"one", mk(1, 1), 1000, 1 << 20, []int{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []int
			total := 0
			for _, b := range splitBatches(tt.items, tt.size, tt.max) {
				got = append(got, len(b))
				total += len(b)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) || total != len(tt.items) {
				t.Errorf("batches = %v, want %v", got, tt.want)
			}
		})
	}
}

// A failure anywhere after begin abandons the import and commits nothing; the
// error carries the retry signal and, for a refusal, Bifrost's reason.
func TestFailureAbandonsImport(t *testing.T) {
	type outcome int
	const (
		retriable outcome = iota
		permanent
		rateLimited
	)
	tests := []struct {
		name       string
		kind       string // which call fails
		nth        int
		fault      fault
		want       outcome
		wantText   string
		wantAbort  bool
		wantCommit bool // whether a commit request is expected
	}{
		{"items 500", "items", 2, fault{status: 500, body: `{"error":"internal error"}`}, retriable, "bifrost items 500", true, false},
		{"items 503 html", "items", 1, fault{status: 503, body: `<html>busy</html>`}, retriable, "<html>busy</html>", true, false},
		{"items connection dropped", "items", 2, fault{drop: true}, retriable, "bifrost items", true, false},
		{"items 429", "items", 2, fault{status: 429, body: `{"error":"slow down"}`, header: map[string]string{"Retry-After": "7"}}, rateLimited, "slow down", true, false},
		{"items 404 expired", "items", 2, fault{status: 404, body: `{"error":"no such import — it may have expired"}`}, retriable, "may have expired", true, false},
		{"items 403", "items", 1, fault{status: 403, body: `{"error":"forbidden"}`}, retriable, "retry the message from the DLQ", true, false},
		{"items 422 validation", "items", 3, fault{status: 422, body: `{"error":"Failed to deserialize the JSON body"}`}, permanent, "Failed to deserialize", true, false},
		{"items 400 from a proxy", "items", 1, fault{status: 400, body: `{"error":"bad batch"}`}, retriable, "lower batch_size", true, false},
		{"items 413 from a proxy", "items", 2, fault{status: 413, body: `<html>413 Request Entity Too Large</html>`}, retriable, "lower batch_size", true, false},
		{"items total_so_far too low", "items", 2, fault{status: 200, body: `{"received":1000,"total_so_far":1,"replayed":false}`}, retriable, "import holds 1 items after this batch, sent 2000", true, false},
		{"items 200 html", "items", 1, fault{status: 200, body: `<html>login</html>`}, retriable, "no total_so_far", true, false},
		{"commit 200 html", "commit", 1, fault{status: 200, body: `<html>login</html>`}, retriable, "not a JSON report", true, true},
		{"commit 422 shrink", "commit", 1, fault{status: 422, body: `{"error":"shrink guard: 10 of 100"}`}, permanent, "shrink guard: 10 of 100", true, true},
		{"commit 500", "commit", 1, fault{status: 500, body: `{"error":"internal error"}`}, retriable, "bifrost commit 500", true, true},
		{"commit 401", "commit", 1, fault{status: 401, body: `{"error":"unauthorized"}`}, retriable, "check the node's token", true, true},
		{"commit connection dropped", "commit", 1, fault{drop: true}, retriable, "bifrost commit", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newBifrostStub(t)
			stub.seedN(5)
			before := stub.liveSnapshot()
			stub.failOnce(tt.kind, tt.nth, tt.fault)
			if tt.fault.drop {
				// Go resends a request that carries an Idempotency-Key when
				// the connection died before any answer; drop that one too.
				stub.failOnce(tt.kind, tt.nth+1, tt.fault)
			}

			err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(items(3500))}, `[]`,
				nodeJSON(stub.URL(), `"mode":"delta","batch_size":1000`))
			if err == nil {
				t.Fatal("Deliver succeeded, want an error")
			}
			switch tt.want {
			case permanent:
				if !sdk.IsPermanent(err) {
					t.Errorf("error is not Permanent: %v", err)
				}
			case retriable:
				if sdk.IsPermanent(err) {
					t.Errorf("error is Permanent (message would be dropped): %v", err)
				}
			case rateLimited:
				if d, ok := sdk.RetryAfter(err); !ok || d != 7*time.Second {
					t.Errorf("RetryAfter = %v, %v; want 7s, true", d, ok)
				}
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("error %q lacks %q", err, tt.wantText)
			}
			if strings.HasSuffix(stub.kinds(), "abort") != tt.wantAbort {
				t.Errorf("calls = %q, wantAbort = %v", stub.kinds(), tt.wantAbort)
			}
			if strings.Contains(stub.kinds(), "commit") != tt.wantCommit {
				t.Errorf("calls = %q, wantCommit = %v", stub.kinds(), tt.wantCommit)
			}
			sameCatalogue(t, stub.liveSnapshot(), before) // nothing half-applied
			if n := stub.openImports(); n != 0 {
				t.Errorf("%d imports left open", n)
			}
		})
	}
}

// begin has no import to abandon; its failures are classified the same way.
func TestBeginFailures(t *testing.T) {
	tests := []struct {
		name      string
		fault     fault
		permanent bool
		wantText  string
	}{
		{"500", fault{status: 500, body: `{"error":"internal error"}`}, false, "bifrost begin 500"},
		{"429 too many imports", fault{status: 429, body: `{"error":"too many imports are open — commit or abandon one first"}`, header: map[string]string{"Retry-After": "7"}}, false, "too many imports"},
		{"401", fault{status: 401, body: `{"error":"unauthorized"}`}, false, "check the node's token"},
		{"404 wrong base_url", fault{status: 404, body: `not found`}, false, "bifrost begin 404: not found"},
		{"400", fault{status: 400, body: `{"error":"kind 'x' is not 'full' or 'delta'"}`}, true, "kind 'x'"},
		{"2xx without an import id", fault{status: 200, body: `<html>login</html>`}, false, "no import_id"},
		{"302 redirect", fault{status: 302, header: map[string]string{"Location": "https://elsewhere.example/"}}, false, "bifrost begin 302"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newBifrostStub(t)
			stub.failOnce("begin", 1, tt.fault)
			err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(items(3))}, `[]`, nodeJSON(stub.URL(), `"mode":"delta"`))
			if err == nil || sdk.IsPermanent(err) != tt.permanent {
				t.Fatalf("err = %v, permanent = %v, want permanent = %v", err, sdk.IsPermanent(err), tt.permanent)
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("error %q lacks %q", err, tt.wantText)
			}
			if got := stub.kinds(); got != "begin" {
				t.Errorf("calls = %q, want only begin (nothing to abandon)", got)
			}
		})
	}
}

// A shrink-guard refusal: permanent, Bifrost's reason in the text, the live
// catalogue untouched, and the untouched redelivery is not attempted.
func TestShrinkGuardRefusalIsPermanentWithReason(t *testing.T) {
	stub := newBifrostStub(t)
	stub.seedN(100)
	before := stub.liveSnapshot()

	err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(items(10))}, `[]`, nodeJSON(stub.URL(), `"mode":"full"`))
	if err == nil || !sdk.IsPermanent(err) {
		t.Fatalf("err = %v, want Permanent", err)
	}
	if !strings.Contains(err.Error(), "refusing a full import of 10 articles over a live catalogue of 100: shrink guard") {
		t.Errorf("Bifrost's reason is not in %q", err)
	}
	sameCatalogue(t, stub.liveSnapshot(), before)
}

// allow_shrink_reason goes to Bifrost on begin and lets the import through.
func TestAllowShrinkReason(t *testing.T) {
	stub := newBifrostStub(t)
	stub.seedN(100)
	err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(items(10))}, `[]`,
		nodeJSON(stub.URL(), `"mode":"full","allow_shrink_reason":"store closed, range cut"`))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if b := stub.beginBodies(); len(b) != 1 || b[0].AllowShrink != "store closed, range cut" {
		t.Errorf("begin bodies = %+v", b)
	}
	if n := len(stub.liveSnapshot()); n != 10 {
		t.Errorf("catalogue holds %d, want 10", n)
	}
}

// A retry after a failure converges: the first attempt dies at batch 2, the
// second runs from a fresh begin, and the catalogue holds each article once.
func TestRetryAfterFailureConverges(t *testing.T) {
	stub := newBifrostStub(t)
	stub.seedN(20)
	payload := items(2500)
	stub.failOnce("items", 2, fault{status: 502, body: `bad gateway`})
	cfg := nodeJSON(stub.URL(), `"mode":"full","batch_size":1000`)

	env := &envelope.Envelope{Payload: []byte(payload)}
	if err := deliver(t, testProducer(), env, `[]`, cfg); err == nil || sdk.IsPermanent(err) {
		t.Fatalf("first attempt: %v, want a retriable error", err)
	}
	if n := len(stub.liveSnapshot()); n != 20 {
		t.Fatalf("after the failed attempt the catalogue holds %d, want the 20 it had", n)
	}
	if err := deliver(t, testProducer(), env, `[]`, cfg); err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	sameCatalogue(t, stub.liveSnapshot(), liveFromItems(t, payload))
	if n := stub.openImports(); n != 0 {
		t.Errorf("%d imports left open", n)
	}
	if got, want := stub.kinds(), "begin items items abort begin items items items commit"; got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

// The runner redelivers an envelope whose ack was lost (a crash between commit
// and ack). The second delivery must leave the same catalogue and nothing open.
func TestRedeliveryAfterCommitBeforeAck(t *testing.T) {
	for _, mode := range []string{"full", "delta"} {
		t.Run(mode, func(t *testing.T) {
			stub := newBifrostStub(t)
			stub.seedN(30)
			payload := items(1200)
			cfg := nodeJSON(stub.URL(), fmt.Sprintf(`"mode":%q,"batch_size":500`, mode))
			env := &envelope.Envelope{Payload: []byte(payload)}

			if err := deliver(t, testProducer(), env, `[]`, cfg); err != nil {
				t.Fatal(err)
			}
			afterFirst := stub.liveSnapshot()
			// (crash: the ack never reaches JetStream) — same envelope again.
			if err := deliver(t, testProducer(), env, `[]`, cfg); err != nil {
				t.Fatalf("redelivery: %v", err)
			}
			sameCatalogue(t, stub.liveSnapshot(), afterFirst)
			if n := stub.openImports(); n != 0 {
				t.Errorf("%d imports left open", n)
			}
			if n := strings.Count(stub.kinds(), "begin"); n != 2 {
				t.Errorf("%d imports begun, want 2 (one per delivery)", n)
			}
		})
	}
}

// Commit landed in Bifrost but its answer was lost. The delivery fails
// (retriable), the abort finds nothing (404 is fine), and the redelivery
// leaves the same catalogue.
func TestCommitAnswerLost(t *testing.T) {
	stub := newBifrostStub(t)
	payload := items(300)
	stub.failOnce("commit", 1, fault{dropAfter: true})
	cfg := nodeJSON(stub.URL(), `"mode":"full"`)
	env := &envelope.Envelope{Payload: []byte(payload)}

	err := deliver(t, testProducer(), env, `[]`, cfg)
	if err == nil || sdk.IsPermanent(err) {
		t.Fatalf("err = %v, want retriable", err)
	}
	landed := stub.liveSnapshot()
	sameCatalogue(t, landed, liveFromItems(t, payload))
	if err := deliver(t, testProducer(), env, `[]`, cfg); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	sameCatalogue(t, stub.liveSnapshot(), landed)
	if n := stub.openImports(); n != 0 {
		t.Errorf("%d imports left open", n)
	}
}

// If the delivery context ends mid-import the import is still abandoned.
func TestAbortSurvivesCancelledContext(t *testing.T) {
	stub := newBifrostStub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub.onItems = func(n int) {
		if n == 2 {
			cancel()
		}
	}
	p := testProducer()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	expectNodeRead(t, mock, secretCiphertext(t), `[]`, nodeJSON(stub.URL(), `"mode":"delta","batch_size":100`))
	p.db = db

	err = p.Deliver(ctx, &envelope.Envelope{ID: "env-1", TenantID: "tenant-1", IntegrationID: "conn-1", Payload: []byte(items(500))})
	if err == nil || sdk.IsPermanent(err) {
		t.Fatalf("err = %v, want a retriable error", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Logf("error does not wrap context.Canceled: %v", err)
	}
	if !strings.HasSuffix(stub.kinds(), "abort") || stub.openImports() != 0 {
		t.Errorf("calls = %q, open imports = %d; want the import abandoned", stub.kinds(), stub.openImports())
	}
	if len(stub.liveSnapshot()) != 0 {
		t.Errorf("catalogue changed")
	}
}

// Error texts reach the activity feed and the DLQ header: no token, no
// address, no URL.
func TestErrorTextsLeakNothing(t *testing.T) {
	t.Run("token echoed by the server", func(t *testing.T) {
		stub := newBifrostStub(t)
		stub.failOnce("items", 1, fault{status: 422, body: `{"error":"bad item; Authorization: Bearer ` + stubToken + `\nsecond line"}`})
		err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(items(3))}, `[]`, nodeJSON(stub.URL(), ``))
		if err == nil {
			t.Fatal("want an error")
		}
		if strings.Contains(err.Error(), stubToken) || !strings.Contains(err.Error(), "[redacted]") || strings.Contains(err.Error(), "\n") {
			t.Errorf("error text %q", err)
		}
	})
	t.Run("long reason is cut", func(t *testing.T) {
		stub := newBifrostStub(t)
		stub.failOnce("items", 1, fault{status: 422, body: `{"error":"` + strings.Repeat("x", 5000) + `"}`})
		err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(items(3))}, `[]`, nodeJSON(stub.URL(), ``))
		if err == nil || len(err.Error()) > 600 {
			t.Errorf("error length %d", len(fmt.Sprint(err)))
		}
	})
	t.Run("transport error", func(t *testing.T) {
		stub := newBifrostStub(t)
		url := stub.URL()
		stub.srv.Close()
		err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(items(3))}, `[]`, nodeJSON(url, ``))
		if err == nil || sdk.IsPermanent(err) {
			t.Fatalf("err = %v, want retriable", err)
		}
		for _, bad := range []string{"127.0.0.1", "dial tcp", "http://", "/v1/catalogue"} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("error text %q contains %q", err, bad)
			}
		}
		if !strings.Contains(err.Error(), "connection refused") {
			t.Errorf("error text %q", err)
		}
	})
}

// Node settings that are wrong or missing are retried and then kept in the
// DLQ (#734); nothing is sent.
func TestIncompleteNodeIsRetriableAndSendsNothing(t *testing.T) {
	stub := newBifrostStub(t)
	tests := []struct {
		name   string
		config string
		allow  bool
		want   string
	}{
		{"no base_url", `{"type":"bifrost_catalogue","bifrost_catalogue":{"token_secret_id":"` + harness.TestSecretID + `"}}`, true, "base_url is required"},
		{"no token", `{"type":"bifrost_catalogue","bifrost_catalogue":{"base_url":"` + stub.URL() + `"}}`, true, "no token"},
		{"token stored in plaintext", `{"type":"bifrost_catalogue","bifrost_catalogue":{"base_url":"` + stub.URL() + `","token":"` + stubToken + `"}}`, true, "stored in plaintext"},
		{"no block", `{"type":"bifrost_catalogue"}`, true, "no token"},
		{"token_secret_id not a string", `{"type":"bifrost_catalogue","bifrost_catalogue":{"base_url":"` + stub.URL() + `","token_secret_id":5}}`, true, "token_secret_id must be a string"},
		{"bad mode", nodeJSON(stub.URL(), `"mode":"replace"`), true, `mode "replace"`},
		{"batch too big", nodeJSON(stub.URL(), `"batch_size":100000`), true, "batch_size"},
		{"negative batch", nodeJSON(stub.URL(), `"batch_size":-1`), true, "batch_size"},
		{"blank shrink reason", nodeJSON(stub.URL(), `"allow_shrink_reason":"  "`), true, "allow_shrink_reason"},
		{"http base_url in production", nodeJSON(stub.URL(), ``), false, "base_url"},
		{"private base_url", nodeJSON("https://10.0.0.5", ``), false, "base_url"},
		{"userinfo", nodeJSON("https://u:p@catalogue.example.com", ``), false, "base_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testProducer()
			p.allowPrivate = tt.allow
			err := deliver(t, p, &envelope.Envelope{Payload: []byte(items(3))}, `[]`, tt.config)
			if err == nil || sdk.IsPermanent(err) {
				t.Fatalf("err = %v, want a retriable error", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q lacks %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "u:p") {
				t.Errorf("error %q quotes the credential in the URL", err)
			}
		})
	}
	if got := stub.kinds(); got != "" {
		t.Errorf("the stub received %q, want nothing", got)
	}
}

// A payload that is not a list of items is the message's fault: dropped, and
// never turned into an import.
func TestBadPayloadIsPermanentAndSendsNothing(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		payload string
		want    string
	}{
		{"not json", "delta", `<xml/>`, "not valid JSON"},
		{"empty", "delta", ``, "not valid JSON"},
		{"scalar", "delta", `42`, "neither a list nor an object"},
		{"empty list", "full", `[]`, "no items"},
		{"empty items", "delta", `{"items":[]}`, "no items"},
		{"items not a list", "delta", `{"items":"x"}`, "not a list"},
		{"item not an object", "delta", `[{"article_no":"1"},"2"]`, "item 2 is not an object"},
		{"full mode single item", "full", `{"article_no":"1","name":"A","price_ex_vat":"1","vat_code":"F"}`, "a single item would replace the catalogue"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newBifrostStub(t)
			err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(tt.payload)}, `[]`, nodeJSON(stub.URL(), fmt.Sprintf(`"mode":%q`, tt.mode)))
			if err == nil || !sdk.IsPermanent(err) {
				t.Fatalf("err = %v, want Permanent", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q lacks %q", err, tt.want)
			}
			if got := stub.kinds(); got != "" {
				t.Errorf("the stub received %q, want nothing", got)
			}
		})
	}
}

// With a converter in front, the destination takes only the converter's
// output, not the source's raw page.
func TestRouting(t *testing.T) {
	stub := newBifrostStub(t)
	edges := `[{"source":"conv","target":"n1"}]`
	raw := &envelope.Envelope{Payload: []byte(items(2)), Metadata: map[string]interface{}{"_last_processed_by": "src"}}
	if err := deliver(t, testProducer(), raw, edges, nodeJSON(stub.URL(), ``)); err != nil {
		t.Fatal(err)
	}
	if got := stub.kinds(); got != "" {
		t.Errorf("a foreign page was delivered: %q", got)
	}
	conv := &envelope.Envelope{Payload: []byte(items(2)), Metadata: map[string]interface{}{"_last_processed_by": "conv"}}
	if err := deliver(t, testProducer(), conv, edges, nodeJSON(stub.URL(), ``)); err != nil {
		t.Fatal(err)
	}
	if got := stub.kinds(); got != "begin items commit" {
		t.Errorf("calls = %q", got)
	}
}

// Two Bifrost nodes on one connection: both get the list; what one of them
// refuses does not hide that the other has to be retried, and a retriable
// failure is never turned into a drop by a permanent one beside it.
func TestTwoNodes(t *testing.T) {
	good, flaky, strict := newBifrostStub(t), newBifrostStub(t), newBifrostStub(t)
	flaky.failOnce("commit", 1, fault{status: 503, body: `{"error":"down"}`})
	strict.seedN(100)

	t.Run("one retriable", func(t *testing.T) {
		err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(items(3))}, `[]`,
			nodeJSON(good.URL(), `"mode":"full"`), nodeJSON(flaky.URL(), `"mode":"full"`))
		if err == nil || sdk.IsPermanent(err) || !strings.Contains(err.Error(), "node n2") {
			t.Fatalf("err = %v, want a retriable error naming node n2", err)
		}
		if good.kinds() != "begin items commit" {
			t.Errorf("good node calls = %q", good.kinds())
		}
	})
	t.Run("retriable and permanent", func(t *testing.T) {
		flaky.failOnce("commit", 2, fault{status: 503, body: `{"error":"down"}`})
		err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(items(3))}, `[]`,
			nodeJSON(flaky.URL(), `"mode":"full"`), nodeJSON(strict.URL(), `"mode":"full"`))
		if err == nil || sdk.IsPermanent(err) {
			t.Fatalf("err = %v, want retriable (the permanent one must not make the SDK drop the message)", err)
		}
		if !strings.Contains(err.Error(), "shrink guard") {
			t.Errorf("the permanent node's reason is missing from %q", err)
		}
	})
	t.Run("all permanent", func(t *testing.T) {
		err := deliver(t, testProducer(), &envelope.Envelope{ID: "env-9", Payload: []byte(items(3))}, `[]`,
			nodeJSON(strict.URL(), `"mode":"full"`), nodeJSON(strict.URL(), `"mode":"full"`))
		if err == nil || !sdk.IsPermanent(err) {
			t.Fatalf("err = %v, want Permanent", err)
		}
	})
}

func TestServesConnection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	p := testProducer()
	p.db = db

	expectNodeRead(t, mock, secretCiphertext(t), `[]`, nodeJSON("https://x.example", ""))
	if !p.ServesConnection(context.Background(), "tenant-1", "conn-1") {
		t.Error("false for a connection with a Bifrost node")
	}
	mock.ExpectQuery("FROM connections").WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).
		AddRow([]byte(`[{"id":"n1","type":"producer","config":{"type":"http","http":{"url":"https://x"}}}]`), []byte(`[]`)))
	if p.ServesConnection(context.Background(), "tenant-1", "conn-1") {
		t.Error("true for a connection without one")
	}
	if p.ServesConnection(context.Background(), "tenant-1", "") {
		t.Error("true without a connection id")
	}
}

// A 200 commit whose report cannot be read is a success only when it looks
// like Bifrost's: a JSON object, or a report cut off by the read limit.
func TestCommitReportReadability(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"valid", `{"applied":3,"catalogue_size":3,"rejected":0,"errors":[]}`, false},
		{"object with wrong types", `{"applied":"three"}`, false},
		{"truncated by the read limit", `{"applied":3,"errors":[` + strings.Repeat(`{"row":1,"article_no":"x","error":"y"},`, 40000), false},
		{"large html over the read limit", strings.Repeat("x", 1<<20+5), true},
		{"html", `<html>ok</html>`, true},
		{"empty", ``, true},
		{"array", `[1]`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newBifrostStub(t)
			stub.failOnce("commit", 1, fault{status: 200, body: tt.body})
			err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(items(3))}, `[]`, nodeJSON(stub.URL(), ``))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err != nil && sdk.IsPermanent(err) {
				t.Errorf("error is Permanent: %v", err)
			}
		})
	}
}

// Secrets are write-only: only token_secret_id may resolve. Another
// *_secret_id would resolve into a setting that can come back in an error text
// ("mode %q is not …"). Here mode_secret_id would make the mode the token.
func TestOnlyTokenSecretResolves(t *testing.T) {
	stub := newBifrostStub(t)
	cfg := nodeJSON(stub.URL(), fmt.Sprintf(`"mode_secret_id":%q`, harness.TestSecretID))
	if err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(items(2))}, `[]`, cfg); err != nil {
		t.Fatalf("Deliver: %v (mode_secret_id must be ignored, leaving the default mode)", err)
	}
	if b := stub.beginBodies(); len(b) != 1 || b[0].Kind != "delta" {
		t.Errorf("begin bodies = %+v, want one delta import", b)
	}
}

// A {secret:<id>} placeholder outside token_secret_id is never expanded: the
// setting it sits in can come back in an error text (mode), go out as a header
// (tenant_id) or into Bifrost's audit (allow_shrink_reason). The node is
// refused as a config error that names the setting and not its value, and
// nothing is sent. The placeholder points at the token's own secret, so an
// expansion anywhere would put stubToken where the test looks.
func TestSecretPlaceholderOutsideTokenIsRefused(t *testing.T) {
	placeholder := "{secret:" + harness.TestSecretID + "}"
	tests := []struct {
		name  string
		extra string
		field string
	}{
		{"mode", fmt.Sprintf(`"mode":%q`, placeholder), "mode"},
		{"allow_shrink_reason", fmt.Sprintf(`"mode":"full","allow_shrink_reason":%q`, placeholder), "allow_shrink_reason"},
		{"tenant_id", fmt.Sprintf(`"tenant_id":%q`, placeholder), "tenant_id"},
		{"inside a longer value", fmt.Sprintf(`"tenant_id":%q`, "t-"+placeholder+"-x"), "tenant_id"},
		{"unknown nested setting", fmt.Sprintf(`"extra":{"list":[%q]}`, placeholder), "extra.list[0]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newBifrostStub(t)
			var logs strings.Builder
			p := testProducer()
			p.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			ct := secretCiphertext(t)
			expectNodeRead(t, mock, ct, `[]`, nodeJSON(stub.URL(), tt.extra))
			// Room for the reads an expansion would make, so a regression
			// shows up as a leak rather than as an unexpected query.
			for i := 0; i < 3; i++ {
				mock.ExpectQuery(`FROM secrets`).WithArgs(harness.TestSecretID, "tenant-1").
					WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(ct))
			}
			p.db = db

			env := &envelope.Envelope{ID: "env-1", TenantID: "tenant-1", IntegrationID: "conn-1", Payload: []byte(items(3))}
			err = p.Deliver(context.Background(), env)
			msg := fmt.Sprint(err)
			if err == nil || sdk.IsPermanent(err) {
				t.Errorf("err = %v, want a retriable config error", err)
			}
			if !strings.Contains(msg, tt.field) || !strings.Contains(msg, "placeholder") {
				t.Errorf("error %q does not name the setting %s", msg, tt.field)
			}
			for _, bad := range []string{stubToken, harness.TestSecretID} {
				if strings.Contains(msg, bad) {
					t.Errorf("error %q contains %q", msg, bad)
				}
			}
			if strings.Contains(logs.String(), stubToken) {
				t.Errorf("logs contain the token: %s", logs.String())
			}
			if got := stub.kinds(); got != "" {
				t.Errorf("the stub received %q, want nothing", got)
			}
			for _, b := range stub.beginBodies() {
				if strings.Contains(b.AllowShrink, stubToken) || strings.Contains(b.Kind, stubToken) {
					t.Errorf("begin body carries the token: %+v", b)
				}
			}
			for _, h := range stub.requestHeaders() {
				for k, vs := range h {
					if k == "Authorization" {
						continue
					}
					for _, v := range vs {
						if strings.Contains(v, stubToken) {
							t.Errorf("header %s carries the token", k)
						}
					}
				}
			}
		})
	}
}
