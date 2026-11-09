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
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// reply is one answer of the fake Salesforce. drop closes the connection
// without an answer (a network error).
type reply struct {
	status int
	body   string
	drop   bool
}

// fakeSF is a fake Salesforce org. REST writes are keyed by the record's
// Name, Bulk API calls by "create:<object>", "upload:<object>",
// "close:<object>", "abort:<object>" and "state:<object>" (a job's state
// reads, answered JobComplete with nothing failed). respond gets the key and
// how many times it was called before (0 on the first call); returning false
// answers with success. bulk_test.go has a stateful fake for the Bulk API.
type fakeSF struct {
	t       *testing.T
	srv     *httptest.Server
	respond func(key string, n int) (reply, bool)

	mu    sync.Mutex
	calls map[string]int
}

func newFakeSF(t *testing.T, respond func(key string, n int) (reply, bool)) *fakeSF {
	t.Helper()
	f := &fakeSF{t: t, respond: respond, calls: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSF) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[key]
}

func (f *fakeSF) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := r.URL.Path
	var key string
	var ok reply
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/jobs/ingest"):
		var job struct {
			Object string `json:"object"`
		}
		_ = json.Unmarshal(body, &job)
		key = "create:" + job.Object
		ok = reply{status: http.StatusOK, body: fmt.Sprintf(`{"id":"job-%s","state":"Open"}`, job.Object)}
	case r.Method == http.MethodPut && strings.Contains(path, "/jobs/ingest/job-"):
		obj := strings.TrimSuffix(strings.TrimPrefix(path[strings.Index(path, "/jobs/ingest/job-"):], "/jobs/ingest/job-"), "/batches")
		key = "upload:" + obj
		ok = reply{status: http.StatusCreated}
	case r.Method == http.MethodPatch && strings.Contains(path, "/jobs/ingest/job-"):
		obj := strings.TrimPrefix(path[strings.Index(path, "/jobs/ingest/job-"):], "/jobs/ingest/job-")
		key = "close:" + obj
		if strings.Contains(string(body), "Aborted") {
			key = "abort:" + obj
		}
		ok = reply{status: http.StatusOK, body: `{"state":"UploadComplete"}`}
	case r.Method == http.MethodGet && strings.Contains(path, "/jobs/ingest/job-"):
		obj := strings.TrimPrefix(path[strings.Index(path, "/jobs/ingest/job-"):], "/jobs/ingest/job-")
		key = "state:" + obj
		ok = reply{status: http.StatusOK, body: fmt.Sprintf(`{"id":"job-%s","state":"JobComplete","numberRecordsProcessed":1,"numberRecordsFailed":0}`, obj)}
	case strings.Contains(path, "/sobjects/"):
		var rec map[string]any
		if err := json.Unmarshal(body, &rec); err != nil {
			f.t.Errorf("fake Salesforce: record body is not JSON: %v", err)
		}
		key = fmt.Sprint(rec["Name"])
		ok = reply{status: http.StatusCreated, body: `{"id":"001","success":true,"errors":[]}`}
	default:
		f.t.Errorf("fake Salesforce: unexpected %s %s", r.Method, path)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	f.mu.Lock()
	n := f.calls[key]
	f.calls[key] = n + 1
	f.mu.Unlock()

	rep := ok
	if f.respond != nil {
		if got, scripted := f.respond(key, n); scripted {
			rep = got
		}
	}
	if rep.drop {
		hj, can := w.(http.Hijacker)
		if !can {
			f.t.Fatal("fake Salesforce: cannot hijack")
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.status)
	_, _ = w.Write([]byte(rep.body))
}

const testConn = "sfp-735"

