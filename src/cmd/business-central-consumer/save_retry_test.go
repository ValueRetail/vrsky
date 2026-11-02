package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

// A checkpoint save that fails must not make the next poll read the whole
// entity again after a cursor_field change (#638). The store here behaves like
// the Postgres one: it refuses a cancelled context and can fail or hang.

var errStoreDown = errors.New("store down")

type flakyStore struct {
	checkpoint.Store
	mu        sync.Mutex
	failSaves int  // Saves to refuse; negative = every one
	block     bool // Save waits for its context instead of answering
	saveCalls int
	cancelled int // Saves that arrived with an already cancelled context
}

func (s *flakyStore) Get(ctx context.Context, tenantID, connID, nodeID string) (*checkpoint.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Store.Get(ctx, tenantID, connID, nodeID)
}

func (s *flakyStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	s.mu.Lock()
	s.saveCalls++
	if ctx.Err() != nil {
		s.cancelled++
	}
	fail, block := s.failSaves != 0, s.block
	if s.failSaves > 0 {
		s.failSaves--
	}
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	if fail {
		return errStoreDown
	}
	return s.Store.Save(ctx, cp)
}

func (s *flakyStore) set(fail int, block bool) {
	s.mu.Lock()
	s.failSaves, s.block = fail, block
	s.mu.Unlock()
}

func (s *flakyStore) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveCalls
}

func (b *twoFieldBC) askCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.asks)
}

// pollWith is poll with the caller's context and the error returned.
func pollWith(ctx context.Context, c *bcConsumer, got *[]*envelope.Envelope, mu *sync.Mutex, cfg *BCConfig) ([]string, error) {
	mu.Lock()
	before := len(*got)
	mu.Unlock()
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
		WithHTTPClient(http.DefaultClient)
	err := c.fetchAndPublish(ctx, "conn-1", "tenant-1", cfg, tok, c.logger)
	mu.Lock()
	defer mu.Unlock()
	var ids []string
	for _, env := range (*got)[before:] {
		var rs []map[string]any
		if jerr := json.Unmarshal(env.Payload, &rs); jerr != nil {
			panic(jerr)
		}
		for _, r := range rs {
			ids = append(ids, r["id"].(string))
		}
	}
	return ids, err
}

func TestSaveProgress_FieldChangeSurvivesAFailedSave(t *testing.T) {
	dateMark := dateField + " ge 2026-10-20"
	cases := []struct {
		name  string
		fail  int
		block bool
		// cancel cancels the poll's context once the records are published,
		// as a stop does.
		cancel      bool
		wantPoll2   bool // the field-change poll returns nil
		wantSaves   int  // Save calls during it
		wantNext    string
		wantNextIDs string
	}{
		{"save works", 0, false, false, true, 1, dateMark, ""},
		{"save fails once, the retry stores it", 1, false, false, true, 2, dateMark, ""},
		{"poll cancelled before the save, the retry stores it", 0, false, true, true, 2, dateMark, ""},
		{"cancelled and the retry fails too, the next poll stores it first", 2, false, true, false, 2, dateMark, ""},
		{"save fails twice, the next poll stores it first", 2, false, false, false, 2, dateMark, ""},
		{"store hangs, the next poll stores it first", 0, true, false, false, 2, dateMark, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bc := newTwoFieldBC(t, twoFieldRecs()...)
			c, got, mu := newTestConsumer()
			store := &flakyStore{Store: checkpoint.NewInMemoryStore()}
			c.checkpoints = store
			c.now = func() time.Time { return aheadNow }
			c.saveTimeout = 50 * time.Millisecond

			// Poll 1 on the DateTime field stores a row that names it.
			if ids := poll(t, c, got, mu, bc.cfg(lmField)); sorted(ids) != "a,b" {
				t.Fatalf("poll 1 published %q", sorted(ids))
			}
			base := store.calls()

			// Poll 2 is the first on the new field, on a config the poller
			// keeps, and its save is the one that goes wrong.
			cfg := bc.cfg(dateField)
			store.set(tc.fail, tc.block)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			orig := c.publish
			if tc.cancel {
				c.publish = func(pctx context.Context, env *envelope.Envelope) error {
					err := orig(pctx, env)
					cancel()
					return err
				}
			}
			start := time.Now()
			ids, err := pollWith(ctx, c, got, mu, cfg)
			elapsed := time.Since(start)
			c.publish = orig
			if sorted(ids) != "a,b" {
				t.Fatalf("poll 2 published %q, want a,b once", sorted(ids))
			}
			if (err == nil) != tc.wantPoll2 {
				t.Errorf("poll 2 error = %v, want success = %v", err, tc.wantPoll2)
			}
			if elapsed > 2*time.Second {
				t.Errorf("poll 2 took %v with a 50ms save timeout: the save is not bounded", elapsed)
			}
			if n := store.calls() - base; n != tc.wantSaves {
				t.Errorf("Save was called %d times, want %d", n, tc.wantSaves)
			}

			// Poll 3, same process, store healthy again: the row must name the
			// date field with a watermark, so nothing is read from the start
			// and nothing is sent twice.
			store.set(0, false)
			asksBefore := bc.askCount()
			ids, err = pollWith(context.Background(), c, got, mu, cfg)
			if err != nil {
				t.Fatalf("poll 3: %v", err)
			}
			if bc.askCount() != asksBefore+1 || bc.lastAsk() != tc.wantNext || sorted(ids) != tc.wantNextIDs {
				t.Errorf("poll 3 asked %q (%d requests) and published %q; want %q and %q",
					bc.lastAsk(), bc.askCount()-asksBefore, sorted(ids), tc.wantNext, tc.wantNextIDs)
			}
			cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src")
			if cp == nil || cp.LastProcessedMessageID != "2026-10-20" || storedCursorField(cp) != dateField {
				t.Errorf("row = %+v, want watermark 2026-10-20 on %s", cp, dateField)
			}
		})
	}
}

