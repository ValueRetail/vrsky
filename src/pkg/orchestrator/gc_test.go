package orchestrator

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

const gcNS = "vrsky-platform"

func gcWorkerDeploy(name, connID, nodeID string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gcNS, Labels: buildLabels(connID, nodeID, "consumer", "tenant-1")},
	}
}

func gcWorkerHPA(name, connID, nodeID string) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gcNS, Labels: buildLabels(connID, nodeID, "consumer", "tenant-1")},
	}
}

// TestGarbageCollectOrphanedWorkers: a connection still in the DB keeps its
// workers; an orphaned connection (gone from the DB) has its Deployments + HPAs
// deleted; a standing service (app=vrsky-<name>) is never touched.
func TestGarbageCollectOrphanedWorkers(t *testing.T) {
	objs := []runtime.Object{
		// Live connection A — kept.
		gcWorkerDeploy("vrsky-A-src", "A", "src"),
		gcWorkerDeploy("vrsky-A-dst", "A", "dst"),
		gcWorkerHPA("vrsky-A-src", "A", "src"),
		// Orphan connection B — removed.
		gcWorkerDeploy("vrsky-B-src", "B", "src"),
		gcWorkerHPA("vrsky-B-src", "B", "src"),
		// A standing service (app=vrsky-management-api) — must be untouched.
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "vrsky-management-api", Namespace: gcNS, Labels: map[string]string{LabelApp: "vrsky-management-api"}}},
	}
	client := fake.NewSimpleClientset(objs...)
	exists := func(connID string) bool { return connID == "A" } // B is the orphan

	removed, err := GarbageCollectOrphanedWorkers(context.Background(), client, gcNS, exists)
	if err != nil {
		t.Fatalf("GC error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (connection B only)", removed)
	}

	deps, _ := client.AppsV1().Deployments(gcNS).List(context.Background(), metav1.ListOptions{})
	got := map[string]bool{}
	for i := range deps.Items {
		got[deps.Items[i].Name] = true
	}
	for _, keep := range []string{"vrsky-A-src", "vrsky-A-dst", "vrsky-management-api"} {
		if !got[keep] {
			t.Errorf("expected %q to survive GC, but it was deleted", keep)
		}
	}
	if got["vrsky-B-src"] {
		t.Error("orphan B deployment was not deleted")
	}

	hpas, _ := client.AutoscalingV2().HorizontalPodAutoscalers(gcNS).List(context.Background(), metav1.ListOptions{})
	for i := range hpas.Items {
		if hpas.Items[i].Name == "vrsky-B-src" {
			t.Error("orphan B HPA was not deleted")
		}
	}
}

// TestGarbageCollectOrphanedWorkers_OrphanHPAOnly: a partial teardown can leave
// an orphaned HPA with its Deployment already gone. The GC must still discover
// the connection (from the HPA label) and delete the HPA.
func TestGarbageCollectOrphanedWorkers_OrphanHPAOnly(t *testing.T) {
	client := fake.NewSimpleClientset(
		gcWorkerHPA("vrsky-D-src", "D", "src"), // HPA present, no Deployment
	)
	removed, err := GarbageCollectOrphanedWorkers(context.Background(), client, gcNS, func(string) bool { return false })
	if err != nil {
		t.Fatalf("GC error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (orphan discovered via its HPA)", removed)
	}
	hpas, _ := client.AutoscalingV2().HorizontalPodAutoscalers(gcNS).List(context.Background(), metav1.ListOptions{})
	if len(hpas.Items) != 0 {
		t.Errorf("orphan HPA not deleted: %d remain", len(hpas.Items))
	}
}

// TestGarbageCollectOrphanedWorkers_DBErrorKeepsWorkers: when `exists` can't
// confirm a connection is gone (returns true on error), workers are kept.
func TestGarbageCollectOrphanedWorkers_DBErrorKeepsWorkers(t *testing.T) {
	client := fake.NewSimpleClientset(
		gcWorkerDeploy("vrsky-C-src", "C", "src"),
		gcWorkerHPA("vrsky-C-src", "C", "src"),
	)
	// Simulate a DB blip: exists conservatively returns true for everything.
	removed, err := GarbageCollectOrphanedWorkers(context.Background(), client, gcNS, func(string) bool { return true })
	if err != nil {
		t.Fatalf("GC error: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 (nothing deleted when existence is uncertain)", removed)
	}
}
