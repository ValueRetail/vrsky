package managementapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func edgeNode(id, nodeType, config string) *Node {
	return &Node{ID: id, Type: nodeType, Config: json.RawMessage(config), Enabled: true}
}

// pipeline builds a minimal consumer→producer connection from two node configs.
func pipeline(consumerCfg, producerCfg string) *Connection {
	return &Connection{
		Name: "test",
		Nodes: []*Node{
			edgeNode("c1", "consumer", consumerCfg),
			edgeNode("p1", "producer", producerCfg),
		},
		Edges: []*Edge{{ID: "e1", Source: "c1", Target: "p1"}},
	}
}

const (
	okFileSource = `{"type":"file","file":{"path":"/data/input"}}`
	okHTTPDest   = `{"type":"http","http":{"url":"https://example.test/hook"}}`
)

func TestValidateNodeConfigs_AcceptsAFullyConfiguredPipeline(t *testing.T) {
	v := NewValidator()
	if err := v.ValidateNodeConfigs(pipeline(okFileSource, okHTTPDest)); err != nil {
		t.Fatalf("expected a fully configured pipeline to pass, got: %v", err)
	}
}

// The headline case: http-producer skips a node whose URL is empty, so the
// connection would deploy, report running, and deliver nothing.
func TestValidateNodeConfigs_RejectsSilentlySkippedNode(t *testing.T) {
	v := NewValidator()
	err := v.ValidateNodeConfigs(pipeline(okFileSource, `{"type":"http","http":{"url":""}}`))
	if err == nil {
		t.Fatal("expected an error for an HTTP destination with no URL")
	}
	if !strings.Contains(err.Error(), "http.url") {
		t.Errorf("error should name the missing field, got: %v", err)
	}
	if !strings.Contains(err.Error(), "http-producer") {
		t.Errorf("error should name the service that would skip the node, got: %v", err)
	}
}

func TestValidateNodeConfigs_RejectsUnknownConnectorKind(t *testing.T) {
	v := NewValidator()
	err := v.ValidateNodeConfigs(pipeline(okFileSource, `{"type":"carrier-pigeon"}`))
	if err == nil {
		t.Fatal("expected an error for a destination type no service serves")
	}
	// The message must list the valid choices — this is the #205 failure mode
	// and the user needs to know what they can pick instead.
	if !strings.Contains(err.Error(), "kafka") {
		t.Errorf("error should list valid destination types, got: %v", err)
	}
}

func TestValidateNodeConfigs_RejectsUnconfiguredNode(t *testing.T) {
	v := NewValidator()
	for _, cfg := range []string{``, `{}`, `{"type":""}`} {
		if err := v.ValidateNodeConfigs(pipeline(cfg, okHTTPDest)); err == nil {
			t.Errorf("expected an error for source config %q", cfg)
		}
	}
}

// Every problem is reported at once, so a user fixes the pipeline in one pass
// rather than discovering nodes one failed attempt at a time.
func TestValidateNodeConfigs_ReportsEveryProblem(t *testing.T) {
	v := NewValidator()
	err := v.ValidateNodeConfigs(pipeline(`{"type":"file","file":{}}`, `{"type":"kafka"}`))
	dagErr, ok := err.(*DAGValidationError)
	if !ok {
		t.Fatalf("expected *DAGValidationError, got %T: %v", err, err)
	}
	if len(dagErr.Errors) != 2 {
		t.Fatalf("expected both nodes reported, got %d: %v", len(dagErr.Errors), dagErr.Errors)
	}
}

// A block that is present but empty is "not set" as far as the connector is
// concerned — it checks for a non-nil block, and `{}` unmarshals to non-nil.
// Treating it as configured would let exactly the silent skip through.
func TestValidateNodeConfigs_EmptyBlockIsNotConfigured(t *testing.T) {
	v := NewValidator()
	if err := v.ValidateNodeConfigs(pipeline(okFileSource, `{"type":"kafka","kafka":{}}`)); err == nil {
		t.Fatal("expected an empty kafka block to be rejected")
	}
}

// XML has no inherent record shape, so pkg/records refuses to parse it without
// a record path (ADR 0003) — every message would error rather than flow.
func TestValidateNodeConfigs_XMLTransformNeedsRecordPath(t *testing.T) {
	v := NewValidator()
	conn := pipeline(okFileSource, okHTTPDest)
	conn.Nodes = append(conn.Nodes, edgeNode("cv1", "converter", `{"input_format":"xml"}`))

	err := v.ValidateNodeConfigs(conn)
	if err == nil {
		t.Fatal("expected XML input without a record path to be rejected")
	}
	if !strings.Contains(err.Error(), "input_xml_record_path") {
		t.Errorf("error should name the missing setting, got: %v", err)
	}

	conn.Nodes[2] = edgeNode("cv1", "converter", `{"input_format":"xml","input_xml_record_path":"Orders.Order"}`)
	if err := v.ValidateNodeConfigs(conn); err != nil {
		t.Errorf("expected XML with a record path to pass, got: %v", err)
	}
}

// Other input formats infer their own record shape, so they need no extra
// settings — only XML is special.
func TestValidateNodeConfigs_NonXMLTransformsNeedNothingExtra(t *testing.T) {
	v := NewValidator()
	for _, format := range []string{"", "json", "ndjson", "csv", "tsv", "yaml"} {
		conn := pipeline(okFileSource, okHTTPDest)
		cfg, _ := json.Marshal(map[string]string{"input_format": format})
		conn.Nodes = append(conn.Nodes, edgeNode("cv1", "converter", string(cfg)))
		if err := v.ValidateNodeConfigs(conn); err != nil {
			t.Errorf("input_format %q should need no extra settings, got: %v", format, err)
		}
	}
}

// Saving an incomplete pipeline must stay possible: nodes exist on the canvas
// before their type is chosen, and ValidateDAG runs on every save.
func TestValidateDAG_StillAcceptsAnUnconfiguredPipeline(t *testing.T) {
	v := NewValidator()
	if err := v.ValidateDAG(pipeline(``, ``)); err != nil {
		t.Fatalf("a half-built pipeline must remain saveable, got: %v", err)
	}
}

// --- drift guard ------------------------------------------------------------

// The rule table has to keep up with the connector dropdowns in the UI. A type
// the UI offers with no rule is a type the platform may not be able to run —
// that was #205 — and a rule for a type the UI dropped is dead weight. Parsing
// the UI is uglier than hardcoding a list, but a hardcoded list would only be
// wrong in the same commit that made the rules wrong, which is no guard at all.
func TestNodeConfigRulesCoverUI(t *testing.T) {
	path := filepath.Join("..", "..", "..", "ui", "src", "components", "Pipeline", "PropertyEditor.tsx")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("UI source not available (%v) — drift guard skipped", err)
	}

	for _, tc := range []struct {
		nodeType string
		marker   string
	}{
		{"consumer", "Select source type..."},
		{"producer", "Select destination type..."},
	} {
		t.Run(tc.nodeType, func(t *testing.T) {
			offered := uiConnectorTypes(t, string(src), tc.marker)
			if len(offered) == 0 {
				t.Fatalf("found no options after %q — the dropdown markup changed; update this test", tc.marker)
			}

			ruled := make(map[string]bool)
			for k := range nodeConfigRules {
				if k.node == tc.nodeType {
					ruled[k.config] = true
				}
			}

			for _, typ := range offered {
				if !ruled[typ] {
					t.Errorf("UI offers %s type %q with no rule in nodeConfigRules — a pipeline using it "+
						"would start and silently do nothing (#205). Add a rule mirroring that connector's "+
						"claim condition, and make sure the service is in deploy-connectors-azure.sh.", tc.nodeType, typ)
				}
				delete(ruled, typ)
			}
			var orphaned []string
			for typ := range ruled {
				orphaned = append(orphaned, typ)
			}
			sort.Strings(orphaned)
			if len(orphaned) > 0 {
				t.Errorf("nodeConfigRules has %s rules the UI no longer offers: %v — remove them or restore the UI option",
					tc.nodeType, orphaned)
			}
		})
	}
}

// deployExceptions are connector services intentionally absent from the prod
// deploy script. A type in here still validates and still appears in the UI, so
// a user CAN build a pipeline with it that then does nothing in prod — that is
// the #205 shape, accepted knowingly and tracked, not overlooked.
//
// Empty this map as the services ship; do not add to it to silence a failure.
// It is currently empty, which is the state to keep it in: every type the UI
// offers has a service that runs it.
var deployExceptions = map[string]string{}

// The other half of the ADR 0004 invariant: "a node type with no service in that
// table is a type the platform silently cannot run — that is the failure #205
// was". TestNodeConfigRulesCoverUI proves the UI's types are known to the
// validator; this proves they are also *deployed*. Without both, a type can be
// offered, accepted, started, and served by nothing.
func TestNodeConfigRulesAreDeployed(t *testing.T) {
	path := filepath.Join("..", "..", "..", "infrastructure", "azure", "deploy-connectors-azure.sh")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("deploy script not available (%v) — deploy drift guard skipped", err)
	}

	deployed := deployedServices(t, string(src))
	if len(deployed) == 0 {
		t.Fatal("parsed no services from deploy-connectors-azure.sh — the RETAIL/GENERIC table format changed; update this test")
	}

	// Every service a validator rule promises must actually be deployed.
	seen := make(map[string]bool)
	for kind, rule := range nodeConfigRules {
		if rule.service == "" || seen[rule.service] {
			continue
		}
		seen[rule.service] = true
		if deployed[rule.service] {
			continue
		}
		if why, ok := deployExceptions[rule.service]; ok {
			t.Logf("known gap: %s (%s/%s) is not deployed — %s", rule.service, kind.node, kind.config, why)
			continue
		}
		t.Errorf("node type %s/%s validates but its service %q is not in deploy-connectors-azure.sh — "+
			"a pipeline using it would start, report running, and do nothing (#205). Add it to the "+
			"RETAIL or GENERIC table, or record it in deployExceptions with the reason.",
			kind.node, kind.config, rule.service)
	}

	// And nothing is deployed that no rule accounts for — that would be a
	// service burning resources for node types the UI cannot produce.
	for svc := range deployed {
		if !seen[svc] {
			t.Errorf("deploy-connectors-azure.sh deploys %q but no node config rule maps to it — "+
				"either add the rule (and the UI option) or drop the service", svc)
		}
	}
}

// deployedServices returns the service names in the script's RETAIL and GENERIC
// tables. Each row is "name role port"; the name is the first field.
func deployedServices(t *testing.T, src string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, table := range []string{"RETAIL=\"", "GENERIC=\""} {
		start := strings.Index(src, table)
		if start < 0 {
			t.Fatalf("table %q not found in deploy-connectors-azure.sh — format changed; update this test", table)
		}
		body := src[start+len(table):]
		end := strings.Index(body, "\"")
		if end < 0 {
			t.Fatalf("could not find the end of the %s table", table)
		}
		for _, line := range strings.Split(body[:end], "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
				continue
			}
			out[fields[0]] = true
		}
	}
	return out
}

// uiConnectorTypes extracts the `value:` strings of the options list that starts
// at marker, stopping at the end of that options array.
func uiConnectorTypes(t *testing.T, src, marker string) []string {
	t.Helper()
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatalf("marker %q not found in PropertyEditor.tsx — the dropdown changed; update this test", marker)
	}
	end := strings.Index(src[start:], "]")
	if end < 0 {
		t.Fatalf("could not find the end of the options array after %q", marker)
	}
	block := src[start : start+end]

	re := regexp.MustCompile(`value:\s*'([^']+)'`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		out = append(out, m[1])
	}
	return out
}

// The last link in the chain: a service in the deploy table needs an image in
// the registry, or its pods ImagePullBackOff. TestNodeConfigRulesAreDeployed
// pins validator → deploy script; this pins deploy script → build script.
//
// This gap was not hypothetical. SAP S/4HANA was added to the deploy table
// without being added to build-push-acr.sh, and the first deploy attempt would
// have produced two ImagePullBackOff pods — the deploy script and the validator
// agreed with each other while the registry had nothing to serve.
func TestConnectorImagesAreBuilt(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	deploySrc, err := os.ReadFile(filepath.Join(root, "infrastructure", "azure", "deploy-connectors-azure.sh"))
	if err != nil {
		t.Skipf("deploy script not available (%v) — image drift guard skipped", err)
	}
	buildSrc, err := os.ReadFile(filepath.Join(root, "infrastructure", "azure", "build-push-acr.sh"))
	if err != nil {
		t.Skipf("build script not available (%v) — image drift guard skipped", err)
	}

	deployed := deployedServices(t, string(deploySrc))
	built := builtConnectorImages(string(buildSrc))
	if len(built) == 0 {
		t.Fatal("parsed no connector images from build-push-acr.sh — build_connectors changed; update this test")
	}

	for svc := range deployed {
		if !built[svc] {
			t.Errorf("deploy-connectors-azure.sh deploys %q but build-push-acr.sh never builds it — "+
				"its pods would ImagePullBackOff. Add it to build_connectors.", svc)
		}
	}
	for img := range built {
		if !deployed[img] {
			t.Errorf("build-push-acr.sh builds %q but no service deploys it — drop it or add the service", img)
		}
	}
}

// builtConnectorImages returns the connector image names build_connectors
// produces: the retail loop expands `<vendor>-{consumer,producer}`, and the
// generic array lists full names.
func builtConnectorImages(src string) map[string]bool {
	out := map[string]bool{}

	if m := regexp.MustCompile(`for c in ([a-z0-9 \-]+); do`).FindStringSubmatch(src); m != nil {
		for _, vendor := range strings.Fields(m[1]) {
			out[vendor+"-consumer"] = true
			out[vendor+"-producer"] = true
		}
	}
	if m := regexp.MustCompile(`(?s)local generic=\((.*?)\)`).FindStringSubmatch(src); m != nil {
		for _, name := range strings.Fields(m[1]) {
			out[name] = true
		}
	}
	return out
}

// The webhook Ingress routes public traffic to connector Services by name and
// port. A typo in either is a 503 at the edge for an inbound webhook — silent
// from the platform's side, since nothing inside ever sees the request. The
// Ingress lived only in the live cluster until it was committed, so nothing had
// ever checked it against the services it targets.
func TestWebhookIngressTargetsRealServices(t *testing.T) {
	root := filepath.Join("..", "..", "..", "infrastructure", "kubernetes")
	ing, err := os.ReadFile(filepath.Join(root, "ingress", "webhooks-ingress.yaml"))
	if err != nil {
		t.Skipf("webhook ingress not available (%v) — guard skipped", err)
	}
	svc, err := os.ReadFile(filepath.Join(root, "connectors", "connectors.yaml"))
	if err != nil {
		t.Skipf("connector manifest not available (%v) — guard skipped", err)
	}

	backends := ingressBackends(string(ing))
	if len(backends) == 0 {
		t.Fatal("parsed no backends from webhooks-ingress.yaml — its shape changed; update this test")
	}

	services := connectorServicePorts(string(svc))
	for name, port := range backends {
		got, ok := services[name]
		if !ok {
			t.Errorf("webhook ingress routes to Service %q, which deploy-connectors-azure.sh does not create — "+
				"inbound webhooks to it would 503", name)
			continue
		}
		if got != port {
			t.Errorf("webhook ingress sends %q to port %s but its Service listens on %s", name, port, got)
		}
	}
}

// TestAgentIngressTargetsRealServices is the webhook guard for the remote-agent
// Ingress: its backend must be a Service the connector deploy creates, on the
// port it listens on, or every agent gets a 503 at the edge. It also pins the
// Ingress to /agent alone — the gateway's /events/ stream has no auth of its
// own and must stay internal.
func TestAgentIngressTargetsRealServices(t *testing.T) {
	root := filepath.Join("..", "..", "..", "infrastructure", "kubernetes")
	ing, err := os.ReadFile(filepath.Join(root, "ingress", "agent-ingress.yaml"))
	if err != nil {
		t.Skipf("agent ingress not available (%v) — guard skipped", err)
	}
	svc, err := os.ReadFile(filepath.Join(root, "connectors", "connectors.yaml"))
	if err != nil {
		t.Skipf("connector manifest not available (%v) — guard skipped", err)
	}
	backends := ingressBackends(string(ing))
	if len(backends) != 1 {
		t.Fatalf("agent ingress routes to %v; want exactly vrsky-remote-agent", backends)
	}
	services := connectorServicePorts(string(svc))
	for name, port := range backends {
		if got, ok := services[name]; !ok || got != port {
			t.Errorf("agent ingress sends %q to port %s, but the deployed Service listens on %q", name, port, got)
		}
	}
	paths := regexp.MustCompile(`path: (\S+)`).FindAllStringSubmatch(string(ing), -1)
	for _, p := range paths {
		if p[1] != "/agent" {
			t.Errorf("agent ingress exposes path %q; only /agent may be public", p[1])
		}
	}
}

