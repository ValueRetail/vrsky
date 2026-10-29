package main

// Incremental polling of Sitoo orders (#457, plans/issue-457-sitoo-incremental.md).
//
// Sitoo has no general modified-since filter. The one collection whose
// filter and sort the API reference documents is `orders`: `orderidfrom`
// returns orders from an id on, and `sort=orderid` lists them ascending
// (developer.sitoo.com, checked 2026-10-28). So incremental polling covers
// `orders` only, with that cursor fixed here.
//
// orderid is unique, which keeps the poll simple:
//
//   - Pages are read by id, not by position: each next page asks for orders
//     from the previous page's last id on. Whether Sitoo treats the filter
//     as >= or >, that returns everything after it, and the repeated id is
//     dropped.
//   - Every page is checked before anything on it goes out: each order has
//     an integer orderid, the ids rise strictly, and none is below the id
//     asked for. A sort or filter Sitoo ignores fails the poll instead of
//     losing an order.
//   - Once a page is out, every order up to its last id is out, so the
//     watermark is saved after every page. A poll that fails later, or stops
//     at the page budget, resumes from there.
//   - Each poll starts cursor_overlap ids behind the watermark, for an order
//     whose id was handed out earlier but that became visible later. The
//     ids published inside that window are kept in the checkpoint, so the
//     re-read orders are not sent again.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

const (
	// incrementalResource is the only collection incremental polling covers.
	incrementalResource = "orders"
	// orderIDField is the record field, orderIDParam the filter on it and
	// orderIDSort the sort that lists orders ascending on it.
	orderIDField = "orderid"
	orderIDParam = "orderidfrom"
	orderIDSort  = "orderid"

	// defaultCursorOverlap is how far back each poll re-reads, in orderids.
	// At the default page size that is at most one extra page per poll.
	defaultCursorOverlap = 1000
	// maxCursorOverlap bounds cursor_overlap: the ids published inside the
	// window are kept in the checkpoint and rewritten after every page.
	maxCursorOverlap = 100_000
	// maxPagesPerPoll bounds one poll. The rest is read by the next poll,
	// from the watermark saved after the last page.
	maxPagesPerPoll = 100
	// saveTimeout bounds a checkpoint save, which a stop can't cancel.
	saveTimeout = 10 * time.Second
	// defaultCheckpointNode keys the checkpoint when the node has no id.
	defaultCheckpointNode = "sitoo-consumer"
)

// reservedParams are the query parameters the incremental poller sets
// itself; a resource query string must not carry them.
var reservedParams = []string{"start", "num", "sort", orderIDParam}

var (
	// errNoOrderID: an order without an integer orderid can't be placed
	// against the watermark.
	errNoOrderID = errors.New("sitoo order has no integer orderid")
	// errNotSorted: the listing isn't strictly ascending on orderid, so
	// paging by id could step past an order.
	errNotSorted = errors.New("sitoo orders are not sorted ascending on orderid; sort=orderid was ignored")
	// errFilterIgnored: an order came back below the orderidfrom asked for.
	errFilterIgnored = errors.New("sitoo returned orders below orderidfrom; the filter was ignored")
)

// checkIncremental reports whether the node can be polled incrementally.
// It's checked at start, so a node that can't is refused with a logged
// error before any request, and again at the top of each poll.
func (c *SitooConfig) checkIncremental() error {
	if err := c.checkResource(); err != nil {
		return err
	}
	if res := c.effectiveResource(); res != incrementalResource {
		return fmt.Errorf("incremental polling covers the %q resource only, not %q", incrementalResource, res)
	}
	q := c.resourceQuery()
	for k := range q {
		for _, r := range reservedParams {
			if strings.EqualFold(k, r) {
				return fmt.Errorf("resource %q sets %q, which incremental polling sets itself", c.Resource, k)
			}
		}
	}
	if c.CursorOverlap != nil && (*c.CursorOverlap < 0 || *c.CursorOverlap > maxCursorOverlap) {
		return fmt.Errorf("cursor_overlap must be between 0 and %d (got %d)", maxCursorOverlap, *c.CursorOverlap)
	}
	return nil
}

// incrementalPageSize is the page size an incremental poll asks for. A page
// has to hold the order it repeats and one more.
func (c *SitooConfig) incrementalPageSize() int {
	if c.PageSize <= 0 {
		return defaultPageSize
	}
	return max(c.PageSize, 2)
}

// overlap is how far back each poll re-reads, in orderids.
func (c *SitooConfig) overlap() int64 {
	if c.CursorOverlap != nil {
		return *c.CursorOverlap
	}
	return defaultCursorOverlap
}

// checkpointNode is the node id the checkpoint is stored under.
func (c *SitooConfig) checkpointNode() string {
	return firstNonEmpty(c.NodeID, defaultCheckpointNode)
}

