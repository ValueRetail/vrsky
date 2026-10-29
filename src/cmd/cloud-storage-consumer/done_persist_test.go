package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/doneset"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #538: in poll mode the cloud-storage consumer keeps the objects it
// published in connection_node_checkpoints, per tenant, connection and node,
// so a restart does not publish the bucket again (after_action none), while
// an object replaced under the same key, or deleted and added again, is
// published once more. Event mode is unchanged and not covered here.
//
// Every assertion counts what was published. Most tests drive pollOnce
// directly: a "boot" is a new cloudConsumer (a new pod) with a new done set
// over the same checkpoint store, sharing nothing else with the previous one.
// The end-to-end test runs the consumer through the SDK runner and NATS
// commands.

const (
	pdTenant = "tenant-pd"
	pdConn   = "conn-pd"
	pdWait   = 5 * time.Second
)

var pdT0 = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

// --- checkpoint store --------------------------------------------------------

// pdStore is a goroutine-safe checkpoint.Store that copies on the way in and
// out, the way a database does. Reads fail while failGet is set.
type pdStore struct {
	mu      sync.Mutex
	rows    map[[3]string]checkpoint.Checkpoint
	failGet bool
}

func newPDStore() *pdStore { return &pdStore{rows: make(map[[3]string]checkpoint.Checkpoint)} }

func (s *pdStore) Save(_ context.Context, cp *checkpoint.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *cp
	c.State = append(json.RawMessage(nil), cp.State...)
	s.rows[[3]string{cp.TenantID, cp.ConnectionID, cp.NodeID}] = c
	return nil
}

func (s *pdStore) Get(_ context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failGet {
		return nil, errors.New("checkpoint read failed")
	}
	cp, ok := s.rows[[3]string{tenantID, connectionID, nodeID}]
	if !ok {
		return nil, nil
	}
	cp.State = append(json.RawMessage(nil), cp.State...)
	return &cp, nil
}

func (s *pdStore) Delete(_ context.Context, tenantID, connectionID, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, [3]string{tenantID, connectionID, nodeID})
	return nil
}

func (s *pdStore) DeleteForConnection(_ context.Context, tenantID, connectionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.rows {
		if k[0] == tenantID && k[1] == connectionID {
			delete(s.rows, k)
		}
	}
	return nil
}

func (s *pdStore) setFailGet(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failGet = fail
}

// copyOf is the store as it is right now, detached from later writes: what a
// new pod finds after the old one was killed without a chance to save.
func (s *pdStore) copyOf() *pdStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := newPDStore()
	for k, v := range s.rows {
		v.State = append(json.RawMessage(nil), v.State...)
		out.rows[k] = v
	}
	return out
}

// --- bucket ------------------------------------------------------------------

// pdPut writes key into the fake bucket, last modified pdT0+sec seconds.
func pdPut(f *fakeStore, key, body string, sec int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	if f.modified == nil {
		f.modified = map[string]time.Time{}
	}
	f.objects[key] = []byte(body)
	f.modified[key] = pdT0.Add(time.Duration(sec) * time.Second)
}

func pdDel(f *fakeStore, key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	delete(f.modified, key)
}

// --- published envelopes -----------------------------------------------------

// pdLog records what was published: object key to the payloads, in order.
type pdLog struct {
	mu   sync.Mutex
	pubs map[string][]string
}

func (l *pdLog) add(key, payload string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pubs == nil {
		l.pubs = map[string][]string{}
	}
	l.pubs[key] = append(l.pubs[key], payload)
}

func (l *pdLog) publish(_ context.Context, env *envelope.Envelope) error {
	key, _ := env.Metadata["object_key"].(string)
	l.add(key, string(env.Payload))
	return nil
}

// counts is object key to how many times it was published.
func (l *pdLog) counts() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]int{}
	for n, p := range l.pubs {
		out[n] = len(p)
	}
	return out
}

func (l *pdLog) payloads(key string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.pubs[key]...)
}

func pdFmt(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if m[k] == 0 {
			continue
		}
		b.WriteString(k + "=" + strconv.Itoa(m[k]) + " ")
	}
	return "{" + strings.TrimSpace(b.String()) + "}"
}