// ingressBackends maps backend service name → port for every path in the
// webhook Ingress.
func ingressBackends(src string) map[string]string {
	re := regexp.MustCompile(`(?s)name: (vrsky-[a-z0-9-]+)\s+port:\s+number: (\d+)`)
	out := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// connectorServicePorts maps Service name → port from the generated connector
// manifest, reading only `kind: Service` documents.
func connectorServicePorts(src string) map[string]string {
	out := map[string]string{}
	for _, doc := range strings.Split(src, "\n---") {
		if !strings.Contains(doc, "kind: Service") {
			continue
		}
		name := regexp.MustCompile(`name: (vrsky-[a-z0-9-]+)`).FindStringSubmatch(doc)
		port := regexp.MustCompile(`\n    port: (\d+)`).FindStringSubmatch(doc)
		if name != nil && port != nil {
			out[name[1]] = port[1]
		}
	}
	return out
}

// The UI manifest is shared between two deploy paths that need different image
// references, and only one of them rewrites it.
//
// infrastructure/kubernetes/ui/deployment.yaml names a ghcr.io image because
// the local k3d path side-loads images under exactly that ref
// (infrastructure/scripts/k3d-load-images.sh). AKS cannot pull from ghcr.io, so
// deploy-ui-azure.sh rewrites the ref to ACR with a perl substitution — and
// that substitution carries a hardcoded copy of the manifest's image string.
//
// Edit the manifest's image line and the regex silently stops matching. The
// script still exits 0, still reports a successful rollout, and ships a
// deployment pointing at a registry the cluster cannot reach: ImagePullBackOff,
// discovered in prod. Same for the imagePullPolicy rewrite, which is what stops
// a mutable :latest tag from serving a stale cached image on AKS — the failure
// that made a green `rollout restart` redeploy the previous UI build on
// 2026-09-07.
//
// This pins manifest → deploy script, the same way TestConnectorImagesAreBuilt
// pins deploy script → build script.
func TestUIDeployRewritesMatchManifest(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	manifest, err := os.ReadFile(filepath.Join(root, "infrastructure", "kubernetes", "ui", "deployment.yaml"))
	if err != nil {
		t.Skipf("UI manifest not available (%v) — rewrite drift guard skipped", err)
	}
	script, err := os.ReadFile(filepath.Join(root, "infrastructure", "azure", "deploy-ui-azure.sh"))
	if err != nil {
		t.Skipf("deploy-ui-azure.sh not available (%v) — rewrite drift guard skipped", err)
	}

	imageLine := regexp.MustCompile(`(?m)^\s*image:\s*(\S+)\s*$`).FindStringSubmatch(string(manifest))
	if imageLine == nil {
		t.Fatal("no image: line in ui/deployment.yaml — update this test")
	}
	policyLine := regexp.MustCompile(`(?m)^\s*imagePullPolicy:\s*(\S+)\s*$`).FindStringSubmatch(string(manifest))
	if policyLine == nil {
		t.Fatal("no imagePullPolicy: line in ui/deployment.yaml — update this test")
	}

	// The script's substitution patterns, extracted rather than duplicated so
	// this fails when either side moves. Two delimiter styles are in use:
	// s{PATTERN}{...} (chosen where the pattern contains slashes) and
	// s/PATTERN/.../. The brace form must be read to its closing brace — a
	// pattern like ghcr\.io/... contains slashes, and stopping at the first one
	// silently reduces the guard to matching "ghcr\.io", which every plausible
	// edit still satisfies.
	var patterns []string
	for _, m := range regexp.MustCompile(`perl -pi -e 's\{([^}]*)\}`).FindAllStringSubmatch(string(script), -1) {
		patterns = append(patterns, m[1])
	}
	for _, m := range regexp.MustCompile(`perl -pi -e 's/((?:[^/\\]|\\.)*)/`).FindAllStringSubmatch(string(script), -1) {
		patterns = append(patterns, m[1])
	}
	if len(patterns) == 0 {
		t.Fatal("no perl substitutions found in deploy-ui-azure.sh — the rewrite mechanism changed; update this test")
	}

	var matchedImage, matchedPolicy bool
	for _, p := range patterns {
		// Perl and Go share the syntax used here. A pattern this test cannot
		// compile is not one it can reason about, so skip it rather than fail.
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		// Anchored: the substitution has to cover the whole reference, not
		// merely appear somewhere inside it.
		if loc := re.FindStringIndex(imageLine[1]); loc != nil && loc[0] == 0 && loc[1] == len(imageLine[1]) {
			matchedImage = true
		}
		if re.MatchString("          imagePullPolicy: " + policyLine[1]) {
			matchedPolicy = true
		}
	}

	if !matchedImage {
		t.Errorf("no substitution in deploy-ui-azure.sh matches the manifest image %q — "+
			"an AKS deploy would ship that ref unrewritten and the pods would ImagePullBackOff",
			imageLine[1])
	}
	if !matchedPolicy {
		t.Errorf("no substitution in deploy-ui-azure.sh rewrites imagePullPolicy %q — "+
			"on AKS a mutable tag would then serve whatever :latest a node already cached, "+
			"and a successful-looking rollout would redeploy the old build",
			policyLine[1])
	}
}

// The manifests under infrastructure/kubernetes/ that name a ghcr.io image are
// the ones the local k3d path deploys. The cluster can't pull from ghcr.io, so
// k3d-load-images.sh builds each image and imports it under a ref, and a pod
// with imagePullPolicy: IfNotPresent only finds it when the manifest names
// exactly that ref. Until #419 the script imported data-filter and
// data-converter as localhost:5000/... (the pods then tried ghcr.io and
// failed), loaded no UI image, and the UI manifest's ghcr.io/ValueRetail/...
// was no valid reference at all.
//
// This pins loader → manifest both ways: every core entry in the script names
// its manifest's image, every ghcr.io manifest has a core entry, and each such
// image is a valid (lowercase) reference that k3d will use as imported.
func TestK3dImagesMatchManifests(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	script, err := os.ReadFile(filepath.Join(root, "infrastructure", "scripts", "k3d-load-images.sh"))
	if err != nil {
		t.Skipf("k3d-load-images.sh not available (%v) — k3d image guard skipped", err)
	}

	block := regexp.MustCompile(`(?s)\nCORE=\(\n(.*?)\n\)`).FindSubmatch(script)
	if block == nil {
		t.Fatal("no CORE=( ... ) array in k3d-load-images.sh — update this test")
	}
	loaded := map[string]string{} // manifest directory → ref the script imports
	for _, m := range regexp.MustCompile(`(?m)^\s*"(\S+)\s+(\S+)"\s*$`).FindAllStringSubmatch(string(block[1]), -1) {
		loaded[m[2]] = m[1]
	}
	if len(loaded) == 0 {
		t.Fatal("no entries in k3d-load-images.sh's CORE array — update this test")
	}

	manifests, err := filepath.Glob(filepath.Join(root, "infrastructure", "kubernetes", "*", "deployment.yaml"))
	if err != nil || len(manifests) == 0 {
		t.Fatalf("no infrastructure/kubernetes/*/deployment.yaml found (%v)", err)
	}
	type container struct {
		Image           string `yaml:"image"`
		ImagePullPolicy string `yaml:"imagePullPolicy"`
	}
	type deployment struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []container `yaml:"containers"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	onK3d := map[string]container{} // manifest directory → its ghcr.io container
	for _, path := range manifests {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var d deployment
		if err := yaml.Unmarshal(raw, &d); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			if strings.HasPrefix(strings.ToLower(c.Image), "ghcr.io/") {
				onK3d[filepath.Base(filepath.Dir(path))] = c
			}
		}
	}
	if len(onK3d) == 0 {
		t.Fatal("no deployment.yaml names a ghcr.io image any more — the k3d path changed; update this test")
	}

	// Path components of an image reference are lowercase; Docker and
	// Kubernetes reject anything else (InvalidImageName).
	component := `[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*`
	validRef := regexp.MustCompile(`^[A-Za-z0-9.-]+(?::[0-9]+)?/` + component + `(?:/` + component + `)*:[\w][\w.-]{0,127}$`)

	names := make([]string, 0, len(onK3d))
	for name := range onK3d {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := onK3d[name]
		if !validRef.MatchString(c.Image) {
			t.Errorf("infrastructure/kubernetes/%s/deployment.yaml: %q is no valid image reference "+
				"(repository path must be lowercase) — k3d and AKS both refuse it", name, c.Image)
		}
		if c.ImagePullPolicy != "IfNotPresent" && c.ImagePullPolicy != "Never" {
			t.Errorf("infrastructure/kubernetes/%s/deployment.yaml: imagePullPolicy %q makes k3d pull "+
				"from ghcr.io instead of using the imported image", name, c.ImagePullPolicy)
		}
		ref, ok := loaded[name]
		switch {
		case !ok:
			t.Errorf("k3d-load-images.sh loads no image for infrastructure/kubernetes/%s "+
				"(pulls %s): add it to CORE", name, c.Image)
		case ref != c.Image:
			t.Errorf("k3d-load-images.sh imports %s as %q but infrastructure/kubernetes/%s/deployment.yaml "+
				"pulls %q — the pod would never find the imported image", name, ref, name, c.Image)
		}
	}
	for name, ref := range loaded {
		if _, ok := onK3d[name]; !ok {
			t.Errorf("k3d-load-images.sh imports %q for %s, but infrastructure/kubernetes/%s/deployment.yaml "+
				"names no ghcr.io image to match it", ref, name, name)
		}
	}
}

// deploy-core-azure.sh rolls the core images onto AKS by digest. It carries its
// own table of which four they are, and that table is only correct as long as
// build-push-acr.sh's build_core publishes exactly those images.
//
// Drift either way is silent in a way that only shows up in prod. A service in
// build_core but not the deploy table is built on every release and never
// deployed — the shape of the 2026-09-07 stale-UI incident, where an image
// existed in ACR while the cluster ran an older one. A service in the deploy
// table but not build_core asks ACR for a tag that was never pushed; the
// script's digest guard catches that one, but only at deploy time.
//
// Same relationship as TestConnectorImagesAreBuilt, for the core group.
func TestCoreServicesAreBuilt(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	deploySrc, err := os.ReadFile(filepath.Join(root, "infrastructure", "azure", "deploy-core-azure.sh"))
	if err != nil {
		t.Skipf("core deploy script not available (%v) — drift guard skipped", err)
	}
	buildSrc, err := os.ReadFile(filepath.Join(root, "infrastructure", "azure", "build-push-acr.sh"))
	if err != nil {
		t.Skipf("build script not available (%v) — drift guard skipped", err)
	}

	// The CORE table: first field of each non-empty line.
	deployed := map[string]bool{}
	if m := regexp.MustCompile(`(?s)\nCORE="\n(.*?)\n"`).FindStringSubmatch(string(deploySrc)); m != nil {
		for _, line := range strings.Split(m[1], "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				deployed[f[0]] = true
			}
		}
	}
	if len(deployed) == 0 {
		t.Fatal("parsed no services from deploy-core-azure.sh's CORE table — it changed shape; update this test")
	}

	// build_core's `build vrsky/<name>:latest ...` lines.
	built := map[string]bool{}
	if m := regexp.MustCompile(`(?s)build_core\(\) \{(.*?)\n\}`).FindStringSubmatch(string(buildSrc)); m != nil {
		for _, b := range regexp.MustCompile(`build\s+vrsky/([a-z0-9-]+):latest`).FindAllStringSubmatch(m[1], -1) {
			built[b[1]] = true
		}
	}
	if len(built) == 0 {
		t.Fatal("parsed no images from build_core in build-push-acr.sh — it changed shape; update this test")
	}

	for name := range deployed {
		if !built[name] {
			t.Errorf("deploy-core-azure.sh deploys %q, which build_core never pushes — "+
				"the deploy would fail asking ACR for a tag that does not exist", name)
		}
	}
	for name := range built {
		if !deployed[name] {
			t.Errorf("build_core pushes %q, which deploy-core-azure.sh never deploys — "+
				"it would be rebuilt on every release and never reach the cluster", name)
		}
	}
}

// A connector deploy has to actually replace the running image.
//
// deploy-connectors-azure.sh pins :latest, so after a rebuild the Deployment
// spec is byte for byte what the cluster already has: kubectl reports
// "unchanged", no ReplicaSet is created, nothing restarts, and the rollout
// status that follows returns instantly. The run looks completely successful
// and ships nothing — the same failure that redeployed a stale UI bundle on
// 2026-09-07, in the connector path.
//
// Two things have to hold together for a deploy to land, and neither is
// sufficient alone: every container pulls on start, and the script restarts
// them. This checks both, on the generated manifest rather than the template,
// so a service added to the table without the policy is caught.
func TestConnectorDeployReplacesRunningImage(t *testing.T) {
	root := filepath.Join("..", "..", "..")

	manifest, err := os.ReadFile(filepath.Join(root, "infrastructure", "kubernetes", "connectors", "connectors.yaml"))
	if err != nil {
		t.Skipf("generated connector manifest not available (%v) — deploy guard skipped", err)
	}
	script, err := os.ReadFile(filepath.Join(root, "infrastructure", "azure", "deploy-connectors-azure.sh"))
	if err != nil {
		t.Skipf("connector deploy script not available (%v) — deploy guard skipped", err)
	}

	images := regexp.MustCompile(`(?m)^\s*image:\s*\S+$`).FindAllString(string(manifest), -1)
	policies := regexp.MustCompile(`(?m)^\s*imagePullPolicy:\s*Always\s*$`).FindAllString(string(manifest), -1)
	if len(images) == 0 {
		t.Fatal("no image: lines in the generated connector manifest — regenerate it with GENERATE_ONLY=1")
	}
	if len(policies) != len(images) {
		t.Errorf("%d containers but only %d carry imagePullPolicy: Always — one of them would keep serving "+
			"whatever :latest its node already cached", len(images), len(policies))
	}

	// Kubernetes infers Always from a :latest tag, so the policy alone is not
	// what this is really about; the restart is. Without it the apply is a
	// no-op and no pod ever pulls.
	if !regexp.MustCompile(`kubectl rollout restart deploy/`).Match(script) {
		t.Error("deploy-connectors-azure.sh never restarts a deployment; `kubectl apply` of an unchanged " +
			":latest spec changes nothing, so a rebuilt image would never reach the cluster")
	}
}

// A two-replica connector must not be able to land both replicas on one node.
//
// The replica count and the PodDisruptionBudget together look like HA, and are
// not: a PDB constrains VOLUNTARY disruption — a drain, an upgrade — and does
// nothing about a node failing, which is the case the second replica exists
// for. Without anti-affinity the scheduler is free to co-locate the pair, and
// on 2026-09-09 it did: restarting every connector at once packed most of the
// new pods onto whichever node had room.
//
// The selector is checked as well as the presence, because an anti-affinity
// that names a DIFFERENT deployment is the quiet failure here — it parses,
// applies, schedules, and spreads nothing.
func TestScaledConnectorsSpreadAcrossNodes(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "infrastructure", "kubernetes", "connectors", "connectors.yaml"))
	if err != nil {
		t.Skipf("generated connector manifest not available (%v) — HA guard skipped", err)
	}

	type deployment struct {
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Replicas int `yaml:"replicas"`
			Template struct {
				Spec struct {
					Affinity struct {
						PodAntiAffinity struct {
							Preferred []struct {
								PodAffinityTerm struct {
									TopologyKey   string `yaml:"topologyKey"`
									LabelSelector struct {
										MatchExpressions []struct {
											Key    string   `yaml:"key"`
											Values []string `yaml:"values"`
										} `yaml:"matchExpressions"`
									} `yaml:"labelSelector"`
								} `yaml:"podAffinityTerm"`
							} `yaml:"preferredDuringSchedulingIgnoredDuringExecution"`
						} `yaml:"podAntiAffinity"`
					} `yaml:"affinity"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
		Kind string `yaml:"kind"`
	}

	var scaled int
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		var d deployment
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			continue // services, PDBs and the PVC parse into a zero value or fail; skip
		}
		if d.Kind != "Deployment" || d.Spec.Replicas < 2 {
			continue
		}
		scaled++

		terms := d.Spec.Template.Spec.Affinity.PodAntiAffinity.Preferred
		if len(terms) == 0 {
			t.Errorf("%s runs %d replicas with no podAntiAffinity — both can be scheduled onto one node, "+
				"and the PodDisruptionBudget will not help when that node fails",
				d.Metadata.Name, d.Spec.Replicas)
			continue
		}
		term := terms[0].PodAffinityTerm
		if term.TopologyKey != "kubernetes.io/hostname" {
			t.Errorf("%s spreads over %q, not nodes", d.Metadata.Name, term.TopologyKey)
		}

		var selects string
		for _, e := range term.LabelSelector.MatchExpressions {
			if e.Key == "app" && len(e.Values) > 0 {
				selects = e.Values[0]
			}
		}
		if selects != d.Metadata.Name {
			t.Errorf("%s's anti-affinity selects app=%q — it must select its own pods, or it spreads "+
				"this deployment away from a different one and does nothing for its own replicas",
				d.Metadata.Name, selects)
		}
	}

	if scaled == 0 {
		t.Fatal("found no multi-replica deployments in the generated manifest — regenerate it with GENERATE_ONLY=1")
	}
}

// Every connector Dockerfile must retry `go mod download`.
//
// proxy.golang.org resets an HTTP/2 stream mid-download often enough to matter:
// two of the four pushes to main on 2026-09-09 failed there, on a different
// module each time (Actions runs 34349712198 and 34351200525). A bare
// `RUN go mod download` turns that into a failed image build, so the merge that
// triggered it never publishes its image — and because the branch build passed,
// nothing on the PR says so.
//
// The retry is easy to lose: these 37 Dockerfiles are near-copies, and a new
// connector is made by copying one of them. This fails the moment a bare
// download reappears.
func TestDockerfilesRetryModuleDownload(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	paths, err := filepath.Glob(filepath.Join(root, "src", "cmd", "*", "Dockerfile"))
	if err != nil || len(paths) == 0 {
		t.Skipf("no connector Dockerfiles found (%v) — retry guard skipped", err)
	}

	var unguarded []string
	for _, p := range paths {
		body, err := os.ReadFile(p) //nolint:gosec // paths come from Glob over the repo
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		run, ok := runInstructionContaining(string(body), "go mod download")
		if !ok {
			continue // a Dockerfile that vendors deps another way is fine
		}
		// Guarded if the RUN that downloads also loops. Looking for a loop
		// rather than for this exact snippet leaves the retry free to be
		// rewritten — with a cache mount, a different backoff — while still
		// catching the thing that must not come back: one unprotected attempt.
		if !strings.Contains(run, "for ") && !strings.Contains(run, "until ") && !strings.Contains(run, "while ") {
			unguarded = append(unguarded, filepath.Base(filepath.Dir(p)))
		}
	}

	if len(unguarded) > 0 {
		sort.Strings(unguarded)
		t.Errorf("these Dockerfiles download modules without retrying: %s\n\n"+
			"A single reset stream from proxy.golang.org fails the whole build, and on a push to main "+
			"that means the image is never published. Copy the retry loop from any sibling, e.g. "+
			"src/cmd/sitoo-consumer/Dockerfile.", strings.Join(unguarded, ", "))
	}
}