// newTestProducer is a configured producer whose connection has the given
// targets (the config cache is filled, so no database is read).
func newTestProducer(t *testing.T, bulkThreshold int, forced *atomic.Int32, targets ...*sfTarget) *salesforceProducer {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := &salesforceProducer{
		bulkThreshold:   bulkThreshold,
		bulkPollInitial: time.Millisecond,
		resolveToken: func(_ context.Context, _, _ string, force bool) (string, error) {
			if force && forced != nil {
				forced.Add(1)
			}
			return "secret-token-735", nil
		},
	}
	if err := p.Configure(context.Background(), &sdk.Resources{DB: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	p.cache[testConn] = targets
	p.cacheTime[testConn] = time.Now()
	return p
}

func target(nodeID, instanceURL, object, op string) *sfTarget {
	return &sfTarget{nodeID: nodeID, cfg: SalesforceProducerConfig{
		InstanceURL: instanceURL, OAuthGrantID: "g1", Object: object, Operation: op, ExternalIDField: "ExtId__c",
	}}
}

func testEnvelope(payload string) *envelope.Envelope {
	env := envelope.New()
	env.ID = "env-735"
	env.IntegrationID = testConn
	env.TenantID = "tenant-1"
	env.Payload = []byte(payload)
	return env
}

// checkRetryable: the delivery is NAK'd (so it ends in the DLQ after five
// attempts), not acked and not dropped.
func checkRetryable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("Deliver returned nil: the message would be acked")
	}
	if sdk.IsPermanent(err) {
		t.Fatalf("Deliver returned a permanent error (acked and dropped): %v", err)
	}
	if strings.Contains(err.Error(), "secret-token-735") || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("error carries the credential: %v", err)
	}
}

const threeRecords = `[{"Name":"r0","ExtId__c":"e0"},{"Name":"r1","ExtId__c":"e1"},{"Name":"r2","ExtId__c":"e2"}]`

// A record Salesforce refuses dead-letters the message with the record's
// index and Salesforce's error, and the records that went in are not sent
// again on any of the redeliveries.
func TestREST_RejectedRecordIsDeadLetteredAndOthersNotResent(t *testing.T) {
	cases := []struct {
		name     string
		op       string
		payload  string
		rep      reply
		wantText []string
		wantR1   int // posts of r1 over five attempts
	}{
		{
			name:     "insert validation error",
			op:       "insert",
			payload:  threeRecords,
			rep:      reply{status: 400, body: `[{"message":"Required fields are missing: [Phone]","errorCode":"REQUIRED_FIELD_MISSING","fields":["Phone"]}]`},
			wantText: []string{"record[1]", "400", "REQUIRED_FIELD_MISSING", "Required fields are missing"},
			wantR1:   5,
		},
		{
			name:     "upsert custom validation",
			op:       "upsert",
			payload:  threeRecords,
			rep:      reply{status: 400, body: `[{"message":"Bad region","errorCode":"FIELD_CUSTOM_VALIDATION_EXCEPTION","fields":[]}]`},
			wantText: []string{"record[1]", "FIELD_CUSTOM_VALIDATION_EXCEPTION", "Bad region"},
			wantR1:   5,
		},
		{
			name:     "duplicate on insert is not exempt",
			op:       "insert",
			payload:  threeRecords,
			rep:      reply{status: 400, body: `[{"message":"duplicate value found","errorCode":"DUPLICATE_VALUE","fields":[]}]`},
			wantText: []string{"record[1]", "DUPLICATE_VALUE"},
			wantR1:   5,
		},
		{
			name:     "404 without an error body",
			op:       "insert",
			payload:  threeRecords,
			rep:      reply{status: 404, body: `not here`},
			wantText: []string{"record[1]", "404", "not here"},
			wantR1:   5,
		},
		{
			name:     "upsert matching several records",
			op:       "upsert",
			payload:  threeRecords,
			rep:      reply{status: 300, body: `["/services/data/v60.0/sobjects/Account/001A","/services/data/v60.0/sobjects/Account/001B"]`},
			wantText: []string{"record[1]", "300"},
			wantR1:   5,
		},
		{
			name:     "upsert record with no external id",
			op:       "upsert",
			payload:  `[{"Name":"r0","ExtId__c":"e0"},{"Name":"r1"},{"Name":"r2","ExtId__c":"e2"}]`,
			wantText: []string{"record[1]", "no value for external id field ExtId__c"},
			wantR1:   0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sf := newFakeSF(t, func(key string, _ int) (reply, bool) {
				if key == "r1" && tc.rep.status != 0 {
					return tc.rep, true
				}
				return reply{}, false
			})
			p := newTestProducer(t, 200, nil, target("p1", sf.srv.URL, "Account", tc.op))
			env := testEnvelope(tc.payload)

			for attempt := 1; attempt <= 5; attempt++ {
				err := p.Deliver(context.Background(), env)
				checkRetryable(t, err)
				for _, want := range tc.wantText {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("attempt %d: error %q does not contain %q", attempt, err, want)
					}
				}
				if strings.Contains(err.Error(), "record[0]") || strings.Contains(err.Error(), "record[2]") {
					t.Errorf("attempt %d: error names a record that went in: %v", attempt, err)
				}
			}
			if got := sf.count("r0"); got != 1 {
				t.Errorf("r0 posted %d times over five attempts, want 1", got)
			}
			if got := sf.count("r2"); got != 1 {
				t.Errorf("r2 posted %d times over five attempts, want 1", got)
			}
			if got := sf.count("r1"); got != tc.wantR1 {
				t.Errorf("r1 posted %d times, want %d", got, tc.wantR1)
			}
		})
	}
}

