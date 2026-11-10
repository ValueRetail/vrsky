package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Bulk API 2.0 ingest (#760). A job is created, filled and closed, and then
// the delivery waits for Salesforce to process it: the records the job
// refused or never processed are named in the error, like the REST path's
// refused records (#735), and the ones that went in go into the skip set, so
// the redelivery submits a new job with only the rest.
//
// The job id is remembered per message and target from the moment
// Salesforce answers the create, so a retry after a lost close (or a poll
// that ran out of time) finds the job instead of submitting a second one.
// Bulk API 2.0 jobs carry no tag of ours, so after a restart the job cannot
// be found again: like the skip set, the memory is per process.

const (
	jobOpen           = "Open"
	jobUploadComplete = "UploadComplete"
	jobInProgress     = "InProgress"
	jobComplete       = "JobComplete"
	jobFailed         = "Failed"
	jobAborted        = "Aborted"

	// defaultBulkPollTimeout bounds the wait for one job within one delivery
	// attempt. The SDK marks the message in progress while Deliver runs
	// (#139), so the wait causes no redelivery. Past it the delivery fails
	// and the redelivery goes on polling the same job.
	defaultBulkPollTimeout = 2 * time.Minute
	// The wait between polls starts at defaultBulkPollInitial and doubles up
	// to defaultBulkPollMax.
	defaultBulkPollInitial = time.Second
	defaultBulkPollMax     = 30 * time.Second
	// defaultBulkResultsMax caps a failedResults or unprocessedrecords body.
	// A larger one is not read in part: the delivery fails instead.
	defaultBulkResultsMax = 32 << 20
	// bulkJobsMax bounds the job memory. Forgetting a job early costs a
	// second job on the retry, as forgetting a written record costs a
	// second write.
	bulkJobsMax = 1000
	// maxResponseBody caps every other Salesforce answer.
	maxResponseBody = 1 << 20
)

// bulkJob is a job submitted for one message in one target: its id and the
// indexes of the records it holds.
type bulkJob struct {
	id        string
	submitted []int
}

// jobSet remembers the open bulk job of each message and target, keyed like
// the skip set with the record index bulkRecord. An entry is removed once the
// job is resolved; entries a retry never comes for expire after the TTL.
type jobSet struct {
	mu   sync.Mutex
	ttl  time.Duration
	max  int
	jobs map[writtenKey]jobEntry
}

type jobEntry struct {
	job bulkJob
	at  time.Time
}

func newJobSet(ttl time.Duration, maxEntries int) *jobSet {
	return &jobSet{ttl: ttl, max: maxEntries, jobs: map[writtenKey]jobEntry{}}
}

func (s *jobSet) get(k writtenKey, now time.Time) (bulkJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.jobs[k]
	if ok && now.Sub(e.at) >= s.ttl {
		delete(s.jobs, k)
		return bulkJob{}, false
	}
	return e.job, ok
}

func (s *jobSet) put(k writtenKey, job bulkJob, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[k]; !ok && len(s.jobs) >= s.max {
		for key, e := range s.jobs {
			if now.Sub(e.at) >= s.ttl {
				delete(s.jobs, key)
			}
		}
		for len(s.jobs) >= s.max {
			var oldest writtenKey
			var oldestAt time.Time
			first := true
			for key, e := range s.jobs {
				if first || e.at.Before(oldestAt) {
					oldest, oldestAt, first = key, e.at, false
				}
			}
			delete(s.jobs, oldest)
		}
	}
	s.jobs[k] = jobEntry{job: job, at: now}
}

func (s *jobSet) forget(k writtenKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, k)
}

func (s *jobSet) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

// bulkJobInfo is the part of Salesforce's job info the producer reads.
type bulkJobInfo struct {
	ID           string `json:"id"`
	State        string `json:"state"`
	ErrorMessage string `json:"errorMessage"`
	Processed    int    `json:"numberRecordsProcessed"`
	Failed       int    `json:"numberRecordsFailed"`
}

// bulkCall is one target's Bulk API endpoint during one delivery.
type bulkCall struct {
	p        *salesforceProducer
	tenantID string
	cfg      *SalesforceProducerConfig
	op       string
	base     string // .../services/data/<version>/jobs/ingest
	total    int    // records in the message

	// local are the records the CSV cannot carry (see sendable): refused
	// here, never sent. reported says resolve has put them in its error.
	local    map[int]string
	reported bool
}

// withLocal adds the records refused here to an error that resolve did not
// produce, so they are named whatever else went wrong.
func (b *bulkCall) withLocal(err error) error {
	if b.reported || len(b.local) == 0 {
		return err
	}
	return errors.Join(err, rejectedError(b.cfg, b.op, b.total, listRejected(b.local)))
}

