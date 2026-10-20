package managementapi

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

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
	if len(checked) < 7 {
		t.Fatalf("only %d jobs download Go modules (%v); the Go jobs this guard knows of are Go Tests, Remote agent on Windows, Build remote agent binaries, govulncheck, the DR drill, the connector integration tests and the nightly SAP end-to-end — if one was renamed or dropped, say so here", len(checked), checked)
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
	// The manifests production runs (deploy-vrsky-platform.sh, deploy-azure.sh
	// and deploy-connectors-azure.sh). Everything else under
	// infrastructure/kubernetes — the per-tenant NATS template, Loki and
	// promtail, KES, MinIO's 4-replica layout, Traefik — is lab-only or
	// created on demand and is not counted.
	manifests := []string{
		"platform-nats/statefulset.yaml", "postgresql/statefulset.yaml", "minio/deployment.yaml",
		"management-api/deployment.yaml", "ui/deployment.yaml", "data-filter/deployment.yaml",
		"data-converter/deployment.yaml", "alert-responder/deployment.yaml", "connectors/connectors.yaml",
	}
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
						Spec struct {
							Containers []struct {
								Resources struct {
									Requests map[string]string `yaml:"requests"`
								} `yaml:"resources"`
							} `yaml:"containers"`
						} `yaml:"spec"`
					} `yaml:"template"`
				} `yaml:"spec"`
			}
			if err := dec.Decode(&doc); err != nil {
				break
			}
			if doc.Kind != "Deployment" && doc.Kind != "StatefulSet" && doc.Kind != "DaemonSet" {
				continue
			}
			replicas := 1
			if doc.Spec.Replicas != nil {
				replicas = *doc.Spec.Replicas
			}
			if doc.Kind == "DaemonSet" {
				replicas = 2 // one per production node
			}
			sum := 0
			for _, c := range doc.Spec.Template.Spec.Containers {
				sum += millis(c.Resources.Requests["cpu"])
			}
			if sum > 0 {
				items = append(items, item{filepath.Base(filepath.Dir(path)) + "/" + doc.Metadata.Name + " x" + strconv.Itoa(replicas), sum * replicas})
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
			ID   string `yaml:"id"`
			Name string `yaml:"name"`
			If   string `yaml:"if"`
			Run  string `yaml:"run"`
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
	cmd := exec.Command("bash", filepath.Join("..", "..", "..", "infrastructure", "azure", "affected.sh"))
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\n") + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("affected.sh: %v\n%s", err, out)
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
// connector, a shared-package change every Go image, a rules change the
// monitoring stack, and a docs change nothing at all.
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
		{"a service nobody deploys", []string{"src/cmd/mock-sap/main.go", "src/cmd/vrsky-cli/main.go"}, nil},
		{"rules", []string{"infrastructure/prometheus-rules.yml"}, []string{"monitoring"}},
		{"monitoring values", []string{"infrastructure/kubernetes/monitoring/grafana-values.azure.yaml"}, []string{"monitoring"}},
		{"connector manifest", []string{"infrastructure/azure/deploy-connectors-azure.sh"}, []string{"connectors-manifest"}},
		{"mixed", []string{"ui/x.ts", "src/cmd/sitoo-producer/a.go", "docs/a.md"},
			[]string{"connector sitoo-producer", "core ui", "image sitoo-producer", "image ui"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := affected(t, tc.paths...)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("paths %v\n got %v\nwant %v", tc.paths, got, tc.want)
			}
		})
	}
	// A shared package: every Go image, core and connector, but not the UI.
	got := affected(t, "src/pkg/sdk/restore.go")
	images, coreUI := 0, false
	for _, g := range got {
		if strings.HasPrefix(g, "image ") {
			images++
		}
		if g == "core ui" || g == "image ui" {
			coreUI = true
		}
	}
	if images < 30 || coreUI {
		t.Fatalf("src/pkg change: %d images (want every Go image), ui included=%v:\n%s", images, coreUI, strings.Join(got, "\n"))
	}
	for _, p := range []string{"src/go.mod", "src/cmd/file-consumer/Dockerfile"} {
		if n := len(affected(t, p)); n < 60 {
			t.Fatalf("%s: %d targets, want every Go image rebuilt", p, n)
		}
	}
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
	for _, forbidden := range []string{"deploy-core-azure.sh", "deploy-connectors-azure.sh", "install-monitoring.sh", "kubectl set", "kubectl rollout", "kubectl apply", "az acr build"} {
		if strings.Contains(all, forbidden) {
			t.Errorf("drift check runs %q; it must only compare and dispatch Catch up", forbidden)
		}
	}
}
