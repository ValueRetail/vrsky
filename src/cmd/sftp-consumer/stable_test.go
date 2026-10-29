package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #562: the SFTP consumer publishes a file only once its size and
// mtime are the same on two consecutive polls, at least one poll interval
// apart, so a file still being uploaded is not published half-written. The
// listing is faked at the sftpConn boundary (fakeSFTP); the poll-mode tests use
// the pdRig from done_persist_test.go, whose poll() moves the consumer's clock
// one interval and then polls.

// stStep is one poll of a table case: what the folder holds for the file
// before the poll (nil leaves it as it is), and how many times the file has
// been published in all after the poll.
type stStep struct {
	body *string
	sec  int // the file's mtime, pdT0 plus this many seconds
	want int
}

func stBody(s string) *string { return &s }

// stRun puts each step's version of name in the folder and polls once.
func stRun(t *testing.T, r *pdRig, name string, steps []stStep) {
	t.Helper()
	for i, st := range steps {
		if st.body != nil {
			pdPut(r.fake, name, *st.body, st.sec)
		}
		r.poll()
		if got := r.log.counts()[name]; got != st.want {
			t.Fatalf("after poll %d: %s published %d times, want %d", i+1, name, got, st.want)
		}
	}
}

// AC 1, 2, 3: published only on the second of two polls that see the same
// size and mtime; any change starts the wait again; a file that never settles
// is never published; a settled one is published once, finished.
func TestStable_PublishedOnceAfterTwoUnchangedPolls(t *testing.T) {
	cases := []struct {
		name  string
		steps []stStep
		final string // the payload of the one publish, when there is one
	}{
		{
			name: "unchanged from the first poll",
			steps: []stStep{
				{body: stBody(`{"a":1}`), want: 0},
				{want: 1},
				{want: 1},
				{want: 1},
			},
			final: `{"a":1}`,
		},
		{
			name: "size grows, same mtime",
			steps: []stStep{
				{body: stBody(`{"a"`), want: 0},
				{body: stBody(`{"a":1}`), want: 0},
				{want: 1},
				{want: 1},
			},
			final: `{"a":1}`,
		},
		{
			name: "mtime moves, same size",
			steps: []stStep{
				{body: stBody(`{"a":1}`), sec: 0, want: 0},
				{body: stBody(`{"a":2}`), sec: 1, want: 0},
				{want: 1},
				{want: 1},
			},
			final: `{"a":2}`,
		},
		{
			name: "grows over several polls, then settles",
			steps: []stStep{
				{body: stBody(`{`), sec: 0, want: 0},
				{body: stBody(`{"a"`), sec: 1, want: 0},
				{body: stBody(`{"a":`), sec: 2, want: 0},
				{body: stBody(`{"a":1}`), sec: 3, want: 0},
				{want: 1},
				{want: 1},
			},
			final: `{"a":1}`,
		},
		{
			// The wait compares with the previous look, not the first one.
			name: "changes and changes back",
			steps: []stStep{
				{body: stBody(`{"a":1}`), sec: 0, want: 0},
				{body: stBody(`{"a":12}`), sec: 1, want: 0},
				{body: stBody(`{"a":1}`), sec: 0, want: 0},
				{want: 1},
			},
			final: `{"a":1}`,
		},
		{
			name: "never stops changing",
			steps: []stStep{
				{body: stBody(`1`), sec: 0, want: 0},
				{body: stBody(`12`), sec: 1, want: 0},
				{body: stBody(`123`), sec: 2, want: 0},
				{body: stBody(`1234`), sec: 3, want: 0},
				{body: stBody(`12345`), sec: 4, want: 0},
				{body: stBody(`123456`), sec: 5, want: 0},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPDRig(t, newPDStore(), &fakeSFTP{})
			r.boot()
			stRun(t, r, "a.json", tc.steps)
			if tc.final == "" {
				return
			}
			if got := r.log.payloads("a.json"); len(got) != 1 || got[0] != tc.final {
				t.Errorf("published %q, want the finished file %q once", got, tc.final)
			}
		})
	}
}