// Every CI job that runs Go must fetch the modules first with retries
// (.github/actions/go-modules) and then run with GOPROXY=off, so that one
// dropped download from proxy.golang.org cannot fail a job (#299, #300,
// plans/ci-module-retries.md). A `go install …@latest` is a proxy fetch of
// its own and must sit inside a retry loop.
func TestGoJobsRetryModuleDownloads(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	action, err := os.ReadFile(filepath.Join(root, ".github", "actions", "go-modules", "action.yml"))
	if err != nil {
		t.Fatalf("read the go-modules action: %v", err)
	}
	if !strings.Contains(string(action), "go mod download") || !strings.Contains(string(action), "for i in") {
		t.Fatal(".github/actions/go-modules/action.yml no longer retries `go mod download`")
	}

	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil || len(workflows) == 0 {
		t.Fatalf("no workflows found (%v)", err)
	}
	isLoop := func(run string) bool {
		return strings.Contains(run, "for ") || strings.Contains(run, "until ") || strings.Contains(run, "while ")
	}
	isGoStep := func(run string) bool {
		for _, cmd := range []string{"go test ", "go build ", "go run ", "govulncheck ", "make build-agent"} {
			if strings.Contains(run, cmd) {
				return true
			}
		}
		return false
	}
	var checked []string
	for _, wfPath := range workflows {
		body, err := os.ReadFile(wfPath) //nolint:gosec // paths come from Glob over the repo
		if err != nil {
			t.Fatalf("read %s: %v", wfPath, err)
		}
		var wf struct {
			Jobs map[string]struct {
				Steps []struct {
					Name string            `yaml:"name"`
					Uses string            `yaml:"uses"`
					Run  string            `yaml:"run"`
					Env  map[string]string `yaml:"env"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(body, &wf); err != nil {
			t.Fatalf("parse %s: %v", wfPath, err)
		}
		for jobName, job := range wf.Jobs {
			where := filepath.Base(wfPath) + " job " + jobName
			download := -1
			for i, step := range job.Steps {
				if step.Uses == "./.github/actions/go-modules" || (strings.Contains(step.Run, "go mod download") && isLoop(step.Run)) {
					download = i
					continue
				}
				if strings.Contains(step.Run, "go install ") {
					if !isLoop(step.Run) {
						t.Errorf("%s: step %q runs `go install` without retrying — a proxy fetch of its own", where, step.Name)
					}
					continue
				}
				if !isGoStep(step.Run) {
					continue
				}
				// A step that runs Go inside a container (docker run … golang:…)
				// has no module cache and carries its own retried download and
				// GOPROXY=off in the command line.
				selfContained := strings.Contains(step.Run, "go mod download") && isLoop(step.Run)
				if download < 0 && !selfContained {
					t.Errorf("%s: step %q runs Go before any retried module download — add `uses: ./.github/actions/go-modules` after setup-go, or the loop inside the container", where, step.Name)
				}
				if step.Env["GOPROXY"] != "off" && !strings.Contains(step.Run, "GOPROXY=off") {
					t.Errorf("%s: step %q runs Go without GOPROXY=off, so it can fetch modules itself, unretried", where, step.Name)
				}
				if selfContained {
					download = i
				}
			}
			if download >= 0 {
				checked = append(checked, where)
			}
		}
	}
	sort.Strings(checked)
	// The DR drill left this list in #395: it builds the vrsky-cli image, and
	// the Dockerfile's own retry loop covers it (TestDockerfilesRetryModuleDownload).
	if len(checked) < 6 {
		t.Fatalf("only %d jobs download Go modules (%v); the Go jobs this guard knows of are the Go Tests shards, Go lint, Remote agent on Windows, Build remote agent binaries, govulncheck, the connector integration tests and the nightly SAP end-to-end — if one was renamed or dropped, say so here", len(checked), checked)
	}
}

// The repo's own CPU requests, replicas included, must add up to under a
// budget: requests reserve node capacity whether or not it is used, and on
// 2026-10-14 production sat at 81% reserved and 2% busy because every
// request had been set by guess (plans/cpu-request-headroom.md). The AKS
// add-ons are not in the repo and not counted. Raise the budget only with
// a measured reason in the commit message.
func TestCPURequestsStayWithinBudget(t *testing.T) {
	const budgetMillicores = 2000
	root := filepath.Join("..", "..", "..", "infrastructure")
	type item struct {
		name string
		m    int
	}
	var items []item
	millis := func(v string) int {
		v = strings.TrimSpace(v)
		if strings.HasSuffix(v, "m") {
			n, _ := strconv.Atoi(strings.TrimSuffix(v, "m"))
			return n
		}
		f, _ := strconv.ParseFloat(v, 64)
		return int(f * 1000)
	}
	// The manifests production runs (deploy-vrsky-platform.sh, deploy-azure.sh,
	// deploy-connectors-azure.sh and deploy-backup-azure.sh). Everything else
	// under infrastructure/kubernetes — the per-tenant NATS template, Loki and
	// promtail, KES, MinIO's 4-replica layout, Traefik — is lab-only or
	// created on demand and is not counted. The park CronJob is applied by
	// hand in production and is counted as if its pod always ran.
	// The backup CronJob's pod runs a few minutes a day, but while it runs its
	// request is reserved like any other, so it counts as one replica too.
	manifests := []string{
		"platform-nats/statefulset.yaml", "postgresql/statefulset.yaml", "minio/deployment.yaml",
		"management-api/deployment.yaml", "ui/deployment.yaml", "data-filter/deployment.yaml",
		"data-converter/deployment.yaml", "alert-responder/deployment.yaml", "connectors/connectors.yaml",
		"park/cronjob.yaml", "backup/cronjob.yaml",
	}
	type podSpec struct {
		Containers []struct {
			Resources struct {
				Requests map[string]string `yaml:"requests"`
			} `yaml:"resources"`
		} `yaml:"containers"`
	}
	backupCounted := false
	for _, rel := range manifests {
		path := filepath.Join(root, "kubernetes", rel)
		body, err := os.ReadFile(path) //nolint:gosec // fixed paths inside the repo
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(body)))
		for {
			var doc struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
				Spec struct {
					Replicas *int `yaml:"replicas"`
					Template struct {
						Spec podSpec `yaml:"spec"`
					} `yaml:"template"`
					JobTemplate struct {
						Spec struct {
							Template struct {
								Spec podSpec `yaml:"spec"`
							} `yaml:"template"`
						} `yaml:"spec"`
					} `yaml:"jobTemplate"`
				} `yaml:"spec"`
			}
			if err := dec.Decode(&doc); err != nil {
				break
			}
			if doc.Kind != "Deployment" && doc.Kind != "StatefulSet" && doc.Kind != "DaemonSet" && doc.Kind != "CronJob" {
				continue
			}
			replicas := 1
			if doc.Spec.Replicas != nil {
				replicas = *doc.Spec.Replicas
			}
			if doc.Kind == "DaemonSet" {
				replicas = 2 // one per production node
			}
			pod := doc.Spec.Template.Spec
			if doc.Kind == "CronJob" {
				pod = doc.Spec.JobTemplate.Spec.Template.Spec
				replicas = 1
			}
			sum := 0
			for _, c := range pod.Containers {
				sum += millis(c.Resources.Requests["cpu"])
			}
			if sum > 0 {
				items = append(items, item{filepath.Base(filepath.Dir(path)) + "/" + doc.Metadata.Name + " x" + strconv.Itoa(replicas), sum * replicas})
				if doc.Kind == "CronJob" && doc.Metadata.Name == "vrsky-mgmt-backup" {
					backupCounted = true
				}
			}
		}
	}
	// Helm values for the monitoring stack: the azure files are what
	// production installs (they override the base values).
	for _, f := range []string{"prometheus-values.azure.yaml", "grafana-values.azure.yaml"} {
		body, err := os.ReadFile(filepath.Join(root, "kubernetes", "monitoring", f)) //nolint:gosec // fixed path inside the repo
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`requests:\s*\{\s*cpu:\s*"?([0-9.]+m?)"?`).FindAllStringSubmatch(string(body), -1) {
			items = append(items, item{"monitoring/" + f, millis(m[1])})
		}
	}
	total := 0
	for _, it := range items {
		total += it.m
	}
	sort.Slice(items, func(i, j int) bool { return items[i].m > items[j].m })
	var top []string
	for i, it := range items {
		if i == 8 {
			break
		}
		top = append(top, fmt.Sprintf("%s=%dm", it.name, it.m))
	}
	if len(items) < 20 {
		t.Fatalf("only %d request entries found (%v); the manifest layout probably moved", len(items), top)
	}
	if !backupCounted {
		t.Fatal("backup/cronjob.yaml has no CronJob vrsky-mgmt-backup with a CPU request under " +
			"spec.jobTemplate.spec.template; its pod would reserve CPU this budget never sees")
	}
	if total > budgetMillicores {
		t.Fatalf("CPU requests in the repo add up to %dm, over the %dm budget; largest: %s", total, budgetMillicores, strings.Join(top, ", "))
	}
	t.Logf("CPU requests in the repo: %dm of %dm; largest: %s", total, budgetMillicores, strings.Join(top, ", "))
}

// workflowFile is the part of a GitHub workflow the guards look at.
type workflowFile struct {
	Jobs map[string]struct {
		Needs       any    `yaml:"needs"`
		If          string `yaml:"if"`
		Environment string `yaml:"environment"`
		Concurrency struct {
			Group string `yaml:"group"`
		} `yaml:"concurrency"`
		Steps []struct {
			ID              string `yaml:"id"`
			Name            string `yaml:"name"`
			If              string `yaml:"if"`
			Run             string `yaml:"run"`
			ContinueOnError bool   `yaml:"continue-on-error"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func parseWorkflow(t *testing.T, name string) workflowFile {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", name)) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var wf workflowFile
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return wf
}

// needsList normalises a job's `needs`, which YAML allows as a string or a list.
func needsList(v any) []string {
	switch n := v.(type) {
	case string:
		return []string{n}
	case []any:
		out := make([]string, 0, len(n))
		for _, x := range n {
			out = append(out, fmt.Sprint(x))
		}
		return out
	}
	return nil
}

// affected runs infrastructure/azure/affected.sh on a list of changed paths
// and returns its targets.
func affected(t *testing.T, paths ...string) []string {
	t.Helper()
	return affectedEnv(t, nil, paths...)
}

// affectedEnv is affected with extra environment, e.g. AFFECTED_GO.
func affectedEnv(t *testing.T, env []string, paths ...string) []string {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join("..", "..", "..", "infrastructure", "azure", "affected.sh"))
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\n") + "\n")
	// Targets go to stdout; stderr carries warnings (e.g. the fail-safe).
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("affected.sh: %v\n%s%s", err, out, stderr.String())
	}
	var targets []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			targets = append(targets, l)
		}
	}
	return targets
}

// The deploy job rolls what a merge touched and nothing else
// (plans/auto-deploy.md): a UI change rolls the UI, a connector change its
// connector, a shared-package change the images that import it, a rules
// change the monitoring stack, and a docs change nothing at all.
func TestAffectedServices(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  []string
	}{
		{"docs only", []string{"docs/operator/mail.md", "plans/x.md", "README.md"}, nil},
		{"ui", []string{"ui/src/App.tsx", "ui/package.json"}, []string{"core ui", "image ui"}},
		{"one connector", []string{"src/cmd/webhook-consumer/server.go"}, []string{"connector webhook-consumer", "image webhook-consumer"}},
		{"a core service", []string{"src/cmd/management-api/main.go"}, []string{"core management-api", "image management-api"}},
		{"a service nobody deploys", []string{"src/cmd/mock-sap/main.go"}, nil},
		// The backup CronJob runs vrsky-cli:latest from ACR (#395): the image
		// is rebuilt, but there is no Deployment to roll; the next Job pulls it.
		{"the backup CLI", []string{"src/cmd/vrsky-cli/main.go"}, []string{"image vrsky-cli"}},
		{"backup manifest", []string{"infrastructure/kubernetes/backup/cronjob.yaml"}, []string{"backup"}},
		{"backup readme", []string{"infrastructure/kubernetes/backup/README.md"}, []string{"backup"}},
		{"backup deploy script", []string{"infrastructure/azure/deploy-backup-azure.sh"}, []string{"backup"}},
		{"rules", []string{"infrastructure/prometheus-rules.yml"}, []string{"monitoring"}},
		{"monitoring values", []string{"infrastructure/kubernetes/monitoring/grafana-values.azure.yaml"}, []string{"monitoring"}},
		{"connector manifest", []string{"infrastructure/azure/deploy-connectors-azure.sh"}, []string{"connectors-manifest"}},
		{"mixed", []string{"ui/x.ts", "src/cmd/sitoo-producer/a.go", "docs/a.md"},
			[]string{"connector sitoo-producer", "core ui", "image sitoo-producer", "image ui"}},
		// Tests and test fixtures never ship in an image (#410).
		{"shared package test", []string{"src/pkg/sdk/restore_test.go"}, nil},
		{"connector test", []string{"src/cmd/webhook-consumer/server_test.go"}, nil},
		{"core service test", []string{"src/cmd/management-api/x_test.go"}, nil},
		{"shared package testdata", []string{"src/pkg/managementapi/testdata/driver_snapshot.json"}, nil},
		{"service testdata", []string{"src/cmd/webhook-consumer/testdata/payload.json"}, nil},
		{"tests and testdata together", []string{"src/pkg/sdk/restore_test.go", "src/pkg/managementapi/testdata/a.json", "src/cmd/management-api/x_test.go"}, nil},
		{"connector test and code", []string{"src/cmd/webhook-consumer/server_test.go", "src/cmd/webhook-consumer/server.go"},
			[]string{"connector webhook-consumer", "image webhook-consumer"}},
		{"core service testdata and code", []string{"src/cmd/management-api/testdata/a.json", "src/cmd/management-api/main.go"},
			[]string{"core management-api", "image management-api"}},
		{"test with rules", []string{"src/pkg/x/a_test.go", "infrastructure/prometheus-rules.yml"}, []string{"monitoring"}},
		{"test with backup manifest", []string{"src/cmd/vrsky-cli/backup_test.go", "infrastructure/kubernetes/backup/cronjob.yaml"}, []string{"backup"}},
		// The management-api image bakes in the migrations it runs at start
		// and the Windows agent it serves; either alone must ship it.
		{"migration only", []string{"infrastructure/migrations/000099_x.up.sql", "infrastructure/migrations/000099_x.down.sql"},
			[]string{"core management-api", "image management-api"}},
		{"windows agent", []string{"src/cmd/vrsky-agent/main.go"}, []string{"core management-api", "image management-api"}},
		{"windows agent test", []string{"src/cmd/vrsky-agent/main_test.go"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := affected(t, tc.paths...)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("paths %v\n got %v\nwant %v", tc.paths, got, tc.want)
			}
		})
	}
	// A shared package (#437): only the images whose main package imports it,
	// as `go list` sees it for linux/amd64. vrsky-agent is Windows-only and
	// ships inside management-api. The UI never.
	for _, tc := range []struct {
		name  string
		paths []string
		want  []string
	}{
		// pkg/idempotency: webhook-consumer only.
		{"one connector imports it", []string{"src/pkg/idempotency/store.go"},
			[]string{"connector webhook-consumer", "image webhook-consumer"}},
		{"its test next to it still counts", []string{"src/pkg/idempotency/store_test.go", "src/pkg/idempotency/store.go"},
			[]string{"connector webhook-consumer", "image webhook-consumer"}},
		{"management-api only", []string{"src/pkg/promquery/promquery.go"},
			[]string{"core management-api", "image management-api"}},
		// pkg/oauthcc: the BC, Visma and SAP connectors, both directions.
		{"a few connectors", []string{"src/pkg/oauthcc/x.go"}, []string{
			"connector business-central-consumer", "connector business-central-producer",
			"connector sap-s4hana-consumer", "connector sap-s4hana-producer",
			"connector visma-consumer", "connector visma-producer",
			"image business-central-consumer", "image business-central-producer",
			"image sap-s4hana-consumer", "image sap-s4hana-producer",
			"image visma-consumer", "image visma-producer",
		}},
		// pkg/agent: only the Windows agent, so only management-api, and a
		// windows-only file in it as well.
		{"windows agent package", []string{"src/pkg/agent/runner.go"},
			[]string{"core management-api", "image management-api"}},
		{"windows-only file", []string{"src/pkg/agent/service_windows.go"},
			[]string{"core management-api", "image management-api"}},
		// Imported by no image: nothing to rebuild.
		{"no image imports it", []string{"src/pkg/testdb/testdb.go"}, nil},
		{"src/internal no image imports", []string{"src/internal/config/config.go"}, nil},
		{"src/internal with its test", []string{"src/internal/config/config_test.go", "src/internal/config/config.go"}, nil},
	} {
		t.Run("go deps/"+tc.name, func(t *testing.T) {
			got := affected(t, tc.paths...)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("paths %v\n got %v\nwant %v", tc.paths, got, tc.want)
			}
		})
	}

	every := everyGoImageTarget(t)
	// Each image's own code marks that image, whatever its place in the
	// lists: a name at a line break in CONNECTORS counts like any other.
	for _, e := range every {
		name, ok := strings.CutPrefix(e, "image ")
		if !ok {
			continue
		}
		var want []string
		for _, w := range every {
			if strings.HasSuffix(w, " "+name) {
				want = append(want, w)
			}
		}
		t.Run("own code/"+name, func(t *testing.T) {
			p := "src/cmd/" + name + "/main.go"
			if got := affected(t, p); strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("%s\n got %v\nwant %v", p, got, want)
			}
		})
	}
	// No package is imported by all 36 Go images (alert-responder and
	// vrsky-cli skip the messaging stack), so take the widest one,
	// pkg/envelope: most images, never the UI, and not the fail-safe set.
	t.Run("go deps/widest package", func(t *testing.T) {
		for _, paths := range [][]string{
			{"src/pkg/envelope/x.go"},
			{"src/pkg/envelope/x_test.go", "src/pkg/envelope/x.go"},
		} {
			got := affected(t, paths...)
			images := 0
			for _, g := range got {
				if strings.HasPrefix(g, "image ") {
					images++
				}
				if g == "core ui" || g == "image ui" {
					t.Errorf("%v: %q; a Go change never rolls the UI", paths, g)
				}
			}
			if images < 30 || len(got) >= len(every) {
				t.Errorf("%v: %d images, %d targets (fail-safe is %d); want most images but not all:\n%s",
					paths, images, len(got), len(every), strings.Join(got, "\n"))
			}
		}
	})

	// Fail-safe: when the module, a Dockerfile or an embedded asset changes,
	// or `go list` can't answer, every Go image is rebuilt (never the UI).
	failing := filepath.Join(t.TempDir(), "go")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\necho 'github.com/ValueRetail/vrsky/cmd/webhook-consumer github.com/ValueRetail/vrsky/pkg/idempotency'\nexit 3\n"), 0o755); err != nil { //nolint:gosec // test helper must be executable
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		env   []string
		paths []string
	}{
		{"go.mod", nil, []string{"src/go.mod"}},
		{"go.sum", nil, []string{"src/go.sum"}},
		{"a Dockerfile", nil, []string{"src/cmd/file-consumer/Dockerfile"}},
		{"embedded asset", nil, []string{"src/pkg/managementapi/agent_install.ps1"}},
		{"embedded asset in src/internal", nil, []string{"src/internal/config/defaults.yaml"}},
		{"unknown package", nil, []string{"src/pkg/doesnotexist/x.go"}},
		{"unknown package next to a known one", nil, []string{"src/pkg/idempotency/store.go", "src/pkg/doesnotexist/x.go"}},
		{"helper is false", []string{"AFFECTED_GO=false"}, []string{"src/pkg/idempotency/store.go"}},
		{"helper fails after partial output", []string{"AFFECTED_GO=" + failing}, []string{"src/pkg/idempotency/store.go"}},
		{"helper missing", []string{"AFFECTED_GO=/nonexistent/go"}, []string{"src/pkg/testdb/testdb.go"}},
		{"go.mod without a helper", []string{"AFFECTED_GO=false"}, []string{"src/go.mod"}},
	} {
		t.Run("fail-safe/"+tc.name, func(t *testing.T) {
			got := affectedEnv(t, tc.env, tc.paths...)
			if strings.Join(got, "|") != strings.Join(every, "|") {
				t.Fatalf("paths %v env %v\n got %v\nwant every Go image %v", tc.paths, tc.env, got, every)
			}
		})
	}

	// Paths that never need the helper decide the same without it.
	for _, tc := range []struct {
		name  string
		paths []string
		want  []string
	}{
		{"shared package test", []string{"src/pkg/sdk/restore_test.go"}, nil},
		{"shared package testdata", []string{"src/pkg/managementapi/testdata/driver_snapshot.json"}, nil},
		{"docs", []string{"docs/a.md"}, nil},
		{"rules", []string{"infrastructure/prometheus-rules.yml"}, []string{"monitoring"}},
	} {
		t.Run("no helper/"+tc.name, func(t *testing.T) {
			got := affectedEnv(t, []string{"AFFECTED_GO=false"}, tc.paths...)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("paths %v with AFFECTED_GO=false\n got %v\nwant %v", tc.paths, got, tc.want)
			}
		})
	}

	// vrsky-cli links shared packages (crypto above all: a dump encrypted by
	// a stale CLI must still decrypt), so a change to one it imports, or any
	// fail-safe change, rebuilds it. It is an image only, never a core
	// service or connector.
	for _, p := range []string{"src/pkg/crypto/x.go", "src/go.mod", "src/go.sum", "src/cmd/file-consumer/Dockerfile", "src/cmd/vrsky-cli/Dockerfile"} {
		got := affected(t, p)
		found := false
		for _, g := range got {
			if g == "image vrsky-cli" {
				found = true
			}
			if g == "core vrsky-cli" || g == "connector vrsky-cli" {
				t.Errorf("%s: %q; vrsky-cli is a tool image, nothing deploys it as a service", p, g)
			}
		}
		if !found {
			t.Errorf("%s: no `image vrsky-cli`; the backup Job would keep running a CLI built from older shared code", p)
		}
	}
}