// On an upsert a duplicate is still a refused record: DUPLICATE_VALUE means
// another unique field clashed or a concurrent insert won, and
// DUPLICATES_DETECTED is a blocking duplicate rule, so the request's values
// were not written. Acking would lose them (review of #759).
func TestREST_UpsertDuplicatesAreRefused(t *testing.T) {
	dup := reply{status: 400, body: `[{"message":"duplicate value found: Email__c","errorCode":"DUPLICATE_VALUE","fields":[]}]`}
	rules := reply{status: 400, body: `[{"message":"Use one of these records?","errorCode":"DUPLICATES_DETECTED","fields":[]}]`}
	invalid := reply{status: 400, body: `[{"message":"bad","errorCode":"INVALID_FIELD","fields":[]}]`}
	cases := []struct {
		name     string
		replies  map[string]reply
		wantText []string
		notText  []string
	}{
		{name: "one duplicate", replies: map[string]reply{"r1": dup},
			wantText: []string{"record[1]: 400 DUPLICATE_VALUE"}, notText: []string{"record[0]", "record[2]"}},
		{name: "duplicate rules", replies: map[string]reply{"r0": rules, "r2": dup},
			wantText: []string{"2 of 3", "record[0]: 400 DUPLICATES_DETECTED", "record[2]: 400 DUPLICATE_VALUE"}, notText: []string{"record[1]"}},
		{name: "duplicate and a validation error", replies: map[string]reply{"r0": dup, "r2": invalid},
			wantText: []string{"record[0]: 400 DUPLICATE_VALUE", "record[2]: 400 INVALID_FIELD"}, notText: []string{"record[1]"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sf := newFakeSF(t, func(key string, _ int) (reply, bool) {
				r, ok := tc.replies[key]
				return r, ok
			})
			p := newTestProducer(t, 200, nil, target("p1", sf.srv.URL, "Account", "upsert"))
			env := testEnvelope(threeRecords)
			for attempt := 1; attempt <= 2; attempt++ {
				err := p.Deliver(context.Background(), env)
				checkRetryable(t, err)
				for _, want := range tc.wantText {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("attempt %d: error %q does not contain %q", attempt, err, want)
					}
				}
				for _, not := range tc.notText {
					if strings.Contains(err.Error(), not) {
						t.Errorf("attempt %d: error %q names %s, which went in", attempt, err, not)
					}
				}
			}
			// The refused records are sent again; the one that went in is not.
			for _, k := range []string{"r0", "r1", "r2"} {
				want := 1
				if _, refused := tc.replies[k]; refused {
					want = 2
				}
				if got := sf.count(k); got != want {
					t.Errorf("%s sent %d times over two attempts, want %d", k, got, want)
				}
			}
		})
	}
}

