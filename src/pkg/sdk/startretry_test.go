package sdk_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for retrying a failed start (#725). rtOwner is a minimal
// command-driven consumer built the way the real ones are: start and stop
// commands arrive over NATS (the embedded server), every start goes through
// StartRetrying, and the attempt reserves with ReserveOrRetryContext. What
// an attempt does once it holds the claim is scripted per attempt.

const (
	rtConn    = "conn-725"
	rtTenantA = "tenant-a"
	rtTenantB = "tenant-b"
)

var (
	errDBDown = dbDownError()
	errBadCfg = errors.New("config incomplete")
)

// dbDownError is a database-down error as lib/pq returns it.
func dbDownError() error {
	return fmt.Errorf("read connection: %w", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})
}

type rtOwner struct {
	t      *testing.T
	nc     *nats.Conn
	mu     sync.Mutex
	g      sdk.StartGuard
	active map[string]string // connection -> tenant
	policy sdk.RetryPolicy
	db     *sql.DB // read before each retry when set

	// script returns attempt n's failure (from 1), nil to activate.
	script func(n int) error

	attempts          atomic.Int32 // attempts that held the claim
	activations       atomic.Int32
	ctxDoneAtActivate atomic.Bool // the attempt's ctx had ended when it activated
	stops             chan string // tenant of each stop handled
}

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func newRTOwner(t *testing.T, script func(n int) error) *rtOwner {
	t.Helper()
	nc, _, cleanup := harness.StartEmbeddedJetStream(t)
	o := &rtOwner{
		t: t, nc: nc, active: map[string]string{}, script: script,
		policy: sdk.RetryPolicy{Base: 20 * time.Millisecond, Max: 40 * time.Millisecond},
		stops:  make(chan string, 16),
	}
	start, err := nc.Subscribe("vrsky.commands.*.connection.start", o.handleStart)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := nc.Subscribe("vrsky.commands.*.connection.stop", o.handleStop)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = start.Unsubscribe()
		_ = stop.Unsubscribe()
		o.shutdown()
		cleanup()
	})
	return o
}

type rtCommand struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

func (o *rtOwner) publish(verb, tenant string) {
	o.t.Helper()
	b, _ := json.Marshal(rtCommand{ConnectionID: rtConn, TenantID: tenant})
	if err := o.nc.Publish("vrsky.commands."+tenant+".connection."+verb, b); err != nil {
		o.t.Fatal(err)
	}
	if err := o.nc.Flush(); err != nil {
		o.t.Fatal(err)
	}
}

func (o *rtOwner) handleStart(msg *nats.Msg) {
	var cmd rtCommand
	if json.Unmarshal(msg.Data, &cmd) != nil {
		return
	}
	o.startConnection(context.Background(), cmd.ConnectionID, cmd.TenantID)
}

func (o *rtOwner) startConnection(ctx context.Context, id, tenant string) {
	o.g.StartRetrying(ctx, sdk.RetryOptions{Mu: &o.mu, DB: o.db, Logger: quietLogger, Policy: o.policy}, tenant, id,
		func(ctx context.Context) error { return o.tryStart(ctx, id, tenant) })
}

func (o *rtOwner) tryStart(ctx context.Context, id, tenant string) error {
	claim, ok := o.g.ReserveOrRetryContext(ctx, &o.mu, tenant, id,
		func() bool { _, ok := o.active[id]; return ok },
		func() { o.startConnection(ctx, id, tenant) })
	if !ok {
		return nil
	}
	defer claim.Release()
	n := int(o.attempts.Add(1))
	if err := o.script(n); err != nil {
		return err
	}
	if !claim.Activate(func() { o.active[id] = tenant }) {
		return nil
	}
	if ctx.Err() != nil {
		o.ctxDoneAtActivate.Store(true)
	}
	o.activations.Add(1)
	return nil
}

func (o *rtOwner) handleStop(msg *nats.Msg) {
	var cmd rtCommand
	if json.Unmarshal(msg.Data, &cmd) != nil {
		return
	}
	o.mu.Lock()
	if _, err := o.g.StopForTenantLocked(msg.Subject, cmd.TenantID, cmd.ConnectionID); err == nil {
		delete(o.active, cmd.ConnectionID)
	}
	o.mu.Unlock()
	o.stops <- cmd.TenantID
}

func (o *rtOwner) shutdown() {
	o.mu.Lock()
	o.active = map[string]string{}
	o.g.StopAllLocked()
	o.mu.Unlock()
}

