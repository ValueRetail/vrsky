package managementapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Node config validation for the graph model (#212).
//
// A pipeline node carries a `type` naming the connector kind ("http", "file",
// "sitoo", …) plus a block of settings keyed by that type. At runtime a standing
// connector service claims the node by matching exactly those two things — for
// example http-producer runs
//
//	if nodeConfig.Type != "http" || nodeConfig.HTTP.URL == "" { continue }
//
// and skips the node otherwise. Silently. A connection whose destination has no
// URL therefore saves, deploys, reports "running", and delivers nothing, with no
// error anywhere. That is the same failure shape as #205, one node at a time.
//
// So the rules below mirror each connector's own claim condition: if the service
// would skip the node, we reject it at create/update, where the user can still
// fix it. They deliberately do NOT try to validate semantics the connector
// itself tolerates (a reachable URL, valid credentials) — those fail loudly at
// runtime with a real error, which is a different and much less costly problem.
//
// Keep this table in step with the two connector dropdowns in
// ui/src/components/Pipeline/PropertyEditor.tsx. TestNodeConfigRulesCoverUI
// fails when they drift.

// nodeKind identifies a rule: the coarse node type plus the connector kind named
// by the node config's `type` field.
type nodeKind struct {
	node   string // "consumer" | "producer"
	config string // node.Config "type"
}

// nodeConfigRule is what one connector requires before it will serve a node.
type nodeConfigRule struct {
	// service is the standing service that runs this node kind, named in error
	// messages so an operator knows which logs to read.
	service string
	// requires are dotted paths within the node config that must be present and
	// non-empty, each with the reason it matters.
	requires []configRequirement
}

type configRequirement struct {
	path   string // e.g. "http.url"
	reason string
}

// blockRequired is the common case: the connector checks that the type's config
// block is present, so an absent block means it never claims the node.
func blockRequired(block string) []configRequirement {
	return []configRequirement{{
		path:   block,
		reason: fmt.Sprintf("the %s settings block is missing, so the connector will not claim this node", block),
	}}
}

// nodeConfigRules maps (node type, config type) to the connector's requirements.
//
// Each entry's `requires` mirrors the claim condition in the named service's
// source. When you change one, change the other.
var nodeConfigRules = map[nodeKind]nodeConfigRule{
	// --- generic sources -----------------------------------------------------
	{"consumer", "file"}: {"file-consumer", []configRequirement{{
		"file.path", "the directory to watch is required (e.g. /data/input)",
	}}},
	{"consumer", "http"}:          {"webhook-consumer", nil}, // listens; no required settings
	{"consumer", "api"}:           {"api-consumer", blockRequired("api")},
	{"consumer", "database"}:      {"db-consumer", blockRequired("database")},
	{"consumer", "cloud_storage"}: {"cloud-storage-consumer", blockRequired("cloud_storage")},
	{"consumer", "sftp"}:          {"sftp-consumer", blockRequired("sftp")},
	{"consumer", "kafka"}:         {"kafka-consumer", blockRequired("kafka")},
	{"consumer", "rabbitmq"}:      {"rabbitmq-consumer", blockRequired("rabbitmq")},
	{"consumer", "salesforce"}:    {"salesforce-consumer", blockRequired("salesforce")},
	{"consumer", "tenant"}: {"tenant-consumer", []configRequirement{{
		"tenant.source_connection_id", "the source connection to mirror is required",
	}}},

	// --- generic destinations ------------------------------------------------
	{"producer", "http"}: {"http-producer", []configRequirement{{
		"http.url", "the destination URL is required — without it the producer skips this node and nothing is delivered",
	}}},
	// file.path may be empty: file-producer falls back to its mounted default
	// output dir. Its *format* is checked by validateFileProducerPath (#142).
	{"producer", "file"}: {"file-producer", nil},
	{"producer", "database"}: {"db-producer", []configRequirement{{
		"database.host", "the target database host is required",
	}}},
	{"producer", "cloud_storage"}: {"cloud-storage-producer", blockRequired("cloud_storage")},
	{"producer", "sftp"}:          {"sftp-producer", blockRequired("sftp")},
	{"producer", "kafka"}:         {"kafka-producer", blockRequired("kafka")},
	{"producer", "rabbitmq"}:      {"rabbitmq-producer", blockRequired("rabbitmq")},
	{"producer", "salesforce"}: {"salesforce-producer", []configRequirement{{
		"salesforce.object", "the Salesforce object to write is required",
	}}},

	// --- retail / ERP, both directions ---------------------------------------
	{"consumer", "sitoo"}:            {"sitoo-consumer", blockRequired("sitoo")},
	{"producer", "sitoo"}:            {"sitoo-producer", blockRequired("sitoo")},
	{"consumer", "front_systems"}:    {"front-systems-consumer", blockRequired("front_systems")},
	{"producer", "front_systems"}:    {"front-systems-producer", blockRequired("front_systems")},
	{"consumer", "business_central"}: {"business-central-consumer", blockRequired("business_central")},
	{"producer", "business_central"}: {"business-central-producer", blockRequired("business_central")},
	{"consumer", "visma"}:            {"visma-consumer", blockRequired("visma")},
	{"producer", "visma"}:            {"visma-producer", blockRequired("visma")},
	{"consumer", "brightpearl"}:      {"brightpearl-consumer", blockRequired("brightpearl")},
	{"producer", "brightpearl"}:      {"brightpearl-producer", blockRequired("brightpearl")},
	{"consumer", "sap_s4hana"}:       {"sap-s4hana-consumer", blockRequired("sap_s4hana")},
	{"producer", "sap_s4hana"}:       {"sap-s4hana-producer", blockRequired("sap_s4hana")},
}

// ValidateNodeConfigs checks that every node is configured completely enough
// for a standing connector service to claim it.
//
// This runs when a connection is STARTED, not when it is saved. ValidateDAG
// (topology) runs on both, and a pipeline being built on the canvas is
// legitimately incomplete — nodes exist before their type is chosen. Start is
// the moment the promise is made: from here the connection reports "running",
// so from here it must actually be able to run.
//
// Returns a *DAGValidationError listing every problem, so the user fixes the
// whole pipeline in one pass rather than one node per attempt.
func (v *Validator) ValidateNodeConfigs(conn *Connection) error {
	if conn == nil {
		return &BadRequestError{Message: "connection cannot be nil"}
	}

	var errors []string
	for _, node := range conn.Nodes {
		if node == nil {
			continue
		}
		switch node.Type {
		case "consumer", "producer":
			errors = append(errors, validateEdgeNodeConfig(node)...)
		case "filter", "converter":
			errors = append(errors, validateTransformNodeConfig(node)...)
		}
	}

	if len(errors) > 0 {
		return &DAGValidationError{Errors: errors}
	}
	return nil
}

// validateEdgeNodeConfig checks one consumer/producer node against the rule for
// its connector kind, returning error strings (empty when the node is fine).
//
// Unparseable config is not reported here: the node config is stored as opaque
// JSON and a malformed document is caught upstream when the connection is
// unmarshalled.
func validateEdgeNodeConfig(node *Node) []string {
	var raw map[string]json.RawMessage
	if len(node.Config) == 0 || json.Unmarshal(node.Config, &raw) != nil {
		return []string{fmt.Sprintf("node %s: no configuration — pick a %s type and fill in its settings",
			node.ID, sourceOrDestination(node.Type))}
	}

	configType := stringField(raw, "type")
	if configType == "" {
		return []string{fmt.Sprintf("node %s: no %s type selected (choose one of: %s)",
			node.ID, sourceOrDestination(node.Type), strings.Join(knownConfigTypes(node.Type), ", "))}
	}

	rule, ok := nodeConfigRules[nodeKind{node.Type, configType}]
	if !ok {
		// No standing service claims this kind, so the connection would start
		// and do nothing — the #205 failure, caught at save time.
		return []string{fmt.Sprintf("node %s: %q is not a valid %s type (choose one of: %s)",
			node.ID, configType, sourceOrDestination(node.Type), strings.Join(knownConfigTypes(node.Type), ", "))}
	}

	var errs []string
	for _, req := range rule.requires {
		if !hasNonEmpty(raw, req.path) {
			errs = append(errs, fmt.Sprintf("node %s (%s): %s is required — %s (served by %s)",
				node.ID, configType, req.path, req.reason, rule.service))
		}
	}
	return errs
}

// validateTransformNodeConfig checks filter/converter nodes for input-format
// settings that would fail at parse time. XML is the only format with no
// inherent record shape, so pkg/records rejects it without a record path
// (ADR 0003) — every message would error rather than flow.
func validateTransformNodeConfig(node *Node) []string {
	var nc struct {
		InputFormat        string `json:"input_format"`
		InputXmlRecordPath string `json:"input_xml_record_path"`
	}
	if len(node.Config) == 0 || json.Unmarshal(node.Config, &nc) != nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(nc.InputFormat), "xml") {
		return nil
	}
	if strings.TrimSpace(nc.InputXmlRecordPath) == "" {
		return []string{fmt.Sprintf(
			"node %s: XML input requires input_xml_record_path (e.g. \"Orders.Order\") — XML has no inherent record shape, so parsing fails without it",
			node.ID)}
	}
	return nil
}