// incrementalState is what goes into connection_node_checkpoints.state. The
// watermark itself is last_processed_message_id.
type incrementalState struct {
	// The query the watermark belongs to. A node whose resource or query
	// string changes starts over rather than resuming another query's
	// watermark.
	Resource      string `json:"resource"`
	ResourceQuery string `json:"resource_query"`
	// Sent holds the orderids published within the overlap window below the
	// watermark, so the re-read part is not sent twice. It is complete from
	// Floor up: a poll never re-reads below Floor, so raising cursor_overlap
	// can't re-send the orders between the old window and the new one. The
	// window widens as the watermark moves on.
	Sent  []int64 `json:"sent"`
	Floor int64   `json:"floor"`
	// Next is where a poll that stopped at its page budget before getting
	// past the watermark got to. The next poll carries on from there instead
	// of re-reading the overlap first; otherwise an overlap wider than the
	// budget would keep every poll from ever getting past the watermark. A
	// poll that gets past the watermark or reads to the end clears it.
	Next *int64 `json:"next,omitempty"`
}

// queryState is the state of a fresh checkpoint for this node's query.
func (c *SitooConfig) queryState() incrementalState {
	return incrementalState{Resource: c.effectiveResource(), ResourceQuery: c.resourceQuery().Encode()}
}

// loadIncremental returns the stored checkpoint: the watermark, its state
// (the floor and where a poll cut short by its page budget got to) and the
// orderids published within the overlap window. ok is false for a first
// poll.
//
// A store that can't be read fails the poll: polling from the start would
// re-send every order to a destination that may not dedupe, and the next
// tick retries the read anyway. A row that can't be decoded never will be,
// so it counts as no checkpoint and the first save replaces it.
func (s *sitooConsumer) loadIncremental(ctx context.Context, tenantID, connID string, cfg *SitooConfig,
	logger *slog.Logger) (watermark int64, st incrementalState, sent map[int64]bool, ok bool, err error) {
	sent = map[int64]bool{}
	if s.checkpoints == nil {
		return 0, st, sent, false, nil
	}
	cp, err := s.checkpoints.Get(ctx, tenantID, connID, cfg.checkpointNode())
	if err != nil {
		return 0, st, sent, false, fmt.Errorf("read Sitoo checkpoint: %w", err)
	}
	if cp == nil || cp.LastProcessedMessageID == "" {
		return 0, st, sent, false, nil
	}
	watermark, err = strconv.ParseInt(cp.LastProcessedMessageID, 10, 64)
	if err != nil {
		logger.Warn("Stored Sitoo watermark is not a number; reading every order once", "watermark", cp.LastProcessedMessageID)
		return 0, incrementalState{}, sent, false, nil
	}
	if err := json.Unmarshal(cp.State, &st); err != nil {
		logger.Warn("Stored Sitoo checkpoint state unreadable; reading every order once", "error", err)
		return 0, incrementalState{}, sent, false, nil
	}
	if want := cfg.queryState(); st.Resource != want.Resource || st.ResourceQuery != want.ResourceQuery {
		logger.Info("Sitoo query changed since the stored watermark; reading every order once",
			"stored_resource", st.Resource, "stored_query", st.ResourceQuery,
			"resource", want.Resource, "query", want.ResourceQuery)
		return 0, incrementalState{}, sent, false, nil
	}
	for _, id := range st.Sent {
		sent[id] = true
	}
	return watermark, st, sent, true, nil
}

// saveIncremental stores the watermark, the orderids published within its
// overlap window and complete from floor up, and next (nil unless the poll
// stopped at its page budget).
func (s *sitooConsumer) saveIncremental(ctx context.Context, tenantID, connID string, cfg *SitooConfig,
	watermark, floor int64, sent map[int64]bool, next *int64, published int) error {
	if s.checkpoints == nil {
		return nil
	}
	// The orders are already out: a stop arriving now must not cost the
	// save, or the next poll re-sends them. A hung database still can't keep
	// a stopped poller alive.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()
	st := cfg.queryState()
	st.Next = next
	st.Floor = max(watermark-cfg.overlap(), floor)
	st.Sent = []int64{}
	for id := range sent {
		if id >= st.Floor {
			st.Sent = append(st.Sent, id)
		}
	}
	slices.Sort(st.Sent)
	state, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode Sitoo checkpoint: %w", err)
	}
	if err := s.checkpoints.Save(ctx, &checkpoint.Checkpoint{
		TenantID:               tenantID,
		ConnectionID:           connID,
		NodeID:                 cfg.checkpointNode(),
		LastProcessedMessageID: strconv.FormatInt(watermark, 10),
		LastProcessedAt:        time.Now().UTC(),
		MessageCount:           int64(published),
		State:                  state,
	}); err != nil {
		return fmt.Errorf("save Sitoo checkpoint at orderid %d: %w", watermark, err)
	}
	return nil
}

