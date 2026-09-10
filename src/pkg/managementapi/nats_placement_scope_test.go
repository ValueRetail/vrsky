package managementapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ADR 0005 narrowed #19: tenant NATS placement is accounting, not routing.
//
// Both guards below exist because the failure they prevent is silent. A tenant
// instance provisions cleanly, passes its health probe, and appears in the API
// — while carrying no traffic, because it runs core NATS and the data plane is
// JetStream. Nothing errors. Nothing alerts. It simply is not what it looks
// like, which is exactly what #209 was filed about.

// Provisioning is refused by default, and the refusal explains itself.
func TestProvisionNATSInstance_RefusedWhileNotRoutable(t *testing.T) {
	t.Setenv(allowTenantNATSEnv, "") // explicit: no opt-in
	p := &K8sNATSProvisioner{client: fake.NewSimpleClientset()}

	_, err := p.ProvisionNATSInstance(context.Background(), "acme", 1, func(int, string) {})
	if err == nil {
		t.Fatal("provisioning succeeded; it must refuse while the data plane cannot use the instance (ADR 0005)")
	}
	if !errors.Is(err, ErrTenantNATSNotRoutable) {
		t.Fatalf("got %v, want ErrTenantNATSNotRoutable", err)
	}
	// The message has to carry the way out, or the next person just deletes the guard.
	for _, want := range []string{"JetStream", allowTenantNATSEnv, "ADR 0005"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}

	// Nothing was created.
	deps, _ := p.client.AppsV1().Deployments(tenantNATSNamespace).List(context.Background(), metav1.ListOptions{})
	if len(deps.Items) != 0 {
		t.Errorf("refused provisioning still created %d Deployment(s)", len(deps.Items))
	}
}

// The opt-in works, so the guard blocks a mistake rather than the work that
// would make tenant instances routable.
func TestProvisionNATSInstance_OptInProceeds(t *testing.T) {
	t.Setenv(allowTenantNATSEnv, "1")
	p := &K8sNATSProvisioner{client: fake.NewSimpleClientset()}

	// It cannot complete against a fake client — nothing ever marks the
	// Deployment Available, so waitForDeployment would spin until the
	// provisioning timeout. A short-lived context cuts that off: the creates
	// happen immediately, then the readiness Get fails on the cancelled
	// context. What matters is that it failed for THAT reason and not the
	// guard, and that the Deployment really was created.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err := p.ProvisionNATSInstance(ctx, "acme", 1, func(int, string) {})
	if errors.Is(err, ErrTenantNATSNotRoutable) {
		t.Fatal("opt-in did not take effect; the guard is unconditional")
	}
	deps, _ := p.client.AppsV1().Deployments(tenantNATSNamespace).List(context.Background(), metav1.ListOptions{})
	if len(deps.Items) == 0 {
		t.Error("opt-in path created no Deployment")
	}
}

// The discovery payload names where data actually flows.
//
// `urls` reads like something to dial. Without data_plane beside it, a caller
// has nothing in the response telling them a placed connection's data does not
// go there — which is how #209's misreading happens in the first place.
func TestNATSInstancesResponse_NamesTheDataPlane(t *testing.T) {
	h := &Handler{} // no repo → the no-discovery-backend branch
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/t1/nats-instances", nil)
	req.SetPathValue("tenant_id", "t1")

	h.HandleListNATSInstances(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Data struct {
			DataPlane string `json:"data_plane"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.DataPlane != dataPlanePlatformNATS {
		t.Errorf("data_plane = %q, want %q — the response must say where tenant data actually flows, "+
			"or `urls` reads as routing (ADR 0005)", body.Data.DataPlane, dataPlanePlatformNATS)
	}
}
