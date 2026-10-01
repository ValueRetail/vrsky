package managementapi

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Platform gauges for alerting (plans/monitoring-prod.md). Connection status
// and remote-agent liveness live only in Postgres; these publish them on the
// management-api /metrics endpoint so Prometheus can alert on a pipeline stuck
// in "error" and on a till that has stopped polling. Every series carries
// tenant_id, which is what routes the alert to that workspace's notification
// targets rather than to the platform ones.
var (
	connectionsByStatus = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vrsky_connections",
		Help: "Connections (pipelines) by tenant and status.",
	}, []string{"tenant_id", "status"})
	remoteAgentOnline = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vrsky_remote_agent_online",
		Help: "1 when a registered remote agent polled within AgentOnlineWindow, 0 when it did not. Revoked agents are absent.",
	}, []string{"tenant_id", "agent_id", "agent"})
)

// DefaultPlatformGaugeInterval matches the Prometheus scrape interval.
const DefaultPlatformGaugeInterval = 30 * time.Second

// PlatformGauges refreshes the gauges above from the database on a ticker.
type PlatformGauges struct {
	db       *sql.DB
	logger   *slog.Logger
	interval time.Duration
	now      func() time.Time
}

// NewPlatformGauges wires the collector; call Start to run it.
func NewPlatformGauges(db *sql.DB, logger *slog.Logger) *PlatformGauges {
	if logger == nil {
		logger = slog.Default()
	}
	return &PlatformGauges{db: db, logger: logger, interval: DefaultPlatformGaugeInterval, now: time.Now}
}

// Start refreshes once immediately and then every interval until ctx ends.
// Every replica publishes the same values, which is fine for gauges.
func (g *PlatformGauges) Start(ctx context.Context) {
	go func() {
		g.refresh(ctx)
		t := time.NewTicker(g.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				g.refresh(ctx)
			}
		}
	}()
}

// refresh replaces both gauge vectors with the current rows. The vectors are
// reset first so a deleted connection, a revoked agent or an emptied status
// stops being reported instead of lingering at its last value — a lingering
// "error" would keep an alert firing after the pipeline was fixed.
func (g *PlatformGauges) refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := g.refreshConnections(ctx); err != nil {
		g.logger.Warn("platform gauges: connections", "error", err)
	}
	if err := g.refreshAgents(ctx); err != nil {
		g.logger.Warn("platform gauges: agents", "error", err)
	}
}

func (g *PlatformGauges) refreshConnections(ctx context.Context) error {
	// lint:tenant-ok — platform-wide aggregate grouped BY tenant for the
	// internal metrics endpoint; nothing here is served to a tenant.
	const q = `SELECT tenant_id, status, COUNT(*) FROM connections GROUP BY tenant_id, status`
	rows, err := g.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type key struct{ tenant, status string }
	counts := map[key]float64{}
	for rows.Next() {
		var k key
		var n float64
		if err := rows.Scan(&k.tenant, &k.status, &n); err != nil {
			return err
		}
		counts[k] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	connectionsByStatus.Reset()
	for k, n := range counts {
		connectionsByStatus.WithLabelValues(k.tenant, k.status).Set(n)
	}
	return nil
}

func (g *PlatformGauges) refreshAgents(ctx context.Context) error {
	// lint:tenant-ok — platform-wide liveness for the internal metrics
	// endpoint; the tenant_id column becomes the routing label.
	const q = `SELECT tenant_id::text, id::text, name, last_seen_at FROM agents WHERE revoked_at IS NULL`
	rows, err := g.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type agentRow struct {
		tenant, id, name string
		online           float64
	}
	var agents []agentRow
	cutoff := g.now().Add(-AgentOnlineWindow)
	for rows.Next() {
		var a agentRow
		var lastSeen sql.NullTime
		if err := rows.Scan(&a.tenant, &a.id, &a.name, &lastSeen); err != nil {
			return err
		}
		if lastSeen.Valid && lastSeen.Time.After(cutoff) {
			a.online = 1
		}
		agents = append(agents, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	remoteAgentOnline.Reset()
	for _, a := range agents {
		remoteAgentOnline.WithLabelValues(a.tenant, a.id, a.name).Set(a.online)
	}
	return nil
}
