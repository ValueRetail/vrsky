package managementapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
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
