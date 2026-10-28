package main

// Incremental polling (#457, plans/issue-457-sitoo-incremental.md).
//
// Sitoo has no general modified-since filter: each collection has its own
// parameter, and some have none. So the cursor is configured per node (field,
// filter parameter and sort), with a default only for the collection whose
// filter and sort the API reference documents.
//
// The poll pages by cursor value (keyset paging), not by offset:
//
//   - The listing must be sorted ascending on the cursor field, and every
//     page is checked for it. Each next page asks the filter for records from
//     the previous page's last cursor value on. A record that changes during
//     the poll gets a newer cursor value, so it lands ahead of where the poll
//     is reading and is read in this poll; nothing shifts behind the poll.
//   - A run of records sharing one cursor value that fills a whole page (a
//     bulk import stamped in one second) cannot be stepped past by value, so
//     the poll pages through that run by offset. Pages there overlap on one
//     record; when it is not where it should be, the run is read again from
//     its start.
//   - Each poll starts cursor_overlap units behind the watermark, so a record
//     committed late with a slightly older cursor value is still read.
//   - Records are deduped by content hash, against the hashes published
//     within the overlap window (kept in the checkpoint) and those met
//     earlier in this poll. A changed record has new content, so it goes out
//     again; an unchanged one does not.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

const (
	// defaultCursorOverlap is how far back each poll re-reads, in cursor units:
	// a minute for a Unix-seconds timestamp, 60 ids for an id cursor.
	defaultCursorOverlap = 60
	// A poll reads at most pageCapFactor times the pages its first answer's
	// totalcount needs, plus pageCapSlack. Past that it fails without saving.
	pageCapFactor = 3
	pageCapSlack  = 10
	// defaultCheckpointNode keys the checkpoint when the node has no id.
	defaultCheckpointNode = "sitoo-consumer"
)

// cursorSpec is the resolved incremental configuration of one node.
type cursorSpec struct {
	field     string // record field holding the cursor value
	param     string // query parameter that filters on it
	sort      string // `sort` value that orders the listing ascending on field
	inclusive bool   // param returns records whose cursor equals the value
	overlap   int64
}

// paramValue is what to send so the filter returns every record whose
// cursor is at least lower: lower itself for an inclusive filter, one less
// for an exclusive ("after") one.
func (s cursorSpec) paramValue(lower int64) int64 {
	if s.inclusive {
		return lower
	}
	return lower - 1
}

// knownCursors are the collections whose incremental filter and sort the
// Sitoo API reference documents (developer.sitoo.com, checked 2026-10-28),
// keyed by the last segment of the resource path:
//
//   - orders: `orderidfrom` returns orders with orderid >= the value, and
//     `sort=orderid` lists them ascending (the default is newest first).
//
// products has a `datelastmodified` filter but no documented sort, so it
// has no default: without a sort on the cursor a change mid-poll can be lost.
var knownCursors = map[string]cursorSpec{
	"orders": {field: "orderid", param: "orderidfrom", sort: "orderid", inclusive: true},
}

// reservedParams are the query parameters the poller sets itself.
var reservedParams = map[string]bool{"start": true, "num": true, "sort": true}