// Other files in the folder are not held back by one still changing.
func TestStable_EachFileWaitsOnItsOwn(t *testing.T) {
	r := newPDRig(t, newPDStore(), &fakeSFTP{})
	r.boot()
	pdPut(r.fake, "done.json", `{"d":1}`, 0)
	pdPut(r.fake, "busy.json", `{`, 0)
	r.poll()
	requireCounts(t, r.log, "first poll", map[string]int{})
	pdPut(r.fake, "busy.json", `{"b"`, 1)
	r.poll()
	requireCounts(t, r.log, "second poll", map[string]int{"done.json": 1})
	pdPut(r.fake, "busy.json", `{"b":1}`, 2)
	r.poll()
	requireCounts(t, r.log, "third poll", map[string]int{"done.json": 1})
	r.poll()
	requireCounts(t, r.log, "fourth poll", map[string]int{"done.json": 1, "busy.json": 1})
	if got := r.log.payloads("busy.json"); got[0] != `{"b":1}` {
		t.Errorf("busy.json published as %q, want the finished file", got[0])
	}
}

// AC 4: the two looks must be at least one poll interval apart on the
// consumer's clock. A look that comes sooner neither publishes nor moves the
// first sighting; a change seen at such a look restarts the wait from there.
func TestStable_LooksTooCloseTogetherDoNotCount(t *testing.T) {
	const interval = 10 * time.Second
	type look struct {
		at   time.Duration // since the first sighting
		body *string       // a new version (mtime +1 s) before this look
		want int
	}
	cases := []struct {
		name  string
		looks []look
	}{
		{"a look right behind the first", []look{
			{at: 0, want: 0},
			{at: 5 * time.Millisecond, want: 0},
			{at: interval - time.Millisecond, want: 0},
			{at: interval, want: 1},
		}},
		{"the first sighting survives a look in between", []look{
			{at: 0, want: 0},
			{at: interval / 2, want: 0},
			{at: interval, want: 1},
		}},
		{"a change at a look in between restarts the wait", []look{
			{at: 0, want: 0},
			{at: interval / 2, body: stBody(`{"a":12}`), want: 0},
			{at: interval, want: 0},
			{at: interval/2 + interval - time.Millisecond, want: 0},
			{at: interval/2 + interval, want: 1},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPDRig(t, newPDStore(), &fakeSFTP{})
			r.cfg.PollIntervalSeconds = int(interval / time.Second)
			r.boot()
			pdPut(r.fake, "a.json", `{"a":1}`, 0)
			start := pdT0.Add(time.Hour)
			for i, l := range tc.looks {
				if l.body != nil {
					pdPut(r.fake, "a.json", *l.body, i)
				}
				r.clock = start.Add(l.at)
				r.c.pollOnce(context.Background(), r.conn, r.tenant, &r.cfg, r.done, r.pending, r.c.logger)
				if got := r.log.counts()["a.json"]; got != l.want {
					t.Fatalf("look %d at +%v: published %d times, want %d", i+1, l.at, got, l.want)
				}
			}
		})
	}
}

