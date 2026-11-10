package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// numbersPayload has the numbers #765 names: 1000000 and 12345678 print as
// 1e+06 and 1.2345678e+07 through a float64, 9007199254740993 (2^53+1) is
// not a float64 at all, and a decimal with a trailing zero must keep it.
const numbersPayload = `{"ExtId__c":12345678,"Million":1000000,"Big":9007199254740993,"Dec":0.10,"Neg":-1.5e3,"Name":"Acme","Nope":null}`

func TestRecordsToCSV_NumbersAndNulls(t *testing.T) {
	recs, err := parseRecords([]byte(`[` + numbersPayload + `,{"ExtId__c":2,"Name":"B"}]`))
	if err != nil {
		t.Fatalf("parseRecords: %v", err)
	}
	// The sorted union of keys; the second record lacks most of them.
	const header = "Big,Dec,ExtId__c,Million,Name,Neg,Nope\n"
	tests := []struct {
		op   string
		want string
	}{
		// Insert: null and absent both stay empty.
		{"insert", header + "9007199254740993,0.10,12345678,1000000,Acme,-1500,\n,,2,,B,,\n"},
		// Upsert: an explicit null is #N/A, an absent field stays empty.
		{"upsert", header + "9007199254740993,0.10,12345678,1000000,Acme,-1500,#N/A\n,,2,,B,,\n"},
	}
	for _, tc := range tests {
		t.Run(tc.op, func(t *testing.T) {
			got, err := recordsToCSV(recs, tc.op)
			if err != nil {
				t.Fatalf("recordsToCSV: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("CSV =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestPlainNumber(t *testing.T) {
	for in, want := range map[string]string{
		"1e+16": "10000000000000000", "1E2": "100", "-1.5e3": "-1500", "1.25e-2": "0.0125",
		"1e-3": "0.001", "5e-1": "0.5", "1.0e0": "1", "-0e5": "0", "12345678.5e1": "123456785",
		"9007199254740993e2": "900719925474099300", "2.5e2": "250",
		// More factors of 5 than 2 in the denominator.
		"2e-1": "0.2", "-4e-2": "-0.04", "8e-3": "0.008",
		// Plain forms are untouched, trailing zeros included.
		"0.10": "0.10", "1000000": "1000000", "-3": "-3", "9007199254740993": "9007199254740993",
		// Too large to expand: left as sent.
		"1e999999": "1e999999", "1e-999999": "1e-999999",
	} {
		if got := plainNumber(json.Number(in)); string(got) != want {
			t.Errorf("plainNumber(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestParseRecords_ExponentNumbersAreExpanded(t *testing.T) {
	recs, err := parseRecords([]byte(`[{"A":1e+16,"N":{"B":[2.5E1,0.10]}}]`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(recs[0])
	if want := `{"A":10000000000000000,"N":{"B":[25,0.10]}}`; string(b) != want {
		t.Errorf("record = %s, want %s", b, want)
	}
}

func TestParseRecords_TrailingDataIsBad(t *testing.T) {
	for _, in := range []string{`{"a":1} {"b":2}`, `[{"a":1}] x`} {
		if _, err := parseRecords([]byte(in)); err == nil {
			t.Errorf("parseRecords(%q) succeeded, want an error", in)
		}
	}
}

// A Bulk upsert refuses locally, as REST does, a record whose external id is
// absent, null or "": never an empty or #N/A key column in the CSV.
func TestSendable_UpsertWithoutExternalIDIsRefused(t *testing.T) {
	recs, err := parseRecords([]byte(`[{"ExtId__c":null,"Name":"A"},{"Name":"B"},{"ExtId__c":"","Name":"C"},{"ExtId__c":0,"Name":"D"},{"ExtId__c":"x","Name":"E"}]`))
	if err != nil {
		t.Fatal(err)
	}
	send, local := sendable(recs, []int{0, 1, 2, 3, 4}, "upsert", "ExtId__c")
	if len(send) != 2 || send[0] != 3 || send[1] != 4 {
		t.Errorf("send = %v, want [3 4]", send)
	}
	for _, i := range []int{0, 1, 2} {
		if !strings.Contains(local[i], "no value for external id field ExtId__c") {
			t.Errorf("local[%d] = %q, want the external id refusal", i, local[i])
		}
	}
	if len(local) != 3 {
		t.Errorf("local = %v, want records 0-2 only", local)
	}
	// An insert has no key to require.
	if send, local := sendable(recs, []int{0, 1, 2}, "insert", ""); len(send) != 3 || len(local) != 0 {
		t.Errorf("insert: send %v local %v, want all sent", send, local)
	}
}

type sfCall struct{ method, path, body string }

// capture is a Salesforce fake that records every request it gets and
// answers REST writes and the Bulk job steps.
type capture struct {
	mu    sync.Mutex
	calls []sfCall
}

func (c *capture) handler(processed int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.calls = append(c.calls, sfCall{r.Method, r.URL.EscapedPath(), string(b)})
		c.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/jobs/ingest") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"job-1","state":"Open"}`))
		case strings.HasSuffix(r.URL.Path, "/batches"):
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/jobs/ingest/job-1") && r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"id":"job-1","state":"UploadComplete"}`))
		case strings.HasSuffix(r.URL.Path, "/jobs/ingest/job-1"):
			_, _ = fmt.Fprintf(w, `{"id":"job-1","state":"JobComplete","numberRecordsProcessed":%d,"numberRecordsFailed":0}`, processed)
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"001","success":true}`))
		}
	})
}

func (c *capture) find(t *testing.T, method, suffix string) sfCall {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for _, x := range c.calls {
			if x.method == method && strings.HasSuffix(x.path, suffix) {
				c.mu.Unlock()
				return x
			}
		}
		c.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s ...%s request; got %+v", method, suffix, c.calls)
	return sfCall{}
}

func payloadHarness(t *testing.T, c *capture, nodeSF string, threshold, processed int, backoff []time.Duration) *harness.ProducerHarness {
	t.Helper()
	srv := httptest.NewServer(c.handler(processed))
	t.Cleanup(srv.Close)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	nodes := fmt.Sprintf(`[{"id":"p1","type":"producer","config":{"type":"salesforce","salesforce":{"instance_url":%q,"oauth_grant_id":"g1","object":"Account",%s}}}]`, srv.URL, nodeSF)
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 8; i++ {
		mock.ExpectQuery("FROM connections WHERE id").WithArgs(testConn).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
	}
	p := &salesforceProducer{
		bulkThreshold:   threshold,
		bulkPollInitial: time.Millisecond,
		bulkPollMax:     time.Millisecond,
		resolveToken:    func(context.Context, string, string, bool) (string, error) { return "tok", nil },
	}
	return harness.NewProducerHarness(t, p, harness.Options{Name: "salesforce-producer", DB: db, Backoff: backoff})
}

func publish(t *testing.T, h *harness.ProducerHarness, payload string) *envelope.Envelope {
	t.Helper()
	env := envelope.New()
	env.ID = "pl-1"
	env.IntegrationID = testConn
	env.TenantID = "tenant-1"
	env.Payload = []byte(payload)
	h.Publish(t, env)
	return env
}

func TestREST_InsertBodyKeepsNumbersAndNull(t *testing.T) {
	c := &capture{}
	h := payloadHarness(t, c, `"operation":"insert"`, 100, 0, nil)
	publish(t, h, numbersPayload)
	got := c.find(t, http.MethodPost, "/sobjects/Account")
	// Compact, key-sorted by encoding/json, numbers verbatim, null kept.
	const want = `{"Big":9007199254740993,"Dec":0.10,"ExtId__c":12345678,"Million":1000000,"Name":"Acme","Neg":-1500,"Nope":null}`
	if got.body != want {
		t.Errorf("REST body = %s\nwant      %s", got.body, want)
	}
}

func TestREST_UpsertURLUsesTheNumberAsSent(t *testing.T) {
	c := &capture{}
	h := payloadHarness(t, c, `"operation":"upsert","external_id_field":"ExtId__c"`, 100, 0, nil)
	publish(t, h, `{"ExtId__c":12345678,"Name":"A"}`)
	got := c.find(t, http.MethodPatch, "/sobjects/Account/ExtId__c/12345678")
	if got.body != `{"ExtId__c":12345678,"Name":"A"}` {
		t.Errorf("REST body = %s", got.body)
	}
}

func TestREST_UpsertURLLargeInteger(t *testing.T) {
	c := &capture{}
	h := payloadHarness(t, c, `"operation":"upsert","external_id_field":"ExtId__c"`, 100, 0, nil)
	publish(t, h, `{"ExtId__c":9007199254740993}`)
	c.find(t, http.MethodPatch, "/sobjects/Account/ExtId__c/9007199254740993")
}

func TestREST_UpsertURLExponentFormIsPlainDigits(t *testing.T) {
	c := &capture{}
	h := payloadHarness(t, c, `"operation":"upsert","external_id_field":"ExtId__c"`, 100, 0, nil)
	publish(t, h, `{"ExtId__c":1e+16}`)
	got := c.find(t, http.MethodPatch, "/sobjects/Account/ExtId__c/10000000000000000")
	if got.body != `{"ExtId__c":10000000000000000}` {
		t.Errorf("REST body = %s", got.body)
	}
}

func TestBulk_UploadedCSV(t *testing.T) {
	const payload = `[{"ExtId__c":1000000,"Big":9007199254740993,"Dec":0.10,"Gone":null},{"ExtId__c":12345678,"Big":2,"Dec":1.5}]`
	for _, tc := range []struct{ op, nodeSF, want string }{
		{"insert", `"operation":"insert"`,
			"Big,Dec,ExtId__c,Gone\n9007199254740993,0.10,1000000,\n2,1.5,12345678,\n"},
		{"upsert", `"operation":"upsert","external_id_field":"ExtId__c"`,
			"Big,Dec,ExtId__c,Gone\n9007199254740993,0.10,1000000,#N/A\n2,1.5,12345678,\n"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			c := &capture{}
			h := payloadHarness(t, c, tc.nodeSF, 2, 2, nil)
			publish(t, h, payload)
			if got := c.find(t, http.MethodPut, "/batches").body; got != tc.want {
				t.Errorf("uploaded CSV =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// An upsert node with no external_id_field is dead-lettered, not acked.
func TestUpsertWithoutExternalIDFieldIsDeadLettered(t *testing.T) {
	c := &capture{}
	fast := []time.Duration{20 * time.Millisecond, 20 * time.Millisecond, 20 * time.Millisecond, 20 * time.Millisecond}
	h := payloadHarness(t, c, `"operation":"upsert"`, 100, 0, fast)
	env := publish(t, h, `{"Name":"A"}`)
	if got := h.ExpectDLQ(t, 15*time.Second); got.ID != env.ID {
		t.Fatalf("DLQ holds %s, want %s", got.ID, env.ID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) != 0 {
		t.Errorf("%d requests sent to Salesforce, want none", len(c.calls))
	}
}