// cursorSpec resolves the node's cursor settings over the defaults for its
// resource. An error means incremental is on but there is no safe way to ask
// Sitoo for "what changed".
func (c *SitooConfig) cursorSpec() (cursorSpec, error) {
	if err := c.checkResource(); err != nil {
		return cursorSpec{}, err
	}
	res := c.effectiveResource()
	def := knownCursors[res[strings.LastIndex(res, "/")+1:]]
	spec := cursorSpec{
		field:   firstNonEmpty(strings.TrimSpace(c.CursorField), def.field),
		param:   firstNonEmpty(strings.TrimSpace(c.CursorParam), def.param),
		sort:    firstNonEmpty(strings.TrimSpace(c.CursorSort), def.sort),
		overlap: defaultCursorOverlap,
	}
	// Inclusive is a property of the parameter: the default's when the node
	// uses it, otherwise only what the node declares.
	spec.inclusive = c.CursorInclusive || (spec.param == def.param && def.inclusive)
	if spec.field == "" || spec.param == "" {
		return cursorSpec{}, fmt.Errorf("incremental needs cursor_field and cursor_param for resource %q", res)
	}
	if spec.sort == "" {
		return cursorSpec{}, fmt.Errorf("incremental needs cursor_sort for resource %q: a sort that lists records ascending on %q", res, spec.field)
	}
	if reservedParams[strings.ToLower(spec.param)] {
		return cursorSpec{}, fmt.Errorf("cursor_param %q is set by the poller itself; use the collection's filter parameter", spec.param)
	}
	if c.CursorOverlap != nil {
		if *c.CursorOverlap < 0 {
			return cursorSpec{}, fmt.Errorf("cursor_overlap must not be negative (got %d)", *c.CursorOverlap)
		}
		spec.overlap = *c.CursorOverlap
	}
	if spec.overlap == 0 && !spec.inclusive {
		// With no overlap the first request rests entirely on the filter's
		// boundary; only trust that when the filter is known to include it.
		return cursorSpec{}, fmt.Errorf("cursor_overlap 0 needs an inclusive filter; %q is not known to be one (set cursor_inclusive if it is)", spec.param)
	}
	return spec, nil
}

// checkpointNode is the node id the checkpoint is stored under.
func (c *SitooConfig) checkpointNode() string {
	return firstNonEmpty(c.NodeID, defaultCheckpointNode)
}

// incrementalState is what goes into connection_node_checkpoints.state. The
// watermark itself is last_processed_message_id.
type incrementalState struct {
	// The query the watermark belongs to. A node whose resource, filter or
	// cursor changes starts over rather than resuming a different query's
	// watermark.
	Resource      string `json:"resource"`
	ResourceQuery string `json:"resource_query"`
	CursorField   string `json:"cursor_field"`
	CursorParam   string `json:"cursor_param"`
	// Seen maps the content hash of each record published within the overlap
	// window to its cursor value, so the re-read part is not sent twice.
	Seen map[string]int64 `json:"seen"`
}

// queryState is the state of a fresh checkpoint for this node's query.
func (c *SitooConfig) queryState(spec cursorSpec) incrementalState {
	return incrementalState{
		Resource:      c.effectiveResource(),
		ResourceQuery: c.resourceQuery().Encode(),
		CursorField:   spec.field,
		CursorParam:   spec.param,
	}
}

// sameQuery reports whether a stored state belongs to the query in want.
func (st incrementalState) sameQuery(want incrementalState) bool {
	return st.Resource == want.Resource && st.ResourceQuery == want.ResourceQuery &&
		st.CursorField == want.CursorField && st.CursorParam == want.CursorParam
}

// loadIncremental returns the stored watermark and the hashes published
// within its overlap window. ok is false for a first poll.
//
// A store that cannot be read fails the poll. The BC consumer polls from the
// start instead; here that would re-send every record to a destination that
// may not dedupe, and the next tick retries the read anyway.
func (s *sitooConsumer) loadIncremental(ctx context.Context, tenantID, connID string, cfg *SitooConfig, spec cursorSpec,
	logger *slog.Logger) (watermark int64, seen map[string]int64, ok bool, err error) {
	seen = map[string]int64{}
	if s.checkpoints == nil {
		return 0, seen, false, nil
	}
	cp, err := s.checkpoints.Get(ctx, tenantID, connID, cfg.checkpointNode())
	if err != nil {
		return 0, seen, false, fmt.Errorf("read Sitoo checkpoint: %w", err)
	}
	if cp == nil || cp.LastProcessedMessageID == "" {
		return 0, seen, false, nil
	}
	watermark, err = strconv.ParseInt(cp.LastProcessedMessageID, 10, 64)
	if err != nil {
		logger.Warn("Stored Sitoo cursor is not a number; reading everything once", "cursor", cp.LastProcessedMessageID)
		return 0, seen, false, nil
	}
	var st incrementalState
	if len(cp.State) > 0 {
		if err := json.Unmarshal(cp.State, &st); err != nil {
			logger.Warn("Stored Sitoo checkpoint state unreadable; reading everything once", "error", err)
			return 0, seen, false, nil
		}
	}
	if want := cfg.queryState(spec); !st.sameQuery(want) {
		logger.Info("Sitoo query changed since the stored cursor; reading everything once",
			"stored_resource", st.Resource, "resource", want.Resource,
			"stored_field", st.CursorField, "field", want.CursorField,
			"stored_param", st.CursorParam, "param", want.CursorParam)
		return 0, seen, false, nil
	}
	for h, v := range st.Seen {
		seen[h] = v
	}
	return watermark, seen, true, nil
}