// everyGoImageTarget is the fail-safe output: every Go image in affected.sh
// (CORE except ui, CONNECTORS, TOOLS) with whatever rolls it, sorted.
func everyGoImageTarget(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "infrastructure", "azure", "affected.sh"))
	if err != nil {
		t.Fatal(err)
	}
	list := func(name string) []string {
		m := regexp.MustCompile(`(?s)\b` + name + `="([^"]*)"`).FindStringSubmatch(string(b))
		if m == nil {
			t.Fatalf("no %s=\"…\" in affected.sh", name)
		}
		return strings.Fields(m[1])
	}
	var out []string
	for _, n := range list("CORE") {
		if n != "ui" {
			out = append(out, "image "+n, "core "+n)
		}
	}
	for _, n := range list("CONNECTORS") {
		out = append(out, "image "+n, "connector "+n)
	}
	for _, n := range list("TOOLS") {
		out = append(out, "image "+n)
	}
	sort.Strings(out)
	return out
}

// affected.sh carries its own copy of the service lists; they must equal
// what build-push-acr.sh builds and deploy-core-azure.sh deploys, or a new
// connector would be built and deployed by hand but never by the deploy job.
func TestAffectedServicesKnowsEveryImage(t *testing.T) {
	root := filepath.Join("..", "..", "..", "infrastructure", "azure")
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(root, name)) //nolint:gosec // fixed paths inside the repo
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	listIn := func(src, varName string) []string {
		re := regexp.MustCompile(`(?s)` + varName + `="([^"]*)"`)
		m := re.FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("no %s=\"…\" in affected.sh", varName)
		}
		return strings.Fields(m[1])
	}
	aff := read("affected.sh")
	gotConn := listIn(aff, "CONNECTORS")
	var wantConn []string
	for img := range builtConnectorImages(read("build-push-acr.sh")) {
		wantConn = append(wantConn, img)
	}
	sort.Strings(gotConn)
	sort.Strings(wantConn)
	if strings.Join(gotConn, " ") != strings.Join(wantConn, " ") {
		t.Errorf("affected.sh CONNECTORS differ from what build-push-acr.sh builds:\n got %v\nwant %v", gotConn, wantConn)
	}
	var wantCore []string
	for _, l := range strings.Split(regexp.MustCompile(`(?s)CORE="\n(.*?)"`).FindStringSubmatch(read("deploy-core-azure.sh"))[1], "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			wantCore = append(wantCore, f[0])
		}
	}
	gotCore := listIn(aff, "CORE")
	sort.Strings(gotCore)
	sort.Strings(wantCore)
	if strings.Join(gotCore, " ") != strings.Join(wantCore, " ") {
		t.Errorf("affected.sh CORE differs from deploy-core-azure.sh:\n got %v\nwant %v", gotCore, wantCore)
	}

	// Tool images (#395): built into ACR, run by something other than a
	// Deployment. TOOLS must equal what build_tools builds, include vrsky-cli
	// (the backup CronJob's image), and `all` must build them, or a release
	// leaves the CronJob pulling a CLI nobody rebuilt.
	build := read("build-push-acr.sh")
	gotTools := listIn(aff, "TOOLS")
	var wantTools []string
	if m := regexp.MustCompile(`(?s)build_tools\(\) \{(.*?)\n\}`).FindStringSubmatch(build); m != nil {
		for _, b := range regexp.MustCompile(`build\s+vrsky/([a-z0-9-]+):latest`).FindAllStringSubmatch(m[1], -1) {
			wantTools = append(wantTools, b[1])
		}
	}
	if len(wantTools) == 0 {
		t.Fatal("parsed no images from build_tools in build-push-acr.sh; it is missing or changed shape")
	}
	sort.Strings(gotTools)
	sort.Strings(wantTools)
	if strings.Join(gotTools, " ") != strings.Join(wantTools, " ") {
		t.Errorf("affected.sh TOOLS differ from what build_tools builds:\n got %v\nwant %v", gotTools, wantTools)
	}
	hasCLI := false
	for _, n := range gotTools {
		if n == "vrsky-cli" {
			hasCLI = true
		}
	}
	if !hasCLI {
		t.Errorf("affected.sh TOOLS %v lacks vrsky-cli, the backup CronJob's image", gotTools)
	}
	all := regexp.MustCompile(`(?m)^\s*all\)(.*)$`).FindStringSubmatch(build)
	if all == nil || !regexp.MustCompile(`\bbuild_tools\b`).MatchString(all[1]) {
		t.Errorf("build-push-acr.sh's all) branch does not run build_tools: %q", all)
	}
	if !regexp.MustCompile(`(?m)^\s*tools\)\s*build_tools\b`).MatchString(build) {
		t.Error("build-push-acr.sh has no `tools) build_tools` group")
	}
	// The build-push.yml matrix builds an image from src/cmd/<img>/Dockerfile.
	if _, err := os.Stat(filepath.Join("..", "..", "..", "src", "cmd", "vrsky-cli", "Dockerfile")); err != nil {
		t.Errorf("no src/cmd/vrsky-cli/Dockerfile for the build matrix to build: %v", err)
	}
}

// The production rollout runs only on a push to main, only after every test
// job on that commit, serialised, against the `production` environment — and
// the registry nothing reads is gone (plans/auto-deploy.md).
func TestDeployJobGatesOnTests(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "build-push.yml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "ghcr.io") {
		t.Error("build-push.yml still pushes to ghcr.io, which production never reads")
	}
	var wf struct {
		Jobs map[string]struct {
			Needs       any    `yaml:"needs"` // a string or a list in YAML
			If          string `yaml:"if"`
			Environment string `yaml:"environment"`
			Concurrency struct {
				Group string `yaml:"group"`
			} `yaml:"concurrency"`
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, ok := wf.Jobs["plan"]
	if !ok {
		t.Fatal("no plan job")
	}
	// affected.sh skips _test.go paths (#410). With rename detection on,
	// `git mv x.go x_test.go` lists only x_test.go and deploys nothing, so
	// the diff that feeds it must list both sides of a rename.
	feeds := 0
	for _, st := range plan.Steps {
		if !strings.Contains(st.Run, "affected.sh") {
			continue
		}
		for _, l := range strings.Split(st.Run, "\n") {
			if strings.Contains(l, "git diff") && !strings.HasPrefix(strings.TrimSpace(l), "#") {
				feeds++
				if !strings.Contains(l, "--no-renames") {
					t.Errorf("plan job diff feeding affected.sh lacks --no-renames: %q", strings.TrimSpace(l))
				}
			}
		}
	}
	if feeds == 0 {
		t.Error("found no `git diff` feeding affected.sh in the plan job")
	}
	// Every job that runs tests must gate the plan.
	for name, job := range wf.Jobs {
		runsTests := false
		for _, st := range job.Steps {
			if strings.Contains(st.Run, "go test") || strings.Contains(st.Run, "npm run test") {
				runsTests = true
			}
		}
		if !runsTests {
			continue
		}
		found := false
		for _, n := range needsList(plan.Needs) {
			if n == name {
				found = true
			}
		}
		if !found {
			t.Errorf("job %q runs tests but the plan job does not need it: a red test would not stop a deploy", name)
		}
	}
	for _, want := range []string{"github.event_name == 'push'", "github.ref == 'refs/heads/main'", "!contains(needs.*.result, 'failure')", "!contains(needs.*.result, 'cancelled')"} {
		if !strings.Contains(plan.If, want) {
			t.Errorf("plan.if lacks %q: %s", want, plan.If)
		}
	}
	build, ok := wf.Jobs["build-images"]
	if !ok {
		t.Fatal("no build-images job")
	}
	// Without a status function the implicit success() looks at every
	// ancestor, and the skipped UI test job (no UI change) skips the build.
	if !strings.Contains(build.If, "!cancelled()") && !strings.Contains(build.If, "always()") {
		t.Errorf("build-images.if has no status function; a skipped ancestor (the UI tests on a non-UI merge) skips the build: %s", build.If)
	}
	if !strings.Contains(build.If, "needs.plan.result == 'success'") {
		t.Errorf("build-images.if does not require the plan to have succeeded: %s", build.If)
	}
	deploy, ok := wf.Jobs["deploy"]
	if !ok {
		t.Fatal("no deploy job")
	}
	// The API server accepts a few approved addresses only; every kubectl and
	// helm in the deploy job must go through in-cluster.sh (az aks command
	// invoke), never run on the runner.
	for _, st := range deploy.Steps {
		if strings.Contains(st.Run, "in-cluster.sh") {
			continue
		}
		if strings.Contains(st.Run, "kubectl ") || strings.Contains(st.Run, "helm ") || strings.Contains(st.Run, "aks get-credentials") {
			t.Errorf("deploy step runs kubectl/helm on the runner, which cannot reach the API server: %q", strings.SplitN(st.Run, "\n", 2)[0])
		}
	}
	if deploy.Environment != "production" || deploy.Concurrency.Group != "production" {
		t.Errorf("deploy job: environment %q, concurrency group %q; want production/production", deploy.Environment, deploy.Concurrency.Group)
	}
	if !strings.Contains(deploy.If, "needs.build-images.result != 'failure'") {
		t.Errorf("deploy.if does not stop on a failed image build: %s", deploy.If)
	}
}

// A merge while the cluster is parked must deploy nothing and stay green:
// the deploy job reads the power state right after signing in, and every
// in-cluster step is gated on it (plans/deploy-while-parked.md).
func TestDeployJobSkipsWhenClusterStopped(t *testing.T) {
	wf := parseWorkflow(t, "build-push.yml")
	deploy, ok := wf.Jobs["deploy"]
	if !ok {
		t.Fatal("no deploy job")
	}
	stateAt := -1
	for i, st := range deploy.Steps {
		if st.ID == "cluster" && strings.Contains(st.Run, "powerState.code") {
			stateAt = i
		}
		if !strings.Contains(st.Run, "in-cluster.sh") {
			continue
		}
		if stateAt < 0 {
			t.Errorf("deploy step %q runs in-cluster before the power state is known", st.Name)
		}
		if !strings.Contains(st.If, "steps.cluster.outputs.state == 'Running'") {
			t.Errorf("deploy step %q is not gated on the cluster running; it would fail on a parked cluster: if=%q", st.Name, st.If)
		}
	}
	if stateAt < 0 {
		t.Fatal("the deploy job has no `cluster` step reading powerState.code")
	}
}

// The Catch up workflow brings production to main after a start: manual,
// serialised with deploys, refuses a stopped cluster, and runs the same
// scripts through the same in-cluster path as the deploy job.
func TestCatchUpWorkflow(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "catch-up.yml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	var on struct {
		On map[string]any `yaml:"on"`
	}
	if err := yaml.Unmarshal(body, &on); err != nil {
		t.Fatal(err)
	}
	if _, ok := on.On["workflow_dispatch"]; !ok || len(on.On) != 1 {
		t.Errorf("catch-up.yml must run on workflow_dispatch only, got triggers %v", on.On)
	}
	wf := parseWorkflow(t, "catch-up.yml")
	job, ok := wf.Jobs["catch-up"]
	if !ok {
		t.Fatal("no catch-up job")
	}
	if job.Environment != "production" || job.Concurrency.Group != "production" {
		t.Errorf("catch-up job: environment %q, concurrency %q; want production/production so it never interleaves with a deploy", job.Environment, job.Concurrency.Group)
	}
	all := ""
	refuses := false
	for i, st := range job.Steps {
		all += st.Run + "\n"
		if strings.Contains(st.Run, "powerState.code") && strings.Contains(st.Run, "exit 1") {
			refuses = true
			for _, later := range job.Steps[i+1:] {
				if (strings.Contains(later.Run, "kubectl ") || strings.Contains(later.Run, "helm ")) && !strings.Contains(later.Run, "in-cluster.sh") {
					t.Errorf("catch-up step %q runs kubectl/helm on the runner", later.Name)
				}
			}
		}
	}
	if !refuses {
		t.Error("catch-up has no step that reads powerState.code and exits 1 when the cluster is not running")
	}
	for _, script := range []string{"deploy-core-azure.sh", "deploy-connectors-azure.sh", "install-monitoring.sh", "smoke-prod.sh"} {
		if !strings.Contains(all, script) {
			t.Errorf("catch-up never runs %s", script)
		}
	}
	if strings.Contains(all, "az acr build") {
		t.Error("catch-up rebuilds images; it must only roll what the registry has")
	}
}

// runInstructionContaining returns the full RUN instruction whose body contains
// needle, joining the backslash continuations that make up a multi-line one.
func runInstructionContaining(dockerfile, needle string) (string, bool) {
	lines := strings.Split(dockerfile, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "RUN ") {
			continue
		}
		instr := lines[i]
		for strings.HasSuffix(strings.TrimRight(instr, " \t"), "\\") && i+1 < len(lines) {
			i++
			instr += "\n" + lines[i]
		}
		if strings.Contains(instr, needle) {
			return instr, true
		}
	}
	return "", false
}

// Every standing consumer must restore running pipelines on boot.
//
// Start/stop are NATS commands and are not persisted. A consumer that only
// reacts to commands silently drops every pipeline it runs when it restarts
// — a rollout, a crash, a node move — until someone redeploys from the
// builder, and a webhook source answers 404 meanwhile. sdk.RestoreRunning
// is the one line that prevents it (plans/stable-connections.md); a new
// consumer made by copying an old one must not lose it.
// The manifest is what a rebuild and any `kubectl apply` put on the cluster,
// so it must name at least one platform operator; with an empty list nobody
// can activate a plan (plans/operator-manifest-and-last-payload.md).
func TestManifestNamesAPlatformOperator(t *testing.T) {
	path := filepath.Join("..", "..", "..", "infrastructure", "kubernetes", "management-api", "deployment.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(body), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "- name: PLATFORM_OPERATORS" || i+1 >= len(lines) {
			continue
		}
		value := strings.Trim(strings.TrimPrefix(strings.TrimSpace(lines[i+1]), "value:"), ` "`)
		if value == "" {
			t.Fatalf("%s: PLATFORM_OPERATORS is empty — nobody could activate a plan after an apply", path)
		}
		for _, email := range strings.Split(value, ",") {
			if !strings.Contains(email, "@") {
				t.Fatalf("%s: PLATFORM_OPERATORS entry %q is not an email", path, email)
			}
		}
		return
	}
	t.Fatalf("%s: no PLATFORM_OPERATORS env entry found", path)
}