// listRejected renders refused records as "record[i]: why", by index.
func listRejected(out map[int]string) []string {
	idx := make([]int, 0, len(out))
	for i := range out {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	list := make([]string, len(idx))
	for n, i := range idx {
		list[n] = fmt.Sprintf("record[%d]: %s", i, out[i])
	}
	return list
}

// sendable splits the pending records into those a CSV can carry and those
// it cannot. With a single column, a record without a value for it is
// written as an empty line, which CSV readers (Salesforce's too) skip: the
// job would end JobComplete without it. With no column at all, no record has
// anything to send.
func sendable(records []map[string]any, pending []int, op string) ([]int, map[int]string) {
	header, rows := csvTable(subset(records, pending), op)
	if len(header) > 1 {
		return pending, nil
	}
	var send []int
	local := map[int]string{}
	for j, i := range pending {
		if len(header) == 0 || rows[j][0] == "" || records[i][header[0]] == nil {
			local[i] = "nothing to send (no field has a value, and an empty CSV row is skipped)"
			continue
		}
		send = append(send, i)
	}
	return send, local
}

// bulkCSV renders the records to the CSV that is uploaded, and reads it back
// the way Salesforce does: the header and rows a result row is compared
// with (a CSV reader turns \r\n inside a quoted field into \n, on both
// sides). A read-back that does not give one row per record is an error, so
// a record the CSV lost is never counted as written.
func bulkCSV(records []map[string]any, idx []int, op string) ([]byte, []string, [][]string, error) {
	data, err := recordsToCSV(subset(records, idx), op)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build CSV: %w", err)
	}
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read back the CSV: %w", err)
	}
	if len(rows) == 0 || len(rows)-1 != len(idx) {
		return nil, nil, nil, fmt.Errorf("the CSV of %d records reads back as %d rows; not submitted", len(idx), max(len(rows)-1, 0))
	}
	return data, rows[0], rows[1:], nil
}

func (b *bulkCall) send(ctx context.Context, method, u, contentType string, body []byte) (int, []byte, error) {
	return b.p.doRequest(ctx, b.tenantID, b.cfg.OAuthGrantID, method, u, contentType, body)
}

func (b *bulkCall) create(ctx context.Context) (string, error) {
	jobReq := map[string]string{"object": b.cfg.Object, "operation": b.op, "contentType": "CSV", "lineEnding": "LF"}
	if b.op == "upsert" {
		jobReq["externalIdFieldName"] = b.cfg.ExternalIDField
	}
	jobBody, _ := json.Marshal(jobReq)
	status, respBody, err := b.send(ctx, http.MethodPost, b.base, "application/json", jobBody)
	if err != nil {
		return "", fmt.Errorf("bulk create job: %w", err)
	}
	if err := bulkStep("create job", status, respBody); err != nil {
		return "", err
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &job); err != nil || job.ID == "" {
		return "", fmt.Errorf("bulk job response missing id: %s", truncate(respBody))
	}
	return job.ID, nil
}

func (b *bulkCall) upload(ctx context.Context, jobID string, csvData []byte) error {
	status, respBody, err := b.send(ctx, http.MethodPut, b.base+"/"+jobID+"/batches", "text/csv", csvData)
	if err != nil {
		return fmt.Errorf("bulk upload data: %w", err)
	}
	return bulkStep("upload data", status, respBody)
}

// setState closes (UploadComplete) or aborts (Aborted) a job.
func (b *bulkCall) setState(ctx context.Context, jobID, state, step string) error {
	status, respBody, err := b.send(ctx, http.MethodPatch, b.base+"/"+jobID, "application/json",
		[]byte(fmt.Sprintf(`{"state":%q}`, state)))
	if err != nil {
		return fmt.Errorf("bulk %s: %w", step, err)
	}
	return bulkStep(step, status, respBody)
}

func (b *bulkCall) info(ctx context.Context, jobID string) (bulkJobInfo, error) {
	status, respBody, err := b.send(ctx, http.MethodGet, b.base+"/"+jobID, "application/json", nil)
	if err != nil {
		return bulkJobInfo{}, fmt.Errorf("bulk job state (%s): %w", jobID, err)
	}
	if err := bulkStep("job state", status, respBody); err != nil {
		return bulkJobInfo{}, fmt.Errorf("bulk job %s: %w", jobID, err)
	}
	var info bulkJobInfo
	if err := json.Unmarshal(respBody, &info); err != nil || info.State == "" {
		return bulkJobInfo{}, fmt.Errorf("bulk job %s: state response has no state: %s", jobID, truncate(respBody))
	}
	return info, nil
}

