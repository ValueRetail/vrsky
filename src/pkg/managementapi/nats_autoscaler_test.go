package managementapi

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestAutoscaler_Triggered(t *testing.T) {
	a := NewNATSAutoscaler(newNATSInstRepo(), nil, nil, nil)

	// Integration-count trigger fires immediately.
	if !a.triggered("i1", 50, 0) {
		t.Error("integrations >= 50 should trigger")
	}
	if a.triggered("i2", 49, 0) {
		t.Error("integrations < 50 should not trigger")
	}

	// Msg-rate trigger must be SUSTAINED: first cross arms the timer, doesn't fire.
	if a.triggered("i3", 0, 200_000) {
		t.Error("first msg-rate cross should arm, not fire")
	}
	// Backdate the armed timestamp past the sustain window → now it fires.
	a.mu.Lock()
	a.hotSince["i3"] = time.Now().Add(-6 * time.Minute)
	a.mu.Unlock()
	if !a.triggered("i3", 0, 200_000) {
		t.Error("sustained msg-rate should trigger")
	}
	// Dropping back below the rate clears the armed state.
	if a.triggered("i3", 0, 10) {
		t.Error("below-threshold rate should not trigger")
	}
	a.mu.Lock()
	_, still := a.hotSince["i3"]
	a.mu.Unlock()
	if still {
		t.Error("armed timer should clear once rate drops")
	}
}

func TestAutoscaler_CapacityPct(t *testing.T) {
	a := NewNATSAutoscaler(newNATSInstRepo(), nil, nil, nil)
	if got := a.capacityPct(25, 0); got != 50 { // 25/50
		t.Errorf("integration pct = %v, want 50", got)
	}
	if got := a.capacityPct(0, 80_000); got != 80 { // 80k/100k
		t.Errorf("msgrate pct = %v, want 80", got)
	}
	if got := a.capacityPct(45, 10_000); got != 90 { // max(90, 10)
		t.Errorf("max pct = %v, want 90", got)
	}
}

func TestAutoscaler_ReconcileRecordsMetrics(t *testing.T) {
	repo := newNATSInstRepo()
	repo.instances = []*NATSInstance{
		{ID: "n1", TenantID: "t-1", InstanceNumber: 1, DNSName: "nats-t-1-1", Status: "active"},
	}
	repo.connCounts = map[string]int{"n1": 7}

	a := NewNATSAutoscaler(repo, nil, nil, nil)
	// Stub the scrape so no real HTTP happens.
	a.scrape = func(_ context.Context, _ *NATSInstance) (instanceMetrics, error) {
		return instanceMetrics{Connections: 12, MemoryMB: 64, MsgRate: 5}, nil
	}

	a.runOnce(context.Background())

	if repo.metricUpdates["n1"] != 7 {
		t.Fatalf("expected integration count 7 recorded for n1, got %d", repo.metricUpdates["n1"])
	}
}

// The scraped gauges are deliberately not published while #209 is open: a
// tenant NATS instance carries none of its tenant's traffic, so a graph of
// them reads as "idle tenant" rather than "unwired routing". See
// publishScrapedMetrics.
//
// This asserts the absence, because the whole point is that an operator should
// find no series rather than a flat zero. It is expected to be deleted along
// with the constant when connections are actually routed to their placed
// instance.
func TestAutoscaler_ScrapedGaugesAreNotPublished(t *testing.T) {
	if publishScrapedMetrics {
		t.Skip("scraped metrics are published again — #209 presumably landed; delete this test with the constant")
	}

	repo := newNATSInstRepo()
	repo.instances = []*NATSInstance{
		{ID: "n1", TenantID: "t-metrics", InstanceNumber: 1, DNSName: "nats-t-1", Status: "active"},
	}
	repo.connCounts = map[string]int{"n1": 7}

	a := NewNATSAutoscaler(repo, nil, nil, nil)
	// Non-zero on purpose: if the gating regressed, these values would show up
	// and the assertions below would catch a real number, not a zero.
	a.scrape = func(_ context.Context, _ *NATSInstance) (instanceMetrics, error) {
		return instanceMetrics{Connections: 12, MemoryMB: 64, MsgRate: 5}, nil
	}
	a.runOnce(context.Background())

	for name, g := range map[string]*prometheus.GaugeVec{
		"vrsky_nats_instance_connections": natsInstConnections,
		"vrsky_nats_instance_msg_rate":    natsInstMsgRate,
	} {
		if n := testutil.CollectAndCount(g); n != 0 {
			t.Errorf("%s emitted %d series; it must emit none until a placed connection's traffic actually reaches its instance (#209)", name, n)
		}
	}

	// The placement-derived gauges are real and must keep working — this change
	// was about not publishing a number that cannot move, not about going quiet.
	if n := testutil.CollectAndCount(natsInstIntegrations); n == 0 {
		t.Error("vrsky_nats_instance_integrations emitted no series; the placement count is real and should still be published")
	}
}
