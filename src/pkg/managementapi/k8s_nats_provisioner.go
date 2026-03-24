package managementapi

import (
	"context"
	"fmt"
	"log"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

const (
	tenantNATSNamespace = "vrsky-tenants"
	natsImage           = "nats:2.12-alpine"
	provisionTimeout    = 120 * time.Second
	pollInterval        = 3 * time.Second
)

// K8sNATSProvisioner creates and deletes per-tenant NATS instances in Kubernetes.
// Translates the shell scripts in infrastructure/kubernetes/tenant-nats/ to Go.
type K8sNATSProvisioner struct {
	client kubernetes.Interface
	logger *log.Logger
}

// NewK8sNATSProvisioner creates a new provisioner. Returns nil if client is nil.
func NewK8sNATSProvisioner(client kubernetes.Interface, logger *log.Logger) *K8sNATSProvisioner {
	if client == nil {
		return nil
	}
	return &K8sNATSProvisioner{client: client, logger: logger}
}

// NATSUrl returns the in-cluster DNS URL for a tenant's NATS instance.
func NATSUrl(tenantSlug string) string {
	return fmt.Sprintf("nats://nats-%s-1.%s.svc.cluster.local:4222", tenantSlug, tenantNATSNamespace)
}

// natsResourceName returns the K8s resource name for a tenant's NATS instance.
func natsResourceName(tenantSlug string) string {
	return fmt.Sprintf("nats-%s-1", tenantSlug)
}

// ProvisionNATS creates a Deployment, Service, and NetworkPolicy for a tenant's NATS instance.
// onProgress is called with (percent, stepDescription) as provisioning progresses.
func (p *K8sNATSProvisioner) ProvisionNATS(ctx context.Context, tenantSlug string, onProgress func(int, string)) (string, error) {
	name := natsResourceName(tenantSlug)
	labels := map[string]string{
		"app":          "nats",
		"component":    "tenant-messaging",
		"tenant-id":    tenantSlug,
		"instance-num": "1",
	}
	selectorLabels := map[string]string{
		"app":          "nats",
		"tenant-id":    tenantSlug,
		"instance-num": "1",
	}

	// Step 1: Create Deployment
	onProgress(10, "Creating NATS deployment...")
	if err := p.createDeployment(ctx, name, labels, selectorLabels, tenantSlug); err != nil {
		return "", fmt.Errorf("create deployment: %w", err)
	}

	// Step 2: Create Service
	onProgress(30, "Creating NATS service...")
	if err := p.createService(ctx, name, labels, selectorLabels); err != nil {
		return "", fmt.Errorf("create service: %w", err)
	}

	// Step 3: Create NetworkPolicy
	onProgress(50, "Applying network isolation...")
	if err := p.createNetworkPolicy(ctx, tenantSlug, selectorLabels); err != nil {
		return "", fmt.Errorf("create network policy: %w", err)
	}

	// Step 4: Wait for deployment to become available
	onProgress(60, "Waiting for NATS pod to start...")
	if err := p.waitForDeployment(ctx, name, onProgress); err != nil {
		return "", fmt.Errorf("wait for deployment: %w", err)
	}

	onProgress(100, "NATS instance ready")
	return NATSUrl(tenantSlug), nil
}

// DeprovisionNATS deletes all K8s resources for a tenant's NATS instance.
func (p *K8sNATSProvisioner) DeprovisionNATS(ctx context.Context, tenantSlug string) error {
	name := natsResourceName(tenantSlug)
	npName := fmt.Sprintf("tenant-nats-isolation-%s", tenantSlug)

	// Delete in reverse order: NetworkPolicy, Service, Deployment
	err := p.client.NetworkingV1().NetworkPolicies(tenantNATSNamespace).Delete(ctx, npName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		p.logger.Printf("Warning: failed to delete NetworkPolicy %s: %v", npName, err)
	}

	err = p.client.CoreV1().Services(tenantNATSNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		p.logger.Printf("Warning: failed to delete Service %s: %v", name, err)
	}

	err = p.client.AppsV1().Deployments(tenantNATSNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete Deployment %s: %w", name, err)
	}

	return nil
}

func (p *K8sNATSProvisioner) createDeployment(ctx context.Context, name string, labels, selectorLabels map[string]string, tenantSlug string) error {
	replicas := int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tenantNATSNamespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "nats",
							Image: natsImage,
							Args: []string{
								"--port", "4222",
								"--http_port", "8222",
								"--server_name", name,
								"--max_payload", "8MB",
								"--max_connections", "1000",
							},
							Ports: []corev1.ContainerPort{
								{Name: "client", ContainerPort: 4222},
								{Name: "monitor", ContainerPort: 8222},
							},
							Env: []corev1.EnvVar{
								{Name: "TENANT_ID", Value: tenantSlug},
								{Name: "INSTANCE_NUM", Value: "1"},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromInt32(8222),
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       30,
								TimeoutSeconds:      5,
								FailureThreshold:    3,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromInt32(8222),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
								TimeoutSeconds:      3,
							},
							Lifecycle: &corev1.Lifecycle{
								PreStop: &corev1.LifecycleHandler{
									Exec: &corev1.ExecAction{
										Command: []string{"/bin/sh", "-c", "nats-server --signal quit"},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	_, err := p.client.AppsV1().Deployments(tenantNATSNamespace).Create(ctx, deployment, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		_, err = p.client.AppsV1().Deployments(tenantNATSNamespace).Update(ctx, deployment, metav1.UpdateOptions{})
	}
	return err
}

func (p *K8sNATSProvisioner) createService(ctx context.Context, name string, labels, selectorLabels map[string]string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tenantNATSNamespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: selectorLabels,
			Type:     corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{Name: "client", Port: 4222, TargetPort: intstr.FromInt32(4222), Protocol: corev1.ProtocolTCP},
				{Name: "monitor", Port: 8222, TargetPort: intstr.FromInt32(8222), Protocol: corev1.ProtocolTCP},
			},
		},
	}

	_, err := p.client.CoreV1().Services(tenantNATSNamespace).Create(ctx, svc, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil // Service already exists, that's fine
	}
	return err
}

func (p *K8sNATSProvisioner) createNetworkPolicy(ctx context.Context, tenantSlug string, podSelector map[string]string) error {
	npName := fmt.Sprintf("tenant-nats-isolation-%s", tenantSlug)
	tcpProto := corev1.ProtocolTCP
	udpProto := corev1.ProtocolUDP
	port4222 := intstr.FromInt32(4222)
	port8222 := intstr.FromInt32(8222)
	port53 := intstr.FromInt32(53)

	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      npName,
			Namespace: tenantNATSNamespace,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":       "nats",
					"tenant-id": tenantSlug,
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"tenant-id": tenantSlug},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcpProto, Port: &port4222},
					},
				},
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"name": "vrsky-monitoring"},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcpProto, Port: &port8222},
					},
				},
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				// Allow DNS queries to kube-system
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"name": "kube-system"},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &udpProto, Port: &port53},
					},
				},
				// Allow communication between NATS pods within the same tenant namespace
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"tenant-id": tenantSlug},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcpProto, Port: &port4222},
						{Protocol: &tcpProto, Port: &port8222},
					},
				},
			},
		},
	}

	_, err := p.client.NetworkingV1().NetworkPolicies(tenantNATSNamespace).Create(ctx, np, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (p *K8sNATSProvisioner) waitForDeployment(ctx context.Context, name string, onProgress func(int, string)) error {
	deadline := time.Now().Add(provisionTimeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for deployment %s to become ready", name)
		}

		deploy, err := p.client.AppsV1().Deployments(tenantNATSNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get deployment status: %w", err)
		}

		for _, cond := range deploy.Status.Conditions {
			if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
				return nil
			}
		}

		// Calculate progress between 60-95%
		elapsed := time.Since(deadline.Add(-provisionTimeout))
		pct := 60 + int(float64(elapsed)/float64(provisionTimeout)*35)
		if pct > 95 {
			pct = 95
		}
		onProgress(pct, "Waiting for NATS pod to become ready...")

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
