package main

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"
)

// Tests for #498: a stop command acts only when it names the tenant the
// connection was started for, in its payload and on its subject. A stop that
// names another tenant leaves the connection running, writes no status and
// logs one warning; it does not abandon a start in progress either. Built on
// the sqlmock fixture and hold helpers in start_race_test.go and the poller
// count in start_guard_fix_test.go; no sleeps, every wait bounded by srWait.

const (
	stOther  = "tenant-other"
	stMarker = "st-payload-marker" // an extra payload field that must not be logged
)

// stSubject is the stop subject for tenant.
func stSubject(tenant string) string { return "vrsky.commands." + tenant + ".connection.stop" }

// stStop is a stop for srConn on subject whose payload names tenant.
func stStop(subject, tenant string) *nats.Msg {
	data, _ := json.Marshal(map[string]string{"connection_id": srConn, "tenant_id": tenant, "note": stMarker})
	return &nats.Msg{Subject: subject, Data: data}
}

// stLogBuf collects the consumer's log output; pollers log from their own
// goroutines.
type stLogBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *stLogBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *stLogBuf) warnings() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range strings.Split(l.b.String(), "\n") {
		if strings.Contains(line, "level=WARN") {
			out = append(out, line)
		}
	}
	return out
}

// stStatusRec matches any status write and records its arguments.
type stStatusRec struct {
	mu   sync.Mutex
	args []driver.Value
}

func (r *stStatusRec) Match(v driver.Value) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.args = append(r.args, v)
	return true
}

func (r *stStatusRec) got() []driver.Value {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]driver.Value(nil), r.args...)
}

// stSetup is the race fixture with its log output captured. The logger is
// swapped before anything runs.
func stSetup(t *testing.T) (*srFixture, *stLogBuf) {
	t.Helper()
	f := newSRFixture(t)
	logs := &stLogBuf{}
	f.c.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return f, logs
}

// stExpectStatus lets the next status write through and records it.
func stExpectStatus(f *srFixture) *stStatusRec {
	rec := &stStatusRec{}
	f.mock.ExpectExec("UPDATE connections SET status").WithArgs(rec, rec, rec).
		WillReturnResult(sqlmock.NewResult(0, 1))
	return rec
}

func stActive(f *srFixture) bool {
	f.c.mu.RLock()
	defer f.c.mu.RUnlock()
	_, ok := f.c.activeConnections[srConn]
	return ok
}

// stExpectStart expects one start: the lookup, the "running" write and the
// poller's first query on the source database.
func stExpectStart(f *srFixture) {
	f.expectLookup(nil)
	f.expectRunning()
	f.src.ExpectQuery("SELECT \\* FROM widgets").WillReturnRows(sqlmock.NewRows([]string{"id"}))
}

// stStartRunning starts srConn for srTenant and waits for its poller.
func stStartRunning(t *testing.T, f *srFixture) {
	t.Helper()
	stExpectStart(f)
	f.c.startConnection(context.Background(), srConn, srTenant)
	if !stActive(f) {
		t.Fatal("connection not active after its start")
	}
	gfAwaitPollers(t, 1)
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("db after the start: %v", err)
	}
}

// stCheckRefused checks that the stop just sent changed nothing and logged
// exactly one warning naming the connection and the tenants in want.
func stCheckRefused(t *testing.T, f *srFixture, rec *stStatusRec, logs *stLogBuf, warnsBefore int, want []string) {
	t.Helper()
	if !stActive(f) {
		t.Error("the connection left the active map after a stop for another tenant")
	}
	if n := gfPollers(); n != 1 {
		t.Errorf("pollers running after a stop for another tenant = %d, want 1", n)
	}
	if got := rec.got(); len(got) != 0 {
		t.Errorf("a stop for another tenant wrote a status: %v", got)
	}
	stCheckWarning(t, logs, warnsBefore, want)
}

func stCheckWarning(t *testing.T, logs *stLogBuf, warnsBefore int, want []string) {
	t.Helper()
	warns := logs.warnings()[warnsBefore:]
	if len(warns) != 1 {
		t.Fatalf("warnings logged for the refused stop = %d, want 1: %q", len(warns), warns)
	}
	for _, s := range append([]string{srConn}, want...) {
		if !strings.Contains(warns[0], s) {
			t.Errorf("warning %q does not name %q", warns[0], s)
		}
	}
	if strings.Contains(warns[0], stMarker) {
		t.Errorf("warning %q carries other payload content", warns[0])
	}
}

// stCheckStops sends a stop naming srTenant on its own subject and checks it
// still stops the connection, writing "stopped" for srTenant.
func stCheckStops(t *testing.T, f *srFixture, rec *stStatusRec) {
	t.Helper()
	f.c.handleStopCommand(stStop(stSubject(srTenant), srTenant))
	if stActive(f) {
		t.Error("a stop naming the right tenant left the connection active")
	}
	gfAwaitPollers(t, 0)
	want := []driver.Value{"stopped", srConn, srTenant}
	if got := rec.got(); !reflect.DeepEqual(got, want) {
		t.Errorf("status write for the stop = %v, want %v", got, want)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db: %v", err)
	}
}

// #498 criteria 1 and 3: a stop whose payload names another tenant, on that
// tenant's subject, leaves the connection running and writes nothing; a stop
// naming the right tenant still stops it.
func TestStopTenant_OtherTenantLeavesConnectionRunning(t *testing.T) {
	f, logs := stSetup(t)
	stStartRunning(t, f)
	rec := stExpectStatus(f)

	warns := len(logs.warnings())
	f.c.handleStopCommand(stStop(stSubject(stOther), stOther))
	stCheckRefused(t, f, rec, logs, warns, []string{srTenant, stOther})

	stCheckStops(t, f, rec)
}

// #498 criterion 2: a stop naming the right tenant on a subject that does not
// name it changes nothing.
func TestStopTenant_SubjectMismatchChangesNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		subject string
		want    []string // tenants the warning names
	}{
		{"subject names another tenant", stSubject(stOther), []string{srTenant, stOther}},
		{"subject is not a command subject", "vrsky.commands.connection.stop", []string{srTenant}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, logs := stSetup(t)
			stStartRunning(t, f)
			rec := stExpectStatus(f)

			warns := len(logs.warnings())
			f.c.handleStopCommand(stStop(tc.subject, srTenant))
			stCheckRefused(t, f, rec, logs, warns, tc.want)

			stCheckStops(t, f, rec)
		})
	}
}

// #498 criterion 4: a stop naming another tenant while a start is in progress
// does not abandon the start: it activates and writes "running".
func TestStopTenant_OtherTenantDoesNotAbandonStart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		subject string
		tenant  string
	}{
		{"payload names another tenant", stSubject(stOther), stOther},
		{"subject names another tenant", stSubject(stOther), srTenant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, logs := stSetup(t)
			stExpectStart(f)
			h := srHoldFirst(&f.c.starts)

			first := f.goStart()
			srAwait(t, h.reached, "start to claim the connection")
			warns := len(logs.warnings())
			f.c.handleStopCommand(stStop(tc.subject, tc.tenant))
			close(h.release)
			srAwait(t, first, "start")

			if !stActive(f) {
				t.Fatal("a stop for another tenant abandoned the start")
			}
			gfAwaitPollers(t, 1)
			if err := f.mock.ExpectationsWereMet(); err != nil {
				t.Errorf(`db (lookup, then "running"): %v`, err)
			}
			stCheckWarning(t, logs, warns, []string{srTenant, stOther})

			stCheckStops(t, f, stExpectStatus(f))
		})
	}
}