// sourceOrDestination renders a node type in the UI's vocabulary, so an error
// message matches the label the user actually clicked.
func sourceOrDestination(nodeType string) string {
	if nodeType == "producer" {
		return "destination"
	}
	return "source"
}

// knownConfigTypes lists the connector kinds valid for a node type, sorted so
// error messages are stable.
func knownConfigTypes(nodeType string) []string {
	var out []string
	for k := range nodeConfigRules {
		if k.node == nodeType {
			out = append(out, k.config)
		}
	}
	sort.Strings(out)
	return out
}

// stringField reads a top-level string field from a raw config object.
func stringField(raw map[string]json.RawMessage, key string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// hasNonEmpty reports whether a dotted path within the config resolves to a
// present, non-empty value. A path of one segment ("kafka") tests for the block
// itself; deeper paths ("http.url") test a field inside it.
//
// "Empty" means JSON null, "", {}, or [] — the states a connector treats as
// absent. Numbers and booleans always count as present.
func hasNonEmpty(raw map[string]json.RawMessage, path string) bool {
	segments := strings.Split(path, ".")
	cur := raw
	for i, seg := range segments {
		v, ok := cur[seg]
		if !ok {
			return false
		}
		if i == len(segments)-1 {
			return !isEmptyJSON(v)
		}
		var next map[string]json.RawMessage
		if json.Unmarshal(v, &next) != nil {
			return false // an intermediate segment isn't an object
		}
		cur = next
	}
	return false
}

// isEmptyJSON reports whether a raw value is one of the "not set" shapes.
func isEmptyJSON(v json.RawMessage) bool {
	s := strings.TrimSpace(string(v))
	switch s {
	case "", "null", `""`, "{}", "[]":
		return true
	}
	return false
}
