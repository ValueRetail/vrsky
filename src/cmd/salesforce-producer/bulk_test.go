package main

import (
	"bytes"
	"context"
	"encoding/csv"
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
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// fakeJob is one Bulk API 2.0 ingest job in fakeBulk.
type fakeJob struct {
	id     string
	object string
	state  string
	header []string
	rows   [][]string
	reads  int // state reads after the close

	processed   int
	failed      [][]string // sf__Id, sf__Error, then the uploaded columns
	unprocessed [][]string
	errorMsg    string
}

// fakeBulk is a stateful fake of Salesforce's Bulk API 2.0. A job is
// processed when a state read finds it closed and done reading: each row is
// taken in (counted in taken by its Name), refused (refuse returns its
// sf__Error) or, past processLimit on a Failed job, left unprocessed. Result
// rows come back in reverse order, as Salesforce gives no order.
type fakeBulk struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// knobs, set by the test (under mu once the producer runs)
	refuse       func(row map[string]string) string
	inProgress   int    // state reads answered InProgress before the job finishes; -1 never
	endState     string // JobComplete (default), Failed or Aborted
	processLimit int    // rows a Failed/Aborted job processes; 0 none
	errorMessage string
	// fail scripts an answer for a step (create, upload, close, abort,
	// state, failedResults, unprocessedrecords); n counts earlier calls.
	// applyClose: a scripted close answer still closes the job.
	fail       func(step string, n int) (reply, bool)
	applyClose bool
	// echo rewrites a result row's uploaded values before it is sent.
	echo func(vals []string) []string

	jobs  []*fakeJob
	calls map[string]int
	taken map[string]int // Name → times a job took the row in
}

func newFakeBulk(t *testing.T) *fakeBulk {
	t.Helper()
	f := &fakeBulk{t: t, endState: jobComplete, calls: map[string]int{}, taken: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBulk) set(fn func(f *fakeBulk)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeBulk) count(step string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[step]
}

func (f *fakeBulk) takenOf(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.taken[name]
}

// jobNames is the Name column of every job's upload, in creation order.
func (f *fakeBulk) jobNames() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, j := range f.jobs {
		col := indexOf(j.header, "Name")
		var names []string
		for _, r := range j.rows {
			if col >= 0 {
				names = append(names, r[col])
			}
		}
		out = append(out, names)
	}
	return out
}

func (f *fakeBulk) states() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, j := range f.jobs {
		out = append(out, j.state)
	}
	return out
}

func (f *fakeBulk) job(id string) *fakeJob {
	for _, j := range f.jobs {
		if j.id == id {
			return j
		}
	}
	return nil
}

func (f *fakeBulk) process(j *fakeJob) {
	j.state = f.endState
	j.errorMsg = f.errorMessage
	limit := len(j.rows)
	if f.endState != jobComplete {
		limit = f.processLimit
	}
	nameCol := indexOf(j.header, "Name")
	for n, r := range j.rows {
		if n >= limit {
			j.unprocessed = append([][]string{f.echoed(r)}, j.unprocessed...)
			continue
		}
		j.processed++
		row := map[string]string{}
		for c, h := range j.header {
			row[h] = r[c]
		}
		if f.refuse != nil {
			if why := f.refuse(row); why != "" {
				j.failed = append([][]string{append([]string{"", why}, f.echoed(r)...)}, j.failed...)
				continue
			}
		}
		if nameCol >= 0 {
			f.taken[r[nameCol]]++
		}
	}
}

func (f *fakeBulk) echoed(r []string) []string {
	out := append([]string(nil), r...)
	if f.echo != nil {
		out = f.echo(out)
	}
	return out
}

func writeCSV(header []string, rows [][]string) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(header)
	_ = w.WriteAll(rows)
	return buf.Bytes()
}