// AC 5: every after_action runs once, after the one publish of the finished
// file, and never on a file still changing.
func TestStable_EveryAfterAction(t *testing.T) {
	for _, action := range []string{"none", "delete", "move"} {
		t.Run(action, func(t *testing.T) {
			r := newPDRig(t, newPDStore(), &fakeSFTP{})
			r.cfg.AfterAction = action
			r.cfg.MoveDir = "/in/processed"
			r.boot()
			acted := func() int {
				r.fake.mu.Lock()
				defer r.fake.mu.Unlock()
				return len(r.fake.removed) + len(r.fake.moved)
			}
			steps := []stStep{
				{body: stBody(`a,b`), sec: 0, want: 0},
				{body: stBody(`a,b\n1,2`), sec: 1, want: 0},
				{want: 1},
				{want: 1},
				{want: 1},
			}
			for i, st := range steps {
				if st.body != nil {
					pdPut(r.fake, "data.csv", *st.body, st.sec)
				}
				r.poll()
				got := r.log.counts()["data.csv"]
				if got != st.want {
					t.Fatalf("after poll %d: published %d times, want %d", i+1, got, st.want)
				}
				if got == 0 && acted() != 0 {
					t.Fatalf("after poll %d: the after-action ran on a file not yet published", i+1)
				}
			}
			if got := r.log.payloads("data.csv"); got[0] != `a,b\n1,2` {
				t.Errorf("published %q, want the finished file", got[0])
			}
			r.fake.mu.Lock()
			defer r.fake.mu.Unlock()
			switch action {
			case "none":
				if len(r.fake.removed)+len(r.fake.moved) != 0 || r.fake.files["data.csv"] == nil {
					t.Errorf("after_action none touched the file: removed %v, moved %v", r.fake.removed, r.fake.moved)
				}
			case "delete":
				if len(r.fake.removed) != 1 || r.fake.removed[0] != "data.csv" || len(r.fake.moved) != 0 {
					t.Errorf("removed %v, moved %v; want data.csv removed once", r.fake.removed, r.fake.moved)
				}
			case "move":
				if len(r.fake.moved) != 1 || r.fake.moved["/in/data.csv"] != "/in/processed/data.csv" || len(r.fake.removed) != 0 {
					t.Errorf("moved %v, removed %v; want data.csv moved once", r.fake.moved, r.fake.removed)
				}
			}
		})
	}
}

// AC 6: the done set from #538 still decides what is done. A settled file
// that is done is not published again, also after a restart; a done file
// replaced by a new upload waits like a new one; a restart forgets what was
// pending, so the wait starts again rather than publishing on one look.
func TestStable_WithTheDoneSet(t *testing.T) {
	t.Run("done and settled, across restarts", func(t *testing.T) {
		r := newPDRig(t, newPDStore(), &fakeSFTP{})
		r.boot()
		stRun(t, r, "a.json", []stStep{{body: stBody(`{"a":1}`), want: 0}, {want: 1}})
		r.boot()
		stRun(t, r, "a.json", []stStep{{want: 1}, {want: 1}, {want: 1}})
		r.boot()
		r.poll()
		requireCounts(t, r.log, "second restart", map[string]int{"a.json": 1})
	})

	t.Run("a done file replaced by an upload in progress", func(t *testing.T) {
		r := newPDRig(t, newPDStore(), &fakeSFTP{})
		r.boot()
		stRun(t, r, "a.json", []stStep{
			{body: stBody(`{"a":1}`), sec: 0, want: 0},
			{want: 1},
			{body: stBody(`{"a"`), sec: 10, want: 1},
			{body: stBody(`{"a":22}`), sec: 11, want: 1},
			{want: 2},
			{want: 2},
		})
		if got := r.log.payloads("a.json"); got[1] != `{"a":22}` {
			t.Errorf("second publish %q, want the finished replacement", got[1])
		}
	})

	t.Run("a done file replaced while stopped", func(t *testing.T) {
		r := newPDRig(t, newPDStore(), &fakeSFTP{})
		r.boot()
		stRun(t, r, "a.json", []stStep{{body: stBody(`{"a":1}`), want: 0}, {want: 1}})
		pdPut(r.fake, "a.json", `{"a":22}`, 30)
		r.boot()
		stRun(t, r, "a.json", []stStep{{want: 1}, {want: 2}, {want: 2}})
	})

	t.Run("a restart while a file is pending", func(t *testing.T) {
		r := newPDRig(t, newPDStore(), &fakeSFTP{})
		r.boot()
		stRun(t, r, "a.json", []stStep{{body: stBody(`{"a"`), want: 0}})
		pdPut(r.fake, "a.json", `{"a":1}`, 1)
		r.boot()
		stRun(t, r, "a.json", []stStep{{want: 0}, {want: 1}, {want: 1}})
		if got := r.log.payloads("a.json"); got[0] != `{"a":1}` {
			t.Errorf("published %q, want the finished file", got[0])
		}
	})
}

