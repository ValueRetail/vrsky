package managementapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
