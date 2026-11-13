package crypto

import "strings"

// Where a node config may reference a stored secret (#853).
//
// A secret's value must only ever land in a setting that is a credential: one
// the connector hands to the system it signs in to, and never quotes in an
// error or sends on as data. Resolving references anywhere let an editor put
// one in an enum-like setting ("auth_type") and read any of the tenant's
// write-only secrets back from the error text, the activity feed or the DLQ.
//
// So this table lists, per node config "type", the credential settings, as
// paths relative to the node config. A segment matches a JSON key without
// regard to case (strings.EqualFold, the rule encoding/json uses when it
// fills a struct), and "*" matches any one key or array index.
//
// Every path takes a "<field>_secret_id" reference, the form the editor
// writes (ui/src/utils/secrets.ts). inline marks the one kind that also
// takes a "{secret:<uuid>}" placeholder inside the value: HTTP header values,
// which go only to the target (net/http's errors name the header, not its
// value). A setting whose other parts end up in an error text (a URL, a
// connection string) must not be inline.
//
// Keep it in step with the connectors: a field a connector fills from a
// "_secret_id" reference belongs here, and docs/SECURITY.md lists the same.
// TestSecretFieldsResolve resolves every entry.
var secretFields = map[string][]secretField{
	// webhook-consumer (signature, mTLS client CA) and http-producer (headers,
	// bearer token, mTLS client cert). The tls block sits beside http.
	"http": {
		field("http.headers.*", true),
		field("http.token", false),
		field("http.signature.secret", false),
		field("tls.client_ca", false),
		field("tls.cert", false),
		field("tls.key", false),
	},
	"api":      {field("api.endpoints.*.auth_value", false)},
	"database": {field("database.password", false)},
	"sftp":     {field("sftp.password", false), field("sftp.private_key", false)},
	"kafka": {
		field("kafka.password", false),
		field("kafka.client_cert", false),
		field("kafka.client_key", false),
	},
	"rabbitmq": {field("rabbitmq.password", false)},
	// connection_string takes a reference only: it is not all credential.
	// Its AccountName, BlobEndpoint and EndpointSuffix become the service
	// URL, which transport and DNS errors quote, so an inline placeholder
	// there would come back in the error text.
	"cloud_storage": {
		field("cloud_storage.secret_access_key", false),
		field("cloud_storage.account_key", false),
		field("cloud_storage.connection_string", false),
		field("cloud_storage.credentials_json", false),
	},
	"sitoo": {field("sitoo.api_password", false)},
	"front_systems": {
		field("front_systems.subscription_key", false),
		field("front_systems.api_key", false),
	},
	"business_central": {field("business_central.client_secret", false)},
	"visma":            {field("visma.client_secret", false)},
	"brightpearl":      {field("brightpearl.staff_token", false)},
	"sap_s4hana":       {field("sap_s4hana.password", false), field("sap_s4hana.client_secret", false)},
	"nats": {
		field("nats.password", false),
		field("nats.creds", false),
		field("nats.nkey_seed", false),
		field("nats.ca_cert", false),
	},
	// The connector resolves token_secret_id itself and refuses every
	// placeholder (#840); listed so the management API reads it the same way.
	"bifrost_catalogue": {field("bifrost_catalogue.token", false)},
}

// secretField is one credential setting: a dotted path, and whether it takes
// a placeholder inline as well as a reference.
type secretField struct {
	path   []string
	inline bool
}

func field(path string, inline bool) secretField {
	return secretField{path: strings.Split(path, "."), inline: inline}
}

// matches reports whether a concrete path (keys and "[i]" indices) is this
// field. Lengths must agree: "http.headers.*" is a header value, never
// something nested under one.
func (f secretField) matches(path []string) bool {
	if len(path) != len(f.path) {
		return false
	}
	for i, seg := range f.path {
		if seg != "*" && !strings.EqualFold(seg, path[i]) {
			return false
		}
	}
	return true
}