// --- one-shot (AC 7) ----------------------------------------------------------

// stHookSFTP calls afterList with the number of each List call, after the
// listing was taken: an upload that moves on between two looks.
type stHookSFTP struct {
	*fakeSFTP
	lists     atomic.Int32
	afterList func(n int32)
}

func (f *stHookSFTP) List(dir string) ([]remoteFile, error) {
	out, err := f.fakeSFTP.List(dir)
	n := f.lists.Add(1)
	if f.afterList != nil {
		f.afterList(n)
	}
	return out, err
}

// stOneShot runs one one-shot start on a new consumer over store and waits for
// it to end with status "stopped".
func stOneShot(t *testing.T, conn sftpConn, store *pdStore, settle time.Duration, log *pdLog) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"sftp","sftp":{"host":"sftp","username":"u","password":"p","remote_dir":"/in","after_action":"none","poll_interval_seconds":0}}}]`
	mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(pdConn, pdTenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	mock.ExpectExec("UPDATE connections SET status").WithArgs("running", pdConn, pdTenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE connections SET status").WithArgs("stopped", pdConn, pdTenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
	c := &sftpConsumer{
		db:          db,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		active:      make(map[string]*activeConn),
		publish:     log.publish,
		dial:        func(*SFTPConfig) (sftpConn, error) { return conn, nil },
		checkpoints: store,
		settleMin:   settle,
		settleMax:   settle,
	}
	c.startConnection(context.Background(), pdConn, pdTenant)
	harness.Eventually(t, pdWait, "the one-shot to end stopped", func() bool {
		return mock.ExpectationsWereMet() == nil
	})
}

func TestStable_OneShot(t *testing.T) {
	const settle = 30 * time.Millisecond
	cases := []struct {
		name      string
		afterList func(f *fakeSFTP) func(n int32)
		lists     int32
		want      map[string]int
	}{
		{
			name:  "settled files are published on the second look",
			lists: 2,
			want:  map[string]int{"a.json": 1, "b.json": 1},
		},
		{
			name: "a file still changing at the second look is left",
			afterList: func(f *fakeSFTP) func(int32) {
				return func(n int32) {
					if n == 1 {
						pdPut(f, "b.json", `{"b":12}`, 1)
					}
				}
			},
			lists: 2,
			want:  map[string]int{"a.json": 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeSFTP{}
			pdPut(fake, "a.json", `{"a":1}`, 0)
			pdPut(fake, "b.json", `{"b":1}`, 0)
			conn := &stHookSFTP{fakeSFTP: fake}
			if tc.afterList != nil {
				conn.afterList = tc.afterList(fake)
			}
			log := &pdLog{}
			stOneShot(t, conn, newPDStore(), settle, log)
			requireCounts(t, log, "one-shot", tc.want)
			if got := conn.lists.Load(); got != tc.lists {
				t.Errorf("listed the folder %d times, want %d", got, tc.lists)
			}
		})
	}
}

// A one-shot that finds nothing new looks once and ends without waiting.
func TestStable_OneShotWithNothingNewDoesNotWait(t *testing.T) {
	store := newPDStore()
	fake := &fakeSFTP{}
	pdPut(fake, "a.json", `{"a":1}`, 0)
	log := &pdLog{}
	stOneShot(t, fake, store, 30*time.Millisecond, log)
	requireCounts(t, log, "first run", map[string]int{"a.json": 1})

	// Everything is done: with an hour's settle wait the run must still end
	// within stOneShot's few seconds.
	conn := &stHookSFTP{fakeSFTP: fake}
	stOneShot(t, conn, store, time.Hour, log)
	requireCounts(t, log, "second run", map[string]int{"a.json": 1})
	if got := conn.lists.Load(); got != 1 {
		t.Errorf("listed the folder %d times, want 1", got)
	}
}

// --- end to end through NATS (AC 8) --------------------------------------------

// An upload that grows over the first polls of a running connection arrives on
// the data stream once, finished, not once per size it had.
func TestStable_EndToEnd_UploadInProgress(t *testing.T) {
	fake := &fakeSFTP{}
	versions := []string{`{"order":`, `{"order":{"id":`, `{"order":{"id":7}}`}
	pdPut(fake, "order.json", versions[0], 0)
	var mu sync.Mutex
	conn := &stHookSFTP{fakeSFTP: fake, afterList: func(n int32) {
		mu.Lock()
		defer mu.Unlock()
		if int(n) < len(versions) {
			pdPut(fake, "order.json", versions[n], int(n))
		}
	}}
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"sftp","sftp":{"host":"sftp","username":"u","password":"p","remote_dir":"/in","after_action":"none","poll_interval_seconds":1}}}]`
	db := pdMockDB(t, pdConn, pdTenant, nodes, 1)
	// settleMin below the 1 s interval: the settle gap is the interval.
	c := &sftpConsumer{dial: func(*SFTPConfig) (sftpConn, error) { return conn, nil }, checkpoints: newPDStore(), settleMin: 100 * time.Millisecond}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "sftp-consumer", DB: db})
	data := pdSubscribe(t, h.NATS())
	time.Sleep(200 * time.Millisecond) // the consumer subscribes to commands in Run

	pdCommand(t, h.NATS(), "start", pdConn, pdTenant)

	// Polls at about 0, 1, 2 and 3 s: the last version is listed at the third
	// and published at the fourth. Wait for it, then for one more poll.
	got := h.ExpectEnvelope(t, harness.MatchTenant(pdTenant), 8*time.Second)
	if string(got.Payload) != versions[len(versions)-1] {
		t.Fatalf("first publish = %q, want the finished upload %q", got.Payload, versions[len(versions)-1])
	}
	listed := conn.lists.Load()
	harness.Eventually(t, 5*time.Second, "one more poll", func() bool { return conn.lists.Load() > listed })
	data.require(t, "after the upload settled", map[string]int{"order.json": 1})
}