// saveIncremental stores the new watermark and the hashes still inside its
// overlap window.
func (s *sitooConsumer) saveIncremental(ctx context.Context, tenantID, connID string, cfg *SitooConfig, spec cursorSpec,
	watermark int64, seen map[string]int64, published int, logger *slog.Logger) {
	if s.checkpoints == nil {
		return
	}
	// The records are already out: a stop arriving now must not cost the
	// save, or the next start re-sends this poll.
	ctx = context.WithoutCancel(ctx)
	st := cfg.queryState(spec)
	st.Seen = make(map[string]int64, len(seen))
	for h, v := range seen {
		if v >= watermark-spec.overlap {
			st.Seen[h] = v
		}
	}
	state, err := json.Marshal(st)
	if err == nil {
		err = s.checkpoints.Save(ctx, &checkpoint.Checkpoint{
			TenantID:               tenantID,
			ConnectionID:           connID,
			NodeID:                 cfg.checkpointNode(),
			LastProcessedMessageID: strconv.FormatInt(watermark, 10),
			LastProcessedAt:        time.Now().UTC(),
			MessageCount:           int64(published),
			State:                  state,
		})
	}
	if err != nil {
		// The records are out; failing to remember that costs a repeat of this
		// poll's records next time, not data.
		logger.Error("save Sitoo checkpoint", "error", err, "cursor", watermark)
	}
}

var (
	// errNotSorted: the listing is not ascending on the cursor field, so
	// paging by cursor value could step past a record.
	errNotSorted = errors.New("sitoo listing is not sorted ascending on the cursor field; check cursor_sort")
	// errFilterIgnored: a record came back below the cursor value asked for.
	errFilterIgnored = errors.New("sitoo returned records below the cursor filter; check cursor_param")
	// errPageCap: the poll read far more pages than the collection needs.
	errPageCap = errors.New("sitoo poll read more pages than the collection needs")
)