// The management API's default slog logger is its JSON one: the slog.Default()
// call sites (sign-in limits, billing, orchestrator failures) must not fall
// back to the text handler (plans/hygiene-fixes.md).
func TestManagementAPIUsesJSONDefaultLogger(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "cmd", "management-api", "main.go"))
	if err != nil {
		t.Fatalf("read cmd/management-api/main.go: %v", err)
	}
	src := string(body)
	newAt, setAt := strings.Index(src, `logging.New("management-api")`), strings.Index(src, "slog.SetDefault(appLog)")
	if newAt < 0 || setAt < 0 || setAt < newAt {
		t.Fatalf("cmd/management-api/main.go must call slog.SetDefault(appLog) right after logging.New (found at %d and %d)", newAt, setAt)
	}
}

func TestConsumersRestoreRunningOnBoot(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	paths, err := filepath.Glob(filepath.Join(root, "src", "cmd", "*", "service.go"))
	if err != nil || len(paths) == 0 {
		t.Skipf("no connector services found (%v) — restore guard skipped", err)
	}
	var missing, checked []string
	for _, p := range paths {
		body, err := os.ReadFile(p) //nolint:gosec // paths come from Glob over the repo
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if !strings.Contains(string(body), `.connection.start"`) {
			continue // producers and workers: no start command, nothing to restore
		}
		name := filepath.Base(filepath.Dir(p))
		checked = append(checked, name)
		if !strings.Contains(string(body), "sdk.RestoreRunning(") {
			missing = append(missing, name)
		}
	}
	if len(checked) < 10 {
		t.Fatalf("only %d consumers subscribe to start commands (%v); the search pattern is probably stale", len(checked), checked)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("these consumers handle start commands but do not restore running pipelines on boot: %s\n\n"+
			"Call sdk.RestoreRunning(ctx, db, logger, \"<node type>\", startConnection) in Run, after the command "+
			"subscriptions, as every other consumer does.", strings.Join(missing, ", "))
	}
}

// Everything the deploy and catch-up jobs run inside the cluster executes in
// the `az aks command invoke` runtime, which has bash, awk, grep, jq, kubectl
// and helm but no sed, python or envsubst (probed 2026-10-19; a sed in the
// monitoring installer failed the first catch-up). Every script those steps
// run, and the inline commands themselves, must stay within that toolset.
func TestInClusterScriptsUseOnlyRuntimeTools(t *testing.T) {
	missing := regexp.MustCompile(`(?m)(^|[\s;|&(` + "`" + `])(sed|python3?|envsubst)(\s|$)`)
	scriptRef := regexp.MustCompile(`infrastructure/[A-Za-z0-9_./-]+\.sh`)
	scripts := map[string]bool{}
	for _, name := range []string{"build-push.yml", "catch-up.yml", "drift-check.yml"} {
		wf := parseWorkflow(t, name)
		for jobName, job := range wf.Jobs {
			for _, st := range job.Steps {
				if !strings.Contains(st.Run, "in-cluster.sh") {
					continue
				}
				if missing.MatchString(st.Run) {
					t.Errorf("%s job %s step %q runs sed/python/envsubst in-cluster; the runtime has none", name, jobName, st.Name)
				}
				for _, ref := range scriptRef.FindAllString(st.Run, -1) {
					if !strings.HasSuffix(ref, "/in-cluster.sh") {
						scripts[ref] = true
					}
				}
			}
		}
	}
	if len(scripts) < 4 {
		t.Fatalf("found only %d scripts run in-cluster (%v); the workflow layout probably moved", len(scripts), scripts)
	}
	// The backup deploy copies acr-pull with kubectl and jq precisely
	// because the runtime has no sed; make sure it is among those checked.
	if !scripts["infrastructure/azure/deploy-backup-azure.sh"] {
		t.Errorf("deploy-backup-azure.sh is not run in-cluster by any workflow, so its tools go unchecked (found %v)", scripts)
	}
	for ref := range scripts {
		body, err := os.ReadFile(filepath.Join("..", "..", "..", ref)) //nolint:gosec // paths come from the repo's own workflows
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if missing.MatchString(line) {
				t.Errorf("%s:%d uses a tool the in-cluster runtime lacks: %s", ref, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// drift runs infrastructure/azure/drift.sh on a running and a registry
// listing and returns its lines.
func drift(t *testing.T, running, latest string) []string {
	t.Helper()
	dir := t.TempDir()
	r := filepath.Join(dir, "running.txt")
	l := filepath.Join(dir, "latest.txt")
	if err := os.WriteFile(r, []byte(running), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l, []byte(latest), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", filepath.Join("..", "..", "..", "infrastructure", "azure", "drift.sh"), r, l).CombinedOutput()
	if err != nil {
		t.Fatalf("drift.sh: %v\n%s", err, out)
	}
	var lines []string
	for _, x := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if x != "" {
			lines = append(lines, x)
		}
	}
	return lines
}

// The drift check compares what every container runs with the registry's
// :latest and reports exactly the pairs that differ (plans/digest-drift.md):
// a service behind, a connector behind, the old pod of a rollout next to the
// new one, and an image the registry no longer has. Equal is silence.
func TestDriftDetection(t *testing.T) {
	const a, b, c = "sha256:" + "a000000000000000000000000000000000000000000000000000000000000000",
		"sha256:" + "b000000000000000000000000000000000000000000000000000000000000000",
		"sha256:" + "c000000000000000000000000000000000000000000000000000000000000000"
	latest := "management-api " + a + "\nsitoo-consumer " + b + "\nui " + c + "\n"
	cases := []struct {
		name    string
		running string
		want    []string
	}{
		{"at main", "management-api " + a + "\nsitoo-consumer " + b + "\nui " + c + "\n", nil},
		{"two replicas at main", "management-api " + a + "\nmanagement-api " + a + "\n", nil},
		{"core behind", "management-api " + b + "\nui " + c + "\n", []string{"management-api " + b + " " + a}},
		{"connector behind", "sitoo-consumer " + a + "\n", []string{"sitoo-consumer " + a + " " + b}},
		{"rollout: old and new pod", "ui " + a + "\nui " + c + "\n", []string{"ui " + a + " " + c}},
		{"not in registry", "ghost-consumer " + a + "\n", []string{"ghost-consumer " + a + " (not in registry)"}},
		{"nothing running", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := drift(t, tc.running, latest)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("drift.sh\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// The drift check is read-only except for one dispatch of Catch up: it runs
// on a schedule and by hand, in the production environment and concurrency
// group so it never looks at a cluster mid-rollout, gates on the power state
// and on no build in progress before any in-cluster step, reads digests
// through in-cluster.sh only, and never runs a deploy script itself.
func TestDriftCheckWorkflow(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "drift-check.yml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	var top struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top.On["schedule"]; !ok {
		t.Error("drift-check.yml has no schedule; it is the thing that notices without a click")
	}
	if _, ok := top.On["workflow_dispatch"]; !ok || len(top.On) != 2 {
		t.Errorf("drift-check.yml must run on schedule and workflow_dispatch only, got triggers %v", top.On)
	}
	wf := parseWorkflow(t, "drift-check.yml")
	job, ok := wf.Jobs["drift"]
	if !ok {
		t.Fatal("no drift job")
	}
	if job.Environment != "production" || job.Concurrency.Group != "production" {
		t.Errorf("drift job: environment %q, concurrency %q; want production/production so it queues behind a deploy", job.Environment, job.Concurrency.Group)
	}
	if top.Jobs["drift"].Permissions["actions"] != "write" {
		t.Error("drift job cannot dispatch Catch up without `actions: write`")
	}
	gate := -1
	all := ""
	for i, st := range job.Steps {
		all += st.Run + "\n"
		if st.ID == "gate" {
			gate = i
			if !strings.Contains(st.Run, "powerState.code") || !strings.Contains(st.Run, "--status in_progress") {
				t.Error("the gate step must read the cluster's power state and look for a build in progress on main")
			}
		}
	}
	if gate < 0 {
		t.Fatal("no step with id gate")
	}
	for i, st := range job.Steps {
		if st.Run == "" || i == gate {
			continue
		}
		inCluster := strings.Contains(st.Run, "in-cluster.sh")
		if (strings.Contains(st.Run, "kubectl ") || strings.Contains(st.Run, "helm ")) && !inCluster {
			t.Errorf("step %q runs kubectl/helm on the runner", st.Name)
		}
		if i > gate && !strings.Contains(st.If, "steps.gate.outputs.go == 'true'") {
			t.Errorf("step %q is not gated on steps.gate.outputs.go", st.Name)
		}
		if i < gate && inCluster {
			t.Errorf("step %q reaches the cluster before the gate", st.Name)
		}
	}
	for _, needle := range []string{"running-digests.sh", "drift.sh", "gh workflow run catch-up.yml"} {
		if !strings.Contains(all, needle) {
			t.Errorf("drift check never uses %s", needle)
		}
	}
	// The heartbeat (plans/monitoring-heartbeat.md): an in-cluster step
	// before the comparison that may fail without stopping it, and a
	// verdict step at the end that turns its failure into a red run.
	alive, compare, verdict, budget := -1, -1, -1, -1
	for i, st := range job.Steps {
		switch {
		case strings.Contains(st.Run, "budget.sh"):
			budget = i
			if !strings.Contains(st.If, "vars.MONTHLY_BUDGET_NOK != ''") || !strings.Contains(st.Run, "az aks stop") || !strings.Contains(st.Run, "MonthToDate") {
				t.Errorf("the budget step must be skipped without MONTHLY_BUDGET_NOK, read the month-to-date cost, and stop the cluster at the budget (if=%q)", st.If)
			}
		case strings.Contains(st.Run, "bash infrastructure/azure/monitoring-alive.sh"):
			alive = i
			if !strings.Contains(st.Run, "in-cluster.sh") || st.ID != "alive" || !st.ContinueOnError {
				t.Errorf("the monitoring-alive step must run in-cluster with id alive and continue-on-error: true (got id %q, continue-on-error %v)", st.ID, st.ContinueOnError)
			}
		case strings.Contains(st.Run, "drift.sh"):
			compare = i
		case strings.Contains(st.If, "steps.alive.outcome == 'failure'") && strings.Contains(st.Run, "exit 1"):
			verdict = i
		}
	}
	if alive < 0 {
		t.Error("no step runs monitoring-alive.sh; a dead Prometheus would look like all is well")
	}
	if verdict < 0 {
		t.Error("no verdict step fails the run when steps.alive.outcome == 'failure'")
	}
	if alive >= 0 && compare >= 0 && !(alive < compare && compare < verdict) {
		t.Errorf("step order must be heartbeat (%d) < comparison (%d) < verdict (%d)", alive, compare, verdict)
	}
	if budget < 0 {
		t.Error("no step runs budget.sh; the monthly budget would be an e-mail, not a stop (plans/cost-cap.md)")
	} else if alive >= 0 && budget > alive {
		t.Errorf("the budget step (%d) must come before the heartbeat (%d): over budget, nothing else runs", budget, alive)
	}
	for _, forbidden := range []string{"deploy-core-azure.sh", "deploy-connectors-azure.sh", "install-monitoring.sh", "kubectl set", "kubectl rollout", "kubectl apply", "az acr build"} {
		if strings.Contains(all, forbidden) {
			t.Errorf("drift check runs %q; it must only compare and dispatch Catch up", forbidden)
		}
	}
}

// smokeAlerts runs infrastructure/azure/smoke-alerts.sh on a Prometheus
// /api/v1/rules document and returns the alert names it prints.
func smokeAlerts(t *testing.T, rulesJSON string) []string {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join("..", "..", "..", "infrastructure", "azure", "smoke-alerts.sh"))
	cmd.Stdin = strings.NewReader(rulesJSON)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("smoke-alerts.sh: %v\n%s", err, out)
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			names = append(names, l)
		}
	}
	return names
}

// rulesDoc builds a /api/v1/rules document from (group, alert, severity,
// state) rows; an empty state means the rule has no active alert.
func rulesDoc(rows ...[4]string) string {
	groups := map[string][]string{}
	var order []string
	for _, r := range rows {
		group, alert, severity, state := r[0], r[1], r[2], r[3]
		alerts := "[]"
		if state != "" {
			alerts = fmt.Sprintf(`[{"state":%q,"labels":{"alertname":%q,"severity":%q}}]`, state, alert, severity)
		}
		if _, ok := groups[group]; !ok {
			order = append(order, group)
		}
		groups[group] = append(groups[group], fmt.Sprintf(`{"type":"alerting","name":%q,"labels":{"severity":%q},"alerts":%s}`, alert, severity, alerts))
	}
	var gs []string
	for _, g := range order {
		gs = append(gs, fmt.Sprintf(`{"name":%q,"rules":[%s]}`, g, strings.Join(groups[g], ",")))
	}
	return `{"status":"success","data":{"groups":[` + strings.Join(gs, ",") + `]}}`
}

// The smoke check fails a deploy for VRSky's own alerts above info and for
// anything critical, and for nothing else: a stock warning that a fleet
// restart produces by itself must not turn a good rollout red
// (plans/smoke-alert-scope.md).
func TestSmokeAlertScope(t *testing.T) {
	cases := []struct {
		name string
		rows [][4]string
		want []string
	}{
		{"stock warning firing", [][4]string{{"kubernetes-system-apiserver", "KubeClientErrors", "warning", "firing"}}, nil},
		{"vrsky alert firing", [][4]string{{"vrsky-platform", "ConnectorUnavailable", "warning", "firing"}}, []string{"ConnectorUnavailable"}},
		{"vrsky alert only pending", [][4]string{{"vrsky-pipelines", "PipelineDown", "critical", "pending"}}, nil},
		{"stock critical firing", [][4]string{{"kubernetes-system-apiserver", "KubeAPIDown", "critical", "firing"}}, []string{"KubeAPIDown"}},
		{"vrsky info firing", [][4]string{{"vrsky-pipelines", "ConnectionQuiet", "info", "firing"}}, nil},
		{"watchdog and inhibitor", [][4]string{{"general.rules", "Watchdog", "none", "firing"}, {"general.rules", "InfoInhibitor", "none", "firing"}}, nil},
		{"two that count, sorted", [][4]string{{"vrsky-platform", "MgmtAPIErrorRate", "warning", "firing"}, {"node-exporter", "NodeFilesystemAlmostOutOfSpace", "critical", "firing"}, {"kubernetes-apps", "KubeDeploymentReplicasMismatch", "warning", "firing"}}, []string{"MgmtAPIErrorRate", "NodeFilesystemAlmostOutOfSpace"}},
		{"nothing active", [][4]string{{"vrsky-platform", "ConnectorUnavailable", "warning", ""}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := smokeAlerts(t, rulesDoc(tc.rows...))
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("smoke-alerts.sh\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// smoke-prod.sh decides on alerts through smoke-alerts.sh and the rules API,
// never through an inline filter of /api/v1/alerts again.
func TestSmokeProdUsesAlertScope(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "infrastructure", "azure", "smoke-prod.sh")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, "api/v1/rules") || !strings.Contains(s, "smoke-alerts.sh") {
		t.Error("smoke-prod.sh must pipe /api/v1/rules into smoke-alerts.sh")
	}
	if strings.Contains(s, "api/v1/alerts") || strings.Contains(s, `select(.state=="firing"`) {
		t.Error("smoke-prod.sh filters alerts inline; the scope lives in smoke-alerts.sh so TestSmokeAlertScope covers it")
	}
}

// monitoringAlive runs infrastructure/azure/monitoring-alive.sh against a
// stub kubectl that answers `get --raw <path>` from the given bodies (an
// empty body means the call fails, as it does when the service is down).
func monitoringAlive(t *testing.T, prometheusBody, alertmanagerBody string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	stub := "#!/usr/bin/env bash\ncase \"$3\" in\n" +
		"  */prometheus-prometheus:*) [ -s \"$PROM\" ] && cat \"$PROM\" || exit 1 ;;\n" +
		"  */prometheus-alertmanager:*) [ -s \"$AM\" ] && cat \"$AM\" || exit 1 ;;\n" +
		"  *) exit 2 ;;\nesac\n"
	kubectl := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(kubectl, []byte(stub), 0o700); err != nil { //nolint:gosec // an executable stub in a temp dir
		t.Fatal(err)
	}
	prom := filepath.Join(dir, "prom.json")
	am := filepath.Join(dir, "am.json")
	if err := os.WriteFile(prom, []byte(prometheusBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(am, []byte(alertmanagerBody), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join("..", "..", "..", "infrastructure", "azure", "monitoring-alive.sh"))
	cmd.Env = append(os.Environ(), "KUBECTL="+kubectl, "PROM="+prom, "AM="+am)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The heartbeat is alive only when Prometheus reports the Watchdog firing
// AND Alertmanager holds it; each missing side is named, and an unreachable
// service counts as missing (plans/monitoring-heartbeat.md).
func TestMonitoringAliveScript(t *testing.T) {
	const promFiring = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"alertname":"Watchdog","alertstate":"firing"},"value":[1,"1"]}]}}`
	const promEmpty = `{"status":"success","data":{"resultType":"vector","result":[]}}`
	const amHolds = `[{"labels":{"alertname":"Watchdog"},"status":{"state":"active"}}]`
	const amEmpty = `[]`
	cases := []struct {
		name     string
		prom, am string
		alive    bool
		mention  string
	}{
		{"both present", promFiring, amHolds, true, "monitoring alive"},
		{"watchdog not firing in prometheus", promEmpty, amHolds, false, "prometheus: no firing Watchdog"},
		{"alertmanager without it", promFiring, amEmpty, false, "alertmanager: no Watchdog"},
		{"prometheus unreachable", "", amHolds, false, "prometheus: no firing Watchdog"},
		{"alertmanager unreachable", promFiring, "", false, "alertmanager: no Watchdog"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := monitoringAlive(t, tc.prom, tc.am)
			if (err == nil) != tc.alive {
				t.Errorf("alive=%v, want %v\n%s", err == nil, tc.alive, out)
			}
			if !strings.Contains(out, tc.mention) {
				t.Errorf("output lacks %q:\n%s", tc.mention, out)
			}
		})
	}
}

// budgetVerdict runs infrastructure/azure/budget.sh and returns its exit
// code and output.
func budgetVerdict(t *testing.T, mtd, budget string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join("..", "..", "..", "infrastructure", "azure", "budget.sh"), mtd, budget)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("budget.sh: %v\n%s", err, out)
		}
		code = ee.ExitCode()
	}
	return code, string(out)
}

// The budget verdict: below → 0, at or above → 3 (the caller parks the
// cluster), and anything that is not a number → 2 (the caller fails loudly
// and parks nothing: a broken cost query must never stop production).
func TestBudgetVerdict(t *testing.T) {
	cases := []struct {
		name, mtd, budget string
		code              int
		mention           string
	}{
		{"below", "1234.5", "1500", 0, "spent 1234 of 1500 NOK this month (82%)"},
		{"at", "1500", "1500", 3, "reached"},
		{"above", "5414.17", "1500", 3, "reached"},
		{"zero spend", "0", "1500", 0, "(0%)"},
		{"query failed", "query failed", "1500", 2, "not a number"},
		{"empty", "", "1500", 2, "not a number"},
		{"zero budget", "100", "0", 2, "not a positive number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := budgetVerdict(t, tc.mtd, tc.budget)
			if code != tc.code {
				t.Errorf("exit %d, want %d\n%s", code, tc.code, out)
			}
			if !strings.Contains(out, tc.mention) {
				t.Errorf("output lacks %q:\n%s", tc.mention, out)
			}
		})
	}
}

// Park stops a Running cluster whenever one of its runs lands outside Oslo
// working hours (16:00 to 07:00) and does nothing else. GitHub fires
// schedules hours late on this repository (2026-10-20: the 14:00 UTC cron
// ran at 18:10), so the guard wants a half-hourly cron across the whole
// window and a window check, not an "is it 16:00" check; the production
// environment and group so it queues behind a deploy; no deploy script, no
// kubectl, no start (plans/cost-cap.md).
func TestParkWorkflow(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "park.yml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	var top struct {
		On struct {
			Schedule []struct {
				Cron string `yaml:"cron"`
			} `yaml:"schedule"`
			Dispatch any `yaml:"workflow_dispatch"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	crons := map[string]bool{}
	for _, s := range top.On.Schedule {
		crons[s.Cron] = true
	}
	for _, want := range []string{"7,37 14-23 * * *", "7,37 0-5 * * *"} {
		if !crons[want] {
			t.Errorf("park.yml lacks the cron %q; a single cron at 16:00 ran four hours late on 2026-10-20 and stopped nothing", want)
		}
	}
	// Off the round minutes: GitHub delays or drops scheduled runs at :00 and
	// :30 under load, and on 2026-10-21 every one of them from 16:00 to 20:00
	// Oslo was dropped.
	for c := range crons {
		minute := strings.Fields(c)[0]
		for _, m := range strings.Split(minute, ",") {
			if m == "0" || m == "30" || strings.HasPrefix(m, "*") {
				t.Errorf("park.yml cron %q fires on a round minute (%s); use minutes like 7 and 37", c, m)
			}
		}
	}
	wf := parseWorkflow(t, "park.yml")
	job, ok := wf.Jobs["park"]
	if !ok {
		t.Fatal("no park job")
	}
	if job.Environment != "production" || job.Concurrency.Group != "production" {
		t.Errorf("park job: environment %q, concurrency %q; want production/production so it never interrupts a deploy", job.Environment, job.Concurrency.Group)
	}
	clock, stop := -1, -1
	all := ""
	for i, st := range job.Steps {
		all += st.Run + "\n"
		if st.ID == "clock" {
			clock = i
			if !strings.Contains(st.Run, "TZ=Europe/Oslo date +%H") || !strings.Contains(st.Run, `"$hour" -ge 16`) || !strings.Contains(st.Run, `"$hour" -lt 7`) {
				t.Errorf("the clock step must read the Oslo hour and act on the whole 16:00-07:00 window, not one hour: %q", strings.SplitN(st.Run, "\n", 2)[0])
			}
		}
		if strings.Contains(st.Run, "az aks stop") {
			stop = i
			if !strings.Contains(st.If, "steps.clock.outputs.go == 'true'") {
				t.Errorf("the stop step is not gated on the Oslo clock: if=%q", st.If)
			}
			if !strings.Contains(st.Run, "powerState.code") {
				t.Error("the stop step must read the power state and stop only a Running cluster")
			}
		}
	}
	if clock < 0 || stop < 0 || clock >= stop {
		t.Errorf("want a step with id clock (%d) before the stop step (%d)", clock, stop)
	}
	for _, forbidden := range []string{"az aks start", "kubectl ", "helm ", "deploy-core-azure.sh", "deploy-connectors-azure.sh", "in-cluster.sh"} {
		if strings.Contains(all, forbidden) {
			t.Errorf("park.yml runs %q; it must only stop the cluster", forbidden)
		}
	}
}

// The cluster stops itself at 16:00 Oslo time from a CronJob, because GitHub
// runs Park hours late and Kubernetes runs a CronJob on time (#352). The job
// logs in with workload identity, reads vrsky-prod's power state and stops it
// only when it is Running. It must never start, change or delete anything.
func TestClusterParkCronJob(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "infrastructure", "kubernetes", "park", "cronjob.yaml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	type container struct {
		Name    string   `yaml:"name"`
		Command []string `yaml:"command"`
		Args    []string `yaml:"args"`
		Env     []struct {
			Name  string `yaml:"name"`
			Value string `yaml:"value"`
		} `yaml:"env"`
		Resources struct {
			Requests map[string]string `yaml:"requests"`
		} `yaml:"resources"`
	}
	type doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name        string         `yaml:"name"`
			Namespace   string         `yaml:"namespace"`
			Annotations map[string]any `yaml:"annotations"`
		} `yaml:"metadata"`
		Spec struct {
			Schedule                   string `yaml:"schedule"`
			TimeZone                   string `yaml:"timeZone"`
			ConcurrencyPolicy          string `yaml:"concurrencyPolicy"`
			SuccessfulJobsHistoryLimit *int   `yaml:"successfulJobsHistoryLimit"`
			FailedJobsHistoryLimit     *int   `yaml:"failedJobsHistoryLimit"`
			StartingDeadlineSeconds    *int   `yaml:"startingDeadlineSeconds"`
			JobTemplate                struct {
				Spec struct {
					ActiveDeadlineSeconds *int `yaml:"activeDeadlineSeconds"`
					Template              struct {
						Metadata struct {
							Labels map[string]any `yaml:"labels"`
						} `yaml:"metadata"`
						Spec struct {
							RestartPolicy      string      `yaml:"restartPolicy"`
							ServiceAccountName string      `yaml:"serviceAccountName"`
							InitContainers     []container `yaml:"initContainers"`
							Containers         []container `yaml:"containers"`
						} `yaml:"spec"`
					} `yaml:"template"`
				} `yaml:"spec"`
			} `yaml:"jobTemplate"`
		} `yaml:"spec"`
	}
	var cronJobs, accounts []doc
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	for {
		var d doc
		if err := dec.Decode(&d); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("park/cronjob.yaml: %v", err)
		}
		switch d.Kind {
		case "CronJob":
			cronJobs = append(cronJobs, d)
		case "ServiceAccount":
			accounts = append(accounts, d)
		}
	}
	if len(cronJobs) != 1 {
		t.Fatalf("park/cronjob.yaml holds %d CronJobs, want exactly 1", len(cronJobs))
	}
	cj := cronJobs[0]

	for _, f := range []struct{ field, got, want, why string }{
		{"schedule", cj.Spec.Schedule, "0 16 * * *", "the cluster stops at 16:00"},
		{"timeZone", cj.Spec.TimeZone, "Europe/Oslo", "read in UTC, the stop lands at 17:00 or 18:00 Oslo"},
		{"concurrencyPolicy", cj.Spec.ConcurrencyPolicy, "Forbid", "two runs must never race to stop the same cluster"},
	} {
		if f.got != f.want {
			t.Errorf("CronJob %s is %q, want %q: %s", f.field, f.got, f.want, f.why)
		}
	}
	for _, h := range []struct {
		field string
		v     *int
	}{
		{"successfulJobsHistoryLimit", cj.Spec.SuccessfulJobsHistoryLimit},
		{"failedJobsHistoryLimit", cj.Spec.FailedJobsHistoryLimit},
	} {
		if h.v == nil || *h.v < 1 || *h.v > 5 {
			t.Errorf("CronJob %s must be set between 1 and 5 so a few runs stay readable and finished pods do not pile up", h.field)
		}
	}
	for _, d := range []struct {
		field string
		v     *int
		why   string
	}{
		{"spec.startingDeadlineSeconds", cj.Spec.StartingDeadlineSeconds,
			"without it a cluster started the next morning runs the missed 16:00 job at once and stops itself at 08:00"},
		{"spec.jobTemplate.spec.activeDeadlineSeconds", cj.Spec.JobTemplate.Spec.ActiveDeadlineSeconds,
			"a run left over across a stop must be failed when the cluster starts, not retried"},
	} {
		if d.v == nil || *d.v < 1 || *d.v > 900 {
			t.Errorf("CronJob %s must be set and at most 900: %s", d.field, d.why)
		}
	}

	pod := cj.Spec.JobTemplate.Spec.Template
	if pod.Spec.RestartPolicy != "Never" {
		t.Errorf("pod restartPolicy is %q, want Never: a failed stop is retried by the Job's backoff, not by restarting az in place", pod.Spec.RestartPolicy)
	}
	// A string, not a YAML bool: Kubernetes rejects a label value that is not a string.
	if v, ok := pod.Metadata.Labels["azure.workload.identity/use"].(string); !ok || v != "true" {
		t.Errorf(`pod template label azure.workload.identity/use is %#v, want the string "true": without it the webhook mounts no token and az login fails`, pod.Metadata.Labels["azure.workload.identity/use"])
	}

	sa := pod.Spec.ServiceAccountName
	found := false
	for _, a := range accounts {
		if a.Metadata.Name != sa || a.Metadata.Namespace != cj.Metadata.Namespace {
			continue
		}
		found = true
		if id, _ := a.Metadata.Annotations["azure.workload.identity/client-id"].(string); strings.TrimSpace(id) == "" {
			t.Errorf("ServiceAccount %s has no azure.workload.identity/client-id annotation; it names the managed identity the job logs in as", sa)
		}
	}
	if sa == "" || !found {
		t.Errorf("pod serviceAccountName %q names no ServiceAccount in this file in namespace %q; the federated credential trusts that one account", sa, cj.Metadata.Namespace)
	}

	var script strings.Builder
	env := map[string]string{}
	containers := append(append([]container{}, pod.Spec.InitContainers...), pod.Spec.Containers...)
	if len(pod.Spec.Containers) == 0 {
		t.Fatal("the CronJob's pod has no containers")
	}
	for _, c := range containers {
		cpu, mem := c.Resources.Requests["cpu"], c.Resources.Requests["memory"]
		if cpu == "" || mem == "" {
			t.Errorf("container %s requests cpu=%q memory=%q; both must be set so the CPU budget counts it and the scheduler reserves room", c.Name, cpu, mem)
		}
		script.WriteString(strings.Join(c.Command, "\n") + "\n" + strings.Join(c.Args, "\n") + "\n")
		for _, e := range c.Env {
			env[e.Name] = e.Value
		}
	}
	// Shell line continuations would split "az aks stop \" from its flags; join them.
	// Then put the container's env values in for $NAME and ${NAME}, and drop
	// quotes, so `-n "$CLUSTER"` reads as the cluster it names and a quoted
	// "az" is still az.
	text := strings.ReplaceAll(script.String(), "\\\n", " ")
	text = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`).ReplaceAllStringFunc(text, func(ref string) string {
		if v, ok := env[strings.Trim(ref, "${}")]; ok {
			return v
		}
		return ref
	})
	text = strings.NewReplacer(`"`, "", "'", "").Replace(text)

	allowed := map[string]bool{"login": true, "aks show": true, "aks stop": true}
	seen := map[string]int{}
	azCall := regexp.MustCompile(`(?:^|[^\w.-])az[ \t]+(\S+)(?:[ \t]+(\S+))?`)
	for _, m := range azCall.FindAllStringSubmatch(text, -1) {
		verb := m[1]
		if verb == "aks" {
			verb += " " + m[2]
		}
		seen[verb]++
		if !allowed[verb] {
			t.Errorf("the job runs `az %s`; it may only run az login, az aks show and az aks stop", verb)
		}
	}
	for _, want := range []string{"aks show", "aks stop"} {
		if seen[want] == 0 {
			t.Errorf("the job never runs `az %s`; it must read the power state and then stop the cluster", want)
		}
	}
	// Every az aks call names the production cluster, its resource group and
	// its subscription, and the stop never waits.
	aksCall := regexp.MustCompile(`(?:^|[^\w.-])az[ \t]+aks[ \t]+(\S+)`)
	target := map[string]string{"-n": "vrsky-prod", "-g": "vrsky-prod", "--subscription": "8c020b54-b237-4d6e-82ba-190f6a415d1d"}
	long := map[string]string{"--name": "-n", "--resource-group": "-g"}
	for _, line := range strings.Split(text, "\n") {
		m := aksCall.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] == "stop" && !strings.Contains(line, "--no-wait") {
			t.Errorf("`az aks stop` runs without --no-wait: %q; the pod runs on the nodes it stops and would be killed mid-call", strings.TrimSpace(line))
		}
		got := map[string]string{}
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			flag := fields[i]
			if short, ok := long[flag]; ok {
				flag = short
			}
			if _, ok := target[flag]; ok {
				got[flag] = fields[i+1]
			}
		}
		for flag, want := range target {
			if got[flag] != want {
				t.Errorf("`az aks %s` has %s %q, want %q: the job stops the production cluster and nothing else", m[1], flag, got[flag], want)
			}
		}
	}
	if !strings.Contains(text, "powerState") || !strings.Contains(text, "Running") {
		t.Error("the job must read powerState and stop the cluster only when it is Running")
	}
	for _, tool := range []string{"kubectl", "helm"} {
		if regexp.MustCompile(`\b` + tool + `\b`).MatchString(text) {
			t.Errorf("the job runs %s; it must only stop the cluster through az", tool)
		}
	}
}