// results reads one of the job's result sets (failedResults or
// unprocessedrecords) as CSV. A body over the cap, or one that cannot be
// read whole, is an error: a partial list would count the missing rows as
// written.
func (b *bulkCall) results(ctx context.Context, jobID, which string) ([]string, [][]string, error) {
	p := b.p
	u := b.base + "/" + jobID + "/" + which + "/"
	status, body, truncated, err := p.request(ctx, b.tenantID, b.cfg.OAuthGrantID, http.MethodGet, u, "application/json", "text/csv", nil, p.bulkResultsMax)
	if err != nil {
		return nil, nil, fmt.Errorf("bulk job %s %s: %w", jobID, which, err)
	}
	if err := bulkStep(which, status, body); err != nil {
		return nil, nil, fmt.Errorf("bulk job %s: %w", jobID, err)
	}
	if truncated {
		return nil, nil, fmt.Errorf("bulk job %s %s is larger than %d bytes; not read, nothing is sent again", jobID, which, p.bulkResultsMax)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil, nil
	}
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("bulk job %s %s is not CSV: %w", jobID, which, err)
	}
	if len(rows) == 0 {
		return nil, nil, nil
	}
	return rows[0], rows[1:], nil
}

// bulkWrite ingests the records the skip set does not have through one Bulk
// API 2.0 job: create, upload the CSV, close, wait for the job, resolve its
// results. A job remembered for this message and target is resumed instead.
func (p *salesforceProducer) bulkWrite(ctx context.Context, tenantID string, cfg *SalesforceProducerConfig, op string, records []map[string]any, key func(int) writtenKey, res *writeResult) error {
	now := time.Now()
	var pending []int
	for i := range records {
		k := key(i)
		if p.written.has(k, now) {
			res.done = append(res.done, k)
			continue
		}
		pending = append(pending, i)
	}
	if len(pending) == 0 {
		p.logger.Info("Salesforce Bulk API: every record of this message is already in; skipping", "object", cfg.Object, "op", op)
		return nil
	}
	b := &bulkCall{p: p, tenantID: tenantID, cfg: cfg, op: op, total: len(records),
		base: fmt.Sprintf("%s/services/data/%s/jobs/ingest", strings.TrimRight(cfg.InstanceURL, "/"), cfg.APIVersion)}
	pending, b.local = sendable(records, pending, op)
	for i, why := range b.local {
		p.logger.Error("Salesforce Bulk API: record cannot be sent", "record", i, "object", cfg.Object, "op", op, "error", why)
	}
	if len(pending) == 0 {
		return b.withLocal(nil)
	}
	return b.withLocal(p.bulkJobWrite(ctx, b, records, pending, key, res))
}

// bulkJobWrite resumes the job remembered for this message and target, or
// submits the pending records in a new one, and resolves it.
func (p *salesforceProducer) bulkJobWrite(ctx context.Context, b *bulkCall, records []map[string]any, pending []int, key func(int) writtenKey, res *writeResult) error {
	cfg, op := b.cfg, b.op
	now := time.Now()
	jk := key(bulkRecord)

	if job, ok := p.jobs.get(jk, now); ok {
		info, err := b.info(ctx, job.id)
		if err != nil {
			return err
		}
		if info.State != jobOpen {
			p.logger.Info("Salesforce Bulk API: resuming the job submitted for this message", "object", cfg.Object, "op", op, "job_id", job.id, "state", info.State)
			return b.finish(ctx, jk, job, info, records, key, res)
		}
		// Open: the close never reached Salesforce, or an upload failed and
		// its abort did not go through. An open job processes nothing, so
		// abort it and start again.
		if err := b.setState(ctx, job.id, jobAborted, "abort job"); err != nil {
			return fmt.Errorf("bulk job %s: %w", job.id, err)
		}
		p.jobs.forget(jk)
		p.logger.Info("Salesforce Bulk API: aborted the open job of an earlier attempt", "object", cfg.Object, "op", op, "job_id", job.id)
	}

	csvData, _, _, err := bulkCSV(records, pending, op)
	if err != nil {
		return err
	}
	jobID, err := b.create(ctx)
	if err != nil {
		return err
	}
	job := bulkJob{id: jobID, submitted: pending}
	p.jobs.put(jk, job, time.Now())

	if err := b.upload(ctx, jobID, csvData); err != nil {
		if aerr := b.setState(ctx, jobID, jobAborted, "abort job"); aerr != nil {
			// Kept: the retry finds the job open and aborts it.
			p.logger.Warn("Salesforce Bulk API: could not abort the job after the failed upload", "job_id", jobID, "error", aerr)
		} else {
			p.jobs.forget(jk)
		}
		return err
	}
	if err := b.setState(ctx, jobID, jobUploadComplete, "close job"); err != nil {
		// Kept: the close may have gone through. The retry reads the job's
		// state instead of creating a second job.
		return fmt.Errorf("bulk job %s: %w", jobID, err)
	}
	p.logger.Info("Salesforce Bulk API job submitted", "object", cfg.Object, "op", op, "job_id", jobID, "records", len(pending))
	return b.finish(ctx, jk, job, bulkJobInfo{ID: jobID, State: jobUploadComplete}, records, key, res)
}