// --- review round 1: the settle gap is not the poll interval -----------------

// Review of #568: a poller that lives shorter than its poll interval (the
// cluster is parked 16:00-07:00, so a pod lives at most ~9 h; a deploy that
// touches the SDK restarts it sooner) never saw a file twice, so a file that
// was finished long ago was never published. Before #562 it went out on each
// start's first poll. Now the look that publishes comes a settle gap (at most
// 30 s) after the first, so each day polls until the park, and the file goes
// out once on the first day.
func TestReview_RestartsShorterThanTheIntervalNeverPublish(t *testing.T) {
	r := newPDRig(t, newPDStore(), &fakeSFTP{})
	r.cfg.PollIntervalSeconds = 24 * 3600       // a daily pickup
	pdPut(r.fake, "daily.csv", "a,b\n1,2\n", 0) // uploaded and finished long ago
	start := pdT0.Add(24 * time.Hour)
	for day := 0; day < 5; day++ {
		r.clock = start.Add(time.Duration(day) * 24 * time.Hour) // 07:00, the cluster starts
		r.boot()
		park := r.clock.Add(9 * time.Hour) // 16:00
		for polls := 0; r.clock.Add(r.nextStep()).Before(park); polls++ {
			if polls > 100 {
				t.Fatal("more than 100 polls in a day with nothing to do")
			}
			r.poll()
		}
	}
	if got := r.log.counts()["daily.csv"]; got != 1 {
		t.Fatalf("after 5 daily starts daily.csv was published %d times, want 1", got)
	}
}