func (f *fakeBulk) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimSuffix(r.URL.Path, "/")
	i := strings.Index(path, "/jobs/ingest")
	if i < 0 {
		f.t.Errorf("fake Bulk API: unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path[i:], "/jobs/ingest"), "/") // "", id, sub

	f.mu.Lock()
	defer f.mu.Unlock()

	step := ""
	switch {
	case r.Method == http.MethodPost && len(parts) == 1:
		step = "create"
	case r.Method == http.MethodPut && len(parts) == 3 && parts[2] == "batches":
		step = "upload"
	case r.Method == http.MethodPatch && len(parts) == 2:
		step = "close"
		if strings.Contains(string(body), jobAborted) {
			step = "abort"
		}
	case r.Method == http.MethodGet && len(parts) == 2:
		step = "state"
	case r.Method == http.MethodGet && len(parts) == 3:
		step = parts[2]
	default:
		f.t.Errorf("fake Bulk API: unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	n := f.calls[step]
	f.calls[step] = n + 1

	var j *fakeJob
	if len(parts) >= 2 {
		if j = f.job(parts[1]); j == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`[{"errorCode":"NOT_FOUND","message":"no such job"}]`))
			return
		}
	}

	if f.fail != nil {
		if rep, scripted := f.fail(step, n); scripted {
			if step == "close" && f.applyClose {
				j.state = jobUploadComplete
			}
			if rep.drop {
				hj, _ := w.(http.Hijacker)
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
				}
				return
			}
			w.WriteHeader(rep.status)
			_, _ = w.Write([]byte(rep.body))
			return
		}
	}

	switch step {
	case "create":
		var req map[string]string
		_ = json.Unmarshal(body, &req)
		j = &fakeJob{id: fmt.Sprintf("750J%04d", len(f.jobs)+1), object: req["object"], state: jobOpen}
		f.jobs = append(f.jobs, j)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"id":%q,"state":"Open"}`, j.id)
	case "upload":
		if j.state != jobOpen || j.header != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
		if err != nil || len(rows) == 0 {
			f.t.Errorf("fake Bulk API: upload is not CSV: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		j.header, j.rows = rows[0], rows[1:]
		w.WriteHeader(http.StatusCreated)
	case "close", "abort":
		if j.state != jobOpen {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`[{"errorCode":"INVALIDJOBSTATE","message":"not open"}]`))
			return
		}
		j.state = jobUploadComplete
		if step == "abort" {
			j.state = jobAborted
		}
		_, _ = fmt.Fprintf(w, `{"id":%q,"state":%q}`, j.id, j.state)
	case "state":
		if j.state == jobUploadComplete || j.state == jobInProgress {
			if f.inProgress < 0 || j.reads < f.inProgress {
				j.state = jobInProgress
			} else {
				f.process(j)
			}
			j.reads++
		}
		failed := len(j.failed)
		_, _ = fmt.Fprintf(w, `{"id":%q,"state":%q,"numberRecordsProcessed":%d,"numberRecordsFailed":%d,"errorMessage":%q}`,
			j.id, j.state, j.processed, failed, j.errorMsg)
	case "failedResults":
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write(writeCSV(append([]string{"sf__Id", "sf__Error"}, j.header...), j.failed))
	case "unprocessedrecords":
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write(writeCSV(j.header, j.unprocessed))
	}
}

// newBulkProducer is a test producer that sends every message through the
// Bulk API, polling fast.
func newBulkProducer(t *testing.T, f *fakeBulk, op string) *salesforceProducer {
	t.Helper()
	p := newTestProducer(t, 1, nil, target("p1", f.srv.URL, "Account", op))
	p.bulkPollInitial = time.Millisecond
	p.bulkPollMax = 5 * time.Millisecond
	p.bulkPollTimeout = 5 * time.Second
	return p
}

const fiveRecords = `[{"Name":"r0","ExtId__c":"e0"},{"Name":"r1","ExtId__c":"e1"},{"Name":"r2","ExtId__c":"e2"},{"Name":"r3","ExtId__c":"e3"},{"Name":"r4","ExtId__c":"e4"}]`

func refuseNames(why map[string]string) func(map[string]string) string {
	return func(row map[string]string) string { return why[row["Name"]] }
}

func checkContains(t *testing.T, err error, want, not []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err, w)
		}
	}
	for _, n := range not {
		if strings.Contains(err.Error(), n) {
			t.Errorf("error %q contains %q", err, n)
		}
	}
}

// Records the job refuses fail the delivery, named by index with
// Salesforce's code, on every attempt; the ones it took in are never sent
// again; each retry submits a job with only the refused records.
func TestBulk_RefusedRecordsAreRetriedAndOthersNotResent(t *testing.T) {
	for _, op := range []string{"insert", "upsert"} {
		t.Run(op, func(t *testing.T) {
			f := newFakeBulk(t)
			f.refuse = refuseNames(map[string]string{
				"r1": "REQUIRED_FIELD_MISSING:Required fields are missing: [Phone]:Phone --",
				"r3": "FIELD_CUSTOM_VALIDATION_EXCEPTION:Bad region:--",
			})
			p := newBulkProducer(t, f, op)
			env := testEnvelope(fiveRecords)

			for attempt := 1; attempt <= 5; attempt++ {
				err := p.Deliver(context.Background(), env)
				checkRetryable(t, err)
				checkContains(t, err,
					[]string{"2 of 5", "record[1]: REQUIRED_FIELD_MISSING: Required fields are missing: [Phone]", "record[3]: FIELD_CUSTOM_VALIDATION_EXCEPTION: Bad region"},
					[]string{"record[0]", "record[2]", "record[4]"})
			}
			jobs := f.jobNames()
			if len(jobs) != 5 {
				t.Fatalf("%d jobs created over five attempts, want 5", len(jobs))
			}
			for n, names := range jobs[1:] {
				if strings.Join(names, ",") != "r1,r3" {
					t.Errorf("job %d holds %v, want only the refused r1,r3", n+2, names)
				}
			}
			for _, name := range []string{"r0", "r2", "r4"} {
				if got := f.takenOf(name); got != 1 {
					t.Errorf("%s taken in %d times, want 1", name, got)
				}
			}

			// Once Salesforce takes them, the message completes.
			f.set(func(f *fakeBulk) { f.refuse = nil })
			if err := p.Deliver(context.Background(), env); err != nil {
				t.Fatalf("Deliver after the fix = %v, want nil", err)
			}
			for _, name := range []string{"r0", "r1", "r2", "r3", "r4"} {
				if got := f.takenOf(name); got != 1 {
					t.Errorf("%s taken in %d times, want 1", name, got)
				}
			}
			if n, m := p.written.size(), p.jobs.size(); n != 0 || m != 0 {
				t.Errorf("skip set %d, job memory %d after the message completed, want 0 and 0", n, m)
			}
		})
	}
}

// Records with the same values are matched one result row each: only one
// of two identical records is named and sent again.
func TestBulk_IdenticalRecordsMatchedOncePerRow(t *testing.T) {
	f := newFakeBulk(t)
	refused := 0
	f.refuse = func(row map[string]string) string {
		if row["Name"] == "x" && refused == 0 {
			refused++
			return "UNABLE_TO_LOCK_ROW:locked:--"
		}
		return ""
	}
	p := newBulkProducer(t, f, "insert")
	env := testEnvelope(`[{"Name":"x"},{"Name":"x"},{"Name":"y"}]`)

	err := p.Deliver(context.Background(), env)
	checkRetryable(t, err)
	checkContains(t, err, []string{"1 of 3", "UNABLE_TO_LOCK_ROW"}, []string{"record[2]"})
	if err := p.Deliver(context.Background(), env); err != nil {
		t.Fatalf("retry = %v, want nil", err)
	}
	jobs := f.jobNames()
	if len(jobs) != 2 || strings.Join(jobs[1], ",") != "x" {
		t.Fatalf("jobs %v, want a second job with one x", jobs)
	}
	if got := f.takenOf("x"); got != 2 {
		t.Errorf("x taken in %d times, want 2", got)
	}
}

// A retry after the close failed finds the job of the first attempt: when
// the close went through it is polled, not submitted again; when it did not,
// the open job is aborted before a new one is created. Either way exactly
// one job is processed and every record goes in once.
func TestBulk_LostCloseDoesNotSubmitASecondJob(t *testing.T) {
	cases := []struct {
		name        string
		fail        func(step string, n int) (reply, bool)
		applyClose  bool
		attempts    int // deliveries until the message completes
		wantCreated int
		wantStates  []string
	}{
		{name: "close applied, answer lost", applyClose: true, attempts: 2, wantCreated: 1, wantStates: []string{jobComplete},
			fail: func(step string, n int) (reply, bool) { return reply{drop: true}, step == "close" && n == 0 }},
		{name: "close applied, answered 503", applyClose: true, attempts: 2, wantCreated: 1, wantStates: []string{jobComplete},
			fail: func(step string, n int) (reply, bool) { return reply{status: 503}, step == "close" && n == 0 }},
		{name: "close never arrived", attempts: 2, wantCreated: 2, wantStates: []string{jobAborted, jobComplete},
			fail: func(step string, n int) (reply, bool) { return reply{drop: true}, step == "close" && n == 0 }},
		{name: "close lost and the state read on the retry fails", applyClose: true, attempts: 3, wantCreated: 1, wantStates: []string{jobComplete},
			fail: func(step string, n int) (reply, bool) {
				return reply{drop: true}, (step == "close" && n == 0) || (step == "state" && n == 0)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeBulk(t)
			f.fail, f.applyClose = tc.fail, tc.applyClose
			p := newBulkProducer(t, f, "insert")
			env := testEnvelope(threeRecords)
			for attempt := 1; attempt < tc.attempts; attempt++ {
				checkRetryable(t, p.Deliver(context.Background(), env))
			}
			if err := p.Deliver(context.Background(), env); err != nil {
				t.Fatalf("attempt %d = %v, want nil", tc.attempts, err)
			}
			if got := f.count("create"); got != tc.wantCreated {
				t.Errorf("%d jobs created, want %d", got, tc.wantCreated)
			}
			if got := f.states(); strings.Join(got, ",") != strings.Join(tc.wantStates, ",") {
				t.Errorf("job states %v, want %v", got, tc.wantStates)
			}
			for _, name := range []string{"r0", "r1", "r2"} {
				if got := f.takenOf(name); got != 1 {
					t.Errorf("%s taken in %d times, want 1", name, got)
				}
			}
		})
	}
}

// A failed upload aborts the job, so it is never processed. When the abort
// fails too, the retry finds the job open and aborts it then.
func TestBulk_FailedUploadAbortsTheJob(t *testing.T) {
	cases := []struct {
		name      string
		fail      func(step string, n int) (reply, bool)
		wantAbort int
	}{
		{name: "upload 400", wantAbort: 1, fail: func(step string, n int) (reply, bool) {
			return reply{status: 400, body: `[{"errorCode":"INVALIDJOB","message":"bad csv"}]`}, step == "upload" && n == 0
		}},
		{name: "upload network error, abort 503", wantAbort: 2, fail: func(step string, n int) (reply, bool) {
			if step == "upload" && n == 0 {
				return reply{drop: true}, true
			}
			return reply{status: 503}, step == "abort" && n == 0
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeBulk(t)
			f.fail = tc.fail
			p := newBulkProducer(t, f, "insert")
			env := testEnvelope(threeRecords)
			checkRetryable(t, p.Deliver(context.Background(), env))
			if err := p.Deliver(context.Background(), env); err != nil {
				t.Fatalf("retry = %v, want nil", err)
			}
			if got := f.count("abort"); got != tc.wantAbort {
				t.Errorf("abort called %d times, want %d", got, tc.wantAbort)
			}
			if got := f.states(); strings.Join(got, ",") != "Aborted,JobComplete" {
				t.Errorf("job states %v, want the first aborted and the second complete", got)
			}
			for _, name := range []string{"r0", "r1", "r2"} {
				if got := f.takenOf(name); got != 1 {
					t.Errorf("%s taken in %d times, want 1", name, got)
				}
			}
		})
	}
}

// A job that ends Failed or Aborted: the records it took in stay in, the
// refused and unprocessed ones are named, with the job's error, and go in a
// new job on the retry.
func TestBulk_FailedOrAbortedJob(t *testing.T) {
	cases := []struct {
		name      string
		endState  string
		limit     int
		refuse    map[string]string
		errMsg    string
		want      []string
		not       []string
		wantRetry string // Names in the retry's job
	}{
		{name: "failed before any record", endState: jobFailed, errMsg: "InvalidBatch : Field name not found : Foo__c",
			want:      []string{"ended Failed: InvalidBatch : Field name not found : Foo__c", "3 of 3", "record[0]: not processed", "record[2]: not processed"},
			wantRetry: "r0,r1,r2"},
		{name: "failed partway", endState: jobFailed, limit: 2, refuse: map[string]string{"r1": "INVALID_FIELD:bad:--"}, errMsg: "Internal error",
			want:      []string{"ended Failed: Internal error", "2 of 3", "record[1]: INVALID_FIELD: bad", "record[2]: not processed"},
			not:       []string{"record[0]"},
			wantRetry: "r1,r2"},
		{name: "aborted in Salesforce", endState: jobAborted,
			want:      []string{"ended Aborted: no error message", "3 of 3"},
			wantRetry: "r0,r1,r2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeBulk(t)
			f.endState, f.processLimit, f.errorMessage = tc.endState, tc.limit, tc.errMsg
			if tc.refuse != nil {
				f.refuse = refuseNames(tc.refuse)
			}
			p := newBulkProducer(t, f, "insert")
			env := testEnvelope(threeRecords)

			err := p.Deliver(context.Background(), env)
			checkRetryable(t, err)
			checkContains(t, err, tc.want, tc.not)

			f.set(func(f *fakeBulk) { f.endState, f.refuse = jobComplete, nil })
			if err := p.Deliver(context.Background(), env); err != nil {
				t.Fatalf("retry = %v, want nil", err)
			}
			jobs := f.jobNames()
			if len(jobs) != 2 || strings.Join(jobs[1], ",") != tc.wantRetry {
				t.Fatalf("jobs %v, want a retry job with %s", jobs, tc.wantRetry)
			}
			for _, name := range []string{"r0", "r1", "r2"} {
				if got := f.takenOf(name); got != 1 {
					t.Errorf("%s taken in %d times, want 1", name, got)
				}
			}
		})
	}
}

// A job that is not done within the poll bound fails the delivery (never an
// ack); the poll backs off; the retry polls the same job instead of
// submitting another, and acks once the job is done.
func TestBulk_PollBound(t *testing.T) {
	f := newFakeBulk(t)
	f.inProgress = -1
	p := newBulkProducer(t, f, "insert")
	p.bulkPollInitial = 5 * time.Millisecond
	p.bulkPollMax = 40 * time.Millisecond
	p.bulkPollTimeout = 200 * time.Millisecond
	env := testEnvelope(threeRecords)

	for attempt := 1; attempt <= 2; attempt++ {
		reads := f.count("state")
		start := time.Now()
		err := p.Deliver(context.Background(), env)
		took := time.Since(start)
		checkRetryable(t, err)
		checkContains(t, err, []string{"still InProgress after 200ms"}, nil)
		if took < 200*time.Millisecond || took > 2*time.Second {
			t.Errorf("attempt %d took %s, want the 200ms bound", attempt, took)
		}
		// Backoff 5, 10, 20, 40, 40, ... ms: about 8 reads in 200 ms. A fixed
		// 5 ms interval would make about 40.
		if got := f.count("state") - reads; got < 3 || got > 12 {
			t.Errorf("attempt %d read the job state %d times, want 3 to 12", attempt, got)
		}
	}
	if got := f.count("create"); got != 1 {
		t.Fatalf("%d jobs created, want 1", got)
	}

	f.set(func(f *fakeBulk) { f.inProgress = 0 })
	if err := p.Deliver(context.Background(), env); err != nil {
		t.Fatalf("Deliver once the job is done = %v, want nil", err)
	}
	if got := f.count("create"); got != 1 {
		t.Errorf("%d jobs created, want 1", got)
	}
	for _, name := range []string{"r0", "r1", "r2"} {
		if got := f.takenOf(name); got != 1 {
			t.Errorf("%s taken in %d times, want 1", name, got)
		}
	}
}

func TestBulk_PollIntervalBacksOff(t *testing.T) {
	f := newFakeBulk(t)
	f.inProgress = 5
	p := newBulkProducer(t, f, "insert")
	p.bulkPollInitial = 10 * time.Millisecond
	p.bulkPollMax = 40 * time.Millisecond
	start := time.Now()
	if err := p.Deliver(context.Background(), testEnvelope(threeRecords)); err != nil {
		t.Fatalf("Deliver = %v", err)
	}
	// Six reads after waits of 10, 20, 40, 40, 40, 40 ms.
	if took := time.Since(start); took < 190*time.Millisecond {
		t.Errorf("six polls took %s, want at least 190ms (10, 20, 40, 40, 40, 40)", took)
	}
	if got := f.count("state"); got != 6 {
		t.Errorf("state read %d times, want 6", got)
	}
}

// When the results cannot be tied to the records, nothing is sent again:
// the job stays remembered and every retry fails the same way, so the
// message ends in the DLQ instead of creating duplicates.
func TestBulk_UnreadableResultsAreNotResent(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fakeBulk, p *salesforceProducer)
		want  string
	}{
		{name: "row matches no record", want: "1 result rows match no record", setup: func(f *fakeBulk, _ *salesforceProducer) {
			f.echo = func(v []string) []string {
				for i := range v {
					v[i] += "?"
				}
				return v
			}
		}},
		{name: "fewer rows than the job reports", want: "lists 0 rows", setup: func(f *fakeBulk, _ *salesforceProducer) {
			f.fail = func(step string, _ int) (reply, bool) { return reply{status: 200, body: ""}, step == "failedResults" }
		}},
		{name: "results over the size cap", want: "larger than 10 bytes", setup: func(_ *fakeBulk, p *salesforceProducer) {
			p.bulkResultsMax = 10
		}},
		{name: "results read fails", want: "failedResults", setup: func(f *fakeBulk, _ *salesforceProducer) {
			f.fail = func(step string, _ int) (reply, bool) { return reply{status: 500}, step == "failedResults" }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeBulk(t)
			f.refuse = refuseNames(map[string]string{"r1": "INVALID_FIELD:bad:--"})
			p := newBulkProducer(t, f, "insert")
			tc.setup(f, p)
			env := testEnvelope(threeRecords)
			for attempt := 1; attempt <= 3; attempt++ {
				err := p.Deliver(context.Background(), env)
				checkRetryable(t, err)
				checkContains(t, err, []string{tc.want}, nil)
			}
			if got := f.count("create"); got != 1 {
				t.Errorf("%d jobs created over three attempts, want 1", got)
			}
			if got := f.takenOf("r0"); got != 1 {
				t.Errorf("r0 taken in %d times, want 1", got)
			}
		})
	}
}

// dlqLastErrors collects X-Vrsky-Last-Error of dead-lettered messages.
func dlqLastErrors(t *testing.T, nc *nats.Conn) chan string {
	t.Helper()
	ch := make(chan string, 4)
	sub, err := nc.Subscribe("vrsky.dlq.>", func(m *nats.Msg) {
		select {
		case ch <- m.Header.Get("X-Vrsky-Last-Error"):
		default:
		}
	})
	if err != nil {
		t.Fatalf("subscribe dlq: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return ch
}

func bulkHarness(t *testing.T, f *fakeBulk, p *salesforceProducer, backoff []time.Duration) *harness.ProducerHarness {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mockConn(mock, testConn, f.srv.URL, "insert")
	p.bulkThreshold = 1
	p.resolveToken = func(context.Context, string, string, bool) (string, error) { return "tok", nil }
	return harness.NewProducerHarness(t, p, harness.Options{Name: "salesforce-producer", DB: db, Backoff: backoff})
}

// Through the SDK runner: a record the job refuses on every attempt ends in
// the DLQ with its index and Salesforce's code; the others go in once.
func TestHarness_BulkRefusedRecordIsDeadLettered(t *testing.T) {
	f := newFakeBulk(t)
	f.refuse = refuseNames(map[string]string{"r1": "REQUIRED_FIELD_MISSING:Required fields are missing: [Phone]:Phone --"})
	p := &salesforceProducer{bulkPollInitial: time.Millisecond, bulkPollMax: time.Millisecond}
	fast := []time.Duration{50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond}
	h := bulkHarness(t, f, p, fast)
	lastErr := dlqLastErrors(t, h.NATS())

	env := testEnvelope(threeRecords)
	h.Publish(t, env)
	got := h.ExpectDLQ(t, 15*time.Second)
	if got.ID != env.ID {
		t.Fatalf("DLQ holds %s, want %s", got.ID, env.ID)
	}
	select {
	case e := <-lastErr:
		for _, want := range []string{"record[1]: REQUIRED_FIELD_MISSING", "1 of 3"} {
			if !strings.Contains(e, want) {
				t.Errorf("X-Vrsky-Last-Error = %q, want it to contain %q", e, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no DLQ last-error header")
	}
	for _, name := range []string{"r0", "r2"} {
		if n := f.takenOf(name); n != 1 {
			t.Errorf("%s taken in %d times, want 1", name, n)
		}
	}
	if n := f.count("create"); n != 5 {
		t.Errorf("%d jobs created, want 5 (one per attempt)", n)
	}
}

// Through the SDK runner with the production ack wait (1 s): a job that
// takes longer than that is polled to the end within one delivery, the
// message is acked, and no second job is created.
func TestHarness_BulkLongJobOneDelivery(t *testing.T) {
	f := newFakeBulk(t)
	f.inProgress = 5
	p := &salesforceProducer{bulkPollInitial: 400 * time.Millisecond, bulkPollMax: 400 * time.Millisecond}
	h := bulkHarness(t, f, p, nil)
	h.Publish(t, testEnvelope(threeRecords))

	harness.Eventually(t, 15*time.Second, "every record taken in", func() bool {
		return f.takenOf("r0") == 1 && f.takenOf("r1") == 1 && f.takenOf("r2") == 1
	})
	h.ExpectNoDLQ(t, 2*time.Second)
	if n := f.count("create"); n != 1 {
		t.Errorf("%d jobs created, want 1", n)
	}
	if n := f.count("state"); n != 6 {
		t.Errorf("state read %d times, want 6 (one delivery)", n)
	}
}
