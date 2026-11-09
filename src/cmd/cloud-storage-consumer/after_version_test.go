package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/doneset"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #718: the after-action must not remove an object that was not
// published. Backlog 189 (the poll retry pass), 190 (GCS metadata edits) and
// 167 (conditional deletes).

// Two bodies of one size: a replacement with the same size and, through
// fsReplace with the same second, the same last-modified.
const (
	avOld = `{"a":1}`
	avNew = `{"a":2}`
)

// avListStore runs afterList once, right after a listing: an upload that lands
// between the listing and the retry pass.
type avListStore struct {
	*flakyStore
	once      sync.Once
	afterList func()
}

func (s *avListStore) List(ctx context.Context, prefix string) ([]objectstore.Object, error) {
	objs, err := s.flakyStore.List(ctx, prefix)
	if s.afterList != nil {
		s.once.Do(s.afterList)
	}
	return objs, err
}

// condStore is a flakyStore that implements objectstore.Conditional the way
// the backends do: DeleteIf and CopyIf act only while the key's generation is
// the version given. beforeAct, while set, runs once at the start of the next
// delete or copy: an upload that lands after the caller's Stat.
type condStore struct {
	*flakyStore
	mu        sync.Mutex
	beforeAct func()
	// beforeDelete is beforeAct for deletes only: an upload between a move's
	// copy and its delete.
	beforeDelete func()
	// skip is how many deletes and copies go by before beforeAct runs.
	skip    int
	refused int
}

func (s *condStore) hook(deleting bool) {
	s.mu.Lock()
	if s.skip > 0 {
		s.skip--
		s.mu.Unlock()
		return
	}
	f := s.beforeAct
	s.beforeAct = nil
	if deleting && f == nil {
		f = s.beforeDelete
		s.beforeDelete = nil
	}
	s.mu.Unlock()
	if f != nil {
		f()
	}
}

func (s *condStore) at(key, version string) error {
	s.fakeStore.mu.Lock()
	_, ok := s.objects[key]
	gen := strconv.Itoa(s.gen[key])
	s.fakeStore.mu.Unlock()
	if !ok {
		return fmt.Errorf("%q: %w", key, objectstore.ErrNotFound)
	}
	if gen != version {
		s.mu.Lock()
		s.refused++
		s.mu.Unlock()
		return fmt.Errorf("%q at %s, not %s: %w", key, gen, version, objectstore.ErrVersionChanged)
	}
	return nil
}

func (s *condStore) DeleteIf(ctx context.Context, key, version string) error {
	s.hook(true)
	if err := s.at(key, version); err != nil {
		return err
	}
	return s.flakyStore.Delete(ctx, key)
}

func (s *condStore) CopyIf(ctx context.Context, src, dst, version string) error {
	s.hook(false)
	if err := s.at(src, version); err != nil {
		return err
	}
	return s.flakyStore.Copy(ctx, src, dst)
}

// Plain Delete and Copy run the hook too, so a test against a consumer that
// does not use the conditional calls sees the same upload.
func (s *condStore) Delete(ctx context.Context, key string) error {
	s.hook(true)
	return s.flakyStore.Delete(ctx, key)
}

func (s *condStore) Copy(ctx context.Context, src, dst string) error {
	s.hook(false)
	return s.flakyStore.Copy(ctx, src, dst)
}

var _ objectstore.Conditional = (*condStore)(nil)

// newAVPoll is a poll rig with in/a.json (avOld) whose first after-action
// fails, polling through the store wrap returns.
func newAVPoll(t *testing.T, action string, wrap func(fk *flakyStore) objectstore.ObjectStore) *faPoll {
	t.Helper()
	p, _ := newFAPoll(t, action, nil)
	pdPut(p.fk.fakeStore, "in/a.json", avOld, 0)
	if action == "move" {
		p.fk.set(0, 1)
	} else {
		p.fk.set(1, 0)
	}
	if wrap != nil {
		p.store = wrap(p.fk)
	}
	return p
}