// fetchIncremental reads what changed since the stored watermark, publishes
// the records not already published, and advances the watermark once every
// page has landed. A poll that fails midway saves nothing: the next one reads
// from the old watermark again, so records may repeat but are not skipped.
func (s *sitooConsumer) fetchIncremental(ctx context.Context, connID, tenantID string, cfg *SitooConfig, spec cursorSpec,
	logger *slog.Logger) error {
	watermark, seen, resume, err := s.loadIncremental(ctx, tenantID, connID, cfg, spec, logger)
	if err != nil {
		return err
	}
	pageSize := cfg.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize < 2 {
		// A page has to hold the record it overlaps on and one more.
		pageSize = 2
	}

	// lower is the cursor value the next page reads from; keyed is false only
	// until the first poll has seen a cursor value.
	lower, keyed := int64(0), resume
	if resume {
		lower = max(watermark-spec.overlap, 0)
	}
	newMark, sawCursor := watermark, resume
	met := map[string]bool{} // every hash met this poll, published or not
	published, skipped, pages, runRestarts, pageCap := 0, 0, 0, 0, 0
	// Inside a run of records sharing one cursor value: the offset into it
	// and the hash of the record the next page must begin with.
	offset, runAnchor := 0, ""

	for {
		if pageCap > 0 && pages >= pageCap {
			logger.Error("Sitoo poll hit its page cap; not advancing the cursor",
				"pages", pages, "run_restarts", runRestarts)
			return fmt.Errorf("%w (%d pages)", errPageCap, pages)
		}
		pages++
		q := url.Values{"sort": {spec.sort}}
		sent := int64(0)
		if keyed {
			sent = spec.paramValue(lower)
			q.Set(spec.param, strconv.FormatInt(sent, 10))
		}
		body, err := s.get(ctx, cfg, cfg.collectionURL(offset, pageSize, q))
		if err != nil {
			return err
		}
		var coll sitooCollection
		if err := json.Unmarshal(body, &coll); err != nil {
			return fmt.Errorf("parse Sitoo collection: %w", err)
		}
		if pageCap == 0 {
			pageCap = pageCapFactor*(coll.TotalCount/pageSize+1) + pageCapSlack
		}

		// Check the page before anything on it is published: ascending on
		// the cursor, and nothing below the filter.
		last, hasLast := int64(0), false
		for _, item := range coll.Items {
			v, ok := cursorValue(item, spec.field)
			if !ok {
				continue
			}
			if keyed && v < sent {
				return fmt.Errorf("%w (%s=%d, got %d)", errFilterIgnored, spec.param, sent, v)
			}
			if hasLast && v < last {
				return fmt.Errorf("%w (%d after %d)", errNotSorted, v, last)
			}
			last, hasLast = v, true
		}

		if offset > 0 && (len(coll.Items) == 0 || contentHash(coll.Items[0]) != runAnchor) {
			// The run changed under the poll; read it again from its start.
			runRestarts++
			offset, runAnchor = 0, ""
			continue
		}

		var batch []json.RawMessage
		for _, item := range coll.Items {
			h := contentHash(item)
			v, hasCursor := cursorValue(item, spec.field)
			if hasCursor && (!sawCursor || v > newMark) {
				newMark, sawCursor = v, true
			}
			if met[h] {
				continue
			}
			met[h] = true
			if _, done := seen[h]; done {
				skipped++
				continue
			}
			batch = append(batch, item)
			if hasCursor {
				seen[h] = v
			}
		}
		if len(batch) > 0 {
			if err := s.publishItems(ctx, connID, tenantID, cfg.effectiveResource(), batch); err != nil {
				return fmt.Errorf("publish items: %w", err)
			}
			published += len(batch)
		}

		if len(coll.Items) < pageSize || (coll.TotalCount > 0 && offset+len(coll.Items) >= coll.TotalCount) {
			break
		}
		if hasLast && (!keyed || last > lower) {
			// Step forward by value. The records at exactly `last` come back
			// on the next page and are met again, not re-sent.
			lower, keyed = last, true
			offset, runAnchor = 0, ""
		} else {
			// The whole page shares one cursor value: page through the run.
			offset += len(coll.Items) - 1
			runAnchor = contentHash(coll.Items[len(coll.Items)-1])
		}
	}

	if sawCursor {
		s.saveIncremental(ctx, tenantID, connID, cfg, spec, newMark, seen, published, logger)
	} else if published > 0 {
		logger.Warn("No Sitoo record carried the cursor field; every poll will read everything",
			"cursor_field", spec.field)
	}
	logger.Info("Sitoo fetch complete", "resource", cfg.effectiveResource(), "items", published,
		"already_sent", skipped, "pages", pages, "run_restarts", runRestarts, "incremental", true,
		"resumed", resume, "since", watermark, "cursor", newMark)
	return nil
}

// contentHash identifies one version of a record.
func contentHash(item json.RawMessage) string {
	sum := sha256.Sum256(item)
	return hex.EncodeToString(sum[:16])
}

// cursorValue reads the cursor field as an integer. Sitoo timestamps and ids
// are JSON numbers; a quoted number is accepted too.
func cursorValue(item json.RawMessage, field string) (int64, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(item, &fields); err != nil {
		return 0, false
	}
	raw, ok := fields[field]
	if !ok {
		return 0, false
	}
	str := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if v, err := strconv.ParseInt(str, 10, 64); err == nil {
		return v, true
	}
	if f, err := strconv.ParseFloat(str, 64); err == nil && f == float64(int64(f)) {
		return int64(f), true
	}
	return 0, false
}