// The settle gap is the poll interval, at least 5 s and at most 30 s; a
// one-shot run gets 5 s.
func TestStable_SettleGap(t *testing.T) {
	cases := []struct {
		interval int
		want     time.Duration
	}{
		{0, 5 * time.Second},
		{1, 5 * time.Second},
		{5, 5 * time.Second},
		{10, 10 * time.Second},
		{30, 30 * time.Second},
		{60, 30 * time.Second},
		{24 * 3600, 30 * time.Second},
	}
	c := &sftpConsumer{}
	for _, tc := range cases {
		if got := c.settleGap(&SFTPConfig{PollIntervalSeconds: tc.interval}); got != tc.want {
			t.Errorf("poll_interval_seconds %d: settle gap %v, want %v", tc.interval, got, tc.want)
		}
	}
}

// After a poll that left files waiting to settle the next poll comes a settle
// gap later, if that is sooner than the interval; otherwise one interval later.
func TestStable_NextPoll(t *testing.T) {
	cases := []struct {
		interval int
		waiting  int
		want     time.Duration
	}{
		{60, 0, 60 * time.Second},
		{60, 1, 30 * time.Second},
		{24 * 3600, 3, 30 * time.Second},
		{24 * 3600, 0, 24 * time.Hour},
		{10, 1, 10 * time.Second},
		{2, 1, 2 * time.Second}, // the 5 s floor does not slow a short interval
	}
	c := &sftpConsumer{}
	for _, tc := range cases {
		if got := c.nextPoll(&SFTPConfig{PollIntervalSeconds: tc.interval}, tc.waiting); got != tc.want {
			t.Errorf("interval %d s, %d waiting: next poll in %v, want %v", tc.interval, tc.waiting, got, tc.want)
		}
	}
}

// Through runPoller and real timers: with an hour's poll interval a file is
// published a settle gap after the first poll, and once nothing is waiting
// the poller goes back to its interval.
func TestStable_RunPollerLooksAgainAfterTheSettleGap(t *testing.T) {
	fake := &fakeSFTP{}
	pdPut(fake, "a.json", `{"a":1}`, 0)
	conn := &stHookSFTP{fakeSFTP: fake}
	log := &pdLog{}
	c := &sftpConsumer{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		publish:     log.publish,
		dial:        func(*SFTPConfig) (sftpConn, error) { return conn, nil },
		checkpoints: newPDStore(),
		settleMin:   20 * time.Millisecond,
		settleMax:   50 * time.Millisecond,
	}
	cfg := &SFTPConfig{Host: "sftp", Username: "u", Password: "p", RemoteDir: "/in", AfterAction: "none", PollIntervalSeconds: 3600, nodeID: "c1"}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		c.runPoller(ctx, &activeConn{cancel: cancel}, pdConn, pdTenant, cfg)
	}()
	defer func() {
		cancel()
		select {
		case <-ended:
		case <-time.After(2 * time.Second):
			t.Error("the poller did not end within 2 s of the stop")
		}
	}()

	harness.Eventually(t, 3*time.Second, "a.json published a settle gap after the first poll", func() bool {
		return log.counts()["a.json"] == 1
	})
	time.Sleep(300 * time.Millisecond)
	if got := conn.lists.Load(); got != 2 {
		t.Errorf("listed the folder %d times, want 2: the first poll and the one a settle gap later", got)
	}
	requireCounts(t, log, "after the settle gap", map[string]int{"a.json": 1})
}