// avReplaced asserts the replacement under in/a.json was published before it
// was removed: two publishes, the second of the new body, and nothing left.
func avReplaced(t *testing.T, p *faPoll, action string) {
	t.Helper()
	requireCounts(t, p.log, "after the polls", map[string]int{"in/a.json": 2})
	if got := p.log.payloads("in/a.json"); got[1] != avNew {
		t.Errorf("second publish carried %q, want the replacement %q", got[1], avNew)
	}
	if p.fk.has("in/a.json") {
		t.Error("the replacement, now published, is still in the bucket")
	}
	if action == "move" {
		if body, _ := fsBody(p.fk.fakeStore, "done/a.json"); body != avNew {
			t.Errorf("done/a.json = %q, want the replacement", body)
		}
	}
}

// 189: an object whose after-action failed is done. A replacement with the
// same size in the same second has the listing's size and last-modified; only
// its version tells it apart. The retry pass must not remove it: it is
// published first.
func TestRetryPass_SameSecondReplacementIsPublished(t *testing.T) {
	for _, action := range []string{"delete", "move"} {
		t.Run(action, func(t *testing.T) {
			p := newAVPoll(t, action, nil)
			p.tick()
			requireCounts(t, p.log, "first poll", map[string]int{"in/a.json": 1})
			if !p.logHas("after-action failed; will retry") {
				t.Fatalf("first after-action did not fail:\n%s", p.logs.String())
			}
			fsReplace(p.fk.fakeStore, "in/a.json", avNew, 0)

			avTicks(p, 4) // two minutes: past the first backoff
			avReplaced(t, p, action)
		})
	}
}

// 189: a replacement that lands after the listing, before the retry pass gets
// to it, is seen by the retry's Stat and left; the done set forgets the key,
// so the same poll publishes it. The store has no conditional delete, so the
// Stat is all that stands between it and the delete.
func TestRetryPass_ReplacedAfterListingIsLeft(t *testing.T) {
	for _, action := range []string{"delete", "move"} {
		t.Run(action, func(t *testing.T) {
			var ls *avListStore
			p := newAVPoll(t, action, func(fk *flakyStore) objectstore.ObjectStore {
				ls = &avListStore{flakyStore: fk}
				return ls
			})
			p.tick()
			requireCounts(t, p.log, "first poll", map[string]int{"in/a.json": 1})

			avTicks(p, 1) // the retry is due at the next poll
			// That poll lists the old object; the replacement lands right
			// after.
			ls.afterList = func() { fsReplace(p.fk.fakeStore, "in/a.json", avNew, 0) }
			ls.once = sync.Once{}
			p.tick()
			if !p.logHas("level=WARN", "replaced since published; left for the next poll", "key=in/a.json") {
				t.Errorf("no Warn for the replacement:\n%s", p.logs.String())
			}
			p.tick()
			avReplaced(t, p, action)
		})
	}
}