// waitStop waits until the stop handler has handled a stop.
func (o *rtOwner) waitStop() {
	o.t.Helper()
	select {
	case <-o.stops:
	case <-time.After(5 * time.Second):
		o.t.Fatal("stop command not handled")
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settle waits long enough for several more retries to have run, had any
// still been pending.
func (o *rtOwner) settle() { time.Sleep(10 * o.policy.Max) }

func (o *rtOwner) noLoopsLeft() {
	o.t.Helper()
	if !sdk.WaitRetriesForTest(&o.g, 5*time.Second) {
		o.t.Fatal("a retry loop is still running after its retry ended (goroutine leak)")
	}
	if n := sdk.PendingRetriesForTest(&o.g, &o.mu); n != 0 {
		o.t.Fatalf("%d retries still pending", n)
	}
}

// failFirst fails the first n attempts with err and lets the rest succeed.
func failFirst(n int, err error) func(int) error {
	return func(a int) error {
		if a <= n {
			return err
		}
		return nil
	}
}

func TestStartRetrying_Outcomes(t *testing.T) {
	cases := []struct {
		name           string
		script         func(int) error
		giveUp         time.Duration
		wantAttempts   int32
		wantActivation int32
	}{
		{"succeeds at once", failFirst(0, nil), 0, 1, 1},
		{"transient then success", failFirst(3, sdk.TransientStart(errDBDown)), 0, 4, 1},
		{"database down through TransientIfDBDown", failFirst(2, sdk.TransientIfDBDown(errDBDown)), 0, 3, 1},
		{"permanent is not retried", failFirst(100, errBadCfg), 0, 1, 0},
		{"refused secret is permanent", failFirst(100, sdk.TransientIfDBDown(errors.New("stored secret not found"))), 0, 1, 0},
		{"transient then permanent ends the retries", func(n int) error {
			if n == 1 {
				return sdk.TransientStart(errDBDown)
			}
			return errBadCfg
		}, 0, 2, 0},
		{"gives up", failFirst(100, sdk.TransientStart(errDBDown)), time.Nanosecond, 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newRTOwner(t, tc.script)
			o.policy.GiveUp = tc.giveUp
			o.publish("start", rtTenantA)
			eventually(t, "the attempts", func() bool { return o.attempts.Load() >= tc.wantAttempts })
			o.settle()
			o.noLoopsLeft()
			if got := o.attempts.Load(); got != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", got, tc.wantAttempts)
			}
			if got := o.activations.Load(); got != tc.wantActivation {
				t.Errorf("activations = %d, want %d", got, tc.wantActivation)
			}
			if o.ctxDoneAtActivate.Load() {
				t.Error("an attempt's context had ended when it activated")
			}
		})
	}
}

// A stop for the connection's tenant ends a pending retry, wherever it
// lands: while the retry waits, or after the wait and before its reserve.
// Nothing starts after it. A stop naming another tenant changes nothing.
func TestStartRetrying_Stop(t *testing.T) {
	cases := []struct {
		name string
		// stopTenant names the stop's tenant; beforeReserve lands it between
		// the retry's wait and its reserve rather than during the wait.
		stopTenant    string
		beforeReserve bool
		wantActive    bool
	}{
		{"stop while waiting", rtTenantA, false, false},
		{"stop between wait and reserve", rtTenantA, true, false},
		{"stop for another tenant", rtTenantB, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Fails once, then would succeed: only the stop keeps it down.
			o := newRTOwner(t, failFirst(1, sdk.TransientStart(errDBDown)))
			if tc.beforeReserve {
				var once sync.Once
				o.g.BeforeRetryAttempt = func(string) {
					once.Do(func() {
						o.publish("stop", tc.stopTenant)
						o.waitStop()
					})
				}
			} else {
				// Long enough that the stop lands while the retry waits, even
				// on a slow -race run.
				o.policy = sdk.RetryPolicy{Base: 3 * time.Second, Max: 3 * time.Second}
			}
			o.publish("start", rtTenantA)
			eventually(t, "the first attempt", func() bool { return o.attempts.Load() == 1 })
			eventually(t, "the retry to be pending", func() bool { return sdk.PendingRetriesForTest(&o.g, &o.mu) == 1 })
			if !tc.beforeReserve {
				o.publish("stop", tc.stopTenant)
				o.waitStop()
			}
			switch {
			case tc.wantActive:
				eventually(t, "the retry to activate", func() bool { return o.activations.Load() == 1 })
			case tc.beforeReserve:
				o.settle()
			default:
				// Ended by the stop, well before its wait is over.
				if !sdk.WaitRetriesForTest(&o.g, time.Second) {
					t.Fatal("the stop did not end the waiting retry")
				}
			}
			o.noLoopsLeft()
			if got := o.activations.Load() == 1; got != tc.wantActive {
				t.Fatalf("activated = %v, want %v (attempts %d)", got, tc.wantActive, o.attempts.Load())
			}
			if !tc.wantActive && o.attempts.Load() != 1 {
				t.Fatalf("attempts = %d after the stop, want 1", o.attempts.Load())
			}
		})
	}
}

