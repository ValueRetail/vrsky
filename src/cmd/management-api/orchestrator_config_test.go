package main

import (
	"testing"

	"github.com/ValueRetail/vrsky/pkg/orchestrator"
)

// What this used to check, and why it does not.
//
// orchestratorConfigFromEnv once assembled a NATS URL and account alongside the
// namespace, from WORKER_NATS_URL / NATS_ACCOUNT and the management-api's own
// config. ADR 0005 removed both: they were stamped onto per-connection worker
// pods, which ADR 0004 stopped deploying, so nothing read either one — an
// operator could set them and change nothing.
//
// Namespace is the only field left, and it is genuinely read: the
// orphaned-worker sweep lists Deployments and HPAs in it. If a field is ever
// added back here, it needs a consumer before it needs a test.

// The default namespace, with no override.
func TestOrchestratorConfigFromEnv_Default(t *testing.T) {
	t.Setenv("ORCHESTRATOR_NAMESPACE", "")

	got := orchestratorConfigFromEnv()

	if want := orchestrator.DefaultConfig().Namespace; got.Namespace != want {
		t.Errorf("Namespace = %q, want default %q", got.Namespace, want)
	}
}

// ORCHESTRATOR_NAMESPACE overrides it — the one env var here that still does
// something.
func TestOrchestratorConfigFromEnv_NamespaceOverride(t *testing.T) {
	t.Setenv("ORCHESTRATOR_NAMESPACE", "vrsky-platform")

	got := orchestratorConfigFromEnv()

	if got.Namespace != "vrsky-platform" {
		t.Errorf("Namespace = %q, want vrsky-platform", got.Namespace)
	}
}

// The env vars ADR 0005 removed must not quietly come back. Setting them
// changes nothing; a future edit that made either take effect again would be
// reintroducing config with no consumer, which is what this cleanup was.
func TestOrchestratorConfigFromEnv_RemovedVarsHaveNoEffect(t *testing.T) {
	t.Setenv("ORCHESTRATOR_NAMESPACE", "")
	base := orchestratorConfigFromEnv()

	t.Setenv("WORKER_NATS_URL", "nats://somewhere-else:4222")
	t.Setenv("NATS_ACCOUNT", "TENANT_A")

	got := orchestratorConfigFromEnv()

	if *got != *base {
		t.Errorf("WORKER_NATS_URL/NATS_ACCOUNT changed the orchestrator config (%+v vs %+v) — "+
			"they were removed in ADR 0005 because nothing consumed them", *got, *base)
	}
}
