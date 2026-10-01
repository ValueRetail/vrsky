package managementapi

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func gaugesForTest(t *testing.T, now time.Time) (*PlatformGauges, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	g := NewPlatformGauges(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	g.now = func() time.Time { return now }
	return g, mock
}

// TestPlatformGauges_PublishPerTenant: both gauges carry tenant_id, because
// that label is what makes Alertmanager route the alert to the tenant's own
// notification targets instead of the platform ones; a pipeline in "error"
// counts under its status; an agent counts as online only inside
// AgentOnlineWindow.
func TestPlatformGauges_PublishPerTenant(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	g, mock := gaugesForTest(t, now)
	mock.ExpectQuery(`FROM connections GROUP BY tenant_id, status`).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "status", "count"}).
			AddRow("tenant-a", "running", 3).
			AddRow("tenant-a", "error", 1).
			AddRow("tenant-b", "stopped", 2))
	mock.ExpectQuery(`FROM agents WHERE revoked_at IS NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "id", "name", "last_seen_at"}).
			AddRow("tenant-a", "a1", "POS-PC", now.Add(-30*time.Second)).
			AddRow("tenant-a", "a2", "TILL-2", now.Add(-2*time.Hour)).
			AddRow("tenant-b", "b1", "LAGER", nil))

	g.refresh(context.Background())

	cases := []struct {
		tenant, status string
		want           float64
	}{{"tenant-a", "running", 3}, {"tenant-a", "error", 1}, {"tenant-b", "stopped", 2}}
	for _, c := range cases {
		if got := testutil.ToFloat64(connectionsByStatus.WithLabelValues(c.tenant, c.status)); got != c.want {
			t.Errorf("vrsky_connections{%s,%s} = %v, want %v", c.tenant, c.status, got, c.want)
		}
	}
	for _, c := range []struct {
		tenant, id, name string
		want             float64
	}{{"tenant-a", "a1", "POS-PC", 1}, {"tenant-a", "a2", "TILL-2", 0}, {"tenant-b", "b1", "LAGER", 0}} {
		if got := testutil.ToFloat64(remoteAgentOnline.WithLabelValues(c.tenant, c.id, c.name)); got != c.want {
			t.Errorf("vrsky_remote_agent_online{%s,%s} = %v, want %v", c.tenant, c.name, got, c.want)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// TestPlatformGauges_StaleSeriesDisappear: when a connection leaves "error"
// (or an agent is revoked) its old series must vanish, not sit at the last
// value — a lingering vrsky_connections{status="error"} 1 would keep
// ConnectionInError firing after the fix.
func TestPlatformGauges_StaleSeriesDisappear(t *testing.T) {
	now := time.Now()
	g, mock := gaugesForTest(t, now)
	mock.ExpectQuery(`FROM connections`).WillReturnRows(sqlmock.NewRows([]string{"t", "s", "n"}).AddRow("tenant-a", "error", 1))
	mock.ExpectQuery(`FROM agents`).WillReturnRows(sqlmock.NewRows([]string{"t", "id", "n", "ls"}).AddRow("tenant-a", "a9", "OLD", now))
	g.refresh(context.Background())
	if testutil.CollectAndCount(connectionsByStatus) != 1 || testutil.CollectAndCount(remoteAgentOnline) != 1 {
		t.Fatalf("precondition: expected one series each")
	}

	mock.ExpectQuery(`FROM connections`).WillReturnRows(sqlmock.NewRows([]string{"t", "s", "n"}).AddRow("tenant-a", "running", 1))
	mock.ExpectQuery(`FROM agents`).WillReturnRows(sqlmock.NewRows([]string{"t", "id", "n", "ls"}))
	g.refresh(context.Background())

	if n := testutil.CollectAndCount(connectionsByStatus); n != 1 {
		t.Errorf("vrsky_connections series = %d, want 1 (the error series must be gone)", n)
	}
	if got := testutil.ToFloat64(connectionsByStatus.WithLabelValues("tenant-a", "running")); got != 1 {
		t.Errorf("running = %v", got)
	}
	if n := testutil.CollectAndCount(remoteAgentOnline); n != 0 {
		t.Errorf("vrsky_remote_agent_online series = %d, want 0 after the agent was revoked", n)
	}
}

// A failing query keeps the previous values rather than zeroing everything,
// so a DB blip does not resolve every alert and re-fire it a scrape later.
func TestPlatformGauges_QueryErrorKeepsLastValues(t *testing.T) {
	now := time.Now()
	g, mock := gaugesForTest(t, now)
	mock.ExpectQuery(`FROM connections`).WillReturnRows(sqlmock.NewRows([]string{"t", "s", "n"}).AddRow("tenant-a", "error", 1))
	mock.ExpectQuery(`FROM agents`).WillReturnRows(sqlmock.NewRows([]string{"t", "id", "n", "ls"}))
	g.refresh(context.Background())

	mock.ExpectQuery(`FROM connections`).WillReturnError(context.DeadlineExceeded)
	mock.ExpectQuery(`FROM agents`).WillReturnError(context.DeadlineExceeded)
	g.refresh(context.Background())
	if got := testutil.ToFloat64(connectionsByStatus.WithLabelValues("tenant-a", "error")); got != 1 {
		t.Errorf("after a failed refresh error = %v, want the previous 1", got)
	}
}