// fetchIncremental publishes the orders not yet published, page by page in
// orderid order, and saves the watermark after each page. A poll that
// fails keeps the pages before the failure; the next one resumes after
// them, so an order can repeat (the failed page) but is not skipped.
func (s *sitooConsumer) fetchIncremental(ctx context.Context, connID, tenantID string, cfg *SitooConfig,
	logger *slog.Logger) error {
	watermark, st, sent, resume, err := s.loadIncremental(ctx, tenantID, connID, cfg, logger)
	if err != nil {
		return err
	}
	pageSize := cfg.incrementalPageSize()

	// from is the orderid the next page reads from; filtered is false only
	// on the first page of a first poll. A poll after one cut short by its
	// page budget carries on where that one got to. Below the floor the
	// stored set is incomplete, so a poll never starts there.
	from, filtered, floor := int64(0), resume, st.Floor
	switch {
	case st.Next != nil:
		from = *st.Next
	case resume:
		from = max(watermark-cfg.overlap(), floor, 0)
	}
	// carrying is true while the stored checkpoint still says to carry on: a
	// poll that reads to the end has to clear it, even with nothing new.
	carrying := st.Next != nil
	mark, loaded := watermark, watermark
	met := map[int64]bool{}
	published, skipped, pages, done := 0, 0, 0, false

	for !done {
		if pages == maxPagesPerPoll {
			// Carry on from here only if this poll never got past the
			// watermark it started with (an overlap wider than the budget).
			// Once it has, the next poll re-reads the overlap as usual: an
			// order that shows up late behind the new watermark is then read,
			// where carrying on would step past it.
			var next *int64
			if mark == loaded {
				next = &from
			}
			if err := s.saveIncremental(ctx, tenantID, connID, cfg, mark, floor, sent, next, published); err != nil {
				return err
			}
			logger.Info("Sitoo poll read its page budget; the next poll carries on",
				"pages", pages, "watermark", mark, "next", from)
			break
		}
		pages++
		q := url.Values{"sort": {orderIDSort}}
		if filtered {
			q.Set(orderIDParam, strconv.FormatInt(from, 10))
		}
		body, err := s.get(ctx, cfg, cfg.collectionURL(0, pageSize, q))
		if err != nil {
			return err
		}
		var coll sitooCollection
		if err := json.Unmarshal(body, &coll); err != nil {
			return fmt.Errorf("parse Sitoo orders: %w", err)
		}

		// Check the whole page before anything on it is published.
		ids := make([]int64, len(coll.Items))
		for i, item := range coll.Items {
			id, ok := orderID(item)
			switch {
			case !ok:
				return fmt.Errorf("%w (record %d on the page)", errNoOrderID, i)
			case filtered && id < from:
				return fmt.Errorf("%w (%s=%d, got %d)", errFilterIgnored, orderIDParam, from, id)
			case i > 0 && id <= ids[i-1]:
				return fmt.Errorf("%w (%d after %d)", errNotSorted, id, ids[i-1])
			}
			ids[i] = id
		}

		var batch []json.RawMessage
		for i, item := range coll.Items {
			if met[ids[i]] {
				continue
			}
			met[ids[i]] = true
			if sent[ids[i]] {
				skipped++
				continue
			}
			batch = append(batch, item)
		}
		if len(batch) > 0 {
			if err := s.publishItems(ctx, connID, tenantID, cfg.effectiveResource(), batch); err != nil {
				return fmt.Errorf("publish orders: %w", err)
			}
			published += len(batch)
			for i := range coll.Items {
				sent[ids[i]] = true
			}
		}
		if len(ids) > 0 && ids[len(ids)-1] > mark {
			mark = ids[len(ids)-1]
		}
		if len(ids) > 0 && (len(batch) > 0 || mark > watermark) {
			if err := s.saveIncremental(ctx, tenantID, connID, cfg, mark, floor, sent, nil, published); err != nil {
				return err
			}
			watermark, carrying = mark, false
		}

		// Paging is by id, so a short page isn't the end: Sitoo may cap num
		// below the page size. The end is a page with nothing past from.
		if len(ids) == 0 || (filtered && ids[len(ids)-1] <= from) {
			done = true
			if carrying {
				if err := s.saveIncremental(ctx, tenantID, connID, cfg, mark, floor, sent, nil, published); err != nil {
					return err
				}
			}
		} else {
			from, filtered = ids[len(ids)-1], true
		}
	}

	logger.Info("Sitoo fetch complete", "resource", cfg.effectiveResource(), "items", published,
		"already_sent", skipped, "pages", pages, "incremental", true, "resumed", resume, "watermark", mark)
	return nil
}

// orderID reads a record's orderid. Sitoo sends it as a JSON number; a
// quoted integer is accepted too.
func orderID(item json.RawMessage) (int64, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(item, &fields); err != nil {
		return 0, false
	}
	raw, ok := fields[orderIDField]
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(string(raw)), `"`), 10, 64)
	return id, err == nil
}