// The skip-set key: after a delivery failed partway (r0 went in and is in
// the skip set), a delivery that differs in one part of the key is a
// different record in a different target, so every record is sent. Each
// case pins one field of writtenKey.
func TestREST_SkipSetKeyedByMessageAndTarget(t *testing.T) {
	sfOther := newFakeSF(t, nil) // a second org
	cases := []struct {
		name   string
		base   string // operation of the first delivery
		change func(t *testing.T, p *salesforceProducer, env *envelope.Envelope, sf *fakeSF)
	}{
		{name: "another envelope with the same payload", base: "insert", change: func(_ *testing.T, _ *salesforceProducer, env *envelope.Envelope, _ *fakeSF) {
			env.ID = "env-735-other"
		}},
		{name: "node re-pointed to another instance", base: "insert", change: func(_ *testing.T, p *salesforceProducer, _ *envelope.Envelope, _ *fakeSF) {
			p.cache[testConn] = []*sfTarget{target("p1", sfOther.srv.URL, "Account", "insert")}
		}},
		{name: "node re-pointed to another object", base: "insert", change: func(_ *testing.T, p *salesforceProducer, _ *envelope.Envelope, sf *fakeSF) {
			p.cache[testConn] = []*sfTarget{target("p1", sf.srv.URL, "Contact", "insert")}
		}},
		{name: "node switched to another operation", base: "insert", change: func(_ *testing.T, p *salesforceProducer, _ *envelope.Envelope, sf *fakeSF) {
			p.cache[testConn] = []*sfTarget{target("p1", sf.srv.URL, "Account", "upsert")}
		}},
		{name: "upsert on another external id field", base: "upsert", change: func(_ *testing.T, p *salesforceProducer, _ *envelope.Envelope, sf *fakeSF) {
			tg := target("p1", sf.srv.URL, "Account", "upsert")
			tg.cfg.ExternalIDField = "Other__c"
			p.cache[testConn] = []*sfTarget{tg}
		}},
		{name: "a second node with the same settings", base: "insert", change: func(_ *testing.T, p *salesforceProducer, _ *envelope.Envelope, sf *fakeSF) {
			p.cache[testConn] = []*sfTarget{target("p1", sf.srv.URL, "Account", "insert"), target("p2", sf.srv.URL, "Account", "insert")}
		}},
	}
	const payload = `[{"Name":"r0","ExtId__c":"e0","Other__c":"o0"},{"Name":"r1","ExtId__c":"e1","Other__c":"o1"},{"Name":"r2","ExtId__c":"e2","Other__c":"o2"}]`
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sf := newFakeSF(t, func(key string, n int) (reply, bool) {
				return reply{status: 503, body: `[{"message":"down","errorCode":"SERVER_UNAVAILABLE"}]`}, key == "r1" && n == 0
			})
			p := newTestProducer(t, 200, nil, target("p1", sf.srv.URL, "Account", tc.base))
			env := testEnvelope(payload)
			checkRetryable(t, p.Deliver(context.Background(), env))
			if n := p.written.size(); n != 1 {
				t.Fatalf("skip set holds %d entries after the partial delivery, want 1 (r0)", n)
			}
			before := sf.count("r0") + sfOther.count("r0")

			tc.change(t, p, env, sf)
			if err := p.Deliver(context.Background(), env); err != nil {
				t.Fatalf("second delivery: %v", err)
			}
			if got := sf.count("r0") + sfOther.count("r0") - before; got < 1 {
				t.Errorf("r0 was not sent by the changed delivery: the skip set matched a different key")
			}
		})
	}
}