// 167: an upload between the Stat and the delete (for a move, the copy) is
// refused by the conditional call and published at the next poll, both on the
// first after-action and on a retry.
func TestAfterAction_ConditionalPoll(t *testing.T) {
	cases := []struct {
		name string
		// arm sets the upload up on cs; retry makes the first after-action
		// fail, so the upload hits the retry.
		arm   func(cs *condStore, fs *fakeStore)
		retry bool
	}{
		{"first after-action", func(cs *condStore, fs *fakeStore) {
			cs.beforeAct = func() { fsReplace(fs, "in/a.json", avNew, 0) }
		}, false},
		{"retry", func(cs *condStore, fs *fakeStore) {
			cs.beforeAct = func() { fsReplace(fs, "in/a.json", avNew, 0) }
		}, true},
		{"between copy and delete", func(cs *condStore, fs *fakeStore) {
			cs.beforeDelete = func() { fsReplace(fs, "in/a.json", avNew, 0) }
		}, false},
	}
	for _, tc := range cases {
		for _, action := range []string{"delete", "move"} {
			if tc.name == "between copy and delete" && action == "delete" {
				continue
			}
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				var cs *condStore
				p := newAVPoll(t, action, func(fk *flakyStore) objectstore.ObjectStore {
					cs = &condStore{flakyStore: fk}
					return cs
				})
				if !tc.retry {
					p.fk.set(0, 0)
					tc.arm(cs, p.fk.fakeStore)
				}
				p.tick()
				if tc.retry {
					requireCounts(t, p.log, "first poll", map[string]int{"in/a.json": 1})
					tc.arm(cs, p.fk.fakeStore)
					// Past the first backoff. The refused retry forgets the
					// key, so the same poll publishes the replacement.
					avTicks(p, 3)
				} else {
					requireCounts(t, p.log, "first poll", map[string]int{"in/a.json": 1})
					if body, ok := fsBody(p.fk.fakeStore, "in/a.json"); !ok || body != avNew {
						t.Fatalf("replacement gone or changed (present %v, body %q): removed unpublished", ok, body)
					}
				}
				if cs.refused == 0 {
					t.Fatalf("no conditional call was refused:\n%s", p.logs.String())
				}
				if !p.logHas("level=WARN", "replaced since published; left for the next poll", "key=in/a.json") {
					t.Errorf("no Warn for the replacement:\n%s", p.logs.String())
				}
				p.tick()
				avReplaced(t, p, action)
				if action == "move" && tc.name == "between copy and delete" {
					// The first copy was of the published object; the
					// replacement's own move overwrote it after its publish.
					if n := len(p.fk.attempts("copy")); n != 2 {
						t.Errorf("copies = %d, want 2", n)
					}
				}
			})
		}
	}
}

// 167, event mode: the first after-action and a retry from the queue.
func TestAfterAction_ConditionalEvent(t *testing.T) {
	const key = "in/a.csv"
	for _, retry := range []bool{false, true} {
		for _, action := range []string{"delete", "move"} {
			t.Run(fmt.Sprintf("retry=%v/%s", retry, action), func(t *testing.T) {
				fs := &fakeStore{}
				pdPut(fs, key, "x", 0)
				fk := &flakyStore{fakeStore: fs, clock: time.Now}
				cs := &condStore{flakyStore: fk}
				cs.beforeAct = func() { fsReplace(fs, key, "y", 0) }
				if retry {
					// The first try fails; the upload lands before the retry's
					// delete or copy, after its Stat.
					if action == "move" {
						fk.set(0, 1)
					} else {
						fk.set(1, 0)
					}
					cs.skip = 1
				}
				l := faStart(t, eaCfg(action), cs, key, nil)
				harness.Eventually(t, srWait, "replacement left", func() bool {
					return l.logHas("level=WARN", "replaced since published", "left for its own notification", "key="+key)
				})
				time.Sleep(50 * time.Millisecond) // a stray retry would show
				l.stop(t)
				if body, ok := fsBody(fs, key); !ok || body != "y" {
					t.Fatalf("%s: present %v, body %q: the replacement was removed unpublished", key, ok, body)
				}
				if l.logHas("level=ERROR") || l.logHas("failed again") {
					t.Errorf("a refused conditional call was counted as a failure:\n%s", l.logs.String())
				}
			})
		}
	}
}

// gcsEdit is a metadata-only edit on GCS: updated moves, the generation and
// the content stay.
func gcsEdit(f *fakeStore, key string, sec int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modified[key] = pdT0.Add(time.Duration(sec) * time.Second)
}

// 190, poll mode: on GCS a metadata-only edit does not make an object new.
// On S3 and Azure last-modified still counts.
func TestGCSMetadataEdit_Poll(t *testing.T) {
	for _, tc := range []struct {
		provider string
		want     int
	}{
		{objectstore.ProviderGCS, 1},
		{objectstore.ProviderS3, 2},
		{objectstore.ProviderAzure, 2},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			bucket := &fakeStore{}
			pdPut(bucket, "in/a.json", avOld, 0)
			r := newPDRig(t, newPDStore(), bucket)
			r.cfg.Provider = tc.provider
			r.boot()
			r.poll()
			gcsEdit(bucket, "in/a.json", 30)
			r.polls(2)
			requireCounts(t, r.log, "after the edit", map[string]int{"in/a.json": tc.want})
		})
	}
}

