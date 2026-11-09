package managementapi

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Guards for inbound webhook mTLS at the edge (#454,
// plans/issue-454-webhook-mtls-edge.md).
//
// The webhook consumer's port 9102 trusts the ssl-client-cert header. That is
// safe only while (1) the one Ingress that routes to it checks client
// certificates, so nginx sets the header itself and replaces a caller's copy;
// (2) nothing else routes to it, because on a host without auth-tls nginx
// passes a caller's header straight through; and (3) the hop is TLS with the
// ingress's own client certificate, so nothing else in the cluster can set
// the header. Each of those lives in a YAML file a later edit could quietly
// change; these tests pin them.

const (
	edgePort        = 9102
	edgeIngressFile = "webhooks-mtls-ingress.yaml"
	edgeServiceName = "vrsky-webhook-consumer"
	edgeProxySSL    = "vrsky-webhook-consumer.vrsky-platform.svc"
)

// edgeDocs decodes every YAML document in a file.
func edgeDocs(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return edgeDocsFrom(t, path, b)
}

func edgeDocsFrom(t *testing.T, name string, b []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	for {
		var d map[string]any
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if d != nil {
			out = append(out, d)
		}
	}
	return out
}

// dig walks nested maps and lists: string keys index maps, ints index lists.
func edgeDig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			l, ok := v.([]any)
			if !ok || k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func edgeList(v any) []any { l, _ := v.([]any); return l }

// edgeIngressRoutes is every (host, path, service, port) an Ingress document routes.
type edgeRoute struct {
	host, path, service string
	port                int
}

// A spec.defaultBackend counts too, as host "" and path "(default)": it takes
// every request no rule matches.
func edgeIngressRoutes(doc map[string]any) []edgeRoute {
	var out []edgeRoute
	if db := edgeDig(doc, "spec", "defaultBackend"); db != nil {
		svc, _ := edgeDig(db, "service", "name").(string)
		port, _ := edgeDig(db, "service", "port", "number").(int)
		out = append(out, edgeRoute{"", "(default)", svc, port})
	}
	for _, rule := range edgeList(edgeDig(doc, "spec", "rules")) {
		host, _ := edgeDig(rule, "host").(string)
		for _, p := range edgeList(edgeDig(rule, "http", "paths")) {
			path, _ := edgeDig(p, "path").(string)
			svc, _ := edgeDig(p, "backend", "service", "name").(string)
			port, _ := edgeDig(p, "backend", "service", "port", "number").(int)
			out = append(out, edgeRoute{host, path, svc, port})
		}
	}
	return out
}

// edgeAllIngresses returns every Ingress document under infrastructure/kubernetes,
// keyed by "file#name".
func edgeAllIngresses(t *testing.T) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	err := filepath.WalkDir(k8sRoot(), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(b), "kind: Ingress") {
			return nil
		}
		for _, doc := range edgeDocsFrom(t, path, b) {
			if doc["kind"] == "Ingress" {
				name, _ := edgeDig(doc, "metadata", "name").(string)
				out[path+"#"+name] = doc
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", k8sRoot(), err)
	}
	return out
}

func edgeIngress(t *testing.T) map[string]any {
	t.Helper()
	docs := edgeDocs(t, filepath.Join(k8sRoot(), "ingress", edgeIngressFile))
	if len(docs) != 1 || docs[0]["kind"] != "Ingress" {
		t.Fatalf("%s: want exactly one Ingress document, got %d", edgeIngressFile, len(docs))
	}
	return docs[0]
}

// TestWebhookMTLSIngressChecksClientCerts pins the annotations that make
// nginx check the caller's certificate and set ssl-client-cert itself, and
// the ones that make the hop to the worker mutual TLS.
func TestWebhookMTLSIngressChecksClientCerts(t *testing.T) {
	ing := edgeIngress(t)
	ann, _ := edgeDig(ing, "metadata", "annotations").(map[string]any)
	want := map[string]string{
		"nginx.ingress.kubernetes.io/auth-tls-secret":                       "vrsky-platform/webhook-client-ca",
		"nginx.ingress.kubernetes.io/auth-tls-verify-client":                "on",
		"nginx.ingress.kubernetes.io/auth-tls-pass-certificate-to-upstream": "true",
		"nginx.ingress.kubernetes.io/backend-protocol":                      "HTTPS",
		"nginx.ingress.kubernetes.io/proxy-ssl-secret":                      "vrsky-platform/webhook-edge-client",
		"nginx.ingress.kubernetes.io/proxy-ssl-verify":                      "on",
		"nginx.ingress.kubernetes.io/proxy-ssl-name":                        edgeProxySSL,
	}
	for k, v := range want {
		got, ok := ann[k].(string)
		if !ok || got != v {
			t.Errorf("annotation %s = %#v, want %q", k, ann[k], v)
		}
	}
	// A snippet can rewrite or drop the headers nginx sets, or set them on a
	// path without the certificate check.
	for k := range ann {
		if strings.Contains(k, "snippet") {
			t.Errorf("annotation %s on the mTLS webhook Ingress: snippets could rewrite ssl-client-cert", k)
		}
	}
	if ns, _ := edgeDig(ing, "metadata", "namespace").(string); ns != "vrsky-platform" {
		t.Errorf("namespace = %q; the Secrets it names live in vrsky-platform", ns)
	}

	routes := edgeIngressRoutes(ing)
	if len(routes) == 0 {
		t.Fatal("the mTLS webhook Ingress routes nothing; its shape changed, update this test")
	}
	hosts := map[string]bool{}
	for _, r := range routes {
		hosts[r.host] = true
		if r.path != "/webhook" || r.service != edgeServiceName || r.port != edgePort {
			t.Errorf("route %+v: the mTLS webhook Ingress may only send /webhook to %s:%d", r, edgeServiceName, edgePort)
		}
	}
	tlsHosts := map[string]bool{}
	for _, entry := range edgeList(edgeDig(ing, "spec", "tls")) {
		for _, h := range edgeList(edgeDig(entry, "hosts")) {
			tlsHosts[h.(string)] = true
		}
	}
	for h := range hosts {
		if h == "" {
			t.Error("a rule without a host: auth-tls is per host, and this would be the default server")
		}
		if !tlsHosts[h] {
			t.Errorf("host %q has no tls: entry; client certificates are checked in the TLS handshake", h)
		}
	}
}

// TestWebhookEdgePortOnlyBehindMTLSIngress: auth-tls is per host in
// ingress-nginx, so no other Ingress may share the mTLS host, and none may
// route to the port that trusts the header. The public webhook Ingress keeps
// sending the webhook consumer to 9100.
func TestWebhookEdgePortOnlyBehindMTLSIngress(t *testing.T) {
	ingresses := edgeAllIngresses(t)
	var edgeKey string
	edgeHosts := map[string]bool{}
	for key, doc := range ingresses {
		if strings.HasSuffix(strings.SplitN(key, "#", 2)[0], edgeIngressFile) {
			edgeKey = key
			for _, r := range edgeIngressRoutes(doc) {
				edgeHosts[r.host] = true
			}
		}
	}
	if edgeKey == "" {
		t.Fatalf("no Ingress found in %s", edgeIngressFile)
	}
	publicToWebhook := 0
	for key, doc := range ingresses {
		if key == edgeKey {
			continue
		}
		for _, r := range edgeIngressRoutes(doc) {
			if edgeHosts[r.host] {
				t.Errorf("%s also routes host %q; auth-tls is per host, so it would share or skip the client-certificate check", key, r.host)
			}
			if r.service == edgeServiceName && r.port == edgePort {
				t.Errorf("%s routes %s:%d (the port that trusts ssl-client-cert) without the certificate check; a caller could set the header", key, r.service, r.port)
			}
			if r.service == edgeServiceName {
				publicToWebhook++
				if r.port != 9100 {
					t.Errorf("%s routes %s to port %d, want 9100", key, r.service, r.port)
				}
			}
		}
	}
	if publicToWebhook == 0 {
		t.Error("no Ingress routes the webhook consumer's public port 9100; the parse missed webhooks-ingress.yaml")
	}
}

// TestWebhookEdgeCertificates: the worker accepts on 9102 only a client
// certificate with the client-auth EKU from the edge CA. If the server
// certificate also had client auth, anything holding the worker's key could
// pose as the ingress; if the client certificate lost it, nginx could not
// connect.
func TestWebhookEdgeCertificates(t *testing.T) {
	certs := map[string]map[string]any{}
	issuers := map[string]map[string]any{}
	for _, doc := range edgeDocs(t, filepath.Join(k8sRoot(), "ingress", "webhook-edge-tls.yaml")) {
		name, _ := edgeDig(doc, "metadata", "name").(string)
		if ns, _ := edgeDig(doc, "metadata", "namespace").(string); ns != "vrsky-platform" {
			t.Errorf("%s %s in namespace %q, want vrsky-platform", doc["kind"], name, ns)
		}
		switch doc["kind"] {
		case "Certificate":
			certs[name] = doc
		case "Issuer":
			issuers[name] = doc
		}
	}
	usages := func(name string) map[string]bool {
		out := map[string]bool{}
		for _, u := range edgeList(edgeDig(certs[name], "spec", "usages")) {
			out[u.(string)] = true
		}
		return out
	}

	if edgeDig(certs["webhook-edge-ca"], "spec", "isCA") != true {
		t.Error("webhook-edge-ca is not a CA")
	}
	if edgeDig(issuers["webhook-edge-ca"], "spec", "ca", "secretName") != "webhook-edge-ca" {
		t.Error("Issuer webhook-edge-ca does not sign with the webhook-edge-ca Secret")
	}
	for name, secret := range map[string]string{"webhook-edge-server": "webhook-edge-server", "webhook-edge-client": "webhook-edge-client"} {
		c, ok := certs[name]
		if !ok {
			t.Errorf("Certificate %s missing", name)
			continue
		}
		if edgeDig(c, "spec", "secretName") != secret {
			t.Errorf("%s writes Secret %v, want %s", name, edgeDig(c, "spec", "secretName"), secret)
		}
		if edgeDig(c, "spec", "issuerRef", "name") != "webhook-edge-ca" {
			t.Errorf("%s is issued by %v, want the edge CA", name, edgeDig(c, "spec", "issuerRef", "name"))
		}
	}
	if u := usages("webhook-edge-server"); !u["server auth"] || u["client auth"] {
		t.Errorf("webhook-edge-server usages %v: want server auth and not client auth", u)
	}
	if u := usages("webhook-edge-client"); !u["client auth"] || u["server auth"] {
		t.Errorf("webhook-edge-client usages %v: want client auth and not server auth", u)
	}
	names := map[string]bool{}
	for _, n := range edgeList(edgeDig(certs["webhook-edge-server"], "spec", "dnsNames")) {
		names[n.(string)] = true
	}
	if !names[edgeProxySSL] {
		t.Errorf("webhook-edge-server dnsNames %v lack %s, the proxy-ssl-name nginx verifies", names, edgeProxySSL)
	}
}

// TestWebhookConsumerEdgeWiring checks what the deploy job applies: the
// webhook consumer listens on the edge port with the edge certificate
// mounted, its Service exposes the port, and production never turns on the
// direct-TLS listener (which accepts any client certificate at the handshake
// and is meant for Docker Compose).
func TestWebhookConsumerEdgeWiring(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "infrastructure", "azure", "deploy-connectors-azure.sh"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := renderConnectorManifest(t, script)

	var dep, svc map[string]any
	for _, doc := range edgeDocsFrom(t, "connectors.yaml", manifest) {
		if edgeDig(doc, "metadata", "name") != edgeServiceName {
			continue
		}
		switch doc["kind"] {
		case "Deployment":
			dep = doc
		case "Service":
			svc = doc
		}
	}
	if dep == nil || svc == nil {
		t.Fatalf("rendered manifest lacks the %s Deployment or Service", edgeServiceName)
	}

	ctr := edgeDig(dep, "spec", "template", "spec", "containers", 0)
	env := map[string]string{}
	for _, e := range edgeList(edgeDig(ctr, "env")) {
		name, _ := edgeDig(e, "name").(string)
		val, _ := edgeDig(e, "value").(string)
		env[name] = val
	}
	if env["WORKER_MTLS_EDGE_PORT"] != "9102" {
		t.Errorf("WORKER_MTLS_EDGE_PORT = %q, want 9102", env["WORKER_MTLS_EDGE_PORT"])
	}
	if _, set := env["WORKER_MTLS_PORT"]; set {
		t.Error("WORKER_MTLS_PORT is set in production; the direct-TLS listener is for Docker Compose")
	}

	mount := ""
	for _, m := range edgeList(edgeDig(ctr, "volumeMounts")) {
		if edgeDig(m, "name") == "webhook-edge" {
			mount, _ = edgeDig(m, "mountPath").(string)
		}
	}
	if mount == "" {
		t.Fatal("the webhook-edge volume is not mounted")
	}
	for _, k := range []string{"WEBHOOK_EDGE_CERT_FILE", "WEBHOOK_EDGE_KEY_FILE", "WEBHOOK_EDGE_CA_FILE"} {
		if !strings.HasPrefix(env[k], mount+"/") {
			t.Errorf("%s = %q, not under the edge Secret mount %s", k, env[k], mount)
		}
	}
	volOK := false
	for _, v := range edgeList(edgeDig(dep, "spec", "template", "spec", "volumes")) {
		if edgeDig(v, "name") == "webhook-edge" {
			volOK = edgeDig(v, "secret", "secretName") == "webhook-edge-server" && edgeDig(v, "secret", "optional") == true
		}
	}
	if !volOK {
		t.Error("the webhook-edge volume must be the optional Secret webhook-edge-server (optional, so the pod starts before it exists)")
	}

	ctrPort, svcPort := false, false
	for _, p := range edgeList(edgeDig(ctr, "ports")) {
		if edgeDig(p, "containerPort") == edgePort {
			ctrPort = true
		}
	}
	for _, p := range edgeList(edgeDig(svc, "spec", "ports")) {
		if edgeDig(p, "port") == edgePort && edgeDig(p, "targetPort") == edgePort {
			svcPort = true
		}
	}
	if !ctrPort || !svcPort {
		t.Errorf("edge port %d: container port %v, Service port %v; the mTLS Ingress would 503", edgePort, ctrPort, svcPort)
	}
}

// TestWebhookEdgeNetworkPolicy: if applied, the policy must let only
// ingress-nginx reach 9102 and leave 9100 and 8080 as open as they are
// without it.
func TestWebhookEdgeNetworkPolicy(t *testing.T) {
	docs := edgeDocs(t, filepath.Join(k8sRoot(), "ingress", "webhook-consumer-networkpolicy.yaml"))
	if len(docs) != 1 || docs[0]["kind"] != "NetworkPolicy" {
		t.Fatalf("want one NetworkPolicy, got %d documents", len(docs))
	}
	np := docs[0]
	if edgeDig(np, "spec", "podSelector", "matchLabels", "app") != edgeServiceName {
		t.Errorf("the policy selects %v, want app=%s", edgeDig(np, "spec", "podSelector"), edgeServiceName)
	}
	openPorts, edgeRules := map[int]bool{}, 0
	for _, rule := range edgeList(edgeDig(np, "spec", "ingress")) {
		from := edgeList(edgeDig(rule, "from"))
		for _, p := range edgeList(edgeDig(rule, "ports")) {
			port, _ := edgeDig(p, "port").(int)
			if port == edgePort {
				edgeRules++
				if len(from) == 0 {
					t.Error("9102 is open to every source")
				}
				for _, f := range from {
					if edgeDig(f, "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != "ingress-nginx" ||
						edgeDig(f, "podSelector", "matchLabels", "app.kubernetes.io/name") != "ingress-nginx" {
						t.Errorf("9102 allowed from %v, want only the ingress-nginx pods", f)
					}
				}
			} else if len(from) == 0 {
				openPorts[port] = true
			}
		}
	}
	if edgeRules != 1 {
		t.Errorf("%d rules mention 9102, want exactly one", edgeRules)
	}
	for _, p := range []int{9100, 8080} {
		if !openPorts[p] {
			t.Errorf("port %d is not open to all; selecting the pod would cut it off", p)
		}
	}
}

// TestWebhookClientCAExampleHasNoMaterial: the bundle Secret is an example;
// real CA certificates go into the cluster, not git.
func TestWebhookClientCAExampleHasNoMaterial(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(k8sRoot(), "ingress", "webhook-client-ca.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "-----BEGIN") {
		t.Error("webhook-client-ca.example.yaml holds PEM material; it is an example only")
	}
}