// The retry ends when the context of the start it came from ends (boot
// restore runs under Run's context), and on StopAllLocked; after
// StopAllLocked nothing is retried at all.
func TestStartRetrying_ContextAndShutdown(t *testing.T) {
	t.Run("context cancelled", func(t *testing.T) {
		o := newRTOwner(t, failFirst(100, sdk.TransientStart(errDBDown)))
		o.policy = sdk.RetryPolicy{Base: 3 * time.Second, Max: 3 * time.Second}
		ctx, cancel := context.WithCancel(context.Background())
		o.startConnection(ctx, rtConn, rtTenantA)
		if sdk.PendingRetriesForTest(&o.g, &o.mu) != 1 {
			t.Fatal("no retry pending after a transient failure")
		}
		cancel()
		o.noLoopsLeft()
		if got := o.attempts.Load(); got != 1 {
			t.Fatalf("attempts = %d after the context ended, want 1", got)
		}
	})
	t.Run("StopAllLocked", func(t *testing.T) {
		o := newRTOwner(t, failFirst(100, sdk.TransientStart(errDBDown)))
		o.policy = sdk.RetryPolicy{Base: 3 * time.Second, Max: 3 * time.Second}
		o.publish("start", rtTenantA)
		eventually(t, "the retry to be pending", func() bool { return sdk.PendingRetriesForTest(&o.g, &o.mu) == 1 })
		o.shutdown()
		o.noLoopsLeft()
		// A start after shutdown that fails is not retried.
		o.startConnection(context.Background(), rtConn, rtTenantA)
		if n := sdk.PendingRetriesForTest(&o.g, &o.mu); n != 0 {
			t.Fatalf("%d retries pending after StopAllLocked", n)
		}
		if got := o.attempts.Load(); got != 2 {
			t.Fatalf("attempts = %d, want 2 (one before, one after shutdown)", got)
		}
	})
}

// A start command that arrives while a retry waits runs at once; it
// activates the connection once, and the pending retry ends with nothing
// left to start.
func TestStartRetrying_StartCommandWhileRetryPending(t *testing.T) {
	o := newRTOwner(t, failFirst(1, sdk.TransientStart(errDBDown)))
	o.policy = sdk.RetryPolicy{Base: 4 * time.Second, Max: 4 * time.Second}
	o.publish("start", rtTenantA)
	eventually(t, "the retry to be pending", func() bool { return sdk.PendingRetriesForTest(&o.g, &o.mu) == 1 })
	o.publish("start", rtTenantA)
	eventually(t, "the start command to activate", func() bool { return o.activations.Load() == 1 })
	// Ended by the activation, well before its wait is over.
	if !sdk.WaitRetriesForTest(&o.g, time.Second) {
		t.Fatal("the pending retry did not end when the start command activated the connection")
	}
	if a, n := o.activations.Load(), o.attempts.Load(); a != 1 || n != 2 {
		t.Fatalf("activations = %d, attempts = %d; want 1 and 2", a, n)
	}
}

// Two starts failing together leave one retry loop, and it activates the
// connection once.
func TestStartRetrying_OneLoopPerConnection(t *testing.T) {
	var mu sync.Mutex
	fails := 0
	o := newRTOwner(t, nil)
	o.script = func(int) error {
		mu.Lock()
		defer mu.Unlock()
		if fails < 2 {
			fails++
			return sdk.TransientStart(errDBDown)
		}
		return nil
	}
	o.publish("start", rtTenantA)
	o.publish("start", rtTenantA)
	eventually(t, "the activation", func() bool { return o.activations.Load() == 1 })
	o.settle()
	o.noLoopsLeft()
	if a := o.activations.Load(); a != 1 {
		t.Fatalf("activations = %d, want 1", a)
	}
}