func (b *bulkCall) finish(ctx context.Context, jk writtenKey, job bulkJob, info bulkJobInfo, records []map[string]any, key func(int) writtenKey, res *writeResult) error {
	info, err := b.wait(ctx, job.id, info)
	if err != nil {
		return err
	}
	return b.resolve(ctx, jk, job, info, records, key, res)
}

// wait polls the job until it is done, with backoff, for at most
// bulkPollTimeout. Running out of time is an error, never an ack.
func (b *bulkCall) wait(ctx context.Context, jobID string, info bulkJobInfo) (bulkJobInfo, error) {
	p := b.p
	deadline := time.Now().Add(p.bulkPollTimeout)
	interval := p.bulkPollInitial
	for {
		switch info.State {
		case jobComplete, jobFailed, jobAborted:
			return info, nil
		case jobUploadComplete, jobInProgress:
		default:
			return info, fmt.Errorf("bulk job %s is %s after the close; the retry checks it again", jobID, info.State)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return info, fmt.Errorf("bulk job %s still %s after %s; the retry checks it again", jobID, info.State, p.bulkPollTimeout)
		}
		t := time.NewTimer(min(interval, remaining))
		select {
		case <-ctx.Done():
			t.Stop()
			return info, fmt.Errorf("bulk job %s: %w", jobID, ctx.Err())
		case <-t.C:
		}
		interval = min(interval*2, p.bulkPollMax)
		next, err := b.info(ctx, jobID)
		if err != nil {
			return info, err
		}
		info = next
	}
}

// resolve reads what a finished job did not take in and settles the
// message's records: the ones that went in are written, the refused and
// unprocessed ones are named in the error and go in a new job on the retry.
// A result row that matches no record keeps the job and fails the delivery,
// so nothing is resent and the message ends in the DLQ.
func (b *bulkCall) resolve(ctx context.Context, jk writtenKey, job bulkJob, info bulkJobInfo, records []map[string]any, key func(int) writtenKey, res *writeResult) error {
	p := b.p
	outstanding := map[int]string{} // record index → why it is not in
	var unmatched []string

	done := info.State == jobComplete
	if done && info.Processed < len(job.submitted) {
		// Processed counts the failed records too, so a complete job that
		// processed fewer than it was given lost some on the way in.
		p.logger.Error("Salesforce Bulk API: job processed fewer records than it was given; nothing is sent again", "job_id", job.id,
			"processed", info.Processed, "submitted", len(job.submitted))
		return fmt.Errorf("salesforce bulk job %s (%s %s) is complete but processed %d of the %d records it was given, so none is sent again",
			job.id, b.cfg.Object, b.op, info.Processed, len(job.submitted))
	}
	switch {
	case !done && info.Processed == 0:
		for _, i := range job.submitted {
			outstanding[i] = "not processed"
		}
	case info.Failed > 0 || (!done && info.Processed < len(job.submitted)):
		m, err := newRowMatcher(records, job.submitted, b.op)
		if err != nil {
			return fmt.Errorf("bulk job %s: %w", job.id, err)
		}
		if info.Failed > 0 {
			header, rows, err := b.results(ctx, job.id, "failedResults")
			if err != nil {
				return err
			}
			if len(rows) < info.Failed {
				return fmt.Errorf("bulk job %s: failedResults lists %d rows, the job reports %d failed; nothing is sent again", job.id, len(rows), info.Failed)
			}
			errCol := indexOf(header, "sf__Error")
			unmatched = append(unmatched, m.match("failed", header, rows, func(i int, row []string) {
				why := ""
				if errCol >= 0 {
					why = row[errCol]
				}
				outstanding[i] = bulkRowError(why)
			})...)
		}
		if !done && info.Processed < len(job.submitted) {
			header, rows, err := b.results(ctx, job.id, "unprocessedrecords")
			if err != nil {
				return err
			}
			if want := len(job.submitted) - info.Processed; len(rows) < want {
				return fmt.Errorf("bulk job %s: unprocessedrecords lists %d rows, the job left %d unprocessed; nothing is sent again", job.id, len(rows), want)
			}
			unmatched = append(unmatched, m.match("unprocessed", header, rows, func(i int, _ []string) {
				outstanding[i] = "not processed"
			})...)
		}
	}
	if len(unmatched) > 0 {
		listed := unmatched
		more := ""
		if len(listed) > maxListed {
			more = fmt.Sprintf("; and %d more", len(listed)-maxListed)
			listed = listed[:maxListed]
		}
		p.logger.Error("Salesforce Bulk API: result rows match no record; nothing is sent again", "job_id", job.id, "rows", len(unmatched))
		return fmt.Errorf("salesforce bulk job %s (%s %s): %d result rows match no record of the message, so none is sent again: %s%s",
			job.id, b.cfg.Object, b.op, len(unmatched), strings.Join(listed, "; "), more)
	}

	p.jobs.forget(jk)
	for _, i := range job.submitted {
		if why, out := outstanding[i]; out {
			p.logger.Error("Salesforce Bulk API job did not take record", "record", i, "object", b.cfg.Object, "op", b.op, "job_id", job.id, "error", why)
			continue
		}
		res.wrote(key(i))
	}
	p.logger.Info("Salesforce Bulk API job finished", "object", b.cfg.Object, "op", b.op, "job_id", job.id, "state", info.State,
		"ok", len(job.submitted)-len(outstanding), "not_taken", len(outstanding), "total", len(records))
	for i, why := range b.local {
		outstanding[i] = why
	}
	b.reported = true
	if len(outstanding) == 0 {
		return nil
	}
	rejected := listRejected(outstanding)
	var errs []error
	if !done && len(outstanding) > len(b.local) {
		msg := info.ErrorMessage
		if msg == "" {
			msg = "no error message"
		}
		errs = append(errs, fmt.Errorf("salesforce bulk job %s ended %s: %s", job.id, info.State, truncateN([]byte(msg), 200)))
	}
	errs = append(errs, rejectedError(b.cfg, b.op, len(records), rejected))
	return errors.Join(errs...)
}

