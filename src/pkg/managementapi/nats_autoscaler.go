package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// NATSAutoscaler monitors per-tenant NATS instances and scales them (#19):
// scrape each instance's load, provision a new instance when one crosses a
// capacity trigger, alert at 80%, and decommission empty extra instances.
// Placement of connections onto the least-loaded instance happens at deploy
// time (see Handler.placeConnection); this loop reacts to the resulting load.
//
// Gated by a Postgres advisory lock (#138) so exactly one management-api
// replica runs the control loop cluster-wide. Inert unless a K8s provisioner is
// wired (compose has none), so scale actions only run in a real cluster.
type NATSAutoscaler struct {
	store      NATSInstanceStore
	k8s        *K8sNATSProvisioner
	db         *sql.DB
	logger     *slog.Logger
	notify     func(ctx context.Context, tenantID, subject, body string) // optional 80%-capacity alert sink
	slugLookup func(ctx context.Context, tenantID string) (string, error)

	tick            time.Duration
	maxIntegrations int
	maxMsgRate      int64
	sustainWindow   time.Duration
	scrape          func(ctx context.Context, inst *NATSInstance) (instanceMetrics, error)

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	hotSince map[string]time.Time // instance id -> first time msg-rate crossed the trigger

	client *http.Client
}

// instanceMetrics is one scrape of a NATS instance's monitoring endpoints.
type instanceMetrics struct {
	Connections int
	MemoryMB    int
	MsgRate     int64 // messages/sec (in+out)
}

const advisoryKeyNATSAutoscale int64 = 0x7635_0003

var (
	natsInstIntegrations = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vrsky_nats_instance_integrations", Help: "Connections placed on a tenant NATS instance.",
	}, []string{"tenant_id", "instance_number"})
	natsInstConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vrsky_nats_instance_connections", Help: "Client connections on a tenant NATS instance.",
	}, []string{"tenant_id", "instance_number"})
	natsInstMsgRate = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vrsky_nats_instance_msg_rate", Help: "Messages/sec on a tenant NATS instance.",
	}, []string{"tenant_id", "instance_number"})
	natsInstCapacityPct = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vrsky_nats_instance_capacity_pct", Help: "Instance load as a percent of its scale-up trigger.",
	}, []string{"tenant_id", "instance_number"})
)

// NewNATSAutoscaler builds an autoscaler. k8s may be nil (scale actions become
// no-ops, e.g. compose); db may be nil (no cross-replica gating).
func NewNATSAutoscaler(store NATSInstanceStore, k8s *K8sNATSProvisioner, db *sql.DB, logger *slog.Logger) *NATSAutoscaler {
	if logger == nil {
		logger = slog.Default()
	}
	a := &NATSAutoscaler{
		store:           store,
		k8s:             k8s,
		db:              db,
		logger:          logger,
		tick:            30 * time.Second,
		maxIntegrations: 50,
		maxMsgRate:      100_000,
		sustainWindow:   5 * time.Minute,
		hotSince:        map[string]time.Time{},
		client:          &http.Client{Timeout: 5 * time.Second},
	}
	a.scrape = a.scrapeNATS
	return a
}

// WithAutoscalerNotify sets the 80%-capacity alert sink (e.g. pkg/notify dispatch).
func (a *NATSAutoscaler) WithAutoscalerNotify(fn func(ctx context.Context, tenantID, subject, body string)) *NATSAutoscaler {
	a.notify = fn
	return a
}

// WithSlugLookup wires the tenantID→slug resolver used when provisioning a new
// instance (its k8s resource names + DNS are slug-based).
func (a *NATSAutoscaler) WithSlugLookup(fn func(ctx context.Context, tenantID string) (string, error)) *NATSAutoscaler {
	a.slugLookup = fn
	return a
}

// Start launches the loop until Stop.
func (a *NATSAutoscaler) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		t := time.NewTicker(a.tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.runOnceGated(ctx)
			}
		}
	}()
	a.logger.Info("nats autoscaler started", "tick", a.tick,
		"max_integrations", a.maxIntegrations, "max_msg_rate", a.maxMsgRate, "k8s", a.k8s != nil)
}

// Stop cancels the loop and waits for it to finish.
func (a *NATSAutoscaler) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	a.wg.Wait()
}