// While a poll's checkpoint is still unsaved, the next poll does not read
// anything: it would only publish the same records again.
func TestSaveProgress_UnsavedFieldChangeBlocksTheNextPoll(t *testing.T) {
	bc := newTwoFieldBC(t, twoFieldRecs()...)
	c, got, mu := newTestConsumer()
	store := &flakyStore{Store: checkpoint.NewInMemoryStore()}
	c.checkpoints = store
	c.now = func() time.Time { return aheadNow }
	c.saveTimeout = 20 * time.Millisecond
	poll(t, c, got, mu, bc.cfg(lmField))

	cfg := bc.cfg(dateField)
	store.set(-1, false)
	if _, err := pollWith(context.Background(), c, got, mu, cfg); err == nil {
		t.Fatal("poll 2: want the save error")
	}
	asks := bc.askCount()
	ids, err := pollWith(context.Background(), c, got, mu, cfg)
	if err == nil || len(ids) != 0 || bc.askCount() != asks {
		t.Errorf("poll 3 with the store still down: err %v, published %q, %d new requests; want an error and no read",
			err, sorted(ids), bc.askCount()-asks)
	}
	store.set(0, false)
	if ids, err := pollWith(context.Background(), c, got, mu, cfg); err != nil || len(ids) != 0 ||
		bc.lastAsk() != dateField+" ge 2026-10-20" {
		t.Errorf("poll 4: err %v, asked %q, published %q; want %q and nothing new",
			err, bc.lastAsk(), sorted(ids), dateField+" ge 2026-10-20")
	}
}

// Without a field change a lost save costs a few repeats, not a stop: the next
// poll still reads.
func TestSaveProgress_NoFieldChangeKeepsPolling(t *testing.T) {
	bc := newTwoFieldBC(t, twoFieldRecs()...)
	c, got, mu := newTestConsumer()
	store := &flakyStore{Store: checkpoint.NewInMemoryStore()}
	c.checkpoints = store
	c.now = func() time.Time { return aheadNow }
	c.saveTimeout = 20 * time.Millisecond
	cfg := bc.cfg(lmField)
	poll(t, c, got, mu, cfg)

	store.set(-1, false)
	bc.mu.Lock()
	bc.recs = append(bc.recs, fieldRec{"c", stamp(-time.Minute), "2026-10-21"})
	bc.mu.Unlock()
	ids, err := pollWith(context.Background(), c, got, mu, cfg)
	if err == nil || sorted(ids) != "c" {
		t.Errorf("poll 2: err %v, published %q; want the save error and c", err, sorted(ids))
	}
	store.set(0, false)
	ids, err = pollWith(context.Background(), c, got, mu, cfg)
	if err != nil || sorted(ids) != "c" || bc.lastAsk() != lmField+" gt "+stamp(-time.Hour) {
		t.Errorf("poll 3: err %v, asked %q, published %q; want c again from the old watermark", err, bc.lastAsk(), sorted(ids))
	}
}

// A store that cannot be read must not be answered with an empty watermark.
func TestSaveProgress_UnreadableStoreDoesNotEraseTheWatermark(t *testing.T) {
	c, _, _ := newTestConsumer()
	mem := checkpoint.NewInMemoryStore()
	c.checkpoints = &flakyStore{Store: mem}
	c.now = func() time.Time { return aheadNow }
	if err := mem.Save(context.Background(), &checkpoint.Checkpoint{
		TenantID: "t", ConnectionID: "c", NodeID: "n", LastProcessedMessageID: "W",
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Read fails on the cancelled context; the retry has a fresh one and works.
	if err := c.saveProgress(ctx, "t", "c", "n", "", false, 1, nil, c.logger); err != nil {
		t.Fatal(err)
	}
	cp, _ := mem.Get(context.Background(), "t", "c", "n")
	if cp == nil || cp.LastProcessedMessageID != "W" {
		t.Errorf("row = %+v, want watermark W kept", cp)
	}
}

// A save at shutdown that cannot finish gives up after its timeout.
func TestSaveProgress_ShutdownIsBounded(t *testing.T) {
	c, _, _ := newTestConsumer()
	store := &flakyStore{Store: checkpoint.NewInMemoryStore(), block: true}
	c.checkpoints = store
	c.now = func() time.Time { return aheadNow }
	c.saveTimeout = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := c.saveProgress(ctx, "t", "c", "n", "W", true, 1, nil, c.logger)
	if err == nil {
		t.Error("want the timeout error")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v, want about one 50ms timeout", d)
	}
}
