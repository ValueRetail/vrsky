package managementapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAlertmanagerConfigTargetsRealRoute: Alertmanager's only receiver is the
// management-api webhook. The URL lives in a Helm values file the Go code
// never reads, so a renamed route, Service or port would silently stop every
// alert. Pin the three to what handler.go and the Service actually expose.
func TestAlertmanagerConfigTargetsRealRoute(t *testing.T) {
	root := filepath.Join("..", "..", "..", "infrastructure", "kubernetes")
	body, err := os.ReadFile(filepath.Join(root, "monitoring", "prometheus-values.azure.yaml"))
	if err != nil {
		t.Skipf("monitoring values not available (%v) — guard skipped", err)
	}
	var values struct {
		Alertmanager struct {
			Spec struct {
				Secrets []string `yaml:"secrets"`
			} `yaml:"alertmanagerSpec"`
			Config struct {
				Route struct {
					Receiver string   `yaml:"receiver"`
					GroupBy  []string `yaml:"group_by"`
				} `yaml:"route"`
				Receivers []struct {
					Name     string `yaml:"name"`
					Webhooks []struct {
						URL        string `yaml:"url"`
						HTTPConfig struct {
							Authorization struct {
								Type            string `yaml:"type"`
								CredentialsFile string `yaml:"credentials_file"`
							} `yaml:"authorization"`
						} `yaml:"http_config"`
					} `yaml:"webhook_configs"`
				} `yaml:"receivers"`
			} `yaml:"config"`
		} `yaml:"alertmanager"`
	}
	if err := yaml.Unmarshal(body, &values); err != nil {
		t.Fatalf("parse values: %v", err)
	}

	handlerSrc, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatalf("read handler.go: %v", err)
	}
	const route = "/api/v1/alerts/webhook"
	if !strings.Contains(string(handlerSrc), `"POST `+route+`"`) {
		t.Fatalf("handler.go no longer registers POST %s; update the Alertmanager receiver and this test", route)
	}
	svc, err := os.ReadFile(filepath.Join(root, "management-api", "service.yaml"))
	if err != nil {
		t.Fatalf("read service.yaml: %v", err)
	}
	if !strings.Contains(string(svc), "name: vrsky-management-api") || !strings.Contains(string(svc), "port: 8080") {
		t.Fatalf("management-api Service is no longer vrsky-management-api:8080; update the receiver URL")
	}

	var webhooks int
	for _, r := range values.Alertmanager.Config.Receivers {
		for _, w := range r.Webhooks {
			webhooks++
			want := "http://vrsky-management-api.vrsky-platform.svc.cluster.local:8080" + route
			if w.URL != want {
				t.Errorf("receiver %q url = %q, want %q", r.Name, w.URL, want)
			}
			if w.HTTPConfig.Authorization.Type != "Bearer" {
				t.Errorf("receiver %q must authenticate with a Bearer token (AlertsWebhook checks Authorization: Bearer)", r.Name)
			}
			var mounted bool
			for _, s := range values.Alertmanager.Spec.Secrets {
				if strings.Contains(w.HTTPConfig.Authorization.CredentialsFile, "/secrets/"+s+"/") {
					mounted = true
				}
			}
			if !mounted {
				t.Errorf("credentials_file %q does not point into a Secret listed in alertmanagerSpec.secrets %v", w.HTTPConfig.Authorization.CredentialsFile, values.Alertmanager.Spec.Secrets)
			}
		}
	}
	if webhooks != 1 {
		t.Errorf("want exactly one webhook receiver (the management-api fans out), got %d", webhooks)
	}
	// Routing per workspace depends on grouping by tenant_id; without it one
	// tenant's alert could be batched with another's into a single delivery.
	if !hasString(values.Alertmanager.Config.Route.GroupBy, "tenant_id") {
		t.Errorf("route.group_by %v must include tenant_id", values.Alertmanager.Config.Route.GroupBy)
	}
}

func hasString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