// Deliveries run concurrently and share the cached target; filling in the
// API version default must not write to it (run with -race).
func TestDeliver_ConcurrentSharesTargetWithoutRace(t *testing.T) {
	sf := newFakeSF(t, nil)
	tg := target("p1", sf.srv.URL, "Account", "insert") // APIVersion empty
	p := newTestProducer(t, 200, nil, tg)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			env := testEnvelope(fmt.Sprintf(`{"Name":"c%d"}`, i))
			env.ID = fmt.Sprintf("env-concurrent-%d", i)
			if err := p.Deliver(context.Background(), env); err != nil {
				t.Errorf("Deliver %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if tg.cfg.APIVersion != "" {
		t.Errorf("cached target's APIVersion was set to %q; it must stay as configured", tg.cfg.APIVersion)
	}
}

// A transient failure mid-message: the delivery is retried, and the retry
// sends only the records that had not gone in.
func TestREST_TransientMidMessageRetriesOnlyTheRest(t *testing.T) {
	cases := []struct {
		name       string
		rep        reply
		wantText   string
		wantForced int32
	}{
		{name: "500", rep: reply{status: 500, body: `[{"message":"oops","errorCode":"UNKNOWN_EXCEPTION"}]`}, wantText: "500 UNKNOWN_EXCEPTION"},
		{name: "503", rep: reply{status: 503, body: `[{"message":"down","errorCode":"SERVER_UNAVAILABLE"}]`}, wantText: "503 SERVER_UNAVAILABLE"},
		{name: "429", rep: reply{status: 429, body: `[{"message":"limit","errorCode":"REQUEST_LIMIT_EXCEEDED"}]`}, wantText: "429 REQUEST_LIMIT_EXCEEDED"},
		{name: "401 after refresh", rep: reply{status: 401, body: `[{"message":"Session expired or invalid","errorCode":"INVALID_SESSION_ID"}]`}, wantText: "401 INVALID_SESSION_ID", wantForced: 1},
		{name: "403", rep: reply{status: 403, body: `[{"message":"no access","errorCode":"INSUFFICIENT_ACCESS"}]`}, wantText: "403 INSUFFICIENT_ACCESS"},
		{name: "network error", rep: reply{drop: true}, wantText: "record[1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var failing atomic.Bool
			failing.Store(true)
			sf := newFakeSF(t, func(key string, _ int) (reply, bool) {
				if key == "r1" && failing.Load() {
					return tc.rep, true
				}
				return reply{}, false
			})
			var forced atomic.Int32
			p := newTestProducer(t, 200, &forced, target("p1", sf.srv.URL, "Account", "insert"))
			env := testEnvelope(threeRecords)

			err := p.Deliver(context.Background(), env)
			checkRetryable(t, err)
			if !strings.Contains(err.Error(), tc.wantText) || !strings.Contains(err.Error(), "record[1]") {
				t.Errorf("error %q does not name record[1] and %q", err, tc.wantText)
			}
			if got := sf.count("r2"); got != 0 {
				t.Errorf("r2 sent %d times after the transient failure, want 0 (the loop stops)", got)
			}
			if got := forced.Load(); got != tc.wantForced {
				t.Errorf("forced token refreshes = %d, want %d", got, tc.wantForced)
			}

			failing.Store(false)
			if err := p.Deliver(context.Background(), env); err != nil {
				t.Fatalf("retry: Deliver = %v, want nil", err)
			}
			// r1 reached the fake once per attempt (the dropped request counts
			// too), plus the resend after the token refresh on a 401.
			want := map[string]int{"r0": 1, "r1": 2 + int(tc.wantForced), "r2": 1}
			for k, n := range want {
				if got := sf.count(k); got != n {
					t.Errorf("%s sent %d times, want %d", k, got, n)
				}
			}
			if n := p.written.size(); n != 0 {
				t.Errorf("skip set holds %d entries after the message completed, want 0", n)
			}
		})
	}
}

// The REST and Bulk paths classify an answer the same way: every failure is
// retryable, names the status and Salesforce's code, and says "rejected" for
// the same statuses.
func TestClassification_RESTAndBulkAgree(t *testing.T) {
	cases := []struct {
		status       int
		code         string
		wantRejected bool
	}{
		{status: 400, code: "INVALID_FIELD", wantRejected: true},
		{status: 404, code: "NOT_FOUND", wantRejected: true},
		{status: 409, code: "CONFLICT", wantRejected: true},
		{status: 401, code: "INVALID_SESSION_ID"},
		{status: 403, code: "INSUFFICIENT_ACCESS"},
		{status: 408, code: "TIMEOUT"},
		{status: 429, code: "REQUEST_LIMIT_EXCEEDED"},
		{status: 500, code: "UNKNOWN_EXCEPTION"},
		{status: 503, code: "SERVER_UNAVAILABLE"},
		{status: 302, code: "REDIRECT"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			rep := reply{status: tc.status, body: fmt.Sprintf(`[{"message":"m-%d","errorCode":%q}]`, tc.status, tc.code)}
			for _, path := range []struct {
				name      string
				threshold int
				key       string
			}{
				{name: "rest", threshold: 200, key: "r0"},
				{name: "bulk", threshold: 1, key: "create:Account"},
			} {
				sf := newFakeSF(t, func(key string, _ int) (reply, bool) { return rep, key == path.key })
				p := newTestProducer(t, path.threshold, nil, target("p1", sf.srv.URL, "Account", "insert"))
				err := p.Deliver(context.Background(), testEnvelope(`[{"Name":"r0"}]`))
				checkRetryable(t, err)
				msg := err.Error()
				for _, want := range []string{fmt.Sprint(tc.status), tc.code, fmt.Sprintf("m-%d", tc.status)} {
					if !strings.Contains(msg, want) {
						t.Errorf("%s: error %q does not contain %q", path.name, msg, want)
					}
				}
				if got := strings.Contains(msg, "rejected"); got != tc.wantRejected {
					t.Errorf("%s: rejected = %v, want %v (%q)", path.name, got, tc.wantRejected, msg)
				}
			}
		})
	}
}