// runSmokeAlerts runs infrastructure/azure/smoke-prod.sh with no consumers
// against a local server for the two URL probes and a stub kubectl whose
// rules API reports a firing VRSky alert for the first firingReads reads and
// nothing after. It returns the output, how many reads the script made, and
// the script's error.
func runSmokeAlerts(t *testing.T, firingReads int) (string, int, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	firing := filepath.Join(dir, "firing.json")
	clear := filepath.Join(dir, "clear.json")
	counter := filepath.Join(dir, "reads")
	write := func(p, body string) {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(firing, rulesDoc([4]string{"vrsky-platform", "ConnectorUnavailable", "warning", "firing"}))
	write(clear, rulesDoc([4]string{"vrsky-platform", "ConnectorUnavailable", "warning", ""}))
	write(counter, "0")
	stub := "#!/usr/bin/env bash\n" +
		"[ \"$1 $2\" = \"get --raw\" ] || exit 2\n" +
		"n=$(( $(cat \"$READS\") + 1 )); echo \"$n\" > \"$READS\"\n" +
		"if [ \"$n\" -le \"$FIRING_READS\" ]; then cat \"$FIRING\"; else cat \"$CLEAR\"; fi\n"
	kubectl := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(kubectl, []byte(stub), 0o700); err != nil { //nolint:gosec // an executable stub in a temp dir
		t.Fatal(err)
	}

	cmd := exec.Command("bash", filepath.Join("..", "..", "..", "infrastructure", "azure", "smoke-prod.sh"))
	cmd.Env = append(os.Environ(),
		"PUBLIC_URL="+srv.URL, "KUBECTL="+kubectl,
		"SMOKE_ALERT_WINDOW=2", "SMOKE_ALERT_EVERY=1",
		"READS="+counter, "FIRING="+firing, "CLEAR="+clear, fmt.Sprintf("FIRING_READS=%d", firingReads))
	out, err := cmd.CombinedOutput()
	raw, rerr := os.ReadFile(counter) //nolint:gosec // the test's own temp file
	if rerr != nil {
		t.Fatal(rerr)
	}
	reads, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return string(out), reads, err
}

// A deploy that fixes a firing alert applies the rule seconds before the
// smoke check reads alerts, and Prometheus serves the old rule until it
// reloads (run 39764021090, 2026-10-22). The check re-reads for a window and
// fails only on an alert that is still firing at its end
// (plans/smoke-rule-reload.md).
func TestSmokeProdWaitsOutARuleReload(t *testing.T) {
	t.Run("an alert that clears within the window passes", func(t *testing.T) {
		out, reads, err := runSmokeAlerts(t, 1)
		if err != nil {
			t.Fatalf("smoke failed on an alert that cleared on the second read: %v\n%s", err, out)
		}
		if !strings.Contains(out, "re-checking") || !strings.Contains(out, "smoke: ok") || reads != 2 {
			t.Errorf("want one re-check, then ok after 2 reads; got %d reads:\n%s", reads, out)
		}
	})
	t.Run("an alert still firing at the end fails", func(t *testing.T) {
		out, reads, err := runSmokeAlerts(t, 1000)
		if err == nil {
			t.Fatalf("smoke passed with an alert firing on every read:\n%s", out)
		}
		if !strings.Contains(out, "alerts firing: ConnectorUnavailable") || !strings.Contains(out, "smoke: FAILED") || reads < 3 {
			t.Errorf("want it to keep reading through the window, then fail; got %d reads:\n%s", reads, out)
		}
	})
	t.Run("nothing firing reads once", func(t *testing.T) {
		out, reads, err := runSmokeAlerts(t, 0)
		if err != nil || strings.Contains(out, "re-checking") || reads != 1 {
			t.Errorf("want ok after a single read; got err=%v, %d reads:\n%s", err, reads, out)
		}
	})
}

// The persistent volumes production declares must add up to under a budget,
// and NATS's JetStream store limit must sit below its volume. A provisioned
// disk is billed every hour whether the cluster runs or is parked; on
// 2026-10-22 the parked floor was ~1 450 NOK a month, ~790 of it in NATS and
// MinIO disks of 100 GiB holding a few MB, sized for KV buckets nothing used
// (plans/disk-costs.md). Raise the budget only with a measured reason.
// Not counted: Azure Files (vrsky-files), billed by what is stored.
func TestPersistentVolumesStayWithinBudget(t *testing.T) {
	const budgetGiB = 120
	root := filepath.Join("..", "..", "..", "infrastructure", "kubernetes")
	gib := func(v string) int {
		v = strings.TrimSpace(v)
		n, err := strconv.Atoi(strings.TrimSuffix(v, "Gi"))
		if err != nil || !strings.HasSuffix(v, "Gi") {
			t.Fatalf("volume size %q is not in Gi", v)
		}
		return n
	}
	type item struct {
		name string
		g    int
	}
	var items []item
	natsVolume := 0

	// StatefulSet templates × replicas, and standalone claims.
	for _, rel := range []string{"platform-nats/statefulset.yaml", "postgresql/statefulset.yaml", "minio/deployment.yaml"} {
		body, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // fixed paths inside the repo
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(body)))
		for {
			var doc struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
				Spec struct {
					Replicas  *int `yaml:"replicas"`
					Resources struct {
						Requests map[string]string `yaml:"requests"`
					} `yaml:"resources"`
					VolumeClaimTemplates []struct {
						Spec struct {
							Resources struct {
								Requests map[string]string `yaml:"requests"`
							} `yaml:"resources"`
						} `yaml:"spec"`
					} `yaml:"volumeClaimTemplates"`
				} `yaml:"spec"`
			}
			if err := dec.Decode(&doc); err != nil {
				break
			}
			switch doc.Kind {
			case "StatefulSet":
				replicas := 1
				if doc.Spec.Replicas != nil {
					replicas = *doc.Spec.Replicas
				}
				for _, vct := range doc.Spec.VolumeClaimTemplates {
					g := gib(vct.Spec.Resources.Requests["storage"])
					items = append(items, item{fmt.Sprintf("%s x%d", doc.Metadata.Name, replicas), g * replicas})
					if doc.Metadata.Name == "nats-platform" {
						natsVolume = g
					}
				}
			case "PersistentVolumeClaim":
				items = append(items, item{doc.Metadata.Name, gib(doc.Spec.Resources.Requests["storage"])})
			}
		}
	}

	// The monitoring stack's volumes, as the Azure overlays size them.
	sizeLine := regexp.MustCompile(`(?m)^\s+(?:storage|size):\s*(\d+Gi)\s*$`)
	for _, rel := range []string{"monitoring/prometheus-values.azure.yaml", "monitoring/grafana-values.azure.yaml"} {
		body, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // fixed paths inside the repo
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range sizeLine.FindAllStringSubmatch(string(body), -1) {
			items = append(items, item{rel, gib(m[1])})
		}
	}

	if natsVolume == 0 || len(items) < 6 {
		t.Fatalf("found %d volumes and no NATS template (%v); the manifest layout probably moved", len(items), items)
	}

	// JetStream refuses to start (or to create streams) once its reservations
	// exceed the disk; max_file must leave room on the volume.
	cfg, err := os.ReadFile(filepath.Join(root, "platform-nats", "configmap.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	mf := regexp.MustCompile(`max_file:\s*(\d+)G\b`).FindStringSubmatch(string(cfg))
	if mf == nil {
		t.Fatal("platform-nats/configmap.yaml has no max_file in G")
	}
	if maxFile, _ := strconv.Atoi(mf[1]); maxFile >= natsVolume {
		t.Errorf("NATS max_file is %dG but each volume is %dGi; JetStream would plan for space the disk does not have", maxFile, natsVolume)
	}

	total := 0
	for _, it := range items {
		total += it.g
	}
	sort.Slice(items, func(i, j int) bool { return items[i].g > items[j].g })
	var top []string
	for _, it := range items {
		top = append(top, fmt.Sprintf("%s=%dGi", it.name, it.g))
	}
	if total > budgetGiB {
		t.Fatalf("persistent volumes add up to %dGi, over the %dGi budget; largest: %s", total, budgetGiB, strings.Join(top, ", "))
	}
	t.Logf("persistent volumes: %dGi of %dGi; %s", total, budgetGiB, strings.Join(top, ", "))
}

// --- management-DB backup (#395) --------------------------------------------

type backupContainer struct {
	Name            string `yaml:"name"`
	Image           string `yaml:"image"`
	ImagePullPolicy string `yaml:"imagePullPolicy"`
	EnvFrom         []struct {
		SecretRef *struct {
			Name string `yaml:"name"`
		} `yaml:"secretRef"`
	} `yaml:"envFrom"`
	Env []struct {
		Name      string  `yaml:"name"`
		Value     *string `yaml:"value"`
		ValueFrom any     `yaml:"valueFrom"`
	} `yaml:"env"`
	SecurityContext struct {
		ReadOnlyRootFilesystem   *bool `yaml:"readOnlyRootFilesystem"`
		AllowPrivilegeEscalation *bool `yaml:"allowPrivilegeEscalation"`
		Capabilities             struct {
			Drop []string `yaml:"drop"`
		} `yaml:"capabilities"`
	} `yaml:"securityContext"`
	VolumeMounts []struct {
		Name      string `yaml:"name"`
		MountPath string `yaml:"mountPath"`
	} `yaml:"volumeMounts"`
}

// backupCronJob is the part of infrastructure/kubernetes/backup/cronjob.yaml
// the guards look at.
type backupCronJob struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Spec struct {
		Schedule                   string `yaml:"schedule"`
		TimeZone                   string `yaml:"timeZone"`
		StartingDeadlineSeconds    *int   `yaml:"startingDeadlineSeconds"`
		ConcurrencyPolicy          string `yaml:"concurrencyPolicy"`
		Suspend                    *bool  `yaml:"suspend"`
		SuccessfulJobsHistoryLimit *int   `yaml:"successfulJobsHistoryLimit"`
		FailedJobsHistoryLimit     *int   `yaml:"failedJobsHistoryLimit"`
		JobTemplate                struct {
			Spec struct {
				TTLSecondsAfterFinished *int `yaml:"ttlSecondsAfterFinished"`
				Template                struct {
					Spec struct {
						AutomountServiceAccountToken *bool `yaml:"automountServiceAccountToken"`
						ImagePullSecrets             []struct {
							Name string `yaml:"name"`
						} `yaml:"imagePullSecrets"`
						SecurityContext struct {
							RunAsNonRoot   *bool  `yaml:"runAsNonRoot"`
							RunAsUser      *int64 `yaml:"runAsUser"`
							RunAsGroup     *int64 `yaml:"runAsGroup"`
							SeccompProfile struct {
								Type string `yaml:"type"`
							} `yaml:"seccompProfile"`
						} `yaml:"securityContext"`
						Volumes []struct {
							Name     string `yaml:"name"`
							EmptyDir any    `yaml:"emptyDir"`
						} `yaml:"volumes"`
						InitContainers []backupContainer `yaml:"initContainers"`
						Containers     []backupContainer `yaml:"containers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		} `yaml:"jobTemplate"`
	} `yaml:"spec"`
}

// The backup CronJob (#395, plans/issue-395-backup-layer2.md criterion 2).
// It runs pg_dump against the management database with the platform's
// ENCRYPTION_KEY in its environment, so it gets the tightest pod the image
// allows, takes every setting from the Secret the human creates, and carries
// no credential in the repo. Its schedule settings decide what a parked
// cluster does: a noon run missed while stopped must not fire hours later at
// start-up, and two runs must never overlap.
func TestBackupCronJobManifest(t *testing.T) {
	path := filepath.Join("..", "..", "..", "infrastructure", "kubernetes", "backup", "cronjob.yaml")
	raw, err := os.ReadFile(path) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatalf("read backup/cronjob.yaml: %v", err)
	}

	var cjs []backupCronJob
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	for {
		var node yaml.Node
		if err := dec.Decode(&node); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("parse backup/cronjob.yaml: %v", err)
			}
			break
		}
		var head struct {
			Kind string `yaml:"kind"`
		}
		if err := node.Decode(&head); err != nil {
			t.Fatalf("parse backup/cronjob.yaml: %v", err)
		}
		switch head.Kind {
		case "Secret":
			t.Error("backup/cronjob.yaml contains a Secret; vrsky-backup-config is created by hand, never from the repo")
		case "CronJob":
			var cj backupCronJob
			if err := node.Decode(&cj); err != nil {
				t.Fatalf("parse the CronJob: %v", err)
			}
			cjs = append(cjs, cj)
		}
	}
	if len(cjs) != 1 {
		t.Fatalf("backup/cronjob.yaml has %d CronJobs, want exactly 1", len(cjs))
	}
	cj := cjs[0]
	spec := cj.Spec
	pod := spec.JobTemplate.Spec.Template.Spec

	if cj.Metadata.Name != "vrsky-mgmt-backup" || cj.Metadata.Namespace != "vrsky-database" {
		t.Errorf("CronJob is %s/%s, want vrsky-database/vrsky-mgmt-backup (the alert and the deploy script name it)",
			cj.Metadata.Namespace, cj.Metadata.Name)
	}
	if spec.Schedule != "0 12 * * 1-5" {
		t.Errorf("schedule %q, want \"0 12 * * 1-5\" (noon on weekdays, while the cluster runs)", spec.Schedule)
	}
	if spec.TimeZone != "Europe/Oslo" {
		t.Errorf("timeZone %q, want Europe/Oslo; without it the schedule is the controller's UTC", spec.TimeZone)
	}
	// At least 4h, so a cluster started any time before the 16:00 park still
	// runs that day's noon dump (a shorter deadline let a daily late start go
	// without dumps, review of #404). Under 19h, so a start at 07:00 doesn't
	// run the previous day's.
	if d := spec.StartingDeadlineSeconds; d == nil || *d < 4*3600 || *d >= 19*3600 {
		t.Errorf("startingDeadlineSeconds %v, want set and in [14400, 68400): a start before 16:00 must still "+
			"run the noon dump, and a start the next morning must not", d)
	}
	if spec.ConcurrencyPolicy != "Forbid" {
		t.Errorf("concurrencyPolicy %q, want Forbid: two dumps at once double the load and race on the object name", spec.ConcurrencyPolicy)
	}
	if spec.Suspend == nil || !*spec.Suspend {
		t.Errorf("suspend %v in the file, want true: the deploy script unsuspends it only when vrsky-backup-config is complete", spec.Suspend)
	}
	// The alert counts failed Jobs since the last success; Kubernetes keeps
	// one failed Job by default, so two failures could never be seen.
	if l := spec.FailedJobsHistoryLimit; l == nil || *l < 2 {
		t.Errorf("failedJobsHistoryLimit %v, want at least 2: MgmtBackupMissing counts two failed Jobs, the default keeps one", l)
	}
	if l := spec.SuccessfulJobsHistoryLimit; l != nil && *l < 1 {
		t.Errorf("successfulJobsHistoryLimit %d: with no successful Job kept, old failures look current to the alert", *l)
	}
	if ttl := spec.JobTemplate.Spec.TTLSecondsAfterFinished; ttl != nil && *ttl < 7*24*3600 {
		t.Errorf("ttlSecondsAfterFinished %d deletes finished Jobs before the alert can count two weekdays of failures", *ttl)
	}

	pullSecret := false
	for _, s := range pod.ImagePullSecrets {
		if s.Name == "acr-pull" {
			pullSecret = true
		}
	}
	if !pullSecret {
		t.Error("the pod has no imagePullSecrets acr-pull; it could not pull from ACR in vrsky-database")
	}
	sc := pod.SecurityContext
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("pod securityContext lacks runAsNonRoot: true")
	}
	// uid/gid 70 is the alpine postgres user, the one the DR drill proves
	// pg_dump works as with a read-only root filesystem.
	if sc.RunAsUser == nil || *sc.RunAsUser != 70 {
		t.Errorf("pod runAsUser %v, want 70 (the image's postgres user, as in the DR drill)", sc.RunAsUser)
	}
	if sc.RunAsGroup == nil || *sc.RunAsGroup != 70 {
		t.Errorf("pod runAsGroup %v, want 70", sc.RunAsGroup)
	}
	if sc.SeccompProfile.Type != "RuntimeDefault" {
		t.Errorf("pod seccompProfile %q, want RuntimeDefault", sc.SeccompProfile.Type)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Error("the pod mounts a service-account token; the backup never talks to the API server")
	}

	if len(pod.Containers) == 0 {
		t.Fatal("the CronJob has no containers")
	}
	emptyDirs := map[string]bool{}
	for _, v := range pod.Volumes {
		if v.EmptyDir != nil {
			emptyDirs[v.Name] = true
		}
	}
	image := regexp.MustCompile(`^vrskyprodacr\.azurecr\.io/vrsky/vrsky-cli(:[A-Za-z0-9._-]+)?(@sha256:[0-9a-f]{64})?$`)
	credential := regexp.MustCompile(`(?i)(KEY|PASSWORD|PASSWD|SECRET|TOKEN|URL|URI|DSN|CONNECTION_STRING|CREDENTIAL)`)
	for _, c := range append(append([]backupContainer{}, pod.InitContainers...), pod.Containers...) {
		if !image.MatchString(c.Image) {
			t.Errorf("container %s image %q, want vrskyprodacr.azurecr.io/vrsky/vrsky-cli (what build-push-acr.sh pushes)", c.Name, c.Image)
		}
		if c.ImagePullPolicy != "Always" {
			t.Errorf("container %s imagePullPolicy %q, want Always so each run takes the CLI ACR has now", c.Name, c.ImagePullPolicy)
		}
		fromSecret := false
		for _, ef := range c.EnvFrom {
			if ef.SecretRef != nil && ef.SecretRef.Name == "vrsky-backup-config" {
				fromSecret = true
			}
		}
		if !fromSecret {
			t.Errorf("container %s has no envFrom secretRef vrsky-backup-config", c.Name)
		}
		for _, e := range c.Env {
			if e.Value != nil && credential.MatchString(e.Name) {
				t.Errorf("container %s sets %s inline; credentials come from vrsky-backup-config only", c.Name, e.Name)
			}
		}
		csc := c.SecurityContext
		if csc.ReadOnlyRootFilesystem == nil || !*csc.ReadOnlyRootFilesystem {
			t.Errorf("container %s: readOnlyRootFilesystem is not true", c.Name)
		}
		if csc.AllowPrivilegeEscalation == nil || *csc.AllowPrivilegeEscalation {
			t.Errorf("container %s: allowPrivilegeEscalation is not false", c.Name)
		}
		dropsAll := false
		for _, d := range csc.Capabilities.Drop {
			if d == "ALL" {
				dropsAll = true
			}
		}
		if !dropsAll {
			t.Errorf("container %s does not drop ALL capabilities", c.Name)
		}
		tmp := false
		for _, m := range c.VolumeMounts {
			if m.MountPath == "/tmp" && emptyDirs[m.Name] {
				tmp = true
			}
		}
		if !tmp {
			t.Errorf("container %s has no emptyDir at /tmp; with a read-only root the dump has nowhere to write", c.Name)
		}
	}

	for _, leak := range []*regexp.Regexp{
		regexp.MustCompile(`postgres(ql)?://[^\s/@:'"]+:[^\s/@'"]+@`),
		regexp.MustCompile(`(?i)AccountKey=`),
	} {
		if m := leak.Find(raw); m != nil {
			t.Errorf("backup/cronjob.yaml contains what looks like a credential: %q", m)
		}
	}
}

// backupWorkflow is the part of a workflow the backup wiring guard reads;
// workflowFile has no outputs or env.
type backupWorkflow struct {
	Jobs map[string]struct {
		Outputs map[string]any `yaml:"outputs"`
		Env     map[string]any `yaml:"env"`
		Steps   []struct {
			Name string         `yaml:"name"`
			If   string         `yaml:"if"`
			Run  string         `yaml:"run"`
			Env  map[string]any `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// The backup step runs after the smoke check in both workflows: a failing
// backup apply must not stop the smoke check from reporting on the deploy
// (review of #404).
func TestBackupStepRunsAfterSmokeCheck(t *testing.T) {
	for _, c := range []struct{ file, job string }{{"build-push.yml", "deploy"}, {"catch-up.yml", "catch-up"}} {
		job, ok := parseWorkflow(t, c.file).Jobs[c.job]
		if !ok {
			t.Fatalf("%s: no %s job", c.file, c.job)
		}
		smoke, backup := -1, -1
		for i, st := range job.Steps {
			if strings.Contains(st.Run, "smoke-prod.sh") {
				smoke = i
			}
			if strings.Contains(st.Run, "deploy-backup-azure.sh") {
				backup = i
			}
		}
		if smoke < 0 || backup < 0 {
			t.Fatalf("%s: smoke step at %d, backup step at %d; both must exist", c.file, smoke, backup)
		}
		if backup < smoke {
			t.Errorf("%s: the backup step (%d) runs before the smoke check (%d)", c.file, backup, smoke)
		}
	}
}

// A merge that touches the backup reaches the cluster the way every other
// in-cluster change does (#395 criterion 4): the plan job says so, the
// staged files include the manifest, and the deploy job runs the script
// through in-cluster.sh, only when the cluster is up and only when the
// backup changed. Catch up runs it too, so a merge made while parked lands
// at the next start and a Secret created by hand unsuspends the CronJob.
func TestBackupDeployWiring(t *testing.T) {
	cpBackup := regexp.MustCompile(`(?m)^.*\bcp\b.*\binfrastructure/kubernetes/backup(/|\s|$)`)
	for _, tc := range []struct {
		workflow, job string
		gated         bool
	}{
		{"build-push.yml", "deploy", true},
		{"catch-up.yml", "catch-up", false},
	} {
		t.Run(tc.workflow, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", tc.workflow)) //nolint:gosec // fixed path inside the repo
			if err != nil {
				t.Fatal(err)
			}
			var wf backupWorkflow
			if err := yaml.Unmarshal(body, &wf); err != nil {
				t.Fatalf("parse %s: %v", tc.workflow, err)
			}
			job, ok := wf.Jobs[tc.job]
			if !ok {
				t.Fatalf("%s has no %s job", tc.workflow, tc.job)
			}
			stageAt, backupAt := -1, -1
			for i, st := range job.Steps {
				if strings.Contains(st.Run, "mkdir -p stage") {
					stageAt = i
					if !cpBackup.MatchString(st.Run) {
						t.Errorf("the stage step %q does not copy infrastructure/kubernetes/backup; the script would find no manifest in-cluster", st.Name)
					}
				}
				if strings.Contains(st.Run, "in-cluster.sh") && strings.Contains(st.Run, "infrastructure/azure/deploy-backup-azure.sh") {
					backupAt = i
					if tc.gated {
						if !strings.Contains(st.If, "steps.cluster.outputs.state == 'Running'") {
							t.Errorf("backup step %q is not gated on the cluster running: if=%q", st.Name, st.If)
						}
						flag := strings.Contains(st.If, "needs.plan.outputs.backup == 'true'")
						if strings.Contains(st.If, "env.BACKUP == 'true'") {
							src := fmt.Sprint(job.Env["BACKUP"])
							if v, ok := st.Env["BACKUP"]; ok {
								src = fmt.Sprint(v)
							}
							flag = flag || strings.Contains(src, "needs.plan.outputs.backup")
						}
						if !flag {
							t.Errorf("backup step %q is not gated on the plan's backup output (env BACKUP from needs.plan.outputs.backup == 'true'): if=%q", st.Name, st.If)
						}
					}
				}
			}
			if stageAt < 0 {
				t.Fatalf("%s job %s has no stage step (mkdir -p stage)", tc.workflow, tc.job)
			}
			if backupAt < 0 {
				t.Fatalf("%s job %s never runs deploy-backup-azure.sh through in-cluster.sh", tc.workflow, tc.job)
			}
			if backupAt < stageAt {
				t.Errorf("%s runs deploy-backup-azure.sh before the files are staged", tc.workflow)
			}
		})
	}

	// The plan job publishes the flag the deploy step reads.
	body, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "build-push.yml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	var wf backupWorkflow
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatal(err)
	}
	plan := wf.Jobs["plan"]
	if out := fmt.Sprint(plan.Outputs["backup"]); plan.Outputs["backup"] == nil || !strings.Contains(out, "backup") {
		t.Errorf("the plan job has no `backup` output (got %q)", out)
	}
	writes := false
	for _, st := range plan.Steps {
		if strings.Contains(st.Run, "backup=") {
			writes = true
		}
	}
	if !writes {
		t.Error("no plan step writes backup=… to GITHUB_OUTPUT")
	}
}

// deployBackupRun is one run of deploy-backup-azure.sh against the stub.
type deployBackupRun struct {
	out    string
	err    error
	calls  [][]string     // argv per kubectl call, in order
	stdins map[int]string // what each `apply -f -` call (by index) read
}

// stubKubectl answers the handful of calls deploy-backup-azure.sh makes,
// logs every argv (one call per line: its PID, then the args, tab-separated)
// and keeps what each `apply -f -` read on stdin in stdin-<PID>. Both ends
// of a `kubectl get | jq | kubectl apply -f -` pipeline run at once, so each
// line goes out in a single write and stdin is keyed by PID, not by line.
const stubKubectl = `#!/usr/bin/env bash
d="$STUB_DIR"
line="$$"
for a in "$@"; do line+=$'\t'"$a"; done
printf '%s\n' "$line" >> "$d/calls.log"
verb=""
for a in "$@"; do
  case "$a" in
    get|apply|patch|create|delete|edit|label|annotate|replace|set|rollout|wait|describe|logs|scale) verb="$a"; break ;;
  esac
done
args=" $* "
ignore=0; [[ "$args" == *" --ignore-not-found"* ]] && ignore=1
notfound() { [ "$ignore" = 1 ] && exit 0; echo "Error from server (NotFound): secrets \"$1\" not found" >&2; exit 1; }
case "$verb" in
  get)
    if [[ "$args" == *acr-pull* ]]; then
      if [[ "$args" == *vrsky-platform* && -f "$d/acr.json" ]]; then cat "$d/acr.json"; exit 0; fi
      notfound acr-pull
    fi
    if [[ "$args" == *vrsky-backup-config* ]]; then
      if [[ "$args" == *vrsky-database* && -f "$d/config.json" ]]; then cat "$d/config.json"; exit 0; fi
      notfound vrsky-backup-config
    fi
    exit 0 ;;
  apply)
    if [[ "$args" == *" -f - "* || "$args" == *" --filename=- "* || "$args" == *" -f=- "* || "$args" == *" --filename - "* ]]; then
      cat > "$d/stdin-$$"
      if grep -q 'kind: CronJob' "$d/stdin-$$" && [ -f "$d/fail-cronjob-apply" ]; then
        echo 'error: the server rejected the CronJob' >&2; exit 1
      fi
      exit 0
    fi
    if [[ "$args" == *kubernetes/backup* && -f "$d/fail-cronjob-apply" ]]; then
      echo 'error: the server rejected the CronJob' >&2; exit 1
    fi
    exit 0 ;;
  patch)
    # patch --local -f <file> -p '{"spec":{"suspend":X}}' -o yaml: the file
    # with suspend set to X, as kubectl would print it. Touches no cluster.
    if [[ "$args" == *" --local "* ]]; then
      f=""; p=""; prev=""
      for a in "$@"; do
        case "$prev" in -f|--filename) f="$a" ;; -p|--patch) p="$a" ;; esac
        prev="$a"
      done
      s="$(printf '%s' "$p" | grep -oE '"suspend" *: *(true|false)' | grep -oE 'true|false')"
      sed -E "s/^(  suspend:) (true|false)/\1 $s/" "$f"
    fi
    exit 0 ;;
  *) exit 0 ;;