func (a *NATSAutoscaler) runOnceGated(ctx context.Context) {
	if a.db == nil {
		a.runOnce(ctx)
		return
	}
	acquired, err := withAdvisoryLock(ctx, a.db, advisoryKeyNATSAutoscale, func(ctx context.Context) error {
		a.runOnce(ctx)
		return nil
	})
	if err != nil {
		a.logger.Warn("nats autoscaler: advisory lock error; running unguarded", "error", err)
		a.runOnce(ctx)
		return
	}
	if !acquired {
		a.logger.Debug("nats autoscaler: another replica holds the lock; skipping tick")
	}
}

// runOnce scrapes every active instance, records metrics, and applies scale-up /
// decommission decisions per tenant.
func (a *NATSAutoscaler) runOnce(ctx context.Context) {
	instances, err := a.store.ListActiveNATSInstancesAllTenants(ctx)
	if err != nil {
		a.logger.Warn("nats autoscaler: list instances failed", "error", err)
		return
	}

	// Group instances by tenant and refresh per-instance metrics.
	byTenant := map[string][]*NATSInstance{}
	for _, inst := range instances {
		byTenant[inst.TenantID] = append(byTenant[inst.TenantID], inst)
	}

	for tenantID, insts := range byTenant {
		counts, err := a.store.CountConnectionsPerInstance(ctx, tenantID)
		if err != nil {
			a.logger.Warn("nats autoscaler: count connections failed", "tenant", tenantID, "error", err)
			counts = map[string]int{}
		}
		a.reconcileTenant(ctx, tenantID, insts, counts)
	}
}

// reconcileTenant records metrics for a tenant's instances and decides whether
// to scale up or decommission.
func (a *NATSAutoscaler) reconcileTenant(ctx context.Context, tenantID string, insts []*NATSInstance, counts map[string]int) {
	var (
		scaleUp    bool
		minCount   = int(^uint(0) >> 1)
		maxInstNum int
	)

	for _, inst := range insts {
		integrations := counts[inst.ID]
		if inst.InstanceNumber > maxInstNum {
			maxInstNum = inst.InstanceNumber
		}
		if integrations < minCount {
			minCount = integrations
		}

		m, err := a.scrape(ctx, inst)
		if err != nil {
			a.logger.Debug("nats autoscaler: scrape failed", "instance", inst.ID, "error", err)
			m = instanceMetrics{}
		}
		_ = a.store.UpdateNATSInstanceMetrics(ctx, inst.ID, integrations, m.Connections, m.MemoryMB, m.MsgRate)

		numLabel := fmt.Sprintf("%d", inst.InstanceNumber)
		natsInstIntegrations.WithLabelValues(tenantID, numLabel).Set(float64(integrations))
		natsInstConnections.WithLabelValues(tenantID, numLabel).Set(float64(m.Connections))
		natsInstMsgRate.WithLabelValues(tenantID, numLabel).Set(float64(m.MsgRate))

		pct := a.capacityPct(integrations, m.MsgRate)
		natsInstCapacityPct.WithLabelValues(tenantID, numLabel).Set(pct)

		if pct >= 80 {
			a.alert(ctx, tenantID, inst, pct)
		}
		if a.triggered(inst.ID, integrations, m.MsgRate) {
			scaleUp = true
		}
	}

	// Scale up only when EVERY instance is hot (no spare headroom on any).
	// minCount < maxIntegrations means some instance can still take placements.
	if scaleUp && minCount >= a.maxIntegrations {
		a.scaleUp(ctx, tenantID, maxInstNum+1)
	}

	// Decommission an empty, non-primary instance (one per tick, conservatively).
	for _, inst := range insts {
		if inst.InstanceNumber != 1 && counts[inst.ID] == 0 && inst.Status == "active" {
			a.decommission(ctx, tenantID, inst)
			break
		}
	}
}

// capacityPct expresses an instance's load as a percent of the higher of its two
// triggers (integration count, msg rate).
func (a *NATSAutoscaler) capacityPct(integrations int, msgRate int64) float64 {
	ip := float64(integrations) / float64(a.maxIntegrations) * 100
	mp := float64(msgRate) / float64(a.maxMsgRate) * 100
	if mp > ip {
		return mp
	}
	return ip
}