// requireCounts fails unless exactly the objects in want were published, each
// that many times in total.
func requireCounts(t *testing.T, l *pdLog, when string, want map[string]int) {
	t.Helper()
	if got := pdFmt(l.counts()); got != pdFmt(want) {
		t.Fatalf("%s: published %s, want %s", when, got, pdFmt(want))
	}
}

// --- logs --------------------------------------------------------------------

// pdLogs counts error-level log records.
type pdLogs struct{ errors atomic.Int32 }

func (h *pdLogs) Enabled(context.Context, slog.Level) bool { return true }
func (h *pdLogs) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		h.errors.Add(1)
	}
	return nil
}
func (h *pdLogs) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *pdLogs) WithGroup(string) slog.Handler      { return h }

// --- rig ---------------------------------------------------------------------

// pdRig is one cloud-storage connection across several consumer lifetimes,
// polled by the test. The log is shared by all lifetimes, so counts are
// totals.
type pdRig struct {
	t      *testing.T
	store  checkpoint.Store
	bucket *fakeStore
	tenant string
	conn   string
	cfg    cloudConfig
	log    *pdLog
	logs   *pdLogs

	c    *cloudConsumer
	done *doneset.Set
}

func newPDRig(t *testing.T, store checkpoint.Store, bucket *fakeStore) *pdRig {
	return &pdRig{
		t: t, store: store, bucket: bucket, tenant: pdTenant, conn: pdConn,
		cfg: cloudConfig{
			Config:      objectstore.Config{Provider: "s3", Bucket: "b", Prefix: "in/"},
			AfterAction: "none", nodeID: "c1",
		},
		log: &pdLog{}, logs: &pdLogs{},
	}
}

// boot is a new pod: a new consumer and a new done set over the same store.
func (r *pdRig) boot() {
	r.c = &cloudConsumer{
		logger:      slog.New(r.logs),
		newStore:    func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return r.bucket, nil },
		publish:     r.log.publish,
		checkpoints: r.store,
	}
	r.done = r.c.doneSet(r.conn, r.tenant, &r.cfg)
}

func (r *pdRig) poll() {
	r.c.pollOnce(context.Background(), r.conn, r.tenant, r.bucket, &r.cfg, r.done, r.c.logger)
}

func (r *pdRig) polls(n int) {
	for i := 0; i < n; i++ {
		r.poll()
	}
}

// --- AC 1: a restart does not publish again ----------------------------------

func TestDonePersist_RestartDoesNotRepublish(t *testing.T) {
	bucket := &fakeStore{}
	pdPut(bucket, "in/a.json", `{"a":1}`, 0)
	pdPut(bucket, "in/b.json", `{"b":1}`, 0)
	pdPut(bucket, "in/c.csv", "c\n1\n", 0)
	r := newPDRig(t, newPDStore(), bucket)

	r.boot()
	r.polls(2)
	requireCounts(t, r.log, "first start", map[string]int{"in/a.json": 1, "in/b.json": 1, "in/c.csv": 1})

	r.boot()
	r.polls(2)
	requireCounts(t, r.log, "after the restart", map[string]int{"in/a.json": 1, "in/b.json": 1, "in/c.csv": 1})

	pdPut(bucket, "in/d.json", `{"d":1}`, 5)
	r.polls(2)
	requireCounts(t, r.log, "new object after the restart", map[string]int{"in/a.json": 1, "in/b.json": 1, "in/c.csv": 1, "in/d.json": 1})

	r.boot()
	r.poll()
	requireCounts(t, r.log, "after a second restart", map[string]int{"in/a.json": 1, "in/b.json": 1, "in/c.csv": 1, "in/d.json": 1})
}