// bulkRowError renders a failed row's sf__Error, which Salesforce writes as
// CODE:message:fields, as "CODE: message".
func bulkRowError(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "refused (no error text)"
	}
	code, msg, found := strings.Cut(s, ":")
	if !found {
		return truncateN([]byte(s), 200)
	}
	return code + ": " + truncateN([]byte(strings.TrimSpace(msg)), 200)
}

// rowMatcher finds the record a result row is about. Salesforce returns the
// rows in no fixed order but echoes every uploaded column, so a row is
// matched by the values of the uploaded columns. Records with the same
// values are interchangeable: each row takes the next unmatched one.
type rowMatcher struct {
	header []string
	free   map[string][]int // row values → record indexes not yet matched
}

func newRowMatcher(records []map[string]any, submitted []int, op string) (*rowMatcher, error) {
	_, header, rows, err := bulkCSV(records, submitted, op)
	if err != nil {
		return nil, err
	}
	m := &rowMatcher{header: header, free: map[string][]int{}}
	for j, row := range rows {
		sig, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		m.free[string(sig)] = append(m.free[string(sig)], submitted[j])
	}
	return m, nil
}

// match calls found for every row it can place and returns a description of
// each row it cannot.
func (m *rowMatcher) match(what string, header []string, rows [][]string, found func(i int, row []string)) []string {
	pos := make([]int, len(m.header))
	for c, col := range m.header {
		pos[c] = indexOf(header, col)
	}
	var unmatched []string
	for n, row := range rows {
		vals := make([]string, len(m.header))
		ok := true
		for c, at := range pos {
			if at < 0 || at >= len(row) {
				ok = false
				break
			}
			vals[c] = row[at]
		}
		var sig []byte
		if ok {
			sig, _ = json.Marshal(vals)
		}
		idx := m.free[string(sig)]
		if !ok || len(idx) == 0 {
			unmatched = append(unmatched, fmt.Sprintf("%s row %d", what, n+1))
			continue
		}
		m.free[string(sig)] = idx[1:]
		found(idx[0], row)
	}
	return unmatched
}

// indexOf finds a column by name, ignoring case like Salesforce does with
// field names (it refuses two columns that differ only in case).
func indexOf(list []string, s string) int {
	for i, v := range list {
		if strings.EqualFold(v, s) {
			return i
		}
	}
	return -1
}

func subset(records []map[string]any, idx []int) []map[string]any {
	out := make([]map[string]any, len(idx))
	for j, i := range idx {
		out[j] = records[i]
	}
	return out
}
