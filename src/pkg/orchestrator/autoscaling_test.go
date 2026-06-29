package orchestrator

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// #135: per-connection worker deployments get configurable replicas + an HPA.

func TestResolveScaling_Defaults(t *testing.T) {
	node := createNode("c1", "consumer", map[string]interface{}{"type": "kafka"})
	s := resolveScaling(node, DefaultConfig())
	if s.MinReplicas != 1 || s.MaxReplicas != 10 || s.TargetCPUPercent != 75 {
		t.Fatalf("defaults wrong: %+v", s)
	}
}

func TestResolveScaling_Override(t *testing.T) {
	node := createNode("c1", "consumer", map[string]interface{}{
		"type":    "kafka",
		"scaling": map[string]interface{}{"min_replicas": 3, "max_replicas": 20, "target_cpu_percent": 60},
	})
	s := resolveScaling(node, DefaultConfig())
	if s.MinReplicas != 3 || s.MaxReplicas != 20 || s.TargetCPUPercent != 60 {
		t.Fatalf("override not applied: %+v", s)
	}
}

func TestResolveScaling_ClampsMaxBelowMin(t *testing.T) {
	node := createNode("c1", "consumer", map[string]interface{}{
		"scaling": map[string]interface{}{"min_replicas": 5, "max_replicas": 2},
	})
	s := resolveScaling(node, DefaultConfig())
	if s.MinReplicas != 5 || s.MaxReplicas != 5 {
		t.Fatalf("expected max clamped up to min, got %+v", s)
	}
}

func TestBuildHPA(t *testing.T) {
	labels := buildLabels("conn-1", "n1", "consumer", "tenant-1")
	hpa := buildHPA(DefaultConfig(), labels, "vrsky-conn-1-n1", NodeScaling{MinReplicas: 2, MaxReplicas: 8, TargetCPUPercent: 70})
	if hpa.Spec.ScaleTargetRef.Name != "vrsky-conn-1-n1" || hpa.Spec.ScaleTargetRef.Kind != "Deployment" {
		t.Fatalf("scale target ref wrong: %+v", hpa.Spec.ScaleTargetRef)
	}
	if hpa.Spec.MinReplicas == nil || *hpa.Spec.MinReplicas != 2 || hpa.Spec.MaxReplicas != 8 {
		t.Fatalf("min/max wrong: min=%v max=%d", hpa.Spec.MinReplicas, hpa.Spec.MaxReplicas)
	}
	if len(hpa.Spec.Metrics) != 1 ||
		hpa.Spec.Metrics[0].Resource == nil ||
		hpa.Spec.Metrics[0].Resource.Name != corev1.ResourceCPU ||
		hpa.Spec.Metrics[0].Resource.Target.AverageUtilization == nil ||
		*hpa.Spec.Metrics[0].Resource.Target.AverageUtilization != 70 {
		t.Fatalf("cpu metric wrong: %+v", hpa.Spec.Metrics)
	}
}

func TestCreateDeploymentSpec_IncludesHPAAndMinReplicas(t *testing.T) {
	graph := &ExecutionGraph{
		ExecutionOrder: []string{"c1"},
		TenantID:       "tenant-1",
		ConnectionID:   "conn-1",
		ConsumerNodeID: "c1",
	}
	node := createNode("c1", "consumer", map[string]interface{}{
		"type":    "kafka",
		"scaling": map[string]interface{}{"min_replicas": 2, "max_replicas": 6},
	})
	spec, err := CreateDeploymentSpec(node, graph, DefaultConfig())
	if err != nil {
		t.Fatalf("CreateDeploymentSpec: %v", err)
	}
	if spec.Deployment.Spec.Replicas == nil || *spec.Deployment.Spec.Replicas != 2 {
		t.Fatalf("deployment should start at min replicas 2, got %v", spec.Deployment.Spec.Replicas)
	}
	if spec.HPA == nil {
		t.Fatal("expected an HPA on the deployment spec")
	}
	if spec.HPA.Spec.MaxReplicas != 6 || spec.HPA.Name != spec.Deployment.Name {
		t.Fatalf("HPA wrong: name=%s max=%d (deployment=%s)", spec.HPA.Name, spec.HPA.Spec.MaxReplicas, spec.Deployment.Name)
	}
}