// A Bulk job that went in for one target is not submitted again when the
// message comes back because another target failed.
func TestBulk_SubmittedJobNotResubmittedOnRedelivery(t *testing.T) {
	// The Contact counts: a failed create or upload makes the retry create a
	// new job (the failed upload's job is aborted first); a close whose answer
	// was lost makes the retry read the job's state instead (#760), which
	// this fake answers JobComplete.
	cases := []struct {
		name       string
		failKey    string
		rep        reply
		wantCreate int
		wantClose  int
		wantAbort  int
	}{
		{name: "create 500", failKey: "create:Contact", rep: reply{status: 500}, wantCreate: 2, wantClose: 1},
		{name: "upload 400", failKey: "upload:Contact", rep: reply{status: 400, body: `[{"message":"bad csv","errorCode":"INVALIDJOB"}]`}, wantCreate: 2, wantClose: 1, wantAbort: 1},
		{name: "close network error", failKey: "close:Contact", rep: reply{drop: true}, wantCreate: 1, wantClose: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sf := newFakeSF(t, func(key string, n int) (reply, bool) {
				return tc.rep, key == tc.failKey && n == 0
			})
			p := newTestProducer(t, 2, nil,
				target("pA", sf.srv.URL, "Account", "insert"),
				target("pB", sf.srv.URL, "Contact", "insert"))
			env := testEnvelope(`[{"Name":"a"},{"Name":"b"}]`)

			err := p.Deliver(context.Background(), env)
			checkRetryable(t, err)
			if !strings.Contains(err.Error(), "node pB") || strings.Contains(err.Error(), "node pA") {
				t.Errorf("error %q should name node pB only", err)
			}
			if err := p.Deliver(context.Background(), env); err != nil {
				t.Fatalf("retry: Deliver = %v, want nil", err)
			}
			if got := sf.count("create:Account"); got != 1 {
				t.Errorf("Account bulk job created %d times, want 1", got)
			}
			for _, c := range []struct {
				key  string
				want int
			}{{"create:Contact", tc.wantCreate}, {"close:Contact", tc.wantClose}, {"abort:Contact", tc.wantAbort}, {"close:Account", 1}} {
				if got := sf.count(c.key); got != c.want {
					t.Errorf("%s called %d times, want %d", c.key, got, c.want)
				}
			}
		})
	}
}

// Through the SDK runner: a rejected record NAKs the message (on main it was
// acked), and the redelivery sends only that record.
func TestHarness_RedeliverySendsOnlyTheRejectedRecord(t *testing.T) {
	sf := newFakeSF(t, func(key string, n int) (reply, bool) {
		if key == "r1" && n == 0 {
			return reply{status: 400, body: `[{"message":"locked","errorCode":"UNABLE_TO_LOCK_ROW"}]`}, true
		}
		return reply{}, false
	})
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mockConn(mock, testConn, sf.srv.URL, "insert")

	p := &salesforceProducer{
		resolveToken: func(context.Context, string, string, bool) (string, error) { return "tok", nil },
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "salesforce-producer", DB: db})
	h.Publish(t, testEnvelope(threeRecords))

	harness.Eventually(t, 15*time.Second, "r1 sent again on the redelivery", func() bool {
		return sf.count("r1") == 2
	})
	h.ExpectNoDLQ(t, 500*time.Millisecond)
	for _, k := range []string{"r0", "r2"} {
		if got := sf.count(k); got != 1 {
			t.Errorf("%s sent %d times, want 1", k, got)
		}
	}
}