// A stop during the wait for the second look ends the poller at once, without
// that look: in a one-shot run and in poll mode.
func TestStable_StopDuringTheSettleWait(t *testing.T) {
	for _, interval := range []int{0, 3600} {
		t.Run("poll_interval_seconds "+strconv.Itoa(interval), func(t *testing.T) {
			fake := &fakeSFTP{}
			pdPut(fake, "a.json", `{"a":1}`, 0)
			conn := &stHookSFTP{fakeSFTP: fake}
			log := &pdLog{}
			c := &sftpConsumer{
				logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
				publish:     log.publish,
				dial:        func(*SFTPConfig) (sftpConn, error) { return conn, nil },
				checkpoints: newPDStore(),
				settleMin:   time.Hour,
				settleMax:   time.Hour,
			}
			cfg := &SFTPConfig{Host: "sftp", Username: "u", Password: "p", RemoteDir: "/in", AfterAction: "none", PollIntervalSeconds: interval, nodeID: "c1"}
			ctx, cancel := context.WithCancel(context.Background())
			ended := make(chan struct{})
			go func() {
				defer close(ended)
				c.runPoller(ctx, &activeConn{cancel: cancel}, pdConn, pdTenant, cfg)
			}()
			harness.Eventually(t, 3*time.Second, "the first poll", func() bool { return conn.lists.Load() == 1 })
			cancel()
			select {
			case <-ended:
			case <-time.After(2 * time.Second):
				t.Fatal("the poller did not end within 2 s of the stop")
			}
			if got := conn.lists.Load(); got != 1 {
				t.Errorf("listed the folder %d times, want 1", got)
			}
			requireCounts(t, log, "after the stop", map[string]int{})
		})
	}
}

// A name gone from the folder is no longer pending; a failed publish keeps its
// sighting, is not counted as waiting, and is retried at the next poll.
func TestStable_PendingBookkeeping(t *testing.T) {
	t.Run("gone names are pruned", func(t *testing.T) {
		r := newPDRig(t, newPDStore(), &fakeSFTP{})
		r.boot()
		pdPut(r.fake, "a.json", `{"a"`, 0)
		pdPut(r.fake, "b.json", `{"b"`, 0)
		r.poll()
		if len(r.pending) != 2 || r.waiting != 2 {
			t.Fatalf("pending %d, waiting %d after the first poll; want 2 and 2", len(r.pending), r.waiting)
		}
		pdDel(r.fake, "a.json")
		r.poll()
		if _, ok := r.pending["a.json"]; ok || len(r.pending) != 0 {
			t.Errorf("pending after a.json went = %v, want empty (b.json published)", r.pending)
		}
		requireCounts(t, r.log, "after the delete", map[string]int{"b.json": 1})
	})

	t.Run("a failed publish is retried", func(t *testing.T) {
		r := newPDRig(t, newPDStore(), &fakeSFTP{})
		r.cfg.PollIntervalSeconds = 60
		r.boot()
		fail := true
		r.c.publish = func(ctx context.Context, env *envelope.Envelope) error {
			if fail {
				fail = false
				return errors.New("bus down")
			}
			return r.log.publish(ctx, env)
		}
		pdPut(r.fake, "a.json", `{"a":1}`, 0)
		r.poll()
		r.poll()
		requireCounts(t, r.log, "the failed publish", map[string]int{})
		if _, ok := r.pending["a.json"]; !ok || r.waiting != 0 {
			t.Fatalf("after the failed publish: pending %v, waiting %d; want a.json kept and 0 waiting", r.pending, r.waiting)
		}
		r.poll()
		requireCounts(t, r.log, "the retry", map[string]int{"a.json": 1})
	})

	t.Run("a failed connect keeps the files waiting", func(t *testing.T) {
		r := newPDRig(t, newPDStore(), &fakeSFTP{})
		r.cfg.PollIntervalSeconds = 3600
		r.boot()
		pdPut(r.fake, "a.json", `{"a":1}`, 0)
		r.poll()
		r.c.dial = func(*SFTPConfig) (sftpConn, error) { return nil, errors.New("down") }
		r.poll()
		if r.waiting != 1 {
			t.Fatalf("waiting after a failed connect = %d, want 1: the next poll comes a settle gap later", r.waiting)
		}
	})
}
