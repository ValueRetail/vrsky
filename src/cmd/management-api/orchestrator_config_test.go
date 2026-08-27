package main

import (
	"testing"

	"github.com/ValueRetail/vrsky/pkg/orchestrator"
)

// TestOrchestratorConfigFromEnv_Defaults verifies that with no overrides the
// config matches orchestrator.DefaultConfig, except the NATS URL which defaults
// to the management-api's own (the in-cluster platform NATS).
func TestOrchestratorConfigFromEnv_Defaults(t *testing.T) {
	for _, k := range []string{"WORKER_NATS_URL", "ORCHESTRATOR_NAMESPACE", "NATS_ACCOUNT"} {
		t.Setenv(k, "")
	}
	def := orchestrator.DefaultConfig()

	got := orchestratorConfigFromEnv(&Config{NATSUrl: "nats://platform:4222"})

	if got.NATSURLs != "nats://platform:4222" {
		t.Errorf("NATSURLs = %q, want the management-api NATS URL %q", got.NATSURLs, "nats://platform:4222")
	}
	if got.Namespace != def.Namespace {
		t.Errorf("Namespace = %q, want default %q", got.Namespace, def.Namespace)
	}
	if got.NATSAccount != def.NATSAccount {
		t.Errorf("NATSAccount = %q, want default %q", got.NATSAccount, def.NATSAccount)
	}
}

// TestOrchestratorConfigFromEnv_Overrides verifies every env override is applied
// and that WORKER_NATS_URL wins over the management-api NATS URL.
func TestOrchestratorConfigFromEnv_Overrides(t *testing.T) {
	t.Setenv("WORKER_NATS_URL", "nats://workers:4222")
	t.Setenv("ORCHESTRATOR_NAMESPACE", "vrsky-platform")
	t.Setenv("NATS_ACCOUNT", "TENANT_A")

	got := orchestratorConfigFromEnv(&Config{NATSUrl: "nats://platform:4222"})

	if got.NATSURLs != "nats://workers:4222" {
		t.Errorf("NATSURLs = %q, want WORKER_NATS_URL override", got.NATSURLs)
	}
	if got.Namespace != "vrsky-platform" {
		t.Errorf("Namespace = %q, want vrsky-platform", got.Namespace)
	}
	if got.NATSAccount != "TENANT_A" {
		t.Errorf("NATSAccount = %q, want TENANT_A", got.NATSAccount)
	}
}

// TestOrchestratorConfigFromEnv_NilConfig ensures a nil *Config doesn't panic
// and leaves the default NATS URL in place.
func TestOrchestratorConfigFromEnv_NilConfig(t *testing.T) {
	for _, k := range []string{"WORKER_NATS_URL", "ORCHESTRATOR_NAMESPACE", "NATS_ACCOUNT"} {
		t.Setenv(k, "")
	}
	got := orchestratorConfigFromEnv(nil)
	if got.NATSURLs != orchestrator.DefaultConfig().NATSURLs {
		t.Errorf("NATSURLs = %q, want default when config is nil", got.NATSURLs)
	}
}