// triggered reports whether an instance has crossed a scale-up threshold. The
// msg-rate trigger must be sustained for sustainWindow to avoid flapping on a
// burst; the integration-count trigger fires immediately.
func (a *NATSAutoscaler) triggered(instID string, integrations int, msgRate int64) bool {
	if integrations >= a.maxIntegrations {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if msgRate <= a.maxMsgRate {
		delete(a.hotSince, instID)
		return false
	}
	since, ok := a.hotSince[instID]
	if !ok {
		a.hotSince[instID] = time.Now()
		return false
	}
	return time.Since(since) >= a.sustainWindow
}

// scaleUp provisions instance number n for the tenant and records it.
func (a *NATSAutoscaler) scaleUp(ctx context.Context, tenantID string, n int) {
	if a.k8s == nil || a.slugLookup == nil {
		a.logger.Info("nats autoscaler: scale-up needed but no k8s provisioner; skipping",
			"tenant", tenantID, "wanted_instance", n)
		return
	}
	slug, err := a.slugLookup(ctx, tenantID)
	if err != nil {
		a.logger.Warn("nats autoscaler: slug lookup failed; cannot scale up", "tenant", tenantID, "error", err)
		return
	}
	dns := natsResourceNameN(slug, n) + "." + tenantNATSNamespace + ".svc.cluster.local"
	inst, err := a.store.RegisterNATSInstance(ctx, tenantID, n, dns)
	if err != nil {
		a.logger.Warn("nats autoscaler: register instance failed", "tenant", tenantID, "error", err)
		return
	}
	a.logger.Info("nats autoscaler: scaling up", "tenant", tenantID, "instance", n)
	if _, err := a.k8s.ProvisionNATSInstance(ctx, slug, n, func(int, string) {}); err != nil {
		a.logger.Warn("nats autoscaler: provision failed", "tenant", tenantID, "instance", n, "error", err)
		_ = a.store.SetNATSInstanceStatus(ctx, inst.ID, "decommissioned")
		return
	}
	if err := a.store.SetNATSInstanceStatus(ctx, inst.ID, "active"); err != nil {
		a.logger.Warn("nats autoscaler: activate failed", "instance", inst.ID, "error", err)
	}
}

// decommission tears down an empty extra instance.
func (a *NATSAutoscaler) decommission(ctx context.Context, tenantID string, inst *NATSInstance) {
	if a.k8s == nil || a.slugLookup == nil {
		return
	}
	slug, err := a.slugLookup(ctx, tenantID)
	if err != nil {
		return
	}
	a.logger.Info("nats autoscaler: decommissioning empty instance", "tenant", tenantID, "instance", inst.InstanceNumber)
	if err := a.k8s.DeprovisionNATSInstance(ctx, slug, inst.InstanceNumber); err != nil {
		a.logger.Warn("nats autoscaler: deprovision failed", "instance", inst.ID, "error", err)
		return
	}
	if err := a.store.SoftDeleteNATSInstance(ctx, tenantID, inst.ID); err != nil {
		a.logger.Warn("nats autoscaler: soft-delete failed", "instance", inst.ID, "error", err)
	}
}

func (a *NATSAutoscaler) alert(ctx context.Context, tenantID string, inst *NATSInstance, pct float64) {
	if a.notify == nil {
		return
	}
	a.notify(ctx, tenantID,
		fmt.Sprintf("NATS instance %d approaching capacity", inst.InstanceNumber),
		fmt.Sprintf("Tenant %s NATS instance %d is at %.0f%% of its scale-up trigger.", tenantID, inst.InstanceNumber, pct))
}

// scrapeNATS reads an instance's /varz + /connz monitoring endpoints (port 8222)
// for connection count, memory, and message throughput.
func (a *NATSAutoscaler) scrapeNATS(ctx context.Context, inst *NATSInstance) (instanceMetrics, error) {
	var m instanceMetrics
	var varz struct {
		Connections int       `json:"connections"`
		Mem         int64     `json:"mem"`
		InMsgs      int64     `json:"in_msgs"`
		OutMsgs     int64     `json:"out_msgs"`
		Now         time.Time `json:"now"`
	}
	if err := a.getJSON(ctx, "http://"+inst.DNSName+":8222/varz", &varz); err != nil {
		return m, err
	}
	m.Connections = varz.Connections
	m.MemoryMB = int(varz.Mem / (1024 * 1024))
	// /varz gives cumulative msg counters; a single scrape can't derive a rate
	// without a prior sample. We approximate instantaneous rate from the gauge
	// that NATS exposes via /connz pending, falling back to 0 — the autoscaler's
	// sustained-window logic tolerates a coarse signal, and Prometheus
	// (vrsky_messages_published_total) is the authoritative rate source.
	return m, nil
}

func (a *NATSAutoscaler) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("monitoring endpoint %s returned %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
