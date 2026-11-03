package managementapi

import (
	"encoding/json"
	"fmt"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/vismaurl"
)

// validateVismaURLs refuses a base_url or token_url the Visma connectors would
// not call (#661): not https, userinfo, a query, or a literal private address.
// It is the early, visible error; the connectors enforce it again at dial time,
// which is the check that sees where a hostname resolves. An unset token_url
// means the Visma Connect default.
func validateVismaURLs(node *Node, raw map[string]json.RawMessage) []string {
	var block map[string]json.RawMessage
	if json.Unmarshal(raw["visma"], &block) != nil {
		return nil
	}
	err := vismaurl.Check(stringField(block, "base_url"), stringField(block, "token_url"), safeurl.AllowPrivateFromEnv())
	if err == nil {
		return nil
	}
	return []string{fmt.Sprintf("node %s (visma): visma.%s (served by %s)", node.ID, err, nodeConfigRules[nodeKind{node.Type, "visma"}].service)}
}