// A stop that cuts a poll short still saves what that poll published, and a
// pod killed mid-poll repeats at most saveEvery objects.
func TestDonePersist_StopMidPollSavesWhatWasPublished(t *testing.T) {
	const objects = saveEvery + 8
	bucket := &fakeStore{}
	all := map[string]int{}
	for i := 0; i < objects; i++ {
		key := "in/f" + strconv.Itoa(1000+i) + ".txt"
		pdPut(bucket, key, "x", 0)
		all[key] = 1
	}

	t.Run("stop", func(t *testing.T) {
		r := newPDRig(t, newPDStore(), bucket)
		r.boot()
		ctx, cancel := context.WithCancel(context.Background())
		var n atomic.Int32
		r.c.publish = func(c context.Context, env *envelope.Envelope) error {
			if n.Add(1) == saveEvery+3 {
				cancel() // the stop lands after this publish
			}
			return r.log.publish(c, env)
		}
		r.c.pollOnce(ctx, r.conn, r.tenant, r.bucket, &r.cfg, r.done, r.c.logger)
		if got := int(n.Load()); got != saveEvery+3 {
			t.Fatalf("published %d before the stop took effect, want %d", got, saveEvery+3)
		}
		r.boot()
		r.poll()
		requireCounts(t, r.log, "after the stop and a start", all)
	})

	t.Run("kill", func(t *testing.T) {
		store := newPDStore()
		r := newPDRig(t, store, bucket)
		r.boot()
		var n atomic.Int32
		var killed *pdStore
		r.c.publish = func(c context.Context, env *envelope.Envelope) error {
			if n.Add(1) == saveEvery+3 {
				killed = store.copyOf() // the pod dies here
			}
			return r.log.publish(c, env)
		}
		r.poll()
		requireCounts(t, r.log, "first poll", all)
		r.store = killed
		r.boot()
		r.poll()
		total := 0
		for _, c := range r.log.counts() {
			total += c
		}
		if total != objects+(objects-saveEvery) {
			t.Errorf("published %d in all, want %d: the objects after the last save, once more", total, objects+(objects-saveEvery))
		}
	})
}

// --- AC 2: a replaced object is published once more ----------------------------

func TestDonePersist_ReplacedObjectIsPublishedOnceMore(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		sec          int
		whileStopped bool
	}{
		{"new size", `{"a":22}`, 0, false},
		{"new last-modified", `{"a":2}`, 60, false},
		{"new size while stopped", `{"a":22}`, 0, true},
		{"new last-modified while stopped", `{"a":2}`, 60, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bucket := &fakeStore{}
			pdPut(bucket, "in/a.json", `{"a":1}`, 0)
			pdPut(bucket, "in/b.json", `{"b":1}`, 0)
			r := newPDRig(t, newPDStore(), bucket)
			r.boot()
			r.poll()
			requireCounts(t, r.log, "first poll", map[string]int{"in/a.json": 1, "in/b.json": 1})

			pdPut(bucket, "in/a.json", tc.body, tc.sec)
			if tc.whileStopped {
				r.boot()
			}
			r.polls(2)
			requireCounts(t, r.log, "after the replace", map[string]int{"in/a.json": 2, "in/b.json": 1})
			if got := r.log.payloads("in/a.json"); got[1] != tc.body {
				t.Errorf("second publish of in/a.json = %q, want the new contents %q", got[1], tc.body)
			}

			r.boot()
			r.polls(2)
			requireCounts(t, r.log, "after a restart", map[string]int{"in/a.json": 2, "in/b.json": 1})
		})
	}
}

// --- AC 3: deleted and added again is published again ------------------------

func TestDonePersist_DeletedThenAddedAgainIsPublishedAgain(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "same poller"
		if restart {
			name = "re-added while stopped"
		}
		t.Run(name, func(t *testing.T) {
			bucket := &fakeStore{}
			pdPut(bucket, "in/a.json", `{"a":1}`, 0)
			pdPut(bucket, "in/b.json", `{"b":1}`, 0)
			r := newPDRig(t, newPDStore(), bucket)
			r.boot()
			r.poll()

			pdDel(bucket, "in/a.json")
			r.poll()
			if restart {
				r.boot()
				r.poll()
			}
			requireCounts(t, r.log, "after the delete", map[string]int{"in/a.json": 1, "in/b.json": 1})

			// Same key, contents and last-modified as before.
			pdPut(bucket, "in/a.json", `{"a":1}`, 0)
			r.polls(2)
			requireCounts(t, r.log, "added again", map[string]int{"in/a.json": 2, "in/b.json": 1})
		})
	}
}

// --- AC 4: tenants are separate -----------------------------------------------

