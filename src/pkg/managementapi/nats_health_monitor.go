package managementapi

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// NATSHealthMonitor periodically probes each tenant NATS instance's monitoring
// endpoint and flips its status between active/unhealthy, so the discovery API
// (#21) stops handing out URLs for instances that are unreachable. Unhealthy
// instances are removed from discovery within ~one tick (default 30s) of going
// dark, well inside the issue's 60s target.
//
// Like the OAuth refresher and usage rollup, it's gated by a Postgres advisory
// lock (#138) so only one management-api replica probes cluster-wide.
type NATSHealthMonitor struct {
	store         NATSInstanceStore
	db            *sql.DB // optional: advisory-lock gating across replicas
	logger        *slog.Logger
	tick          time.Duration
	failThreshold int
	client        *http.Client

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	failures map[string]int // instance id -> consecutive probe failures

	// probeFn checks one instance by DNS name; defaults to the real HTTP probe.
	// Overridable in tests.
	probeFn func(ctx context.Context, dnsName string) bool
}

// advisoryKeyNATSHealth gates the health monitor to one replica per tick.
const advisoryKeyNATSHealth int64 = 0x7635_0002

// NewNATSHealthMonitor builds a monitor. db may be nil (no cross-replica gating,
// e.g. single replica / tests).
func NewNATSHealthMonitor(store NATSInstanceStore, db *sql.DB, logger *slog.Logger) *NATSHealthMonitor {
	if logger == nil {
		logger = slog.Default()
	}
	m := &NATSHealthMonitor{
		store:         store,
		db:            db,
		logger:        logger,
		tick:          30 * time.Second,
		failThreshold: 2,
		client:        &http.Client{Timeout: 5 * time.Second},
		failures:      map[string]int{},
	}
	m.probeFn = m.probe
	return m
}

// Start launches the probe loop until Stop.
func (m *NATSHealthMonitor) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(m.tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.runOnceGated(ctx)
			}
		}
	}()
	m.logger.Info("nats health monitor started", "tick", m.tick, "fail_threshold", m.failThreshold)
}

// Stop cancels the loop and waits for it to finish.
func (m *NATSHealthMonitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}

func (m *NATSHealthMonitor) runOnceGated(ctx context.Context) {
	if m.db == nil {
		m.runOnce(ctx)
		return
	}
	acquired, err := withAdvisoryLock(ctx, m.db, advisoryKeyNATSHealth, func(ctx context.Context) error {
		m.runOnce(ctx)
		return nil
	})
	if err != nil {
		m.logger.Warn("nats health monitor: advisory lock error; probing unguarded", "error", err)
		m.runOnce(ctx)
		return
	}
	if !acquired {
		m.logger.Debug("nats health monitor: another replica holds the lock; skipping tick")
	}
}

// runOnce probes every active/unhealthy instance once and updates status on a
// healthy↔unhealthy transition.
func (m *NATSHealthMonitor) runOnce(ctx context.Context) {
	instances, err := m.store.ListActiveNATSInstancesAllTenants(ctx)
	if err != nil {
		m.logger.Warn("nats health monitor: list instances failed", "error", err)
		return
	}
	for _, inst := range instances {
		healthy := m.probeFn(ctx, inst.DNSName)
		m.mu.Lock()
		if healthy {
			delete(m.failures, inst.ID)
		} else {
			m.failures[inst.ID]++
		}
		fails := m.failures[inst.ID]
		m.mu.Unlock()

		switch {
		case healthy && inst.Status == "unhealthy":
			if err := m.store.SetNATSInstanceStatus(ctx, inst.ID, "active"); err != nil {
				m.logger.Warn("nats health monitor: mark active failed", "instance", inst.ID, "error", err)
			} else {
				m.logger.Info("nats instance recovered", "instance", inst.ID, "tenant", inst.TenantID)
			}
		case !healthy && inst.Status == "active" && fails >= m.failThreshold:
			if err := m.store.SetNATSInstanceStatus(ctx, inst.ID, "unhealthy"); err != nil {
				m.logger.Warn("nats health monitor: mark unhealthy failed", "instance", inst.ID, "error", err)
			} else {
				m.logger.Warn("nats instance marked unhealthy", "instance", inst.ID, "tenant", inst.TenantID, "consecutive_failures", fails)
			}
		}
	}
}

// probe returns true if the instance's NATS monitoring endpoint reports healthy.
// NATS serves /healthz on the monitoring port (8222).
func (m *NATSHealthMonitor) probe(ctx context.Context, dnsName string) bool {
	url := "http://" + dnsName + ":8222/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