// 190, event mode: a metadata-only edit on GCS during the publish does not
// stop the delete or move.
func TestGCSMetadataEdit_Event(t *testing.T) {
	const key = "in/a.csv"
	for _, action := range []string{"delete", "move"} {
		t.Run(action, func(t *testing.T) {
			fs := &fakeStore{}
			pdPut(fs, key, "x", 0)
			store := &eaStore{fakeStore: fs}
			cfg := eaCfg(action)
			cfg.Provider = objectstore.ProviderGCS
			l := faStart(t, cfg, store, key, func(n int32) {
				if n == 1 {
					gcsEdit(fs, key, 9)
				}
			})
			harness.Eventually(t, srWait, "object removed", func() bool {
				_, ok := fsBody(fs, key)
				return !ok
			})
			l.stop(t)
			if l.logHas("replaced since published") {
				t.Errorf("a metadata-only edit read as a replacement:\n%s", l.logs.String())
			}
		})
	}
}

// 190, the event retry queue compares with the provider's match too.
func TestEventRetryQueue_UsesMatch(t *testing.T) {
	published := doneset.Stamp{Size: 1, MTime: 100, Version: "7"}
	edited := objectstore.Object{Key: "in/a", Size: 1, LastModified: time.Unix(109, 0), Version: "7"}
	for _, tc := range []struct {
		name  string
		match doneset.Match
		want  bool
	}{
		{"gcs: generation decides", doneset.MatchVersion, true},
		{"s3: last-modified counts", doneset.MatchTime, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			q := newAfterRetryQueue(slog.New(slog.NewTextHandler(io.Discard, nil)), "delete", "bkt",
				afterRetryTuning{base: time.Hour}, tc.match,
				func(context.Context, string) (objectstore.Object, error) { return edited, nil },
				func(_ context.Context, _, version string) error {
					ran = true
					if version != "7" {
						t.Errorf("retry acted on version %q, want the published 7", version)
					}
					return nil
				})
			q.failed("in/a", published, fmt.Errorf("503"))
			q.attempt(context.Background(), "in/a")
			if ran != tc.want {
				t.Errorf("after-action ran = %v, want %v", ran, tc.want)
			}
		})
	}
}

// afterAction falls back to the plain calls without a version or without a
// conditional store, and maps a refusal to errReplaced.
func TestAfterAction_ConditionalOnlyWithVersion(t *testing.T) {
	c := &cloudConsumer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cfg := &cloudConfig{AfterAction: "delete"}
	fs := &fakeStore{}
	pdPut(fs, "in/a", "x", 0)
	cs := &condStore{flakyStore: &flakyStore{fakeStore: fs, clock: time.Now}}

	if err := c.afterAction(context.Background(), cs, cfg, "in/a", "5"); !isReplaced(err) {
		t.Fatalf("delete at a stale version = %v, want errReplaced", err)
	}
	if _, ok := fsBody(fs, "in/a"); !ok {
		t.Fatal("deleted at a stale version")
	}
	if err := c.afterAction(context.Background(), cs, cfg, "in/a", ""); err != nil {
		t.Fatalf("delete without a version: %v", err)
	}
	if _, ok := fsBody(fs, "in/a"); ok {
		t.Fatal("not deleted without a version")
	}
}

func isReplaced(err error) bool {
	return errors.Is(err, errReplaced) && errors.Is(err, objectstore.ErrVersionChanged)
}

// avTicks is n polls through p.store.
func avTicks(p *faPoll, n int) {
	for i := 0; i < n; i++ {
		p.tick()
	}
}
