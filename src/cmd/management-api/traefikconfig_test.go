package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMiddlewareForPlan(t *testing.T) {
	cases := map[string]string{
		"free":       "rl-free",
		"pro":        "rl-pro",
		"enterprise": "rl-enterprise",
		"":           "rl-free", // unknown → free
		"bogus":      "rl-free",
	}
	for plan, want := range cases {
		if got := middlewareForPlan(plan); got != want {
			t.Errorf("middlewareForPlan(%q) = %q, want %q", plan, got, want)
		}
	}
}

func TestRenderTenantGatewayConfig(t *testing.T) {
	out := renderTenantGatewayConfig(map[string]string{
		"tenant-b": "pro",
		"tenant-a": "enterprise",
	})

	for _, want := range []string{
		"tenant-tenant-a:",
		"tenant-tenant-b:",
		"- rl-enterprise",
		"- rl-pro",
		"Header(`X-Tenant-ID`, `tenant-a`)",
		"service: management-api",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, out)
		}
	}
	// Deterministic ordering: tenant-a's router precedes tenant-b's.
	if strings.Index(out, "tenant-tenant-a:") > strings.Index(out, "tenant-tenant-b:") {
		t.Error("routers not sorted deterministically")
	}
}

func TestRenderTenantGatewayConfigEmpty(t *testing.T) {
	out := renderTenantGatewayConfig(map[string]string{})
	if !strings.Contains(out, "routers: {}") {
		t.Errorf("empty config should yield 'routers: {}', got:\n%s", out)
	}
}

func TestWriteTenantGatewayConfig(t *testing.T) {
	// Empty dir is a no-op (non-gateway deployments).
	if err := writeTenantGatewayConfig("", map[string]string{"x": "pro"}); err != nil {
		t.Fatalf("empty dir should be a no-op, got %v", err)
	}

	dir := t.TempDir()
	if err := writeTenantGatewayConfig(dir, map[string]string{"x": "pro"}); err != nil {
		t.Fatalf("writeTenantGatewayConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tenants.yml"))
	if err != nil {
		t.Fatalf("read tenants.yml: %v", err)
	}
	if !strings.Contains(string(data), "rl-pro") {
		t.Errorf("tenants.yml missing rendered content:\n%s", data)
	}
	// No leftover temp files from the atomic write.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tenants-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}
