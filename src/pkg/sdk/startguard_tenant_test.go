package sdk_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Tests for #498: a stop command acts only when it names, on its subject and
// in its payload, the tenant the connection is starting or active for here.
// They drive StartGuard through its exported API only, the way a consumer's
// start and stop handlers do.

const (
	tsConn    = "conn-498"
	tsTenantA = "tenant-a" // the tenant the connection belongs to
	tsTenantB = "tenant-b" // another tenant
)

func tsSubject(tenant string) string { return "vrsky.commands." + tenant + ".connection.stop" }

func TestCommandSubjectTenant(t *testing.T) {
	cases := []struct {
		subject string
		want    string
		ok      bool
	}{
		{"vrsky.commands.tenant-a.connection.stop", "tenant-a", true},
		{"vrsky.commands.tenant-a.connection.start", "tenant-a", true},
		{"vrsky.commands.11111111-1111-1111-1111-111111111111.connection.stop", "11111111-1111-1111-1111-111111111111", true},
		{"", "", false},
		{"vrsky.commands..connection.stop", "", false},
		{"vrsky.commands.tenant-a.connection.", "", false},
		{"vrsky.commands.tenant-a.connection", "", false},
		{"vrsky.commands.tenant-a.connection.stop.extra", "", false},
		{"vrsky.commands.connection.stop", "", false},
		{"other.commands.tenant-a.connection.stop", "", false},
		{"vrsky.events.tenant-a.connection.stop", "", false},
		{"vrsky.commands.tenant-a.pipeline.stop", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.subject, func(t *testing.T) {
			got, ok := sdk.CommandSubjectTenant(tc.subject)
			if got != tc.want || ok != tc.ok {
				t.Errorf("CommandSubjectTenant(%q) = (%q, %v), want (%q, %v)", tc.subject, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// tsOwner is a minimal consumer: one lock over the active map and the guard.
type tsOwner struct {
	mu     sync.Mutex
	active map[string]bool
	g      sdk.StartGuard
}

func newTSOwner() *tsOwner { return &tsOwner{active: map[string]bool{}} }

func (o *tsOwner) isActive() bool { return o.active[tsConn] }

// reserve claims the connection for tenant; plain uses Reserve, which names
// no tenant.
func (o *tsOwner) reserve(t *testing.T, tenant string, plain bool) *sdk.StartClaim {
	t.Helper()
	var c *sdk.StartClaim
	var ok bool
	if plain {
		c, ok = o.g.Reserve(&o.mu, tsConn, o.isActive)
	} else {
		c, ok = o.g.ReserveOrRetry(&o.mu, tenant, tsConn, o.isActive, nil)
	}
	if !ok {
		t.Fatal("reserve refused on a fresh connection")
	}
	return c
}

func (o *tsOwner) activate(c *sdk.StartClaim) bool {
	return c.Activate(func() { o.active[tsConn] = true })
}

// stop is the consumer's stop handler: the tenant check, and on success the
// cancel and delete.
func (o *tsOwner) stop(subject, tenant string) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	starting, err := o.g.StopForTenantLocked(subject, tenant, tsConn)
	if err != nil {
		return false, err
	}
	delete(o.active, tsConn)
	return starting, nil
}

// reserveAgain reports whether a fresh start could claim the connection now.
func (o *tsOwner) reserveAgain() bool {
	c, ok := o.g.Reserve(&o.mu, tsConn, func() bool { return false })
	if ok {
		c.Release()
	}
	return ok
}

func TestStopForTenantLocked(t *testing.T) {
	const (
		none     = "none"     // the connection is neither starting nor active here
		starting = "starting" // a start for tenant A holds the claim
		active   = "active"   // a start for tenant A has activated
	)
	cases := []struct {
		name    string
		state   string
		plain   bool   // the start came through Reserve, with no tenant
		subject string // the stop's subject
		tenant  string // the tenant in the stop's payload

		wantErr      bool
		wantOther    string // the other tenant the error names, when it names one
		wantStarting bool   // StopForTenantLocked's bool
	}{
		// Another tenant's stop, subject and payload agreeing.
		{name: "active for A, stop for B", state: active, subject: tsSubject(tsTenantB), tenant: tsTenantB, wantErr: true, wantOther: tsTenantA},
		{name: "starting for A, stop for B", state: starting, subject: tsSubject(tsTenantB), tenant: tsTenantB, wantErr: true, wantOther: tsTenantA},

		// Subject and payload disagree, whichever names the owner.
		{name: "active for A, payload A on B's subject", state: active, subject: tsSubject(tsTenantB), tenant: tsTenantA, wantErr: true, wantOther: tsTenantB},
		{name: "active for A, payload B on A's subject", state: active, subject: tsSubject(tsTenantA), tenant: tsTenantB, wantErr: true, wantOther: tsTenantA},
		{name: "starting for A, payload A on B's subject", state: starting, subject: tsSubject(tsTenantB), tenant: tsTenantA, wantErr: true, wantOther: tsTenantB},
		{name: "unknown connection, payload A on B's subject", state: none, subject: tsSubject(tsTenantB), tenant: tsTenantA, wantErr: true, wantOther: tsTenantB},

		// Malformed subjects are a mismatch too.
		{name: "active for A, malformed subject", state: active, subject: "vrsky.commands.connection.stop", tenant: tsTenantA, wantErr: true},
		{name: "active for A, empty subject", state: active, subject: "", tenant: tsTenantA, wantErr: true},
		{name: "starting for A, subject with an extra token", state: starting, subject: tsSubject(tsTenantA) + ".x", tenant: tsTenantA, wantErr: true},

		// The right tenant on a matching subject: as StopLocked.
		{name: "active for A, stop for A", state: active, subject: tsSubject(tsTenantA), tenant: tsTenantA},
		{name: "starting for A, stop for A", state: starting, subject: tsSubject(tsTenantA), tenant: tsTenantA, wantStarting: true},

		// A connection this owner knows nothing of passes, for any tenant.
		{name: "unknown connection, stop for A", state: none, subject: tsSubject(tsTenantA), tenant: tsTenantA},
		{name: "unknown connection, stop for B", state: none, subject: tsSubject(tsTenantB), tenant: tsTenantB},

		// A start through plain Reserve records no tenant: only the subject is checked.
		{name: "plain Reserve active, stop for B", state: active, plain: true, subject: tsSubject(tsTenantB), tenant: tsTenantB},
		{name: "plain Reserve starting, stop for B", state: starting, plain: true, subject: tsSubject(tsTenantB), tenant: tsTenantB, wantStarting: true},
		{name: "plain Reserve starting, payload B on A's subject", state: starting, plain: true, subject: tsSubject(tsTenantA), tenant: tsTenantB, wantErr: true, wantOther: tsTenantA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newTSOwner()
			var claim *sdk.StartClaim
			switch tc.state {
			case starting:
				claim = o.reserve(t, tsTenantA, tc.plain)
				defer claim.Release()
			case active:
				c := o.reserve(t, tsTenantA, tc.plain)
				if !o.activate(c) {
					t.Fatal("Activate refused with no stop")
				}
				c.Release()
			}

			gotStarting, err := o.stop(tc.subject, tc.tenant)

			if gotStarting != tc.wantStarting {
				t.Errorf("starting = %v, want %v", gotStarting, tc.wantStarting)
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("stop refused: %v", err)
				}
			} else {
				var mm *sdk.StopTenantMismatch
				if !errors.As(err, &mm) {
					t.Fatalf("err = %v (%T), want a *StopTenantMismatch", err, err)
				}
				msg := err.Error()
				for _, want := range []string{tsConn, tc.tenant, tc.wantOther} {
					if want != "" && !strings.Contains(msg, want) {
						t.Errorf("error %q does not name %q", msg, want)
					}
				}
			}

			stopped := !tc.wantErr
			switch tc.state {
			case active:
				o.mu.Lock()
				still := o.isActive()
				o.mu.Unlock()
				if still == stopped {
					t.Errorf("connection still active = %v, want %v", still, !stopped)
				}
			case starting:
				// A refused stop leaves the claim in place: a fresh start is
				// still refused, and the held one still activates. One that
				// went through frees the claim and the held start gives up.
				if again := o.reserveAgain(); again != stopped {
					t.Errorf("a fresh start could claim the connection = %v, want %v", again, stopped)
				}
				if got := o.activate(claim); got == stopped {
					t.Errorf("held start activated = %v, want %v", got, !stopped)
				}
			}
		})
	}
}

// After a refused stop the right tenant's stop still works.
func TestStopForTenantLocked_RightTenantAfterRefused(t *testing.T) {
	for _, state := range []string{"starting", "active"} {
		t.Run(state, func(t *testing.T) {
			o := newTSOwner()
			c := o.reserve(t, tsTenantA, false)
			defer c.Release()
			if state == "active" && !o.activate(c) {
				t.Fatal("Activate refused with no stop")
			}
			if _, err := o.stop(tsSubject(tsTenantB), tsTenantB); err == nil {
				t.Fatal("stop for another tenant was not refused")
			}
			starting, err := o.stop(tsSubject(tsTenantA), tsTenantA)
			if err != nil {
				t.Fatalf("stop for the right tenant refused: %v", err)
			}
			if starting != (state == "starting") {
				t.Errorf("starting = %v, want %v", starting, state == "starting")
			}
			if o.active[tsConn] {
				t.Error("connection still active after the right tenant's stop")
			}
			if state == "starting" && o.activate(c) {
				t.Error("held start activated after the right tenant's stop")
			}
		})
	}
}

// A refused stop for tenant B on tenant A's claim drops the start for tenant B
// that ReserveOrRetry queued there (the stop is newer), and keeps tenant A's.
func TestStopForTenantLocked_DropsOwnTenantsQueuedRetry(t *testing.T) {
	o := newTSOwner()
	claim := o.reserve(t, tsTenantA, false)

	var ranA, ranB int
	if _, ok := o.g.ReserveOrRetry(&o.mu, tsTenantB, tsConn, o.isActive, func() { ranB++ }); ok {
		t.Fatal("start for B claimed a connection A is starting")
	}
	if _, ok := o.g.ReserveOrRetry(&o.mu, tsTenantA, tsConn, o.isActive, func() { ranA++ }); ok {
		t.Fatal("second start for A claimed a connection A is starting")
	}

	if _, err := o.stop(tsSubject(tsTenantB), tsTenantB); err == nil {
		t.Fatal("stop for B on A's start was not refused")
	}

	// A's start gives up without activating: its Release runs what is queued.
	claim.Release()
	if ranB != 0 {
		t.Errorf("B's queued start ran %d times after B's stop, want 0", ranB)
	}
	if ranA != 1 {
		t.Errorf("A's queued start ran %d times, want 1 (a stop for B must not drop it)", ranA)
	}
}