func TestDonePersist_TwoTenantsSameConnectionAndNode(t *testing.T) {
	store := newPDStore()
	bucketA, bucketB := &fakeStore{}, &fakeStore{}
	pdPut(bucketA, "in/x.json", `{"x":"a"}`, 0)
	pdPut(bucketB, "in/x.json", `{"x":"b"}`, 0) // same key, size and last-modified as A's
	a := newPDRig(t, store, bucketA)
	a.tenant = "tenant-a"
	b := newPDRig(t, store, bucketB)
	b.tenant = "tenant-b"

	a.boot()
	a.poll()
	requireCounts(t, a.log, "A", map[string]int{"in/x.json": 1})

	b.boot()
	b.poll()
	requireCounts(t, b.log, "B, after A published its x", map[string]int{"in/x.json": 1})
	if got := b.log.payloads("in/x.json"); got[0] != `{"x":"b"}` {
		t.Errorf("B published %q, want its own x", got[0])
	}

	b.boot()
	b.polls(2)
	requireCounts(t, b.log, "B after its restart", map[string]int{"in/x.json": 1})
	a.boot()
	a.polls(2)
	requireCounts(t, a.log, "A after its restart", map[string]int{"in/x.json": 1})
}

// --- AC 5: a failed checkpoint read publishes nothing --------------------------

func TestDonePersist_ReadFailurePublishesNothingUntilItWorks(t *testing.T) {
	t.Run("restart", func(t *testing.T) {
		store := newPDStore()
		bucket := &fakeStore{}
		pdPut(bucket, "in/a.json", `{"a":1}`, 0)
		pdPut(bucket, "in/b.json", `{"b":1}`, 0)
		r := newPDRig(t, store, bucket)
		r.boot()
		r.poll()

		store.setFailGet(true)
		r.boot()
		pdPut(bucket, "in/c.json", `{"c":1}`, 0)
		before := r.logs.errors.Load()
		r.polls(3)
		requireCounts(t, r.log, "while reads fail", map[string]int{"in/a.json": 1, "in/b.json": 1})
		if r.done.Loaded() {
			t.Error("done set counts as loaded while reads fail")
		}
		if r.logs.errors.Load() == before {
			t.Error("no error logged for the failed read")
		}

		store.setFailGet(false)
		r.polls(2)
		requireCounts(t, r.log, "once reads work", map[string]int{"in/a.json": 1, "in/b.json": 1, "in/c.json": 1})
	})

	t.Run("first start", func(t *testing.T) {
		store := newPDStore()
		store.setFailGet(true)
		bucket := &fakeStore{}
		pdPut(bucket, "in/a.json", `{"a":1}`, 0)
		r := newPDRig(t, store, bucket)
		r.boot()
		r.polls(2)
		requireCounts(t, r.log, "while reads fail", map[string]int{})
		store.setFailGet(false)
		r.polls(2)
		requireCounts(t, r.log, "once reads work", map[string]int{"in/a.json": 1})
	})
}

// A one-shot connection (poll_interval_seconds 0) whose read fails publishes
// nothing and ends with status "error"; one whose read works ends "stopped".
func TestDonePersist_OneShotStatus(t *testing.T) {
	cases := []struct {
		name     string
		failRead bool
		status   string
		want     map[string]int
	}{
		{"read works", false, "stopped", map[string]int{"in/a.json": 1}},
		{"read fails", true, "error", map[string]int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			nodes := `[{"id":"c1","type":"consumer","config":{"type":"cloud_storage","cloud_storage":{"provider":"s3","bucket":"b","prefix":"in/","after_action":"none","poll_interval_seconds":0}}}]`
			mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(pdConn, pdTenant).
				WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
			mock.ExpectExec("UPDATE connections SET status").WithArgs("running", pdConn, pdTenant).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("UPDATE connections SET status").WithArgs(tc.status, pdConn, pdTenant).
				WillReturnResult(sqlmock.NewResult(0, 1))

			store := newPDStore()
			store.setFailGet(tc.failRead)
			bucket := &fakeStore{}
			pdPut(bucket, "in/a.json", `{"a":1}`, 0)
			log := &pdLog{}
			c := &cloudConsumer{
				db:          db,
				logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
				active:      make(map[string]*activeConn),
				publish:     log.publish,
				newStore:    func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return bucket, nil },
				checkpoints: store,
			}
			c.startConnection(context.Background(), pdConn, pdTenant)
			harness.Eventually(t, pdWait, "the one-shot to end with status "+tc.status, func() bool {
				return mock.ExpectationsWereMet() == nil
			})
			requireCounts(t, log, "one-shot", tc.want)
		})
	}
}