esac
`

func runDeployBackup(t *testing.T, acr bool, config map[string]string, failCronApply bool) deployBackupRun {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "infrastructure", "azure", "deploy-backup-azure.sh")); err != nil {
		t.Fatalf("deploy-backup-azure.sh: %v", err)
	}
	dir := t.TempDir()
	write := func(name, body string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("kubectl", stubKubectl, 0o755) //nolint:gosec // the stub must be executable
	if acr {
		write("acr.json", `{"apiVersion":"v1","kind":"Secret","type":"kubernetes.io/dockerconfigjson",`+
			`"metadata":{"name":"acr-pull","namespace":"vrsky-platform","uid":"6f1c2a9e-0000-4000-8000-000000000001",`+
			`"resourceVersion":"123456","creationTimestamp":"2026-09-20T10:00:00Z"},`+
			`"data":{".dockerconfigjson":"e30="}}`, 0o600)
	}
	if config != nil {
		data := map[string]string{}
		for k, v := range config {
			data[k] = base64.StdEncoding.EncodeToString([]byte(v))
		}
		secret, _ := json.Marshal(map[string]any{
			"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
			"metadata": map[string]any{
				"name": "vrsky-backup-config", "namespace": "vrsky-database",
				"uid": "6f1c2a9e-0000-4000-8000-000000000002", "resourceVersion": "654321",
				"creationTimestamp": "2026-10-28T09:00:00Z",
			},
			"data": data,
		})
		write("config.json", string(secret), 0o600)
	}
	if failCronApply {
		write("fail-cronjob-apply", "", 0o600)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "infrastructure/azure/deploy-backup-azure.sh")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "STUB_DIR="+dir)
	out, runErr := cmd.CombinedOutput()

	run := deployBackupRun{out: string(out), err: runErr, stdins: map[int]string{}}
	log, _ := os.ReadFile(filepath.Join(dir, "calls.log")) //nolint:gosec // our own temp dir
	for _, line := range strings.Split(strings.TrimRight(string(log), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if b, err := os.ReadFile(filepath.Join(dir, "stdin-"+f[0])); err == nil { //nolint:gosec // our own temp dir
			run.stdins[len(run.calls)] = string(b)
		}
		run.calls = append(run.calls, f[1:])
	}
	return run
}

// kubectlVerb is the first argument that is a kubectl subcommand.
func kubectlVerb(argv []string) string {
	for _, a := range argv {
		switch a {
		case "get", "apply", "patch", "create", "delete", "edit", "label", "annotate", "replace", "set", "rollout", "wait", "describe", "logs", "scale":
			return a
		}
	}
	return ""
}

// The backup deploy script (#395 criterion 3), driven against a stub kubectl
// because it only ever runs in-cluster. What matters is the outcome: the
// CronJob ends up suspended unless vrsky-backup-config has every required
// key; acr-pull lands in vrsky-database clean of the source's identity;
// the Secret the human made is read, never changed, and its values never
// reach the log (the deploy log is public to anyone with repo access).
func TestDeployBackupScript(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH; the in-cluster runtime has it, this machine does not")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	const sentinel = "SENTINEL-DO-NOT-PRINT"
	full := map[string]string{
		"BACKUP_DB_URL":                  "postgres://vrsky:" + sentinel + "@vrsky-postgresql.vrsky-database:5432/vrsky?sslmode=disable",
		"ENCRYPTION_KEY":                 sentinel,
		"BACKUP_PROVIDER":                "azure",
		"BACKUP_BUCKET":                  "vrsky-backups",
		"BACKUP_AZURE_CONNECTION_STRING": "DefaultEndpointsProtocol=https;AccountName=vrskybackup;AccountKey=" + sentinel + ";EndpointSuffix=core.windows.net",
		"BACKUP_AZURE_ACCOUNT_NAME":      "vrskybackup",
		"BACKUP_AZURE_ACCOUNT_KEY":       sentinel + "-account-key",
	}
	only := func(keys ...string) map[string]string {
		m := map[string]string{}
		for _, k := range keys {
			m[k] = full[k]
		}
		return m
	}
	forbidden := []string{sentinel}
	for _, k := range []string{"BACKUP_DB_URL", "ENCRYPTION_KEY", "BACKUP_AZURE_CONNECTION_STRING", "BACKUP_AZURE_ACCOUNT_KEY"} {
		forbidden = append(forbidden, base64.StdEncoding.EncodeToString([]byte(full[k])))
	}

	cases := []struct {
		name          string
		acr           bool
		config        map[string]string
		failCronApply bool
		wantErr       bool
		wantSuspended bool
		wantSaid      []string // the log must say why it stays suspended
	}{
		{name: "secret present", acr: true, config: full, wantSuspended: false},
		// The required keys plus either form of Azure credentials.
		{name: "required keys and a connection string", acr: true,
			config: only("BACKUP_DB_URL", "ENCRYPTION_KEY", "BACKUP_PROVIDER", "BACKUP_BUCKET", "BACKUP_AZURE_CONNECTION_STRING"), wantSuspended: false},
		{name: "required keys and account name and key", acr: true,
			config: only("BACKUP_DB_URL", "ENCRYPTION_KEY", "BACKUP_PROVIDER", "BACKUP_BUCKET", "BACKUP_AZURE_ACCOUNT_NAME", "BACKUP_AZURE_ACCOUNT_KEY"), wantSuspended: false},
		// Without credentials every run would fail at the upload.
		{name: "required keys, no Azure credentials", acr: true,
			config: only("BACKUP_DB_URL", "ENCRYPTION_KEY", "BACKUP_PROVIDER", "BACKUP_BUCKET"), wantSuspended: true,
			wantSaid: []string{"vrsky-backup-config", "BACKUP_AZURE_CONNECTION_STRING"}},
		{name: "account name without its key", acr: true,
			config: only("BACKUP_DB_URL", "ENCRYPTION_KEY", "BACKUP_PROVIDER", "BACKUP_BUCKET", "BACKUP_AZURE_ACCOUNT_NAME"), wantSuspended: true,
			wantSaid: []string{"BACKUP_AZURE_ACCOUNT_KEY"}},
		{name: "secret missing", acr: true, config: nil, wantSuspended: true, wantSaid: []string{"vrsky-backup-config"}},
		{name: "secret partial", acr: true, config: only("BACKUP_DB_URL", "ENCRYPTION_KEY"), wantSuspended: true,
			wantSaid: []string{"vrsky-backup-config", "BACKUP_PROVIDER", "BACKUP_BUCKET"}},
		{name: "no acr-pull to copy", acr: false, config: full, wantErr: true},
		{name: "CronJob apply fails", acr: true, config: full, failCronApply: true, wantErr: true},
	}
	suspendRe := regexp.MustCompile(`"suspend"\s*:\s*(true|false)`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := runDeployBackup(t, tc.acr, tc.config, tc.failCronApply)
			logs := fmt.Sprintf("output:\n%s\ncalls:\n%v", r.out, r.calls)

			// Never leak, never touch the human's Secret: true in every case.
			for _, f := range forbidden {
				if strings.Contains(r.out, f) {
					t.Errorf("the script printed a value from vrsky-backup-config (%.12s…)\n%s", f, r.out)
				}
				for _, argv := range r.calls {
					if strings.Contains(strings.Join(argv, " "), f) {
						t.Errorf("a kubectl argv carries a value from vrsky-backup-config: %v", argv)
					}
				}
			}
			for i, argv := range r.calls {
				if strings.Contains(strings.Join(argv, " "), "vrsky-backup-config") && kubectlVerb(argv) != "get" {
					t.Errorf("call %d %v does more than read vrsky-backup-config", i, argv)
				}
			}
			// The CronJob applied through stdin refers to the Secret (envFrom);
			// what must never go through is the Secret itself.
			for i, in := range r.stdins {
				dec := yaml.NewDecoder(strings.NewReader(in))
				for {
					var doc struct {
						Kind     string `yaml:"kind"`
						Metadata struct {
							Name string `yaml:"name"`
						} `yaml:"metadata"`
					}
					if err := dec.Decode(&doc); err != nil {
						break
					}
					if doc.Metadata.Name == "vrsky-backup-config" || (doc.Kind != "CronJob" && strings.Contains(in, "vrsky-backup-config")) {
						t.Errorf("call %d applies something naming vrsky-backup-config:\n%s", i, in)
					}
				}
			}

			if tc.wantErr {
				if r.err == nil {
					t.Fatalf("want a non-zero exit\n%s", logs)
				}
				return
			}
			if r.err != nil {
				t.Fatalf("exit: %v (a missing or partial Secret is not an error)\n%s", r.err, logs)
			}

			// acr-pull, copied into vrsky-database without the source's identity.
			var acrDoc map[string]any
			// Every apply of the CronJob and the suspend value it carries. A
			// file apply carries the file's suspend: true.
			type cronApply struct {
				at      int
				suspend bool
			}
			var applies []cronApply
			for i, argv := range r.calls {
				if kubectlVerb(argv) != "apply" {
					continue
				}
				for _, a := range argv {
					if strings.Contains(a, "kubernetes/backup") {
						applies = append(applies, cronApply{i, true})
					}
				}
				in, ok := r.stdins[i]
				if !ok {
					continue
				}
				dec := yaml.NewDecoder(strings.NewReader(in))
				for {
					var doc map[string]any
					if err := dec.Decode(&doc); err != nil {
						break
					}
					meta, _ := doc["metadata"].(map[string]any)
					switch {
					case doc["kind"] == "Secret" && meta["name"] == "acr-pull":
						acrDoc = doc
					case doc["kind"] == "CronJob" && meta["name"] == "vrsky-mgmt-backup":
						spec, _ := doc["spec"].(map[string]any)
						s, _ := spec["suspend"].(bool) // absent means false
						applies = append(applies, cronApply{i, s})
					}
				}
			}
			if acrDoc == nil {
				t.Fatalf("no acr-pull Secret applied through stdin\n%s", logs)
			}
			meta, _ := acrDoc["metadata"].(map[string]any)
			if meta["namespace"] != "vrsky-database" {
				t.Errorf("acr-pull applied to namespace %v, want vrsky-database", meta["namespace"])
			}
			for _, k := range []string{"uid", "resourceVersion", "creationTimestamp"} {
				if _, ok := meta[k]; ok {
					t.Errorf("acr-pull still carries metadata.%s from vrsky-platform; apply would conflict or refuse it", k)
				}
			}
			if acrDoc["type"] != "kubernetes.io/dockerconfigjson" {
				t.Errorf("acr-pull type %v, want kubernetes.io/dockerconfigjson", acrDoc["type"])
			}
			data, _ := acrDoc["data"].(map[string]any)
			if data[".dockerconfigjson"] != "e30=" || len(data) != 1 {
				t.Errorf("acr-pull data %v, want the source's {.dockerconfigjson: e30=}", data)
			}

			// The CronJob, then its suspend flag. Applying the file after the
			// patch would put suspend: true back.
			if len(applies) == 0 {
				t.Fatalf("the CronJob was never applied\n%s", logs)
			}
			// With the Secret ready, no apply may carry suspend: true, not even
			// for a moment before a patch: kube-state-metrics would record a
			// suspended sample, and that mutes MgmtBackupMissing for days.
			if !tc.wantSuspended {
				for _, a := range applies {
					if a.suspend {
						t.Errorf("call %d applies the CronJob with suspend: true although vrsky-backup-config is ready: %v\n%s", a.at, r.calls[a.at], logs)
					}
				}
			}
			cronAt := applies[len(applies)-1].at
			suspended, lastPatch := applies[len(applies)-1].suspend, -1
			for i, argv := range r.calls {
				joined := strings.Join(argv, " ")
				if kubectlVerb(argv) != "patch" || !strings.Contains(joined, "vrsky-mgmt-backup") {
					continue
				}
				m := suspendRe.FindStringSubmatch(joined)
				if m == nil {
					continue
				}
				lastPatch = i
				if i > cronAt {
					suspended = m[1] == "true"
				}
				if tc.wantSuspended && m[1] == "false" {
					t.Errorf("call %d unsuspends the CronJob though the Secret is not complete: %v", i, argv)
				}
			}
			if lastPatch >= 0 && lastPatch < cronAt {
				t.Errorf("the suspend patch (call %d) comes before the CronJob apply (call %d), which resets it", lastPatch, cronAt)
			}
			if suspended != tc.wantSuspended {
				t.Errorf("CronJob suspended=%v after the run, want %v\n%s", suspended, tc.wantSuspended, logs)
			}
			for _, s := range tc.wantSaid {
				if !strings.Contains(r.out, s) {
					t.Errorf("the output does not mention %s; nobody reading the deploy log would know why the backup is off\n%s", s, r.out)
				}
			}
		})
	}
}
