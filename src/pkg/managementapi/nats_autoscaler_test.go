package managementapi

import (
	"context"
	"testing"
	"time"
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
