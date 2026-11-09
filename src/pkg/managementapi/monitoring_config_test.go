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

// TestCertManagerPodMonitor: the Postgres certificate alerts (#729) read a
// metric only the cert-manager PodMonitor brings in. Pin the PodMonitor to
// the controller pod as the pinned chart renders it, with honorLabels so the
// metric keeps the Certificate's namespace.
func TestCertManagerPodMonitor(t *testing.T) {
	docs := readYAMLDocs(t, filepath.Join(k8sRoot(), "monitoring", "podmonitors.yaml"))
	pm := findDoc(t, docs, "PodMonitor", "cert-manager")
	if got := str(at(pm, "metadata", "namespace")); got != "vrsky-monitoring" {
		t.Errorf("PodMonitor cert-manager is in %q, want vrsky-monitoring beside the others", got)
	}

	// The namespace, labels and port come from the cert-manager chart that
	// install-cert-manager.sh installs. Another chart version may name them
	// differently; then check the chart and update both.
	installRaw, err := os.ReadFile(filepath.Join(k8sRoot(), "ingress", "install-cert-manager.sh"))
	if err != nil {
		t.Fatalf("read install-cert-manager.sh: %v", err)
	}
	install := string(installRaw)
	if !strings.Contains(install, "--version v1.13.3") {
		t.Error("install-cert-manager.sh no longer pins cert-manager v1.13.3: check that chart's controller pod labels and metrics port name against PodMonitor cert-manager, then update this test")
	}
	if !strings.Contains(install, "--namespace cert-manager") {
		t.Error("install-cert-manager.sh no longer installs into namespace cert-manager, which PodMonitor cert-manager watches")
	}

	var nsOK bool
	for _, n := range list(at(pm, "spec", "namespaceSelector", "matchNames")) {
		nsOK = nsOK || str(n) == "cert-manager"
	}
	if !nsOK {
		t.Errorf("PodMonitor cert-manager does not select namespace cert-manager: %v", at(pm, "spec", "namespaceSelector"))
	}
	for k, want := range map[string]string{
		"app.kubernetes.io/name":      "cert-manager",
		"app.kubernetes.io/component": "controller",
	} {
		if got := str(at(pm, "spec", "selector", "matchLabels", k)); got != want {
			t.Errorf("PodMonitor cert-manager selects %s=%q, want %q (the controller pod; cainjector and webhook have no certificate metrics)", k, got, want)
		}
	}
	eps := list(at(pm, "spec", "podMetricsEndpoints"))
	if len(eps) != 1 {
		t.Fatalf("PodMonitor cert-manager has %d endpoints, want 1", len(eps))
	}
	if got := str(at(eps[0], "port")); got != "http-metrics" {
		t.Errorf("endpoint port %q, want http-metrics (containerPort 9402 in the chart)", got)
	}
	// Without honorLabels Prometheus moves the metric's namespace label to
	// exported_namespace and sets namespace=cert-manager; the rules match on
	// namespace="vrsky-database" and would never see a sample.
	if at(eps[0], "honorLabels") != true {
		t.Error("endpoint lacks honorLabels: true; the certificate's namespace would become exported_namespace")
	}
}

// TestPostgresCertAlertsMatchTheCertificate: the rules name the Certificate
// and namespace by string. Pin them to tls.yaml, so a rename of either does
// not leave both alerts watching nothing (the absent alert would fire, but an
// hour late and only in production).
func TestPostgresCertAlertsMatchTheCertificate(t *testing.T) {
	tls := readYAMLDocs(t, filepath.Join(k8sRoot(), "postgresql", "tls.yaml"))
	cert := findDoc(t, tls, "Certificate", pgTLSSecret)
	matcher := `certmanager_certificate_expiration_timestamp_seconds{namespace="` +
		str(at(cert, "metadata", "namespace")) + `", name="` + str(at(cert, "metadata", "name")) + `"}`

	rules := readYAMLDocs(t, filepath.Join("..", "..", "..", "infrastructure", "prometheus-rules.yml"))
	if len(rules) != 1 {
		t.Fatalf("prometheus-rules.yml has %d documents, want 1", len(rules))
	}
	severities := map[string][]string{}
	for _, g := range list(rules[0]["groups"]) {
		for _, r := range list(at(g, "rules")) {
			name := str(at(r, "alert"))
			if name != "PostgresCertExpiring" && name != "PostgresCertMetricsAbsent" {
				continue
			}
			if !strings.HasPrefix(str(at(g, "name")), "vrsky-") {
				t.Errorf("%s is in group %q; the platform alerts live in vrsky-* groups", name, str(at(g, "name")))
			}
			if !strings.Contains(str(at(r, "expr")), matcher) {
				t.Errorf("%s expr does not select %s:\n%s", name, matcher, str(at(r, "expr")))
			}
			severities[name] = append(severities[name], str(at(r, "labels", "severity")))
		}
	}
	if got := strings.Join(severities["PostgresCertExpiring"], ","); got != "warning,critical" {
		t.Errorf("PostgresCertExpiring severities = %q, want warning,critical", got)
	}
	if got := strings.Join(severities["PostgresCertMetricsAbsent"], ","); got != "warning" {
		t.Errorf("PostgresCertMetricsAbsent severities = %q, want warning", got)
	}
}