// --- AC 6: a first start publishes everything --------------------------------

func TestDonePersist_FirstStartPublishesEverything(t *testing.T) {
	cases := []struct {
		name  string
		saved func(cfg *cloudConfig) // the config a previous lifetime saved under; nil: nothing saved
	}{
		{"nothing saved", nil},
		{"saved for another prefix", func(cfg *cloudConfig) { cfg.Prefix = "" }}, // the whole bucket: lists the same keys
		{"saved for another bucket", func(cfg *cloudConfig) { cfg.Bucket = "old-bucket" }},
		{"saved for another provider", func(cfg *cloudConfig) { cfg.Provider = "gcs" }},
		{"saved for another endpoint", func(cfg *cloudConfig) { cfg.Endpoint = "http://minio.old:9000" }},
		{"saved for another account", func(cfg *cloudConfig) { cfg.AccountName = "oldaccount" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newPDStore()
			bucket := &fakeStore{}
			pdPut(bucket, "in/a.json", `{"a":1}`, 0)
			pdPut(bucket, "in/b.json", `{"b":1}`, 0)
			if tc.saved != nil {
				old := newPDRig(t, store, bucket)
				tc.saved(&old.cfg)
				old.boot()
				old.poll()
				requireCounts(t, old.log, "the old config", map[string]int{"in/a.json": 1, "in/b.json": 1})
			}
			r := newPDRig(t, store, bucket)
			r.boot()
			r.poll()
			requireCounts(t, r.log, "first start", map[string]int{"in/a.json": 1, "in/b.json": 1})
			r.boot()
			r.poll()
			requireCounts(t, r.log, "its restart", map[string]int{"in/a.json": 1, "in/b.json": 1})
		})
	}
}

// --- two pollers of one connection (rolling deploy) --------------------------

// The old pod and the new one overlap: what one publishes the other does not
// publish again, unless it was replaced since.
func TestDonePersist_TwoPollersOfOneConnection(t *testing.T) {
	store := newPDStore()
	bucket := &fakeStore{}
	pdPut(bucket, "in/a.json", `{"a":1}`, 0)
	oldPod := newPDRig(t, store, bucket)
	newPod := newPDRig(t, store, bucket)
	newPod.log = oldPod.log // one count for the connection

	oldPod.boot()
	oldPod.poll()
	newPod.boot()
	newPod.poll()
	requireCounts(t, oldPod.log, "both started", map[string]int{"in/a.json": 1})

	pdPut(bucket, "in/x.json", `{"x":1}`, 0)
	oldPod.poll()
	newPod.poll()
	requireCounts(t, oldPod.log, "old pod published x", map[string]int{"in/a.json": 1, "in/x.json": 1})

	pdPut(bucket, "in/x.json", `{"x":22}`, 9)
	newPod.poll()
	oldPod.poll()
	newPod.poll()
	requireCounts(t, oldPod.log, "new pod published the replaced x", map[string]int{"in/a.json": 1, "in/x.json": 2})

	pdPut(bucket, "in/y.json", `{"y":1}`, 0)
	newPod.poll()
	oldPod.poll()
	r := newPDRig(t, store, bucket)
	r.log = oldPod.log
	r.boot()
	r.poll()
	requireCounts(t, oldPod.log, "a third start", map[string]int{"in/a.json": 1, "in/x.json": 2, "in/y.json": 1})
}

// --- end to end: NATS stop and start ------------------------------------------

// pdCountingStore counts listings, one per poll: once the count passes n,
// poll n has ended, its save included.
type pdCountingStore struct {
	*fakeStore
	lists atomic.Int32
}

func (f *pdCountingStore) List(ctx context.Context, prefix string) ([]objectstore.Object, error) {
	defer f.lists.Add(1)
	return f.fakeStore.List(ctx, prefix)
}

// pdData collects what arrives on the data stream.
type pdData struct {
	sub    *nats.Subscription
	counts map[string]int
}

func pdSubscribe(t *testing.T, nc *nats.Conn) *pdData {
	t.Helper()
	sub, err := nc.SubscribeSync("vrsky.data.>")
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	return &pdData{sub: sub, counts: map[string]int{}}
}