// Marking: only TransientStart is retried, TransientIfDBDown marks only a
// database that does not answer, and the marks keep the cause.
func TestTransientStart(t *testing.T) {
	if sdk.TransientStart(nil) != nil || sdk.TransientIfDBDown(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"marked", sdk.TransientStart(errBadCfg), true},
		{"marked and wrapped", fmt.Errorf("start: %w", sdk.TransientStart(errBadCfg)), true},
		{"unmarked", errBadCfg, false},
		{"database down", sdk.TransientIfDBDown(errDBDown), true},
		{"no rows", sdk.TransientIfDBDown(fmt.Errorf("q: %w", sql.ErrNoRows)), false},
		{"bad config", sdk.TransientIfDBDown(errBadCfg), false},
		{"retriable delivery error is not a start mark", sdk.Retriable(errBadCfg), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sdk.IsTransientStart(tc.err); got != tc.want {
				t.Errorf("IsTransientStart(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
	if !errors.Is(sdk.TransientStart(errBadCfg), errBadCfg) {
		t.Error("TransientStart loses the cause")
	}
}

func TestIsTransientDBError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"no rows", sql.ErrNoRows, false},
		{"secret not found", errors.New("stored secret not found"), false},
		{"connection reset", fmt.Errorf("resolve: %w", syscall.ECONNRESET), true},
		{"connection refused", syscall.ECONNREFUSED, true},
		{"net error", &net.OpError{Op: "read", Err: errors.New("x")}, true},
		{"bad conn", driver.ErrBadConn, true},
		{"conn done", sql.ErrConnDone, true},
		{"timeout", context.DeadlineExceeded, true},
		{"EOF", io.EOF, true},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"pq admin shutdown", &pq.Error{Code: "57P01"}, true},
		{"pq cannot connect now", &pq.Error{Code: "57P03"}, true},
		{"pq connection failure", &pq.Error{Code: "08006"}, true},
		{"pq too many connections", &pq.Error{Code: "53300"}, true},
		{"pq serialization failure", &pq.Error{Code: "40001"}, true},
		{"pq password refused", &pq.Error{Code: "28P01"}, false},
		{"pq undefined table", &pq.Error{Code: "42P01"}, false},
		{"pq bad uuid", &pq.Error{Code: "22P02"}, false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sdk.IsTransientDBError(tc.err); got != tc.want {
				t.Errorf("IsTransientDBError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRetryPolicy(t *testing.T) {
	p := sdk.RetryPolicy{Base: 5 * time.Second, Max: 5 * time.Minute}
	for _, tc := range []struct {
		n    int
		full time.Duration // before jitter
	}{
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{6, 160 * time.Second},
		{7, 5 * time.Minute},
		{50, 5 * time.Minute},
	} {
		t.Run(fmt.Sprint(tc.n), func(t *testing.T) {
			lo, hi := time.Duration(1<<62), time.Duration(0)
			for i := 0; i < 200; i++ {
				d := p.Delay(tc.n)
				lo, hi = min(lo, d), max(hi, d)
			}
			if lo < tc.full/2 || hi > tc.full {
				t.Errorf("Delay(%d) in [%v, %v], want within [%v, %v]", tc.n, lo, hi, tc.full/2, tc.full)
			}
			if hi == lo {
				t.Errorf("Delay(%d) = %v every time; want jitter", tc.n, lo)
			}
		})
	}
	if d := (sdk.RetryPolicy{}).Delay(1); d < sdk.DefaultStartRetryBase/2 || d > sdk.DefaultStartRetryBase {
		t.Errorf("default first delay %v, want within [%v, %v]", d, sdk.DefaultStartRetryBase/2, sdk.DefaultStartRetryBase)
	}
	if d := (sdk.RetryPolicy{}).Delay(100); d > sdk.DefaultStartRetryMax {
		t.Errorf("default delay %v is past the cap %v", d, sdk.DefaultStartRetryMax)
	}
	for _, tc := range []struct {
		giveUp time.Duration
		want   time.Duration
		ok     bool
	}{
		{0, sdk.DefaultStartRetryGiveUp, true},
		{time.Minute, time.Minute, true},
		{-1, 0, false},
	} {
		if d, ok := (sdk.RetryPolicy{GiveUp: tc.giveUp}).GiveUpAfter(); d != tc.want || ok != tc.ok {
			t.Errorf("GiveUpAfter(%v) = (%v, %v), want (%v, %v)", tc.giveUp, d, ok, tc.want, tc.ok)
		}
	}
}
