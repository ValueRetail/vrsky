package managementapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Brightpearl, Front Systems, SAP S/4HANA and Business Central send a
// credential to URLs from tenant config. Their connectors refuse a bad one;
// these tests check the management API refuses it too, with the same rule
// (pkg/connectorurl), when the connection is started (#708).

const urlSecret = "hunter2" // in userinfo and queries below; no error may quote it

// nodeCfg builds a node config of the given type with block as its settings.
func nodeCfg(t *testing.T, typ string, block map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": typ, typ: block})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// startOn validates a pipeline with cfg as its consumer or its producer.
func startOn(side, cfg string) error {
	if side == "consumer" {
		return NewValidator().ValidateNodeConfigs(pipeline(cfg, okHTTPDest))
	}
	return NewValidator().ValidateNodeConfigs(pipeline(okFileSource, cfg))
}

type connectorURLCase struct {
	name    string
	block   map[string]any
	sides   []string // default: both
	wantErr string   // substring; empty = accepted
}

func runConnectorURLCases(t *testing.T, typ string, tests []connectorURLCase) {
	t.Helper()
	for _, tt := range tests {
		sides := tt.sides
		if len(sides) == 0 {
			sides = []string{"consumer", "producer"}
		}
		for _, side := range sides {
			t.Run(side+"/"+tt.name, func(t *testing.T) {
				err := startOn(side, nodeCfg(t, typ, tt.block))
				switch {
				case tt.wantErr == "" && err != nil:
					t.Fatalf("unexpected error: %v", err)
				case tt.wantErr != "" && err == nil:
					t.Fatalf("expected an error containing %q", tt.wantErr)
				case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
					t.Fatalf("error %q does not contain %q", err, tt.wantErr)
				}
				if err != nil && strings.Contains(err.Error(), urlSecret) {
					t.Fatalf("error %q quotes the secret", err)
				}
			})
		}
	}
}

func TestValidateNodeConfigs_BrightpearlURLs(t *testing.T) {
	// A polling consumer (and every producer) calls the base URL; a
	// webhook-only consumer never does, so it is not checked.
	bp := func(kv ...string) map[string]any {
		m := map[string]any{"app_ref": "acme_vrsky", "staff_token_secret_id": "s1", "resource": "/order-service/order", "poll_interval_seconds": 300}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	runConnectorURLCases(t, "brightpearl", []connectorURLCase{
		{name: "derived from datacenter and account code", block: bp("datacenter", "eu1", "account_code", "acme")},
		{name: "public base_url override", block: bp("base_url", "https://euw1.brightpearlconnect.com/public-api/acme")},

		{name: "base_url over http", block: bp("base_url", "http://euw1.brightpearlconnect.com/public-api/acme"), wantErr: "brightpearl.base_url"},
		{name: "base_url loopback", block: bp("base_url", "https://127.0.0.1/x"), wantErr: "private"},
		{name: "base_url metadata", block: bp("base_url", "https://169.254.169.254/latest"), wantErr: "private"},
		{name: "base_url userinfo", block: bp("base_url", "https://u:"+urlSecret+"@euw1.brightpearlconnect.com/x"), wantErr: "user name"},
		{name: "base_url query", block: bp("base_url", "https://euw1.brightpearlconnect.com/x?k="+urlSecret), wantErr: "query"},
		{name: "datacenter moves the host", block: bp("datacenter", "x@10.0.0.1/", "account_code", "acme"), wantErr: "brightpearl.datacenter"},
		{name: "account_code leaves the path", block: bp("datacenter", "eu1", "account_code", "acme/../admin"), wantErr: "brightpearl.account_code"},
		{name: "neither base_url nor datacenter", block: bp(), wantErr: "brightpearl.datacenter"},
		{name: "datacenter with userinfo and a secret", block: bp("datacenter", "u:"+urlSecret+"@eu1", "account_code", "acme"), wantErr: "brightpearl.datacenter"},
		{name: "datacenter with a query and a secret", block: bp("datacenter", "eu1?k="+urlSecret, "account_code", "acme"), wantErr: "brightpearl.datacenter"},
		{name: "account_code with userinfo and a secret", block: bp("datacenter", "eu1", "account_code", "u:"+urlSecret+"@acme"), wantErr: "brightpearl.account_code"},

		// Webhook mode: the consumer publishes what Brightpearl posts and
		// never calls the base URL (#708 review).
		{name: "webhook-only consumer, nothing set", block: map[string]any{"app_ref": "acme_vrsky", "staff_token_secret_id": "s1"}, sides: []string{"consumer"}},
		{name: "webhook-only consumer, no interval", block: map[string]any{"base_url": "https://127.0.0.1/x", "resource": "/order-service/order"}, sides: []string{"consumer"}},
		{name: "webhook-only consumer, no resource", block: map[string]any{"base_url": "https://127.0.0.1/x", "poll_interval_seconds": 300}, sides: []string{"consumer"}},
		{name: "producer, nothing set", block: map[string]any{"app_ref": "acme_vrsky", "staff_token_secret_id": "s1", "resource": "/order-service/order"}, sides: []string{"producer"}, wantErr: "brightpearl.datacenter"},
	})
}

func TestValidateNodeConfigs_FrontSystemsURLs(t *testing.T) {
	fs := func(base string, callback bool) map[string]any {
		m := map[string]any{"base_url": base, "subscription_key_secret_id": "s1", "api_key_secret_id": "s2"}
		if callback {
			m["callback_url"] = "https://vrsky.example.com/frontsystems/events/abc"
			m["events"] = []string{"SaleCreated"}
		}
		return m
	}
	consumer, producer := []string{"consumer"}, []string{"producer"}
	runConnectorURLCases(t, "front_systems", []connectorURLCase{
		{name: "public apim host", block: fs("https://fs-partner.azure-api.net", true)},

		{name: "registering consumer, http", block: fs("http://fs-partner.azure-api.net", true), sides: consumer, wantErr: "front_systems.base_url"},
		{name: "registering consumer, loopback", block: fs("https://127.0.0.1:8443", true), sides: consumer, wantErr: "private"},
		{name: "registering consumer, empty", block: fs("", true), sides: consumer, wantErr: "front_systems.base_url is required"},
		{name: "registering consumer, userinfo", block: fs("https://u:"+urlSecret+"@fs-partner.azure-api.net", true), sides: consumer, wantErr: "user name"},

		// A webhook-only consumer never calls base_url, so the connector does
		// not check it and neither does the start.
		{name: "webhook-only consumer, loopback", block: fs("https://127.0.0.1:8443", false), sides: consumer},
		{name: "webhook-only consumer, empty", block: fs("", false), sides: consumer},
		{name: "callback but no events", block: map[string]any{"base_url": "https://127.0.0.1", "callback_url": "https://vrsky.example.com/cb", "events": []string{}}, sides: consumer},

		// The producer calls base_url on every delivery.
		{name: "producer, http", block: fs("http://fs-partner.azure-api.net", false), sides: producer, wantErr: "front_systems.base_url"},
		{name: "producer, metadata", block: fs("https://169.254.169.254/", false), sides: producer, wantErr: "private"},
		{name: "producer, empty", block: fs("", false), sides: producer, wantErr: "front_systems.base_url is required"},
		{name: "producer, query", block: fs("https://fs-partner.azure-api.net/?k="+urlSecret, false), sides: producer, wantErr: "query"},
	})
}

func TestValidateNodeConfigs_SAPURLs(t *testing.T) {
	const host = "my347623.s4hana.ondemand.com"
	sap := func(kv ...string) map[string]any {
		m := map[string]any{"service": "API_SALES_ORDER_SRV", "entity_set": "A_SalesOrder", "username": "u", "password_secret_id": "s1"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	runConnectorURLCases(t, "sap_s4hana", []connectorURLCase{
		{name: "cloud host", block: sap("host", host)},
		{name: "host with a port", block: sap("host", "sap.example.com:44300")},
		{name: "public api_base_url", block: sap("api_base_url", "https://sap.example.com/sap/opu/odata/sap/API_SALES_ORDER_SRV/")},
		{name: "oauth2, token url from the host", block: sap("host", host, "auth_type", "oauth2")},
		{name: "basic auth ignores token_url", block: sap("host", host, "token_url", "http://127.0.0.1/token")},

		{name: "host moves the target", block: sap("host", "x@10.0.0.1/"), wantErr: "sap_s4hana.host"},
		{name: "host with a scheme", block: sap("host", "https://"+host), wantErr: "sap_s4hana.host"},
		{name: "host with leading space", block: sap("host", " "+host), wantErr: "sap_s4hana.host"},
		{name: "host loopback literal", block: sap("host", "127.0.0.1:8080"), wantErr: "private"},
		{name: "no host and no api_base_url", block: sap(), wantErr: "sap_s4hana.host"},
		{name: "api_base_url over http", block: sap("api_base_url", "http://sap.example.com/odata"), wantErr: "sap_s4hana.api_base_url"},
		{name: "api_base_url private", block: sap("api_base_url", "https://10.0.0.5/odata"), wantErr: "private"},
		{name: "api_base_url userinfo", block: sap("api_base_url", "https://u:"+urlSecret+"@sap.example.com/odata"), wantErr: "user name"},
		{name: "oauth2 token_url loopback", block: sap("host", host, "auth_type", "oauth2", "token_url", "https://127.0.0.1/token"), wantErr: "sap_s4hana.token_url"},
		{name: "oauth2 token_url over http", block: sap("host", host, "auth_type", "oauth2", "token_url", "http://auth.example.com/token"), wantErr: "sap_s4hana.token_url"},
		{name: "oauth2 default token url with no host", block: sap("api_base_url", "https://sap.example.com/odata", "auth_type", "oauth2"), wantErr: "sap_s4hana.token_url defaults to the host"},
		{name: "host with userinfo and a secret", block: sap("host", "u:"+urlSecret+"@sap.example.com"), wantErr: "sap_s4hana.host"},
		{name: "host with a query and a secret", block: sap("host", "sap.example.com?k="+urlSecret), wantErr: "sap_s4hana.host"},
		{name: "oauth2 default token url from a host with a secret", block: sap("api_base_url", "https://sap.example.com/odata", "auth_type", "oauth2", "host", "u:"+urlSecret+"@sap.example.com"), wantErr: "sap_s4hana.token_url defaults to the host"},
	})
}

func TestValidateNodeConfigs_BusinessCentralURLs(t *testing.T) {
	bc := func(kv ...string) map[string]any {
		m := map[string]any{"aad_tenant_id": "t1", "company_id": "c1", "client_id": "id", "client_secret_secret_id": "s1", "entity": "items"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	runConnectorURLCases(t, "business_central", []connectorURLCase{
		{name: "cloud defaults", block: bc()},
		{name: "public overrides", block: bc("api_base_url", "https://bc.example.com:7048/BC/api/v2.0", "token_url", "https://login.example.com/t1/oauth2/v2.0/token")},

		{name: "api_base_url over http", block: bc("api_base_url", "http://bc.example.com/api/v2.0"), wantErr: "business_central.api_base_url"},
		{name: "api_base_url loopback", block: bc("api_base_url", "https://127.0.0.1/api/v2.0"), wantErr: "private"},
		{name: "api_base_url azure wire server", block: bc("api_base_url", "https://168.63.129.16/api/v2.0"), wantErr: "private"},
		{name: "api_base_url userinfo", block: bc("api_base_url", "https://u:"+urlSecret+"@bc.example.com/api/v2.0"), wantErr: "user name"},
		{name: "token_url over http", block: bc("token_url", "http://login.example.com/t/oauth2/v2.0/token"), wantErr: "business_central.token_url"},
		{name: "token_url metadata", block: bc("token_url", "https://169.254.169.254/token"), wantErr: "business_central.token_url"},
		{name: "token_url query", block: bc("token_url", "https://login.example.com/token?k="+urlSecret), wantErr: "query"},

		// The source trims these four before its check (BCConfig.normalise);
		// the destination does not, and refuses a GUID with a newline.
		{name: "pasted tenant id with a newline", block: bc("aad_tenant_id", "guid\n"), sides: []string{"consumer"}},
		{name: "pasted environment with a space", block: bc("environment", "Production "), sides: []string{"consumer"}},
		{name: "pasted tenant id with a newline", block: bc("aad_tenant_id", "guid\n"), sides: []string{"producer"}, wantErr: "business_central.aad_tenant_id"},
	})
}

// An empty settings block is reported once, by the required-field rule, not
// again as a bad URL.
func TestValidateNodeConfigs_ConnectorURLs_EmptyBlockOneError(t *testing.T) {
	for _, typ := range []string{"brightpearl", "front_systems", "sap_s4hana", "business_central"} {
		for _, side := range []string{"consumer", "producer"} {
			t.Run(side+"/"+typ, func(t *testing.T) {
				err := startOn(side, `{"type":"`+typ+`","`+typ+`":{}}`)
				var dve *DAGValidationError
				if !errors.As(err, &dve) {
					t.Fatalf("want a *DAGValidationError, got %v", err)
				}
				if len(dve.Errors) != 1 {
					t.Fatalf("want one error, got %d: %q", len(dve.Errors), dve.Errors)
				}
			})
		}
	}
}

// The error names the node, the field and the service whose logs to read.
func TestValidateNodeConfigs_ConnectorURLs_ErrorShape(t *testing.T) {
	cfg := `{"type":"business_central","business_central":{"api_base_url":"https://10.0.0.5/api/v2.0"}}`
	err := startOn("producer", cfg)
	if err == nil {
		t.Fatal("expected the private api_base_url to be refused")
	}
	want := `node p1 (business_central): business_central.api_base_url "https://10.0.0.5/api/v2.0" points at a private, loopback or link-local address (served by business-central-producer)`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
}

// ALLOW_PRIVATE_UPSTREAMS, the operator's dev switch, lets the start through
// for mock upstreams in docker compose, as the connectors do.
func TestValidateNodeConfigs_ConnectorURLs_DevFlag(t *testing.T) {
	configs := map[string]string{
		"brightpearl":      `{"type":"brightpearl","brightpearl":{"base_url":"http://brightpearl-mock:8080/public-api/acme","resource":"/order-service/order","poll_interval_seconds":300}}`,
		"front_systems":    `{"type":"front_systems","front_systems":{"base_url":"http://127.0.0.1:9000","callback_url":"https://vrsky.example.com/cb","events":["SaleCreated"]}}`,
		"sap_s4hana":       `{"type":"sap_s4hana","sap_s4hana":{"api_base_url":"http://mock-sap:8099/sap/opu/odata/sap/API_SALES_ORDER_SRV","auth_type":"oauth2","token_url":"http://mock-sap:8099/token"}}`,
		"business_central": `{"type":"business_central","business_central":{"api_base_url":"http://127.0.0.1:8080/api/v2.0","token_url":"http://127.0.0.1:8080/token"}}`,
	}
	for name, cfg := range configs {
		for _, side := range []string{"consumer", "producer"} {
			t.Run(side+"/"+name, func(t *testing.T) {
				t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "")
				if err := startOn(side, cfg); err == nil {
					t.Fatal("without the dev flag the mock URL should be refused")
				}
				t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
				if err := startOn(side, cfg); err != nil {
					t.Fatalf("the operator's dev flag should allow a mock upstream: %v", err)
				}
			})
		}
	}
}

// The NATS source (#814): the required fields and the same URL rule the
// connector applies (connectorurl.NATS).
func TestNATSConsumerStartChecks(t *testing.T) {
	ok := func(servers any) map[string]any {
		return map[string]any{"servers": servers, "stream": "RH_EVENTS", "durable": "vrsky"}
	}
	runConnectorURLCases(t, "nats", []connectorURLCase{
		{name: "public tls", block: ok([]string{"tls://nats.example.com:4222"}), sides: []string{"consumer"}},
		{name: "loopback", block: ok([]string{"tls://127.0.0.1:4222"}), sides: []string{"consumer"}, wantErr: "private"},
		{name: "userinfo", block: ok([]string{"tls://rh:" + urlSecret + "@nats.example.com"}), sides: []string{"consumer"}, wantErr: "user name"},
		{name: "not a list", block: ok("tls://nats.example.com"), sides: []string{"consumer"}, wantErr: "list of URLs"},
		{name: "no servers", block: map[string]any{"stream": "S", "durable": "d"}, sides: []string{"consumer"}, wantErr: "nats.servers is required"},
		{name: "no stream", block: map[string]any{"servers": []string{"tls://nats.example.com"}, "durable": "d"}, sides: []string{"consumer"}, wantErr: "nats.stream is required"},
		{name: "no durable", block: map[string]any{"servers": []string{"tls://nats.example.com"}, "stream": "S"}, sides: []string{"consumer"}, wantErr: "nats.durable is required"},
	})
	with := func(kv ...any) map[string]any {
		m := ok([]string{"tls://nats.example.com:4222"})
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	runConnectorURLCases(t, "nats", []connectorURLCase{
		{name: "retailhero partner account", sides: []string{"consumer"}, block: with(
			"auth_type", "nkey", "nkey_seed_secret_id", "s1", "api_prefix", "JS.RH.API", "inbox_prefix", "_INBOX_vrsky_t_a", "create_durable", false)},
		{name: "creds", sides: []string{"consumer"}, block: with("auth_type", "creds", "creds_secret_id", "s1")},
		{name: "user and password", sides: []string{"consumer"}, block: with("auth_type", "user_password", "username", "rh", "password_secret_id", "s1")},
		{name: "unknown auth type", sides: []string{"consumer"}, block: with("auth_type", "token"), wantErr: "nats.auth_type must be one of"},
		{name: "nkey without its secret", sides: []string{"consumer"}, block: with("auth_type", "nkey"), wantErr: "nats.nkey_seed_secret_id is required"},
		{name: "creds without its secret", sides: []string{"consumer"}, block: with("auth_type", "creds"), wantErr: "nats.creds_secret_id is required"},
		{name: "user without a name", sides: []string{"consumer"}, block: with("auth_type", "user_password", "password_secret_id", "s1"), wantErr: "nats.username is required"},
		{name: "plaintext seed", sides: []string{"consumer"}, block: with("auth_type", "nkey", "nkey_seed", "SU"+urlSecret), wantErr: "nats.nkey_seed is stored in plaintext"},
		{name: "plaintext password", sides: []string{"consumer"}, block: with("auth_type", "user_password", "username", "rh", "password", urlSecret), wantErr: "nats.password is stored in plaintext"},
		{name: "api prefix with a wildcard", sides: []string{"consumer"}, block: with("api_prefix", "JS.*.API"), wantErr: "nats.api_prefix must be a subject"},
		{name: "inbox prefix with an empty token", sides: []string{"consumer"}, block: with("inbox_prefix", "_INBOX..x"), wantErr: "nats.inbox_prefix must be a subject"},
		{name: "prefix with a trailing dot", sides: []string{"consumer"}, block: with("api_prefix", "JS.RH.API.")},
	})
	// There is no NATS destination.
	if err := startOn("producer", nodeCfg(t, "nats", ok([]string{"tls://nats.example.com"}))); err == nil {
		t.Error("a nats destination was accepted")
	}
}