// drain takes in everything that arrived, waiting up to quiet for more.
func (d *pdData) drain(t *testing.T, quiet time.Duration) map[string]int {
	t.Helper()
	for {
		m, err := d.sub.NextMsg(quiet)
		if errors.Is(err, nats.ErrTimeout) {
			return d.counts
		}
		if err != nil {
			t.Fatal(err)
		}
		env, err := envelope.Unmarshal(m.Data)
		if err != nil {
			t.Fatal(err)
		}
		key, _ := env.Metadata["object_key"].(string)
		d.counts[key]++
	}
}

// waitFor drains until key has arrived n times in all.
func (d *pdData) waitFor(t *testing.T, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(pdWait)
	for d.counts[key] < n {
		if time.Now().After(deadline) {
			t.Fatalf("%s arrived %d times, want %d", key, d.counts[key], n)
		}
		d.drain(t, 50*time.Millisecond)
	}
}

func (d *pdData) require(t *testing.T, when string, want map[string]int) {
	t.Helper()
	if got := pdFmt(d.drain(t, 100*time.Millisecond)); got != pdFmt(want) {
		t.Fatalf("%s: arrived %s, want %s", when, got, pdFmt(want))
	}
}

func pdCommand(t *testing.T, nc *nats.Conn, verb, connID, tenant string) {
	t.Helper()
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := nc.Publish("vrsky.commands."+tenant+".connection."+verb, cmd); err != nil {
		t.Fatal(err)
	}
}

// pdMockDB is the management database for count starts of the connection:
// the config lookup and the status writes of each.
func pdMockDB(t *testing.T, connID, tenant, nodes string, starts int) *sql.DB {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < starts; i++ {
		mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(connID, tenant).
			WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
		mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	return db
}

// Start, publish the bucket, stop, start again: nothing again; then a new
// object arrives once. (A new pod on the same store is a boot in the tests
// above.)
func TestDonePersist_EndToEnd_StopAndStart(t *testing.T) {
	store := newPDStore()
	bucket := &pdCountingStore{fakeStore: &fakeStore{}}
	pdPut(bucket.fakeStore, "in/a.json", `{"a":1}`, 0)
	pdPut(bucket.fakeStore, "in/b.json", `{"b":1}`, 0)
	pdPut(bucket.fakeStore, "in/c.csv", "c\n1\n", 0)
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"cloud_storage","cloud_storage":{"provider":"s3","bucket":"b","prefix":"in/","after_action":"none","poll_interval_seconds":1}}}]`
	listsAtLeast := func(what string, n int32) {
		t.Helper()
		harness.Eventually(t, pdWait, what, func() bool { return bucket.lists.Load() >= n })
	}

	db := pdMockDB(t, pdConn, pdTenant, nodes, 2)
	c := &cloudConsumer{
		newStore:    func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return bucket, nil },
		checkpoints: store,
	}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "cloud-storage-consumer", DB: db})
	data := pdSubscribe(t, h.NATS())
	time.Sleep(200 * time.Millisecond) // the consumer subscribes to commands in Run

	pdCommand(t, h.NATS(), "start", pdConn, pdTenant)
	data.waitFor(t, "in/c.csv", 1)
	listsAtLeast("the first poll to end", 2)
	data.require(t, "first start", map[string]int{"in/a.json": 1, "in/b.json": 1, "in/c.csv": 1})

	pdCommand(t, h.NATS(), "stop", pdConn, pdTenant)
	harness.Eventually(t, pdWait, "the stop", func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return len(c.active) == 0
	})
	listed := bucket.lists.Load()
	pdCommand(t, h.NATS(), "start", pdConn, pdTenant)
	listsAtLeast("the poll after the restart to list", listed+1)

	// Added after that poll listed: the next poll publishes it, after the
	// restart's own poll has ended.
	pdPut(bucket.fakeStore, "in/d.json", `{"d":1}`, 0)
	data.waitFor(t, "in/d.json", 1)
	listsAtLeast("the poll that published d to end", bucket.lists.Load()+1)
	data.require(t, "after stop and start, and a new object", map[string]int{"in/a.json": 1, "in/b.json": 1, "in/c.csv": 1, "in/d.json": 1})
}
