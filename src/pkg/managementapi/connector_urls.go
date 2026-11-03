package managementapi

import (
	"encoding/json"
	"fmt"

	"github.com/ValueRetail/vrsky/pkg/connectorurl"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
)

// validateConnectorURLs refuses the Brightpearl, Front Systems, SAP S/4HANA
// and Business Central URLs the connector would refuse (#708), with the same
// rule from pkg/connectorurl. It is the early, visible error at start; the
// connectors check again before every call, and their dial check is the one
// that sees where a host name resolves. It returns nil for any other type.
func validateConnectorURLs(node *Node, configType string, raw map[string]json.RawMessage) []string {
	var block map[string]json.RawMessage
	if json.Unmarshal(raw[configType], &block) != nil {
		return nil // no block: the required-field rule reports it
	}
	allowPrivate := safeurl.AllowPrivateFromEnv()
	var err error
	switch configType {
	case "brightpearl":
		err = connectorurl.Brightpearl(rawString(block, "base_url"), rawString(block, "datacenter"),
			rawString(block, "account_code"), allowPrivate)
	case "front_systems":
		// The consumer only calls base_url to register its webhook, which it
		// does when callback_url and events are set; a webhook-only consumer
		// never calls it. The producer calls it on every delivery.
		if node.Type == "consumer" && (rawString(block, "callback_url") == "" || !hasNonEmpty(block, "events")) {
			return nil
		}
		err = connectorurl.FrontSystems(rawString(block, "base_url"), allowPrivate)
	case "sap_s4hana":
		err = connectorurl.SAP(connectorurl.SAPConfig{
			Host:       rawString(block, "host"),
			Service:    rawString(block, "service"),
			APIBaseURL: rawString(block, "api_base_url"),
			TokenURL:   rawString(block, "token_url"),
			AuthType:   rawString(block, "auth_type"),
		}, allowPrivate)
	case "business_central":
		err = connectorurl.BusinessCentral(connectorurl.BusinessCentralConfig{
			APIBaseURL:  rawString(block, "api_base_url"),
			TokenURL:    rawString(block, "token_url"),
			AADTenantID: rawString(block, "aad_tenant_id"),
			Environment: rawString(block, "environment"),
		}, allowPrivate)
	default:
		return nil
	}
	if err == nil {
		return nil
	}
	return []string{fmt.Sprintf("node %s (%s): %s.%s (served by %s)",
		node.ID, configType, configType, err, nodeConfigRules[nodeKind{node.Type, configType}].service)}
}

// rawString reads a top-level string field as the connector gets it, without
// trimming: the connectors refuse " host" for SAP, so this must not accept it.
func rawString(raw map[string]json.RawMessage, key string) string {
	var s string
	if v, ok := raw[key]; !ok || json.Unmarshal(v, &s) != nil {
		return ""
	}
	return s
}
