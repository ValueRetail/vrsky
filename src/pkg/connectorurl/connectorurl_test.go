package connectorurl

import (
	"strings"
	"testing"
)

// secret appears in every userinfo, query and fragment below; no error may
// quote it, since errors reach logs, the activity feed and the DLQ header.
const secret = "hunter2"

// check runs one table row: wantErr "" means allowed, otherwise a substring of
// the error.
func check(t *testing.T, err error, wantErr string) {
	t.Helper()
	switch {
	case wantErr == "" && err != nil:
		t.Fatalf("refused: %v", err)
	case wantErr != "" && err == nil:
		t.Fatalf("allowed, want an error containing %q", wantErr)
	case wantErr != "" && !strings.Contains(err.Error(), wantErr):
		t.Fatalf("error %q does not contain %q", err, wantErr)
	}
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Fatalf("error %q quotes the secret", err)
	}
}

func TestBrightpearl(t *testing.T) {
	tests := []struct {
		name, base, dc, account string
		allowPrivate            bool
		wantErr                 string
	}{
		{"derived", "", "eu1", "acme_01", false, ""},
		{"derived, upper case datacenter", "", "USE1", "acme", false, ""},
		{"public override", "https://euw1.brightpearlconnect.com/public-api/acme", "", "", false, ""},
		{"override wins over a bad datacenter", "https://euw1.brightpearlconnect.com/public-api/acme", "x@10.0.0.1/", "", false, ""},

		{"override http", "http://euw1.brightpearlconnect.com/public-api/acme", "", "", false, "base_url"},
		{"override loopback", "https://127.0.0.1/public-api/acme", "", "", false, "private"},
		{"override metadata", "https://169.254.169.254/latest", "", "", false, "private"},
		{"override ipv6 loopback", "https://[::1]/x", "", "", false, "private"},
		{"override userinfo", "https://u:" + secret + "@euw1.brightpearlconnect.com/x", "", "", false, "user name"},
		{"override query", "https://euw1.brightpearlconnect.com/x?k=" + secret, "", "", false, "query"},
		{"override not a url", "euw1.brightpearlconnect.com", "", "", false, "base_url"},

		{"no override and nothing set", "", "", "", false, "datacenter"},
		{"datacenter moves the host", "", "x@10.0.0.1/", "acme", false, "datacenter"},
		{"datacenter with a dot", "", "eu1.evil.example", "acme", false, "datacenter"},
		{"datacenter with a leading dash", "", "-eu1", "acme", false, "datacenter"},
		{"account_code with a slash", "", "eu1", "acme/../admin", false, "account_code"},
		{"account_code with a query", "", "eu1", "acme?x", false, "account_code"},
		{"account_code empty", "", "eu1", "", false, "account_code"},
		{"datacenter with userinfo and a secret", "", "u:" + secret + "@eu1", "acme", false, "datacenter"},
		{"datacenter with a fragment and a secret", "", "eu1#" + secret, "acme", false, "datacenter"},
		{"account_code with userinfo and a secret", "", "eu1", "u:" + secret + "@acme", false, "account_code"},
		{"account_code with a query and a secret", "", "eu1", "acme?k=" + secret, false, "account_code"},
		{"plain bad datacenter is quoted", "", "eu1.evil", "acme", false, `datacenter "eu1.evil"`},

		{"dev flag: http mock", "http://brightpearl-mock:8080/public-api/acme", "", "", true, ""},
		{"dev flag: loopback mock", "http://127.0.0.1:9000/x", "", "", true, ""},
		{"dev flag still refuses userinfo", "http://u:" + secret + "@127.0.0.1:9000/x", "", "", true, "user name"},
		{"dev flag does not loosen the datacenter", "", "x@10.0.0.1/", "acme", true, "datacenter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check(t, Brightpearl(tt.base, tt.dc, tt.account, tt.allowPrivate), tt.wantErr)
		})
	}
}

func TestFrontSystems(t *testing.T) {
	tests := []struct {
		name, base   string
		allowPrivate bool
		wantErr      string
	}{
		{"public apim host", "https://fs-partner.azure-api.net", false, ""},
		{"public host with a path", "https://fs-partner.azure-api.net/frontsystems/", false, ""},
		{"empty", "", false, "base_url is required"},
		{"blank", "   ", false, "base_url is required"},
		{"bare host", "fs-partner.azure-api.net", false, "base_url"},
		{"http", "http://fs-partner.azure-api.net", false, "must use https"},
		{"loopback", "https://127.0.0.1:8443", false, "private"},
		{"private 10/8", "https://10.0.0.5/", false, "private"},
		{"metadata", "https://169.254.169.254/latest/meta-data/", false, "private"},
		{"azure wire server", "https://168.63.129.16/", false, "private"},
		{"userinfo", "https://u:" + secret + "@fs-partner.azure-api.net", false, "user name"},
		{"http userinfo", "http://u:" + secret + "@fs-partner.azure-api.net", false, "base_url"},
		{"query", "https://fs-partner.azure-api.net/?k=" + secret, false, "query"},
		{"fragment", "https://fs-partner.azure-api.net/#" + secret, false, "fragment"},
		{"dev flag: http mock", "http://fs-mock:8080", true, ""},
		{"dev flag: loopback mock", "http://127.0.0.1:9000", true, ""},
		{"dev flag still refuses userinfo", "http://u:" + secret + "@127.0.0.1:9000", true, "user name"},
		{"dev flag still requires it", "", true, "base_url is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check(t, FrontSystems(tt.base, tt.allowPrivate), tt.wantErr)
		})
	}
}

func TestSAP(t *testing.T) {
	const host = "my347623.s4hana.ondemand.com"
	basic := func(c SAPConfig) SAPConfig {
		if c.APIBaseURL == "" && c.Service == "" {
			c.Service = "API_SALES_ORDER_SRV"
		}
		return c
	}
	oauth := func(c SAPConfig) SAPConfig {
		c = basic(c)
		c.AuthType = "oauth2"
		return c
	}
	tests := []struct {
		name         string
		cfg          SAPConfig
		allowPrivate bool
		wantErr      string
	}{
		{"default cloud host", basic(SAPConfig{Host: host}), false, ""},
		{"host with a port", basic(SAPConfig{Host: "sap.example.com:44300"}), false, ""},
		{"oauth2, token url from the host", oauth(SAPConfig{Host: host}), false, ""},
		{"oauth2 spelled loosely", basic(SAPConfig{Host: host, AuthType: " OAuth2 "}), false, ""},
		{"oauth2, public token url", oauth(SAPConfig{Host: host, TokenURL: "https://acme.authentication.eu10.hana.ondemand.com/oauth/token"}), false, ""},
		{"public api_base_url, host unused", basic(SAPConfig{APIBaseURL: "https://sap.example.com/odata/"}), false, ""},
		{"basic ignores a bad token_url", basic(SAPConfig{Host: host, TokenURL: "http://127.0.0.1/token"}), false, ""},

		{"api_base_url over http", basic(SAPConfig{APIBaseURL: "http://sap.example.com/odata"}), false, "api_base_url"},
		{"api_base_url loopback", basic(SAPConfig{APIBaseURL: "https://127.0.0.1/odata"}), false, "private"},
		{"api_base_url metadata", basic(SAPConfig{APIBaseURL: "https://169.254.169.254/latest"}), false, "private"},
		{"api_base_url userinfo", basic(SAPConfig{APIBaseURL: "https://u:" + secret + "@sap.example.com/odata"}), false, "user name"},
		{"api_base_url query", basic(SAPConfig{APIBaseURL: "https://sap.example.com/odata?k=" + secret}), false, "query"},

		{"empty host", basic(SAPConfig{}), false, "host"},
		{"host with userinfo moves the host", basic(SAPConfig{Host: "x@10.0.0.1/"}), false, "host"},
		{"host with a path", basic(SAPConfig{Host: "evil.example/"}), false, "host"},
		{"host with a scheme", basic(SAPConfig{Host: "https://" + host}), false, "host"},
		{"host with a query", basic(SAPConfig{Host: "evil.example?"}), false, "host"},
		{"host with whitespace", basic(SAPConfig{Host: " " + host}), false, "host"},
		{"host port out of range", basic(SAPConfig{Host: host + ":99999"}), false, "host"},
		{"host port zero", basic(SAPConfig{Host: host + ":0"}), false, "host"},
		{"host with two ports", basic(SAPConfig{Host: host + ":443:80"}), false, "host"},
		{"host ipv6 literal", basic(SAPConfig{Host: "[::1]"}), false, "host"},
		{"host with userinfo and a secret", basic(SAPConfig{Host: "u:" + secret + "@sap.example.com"}), false, "host"},
		{"host with a query and a secret", basic(SAPConfig{Host: "sap.example.com?k=" + secret}), false, "host"},
		{"default token url from a host with a secret", oauth(SAPConfig{APIBaseURL: "https://sap.example.com/odata", Host: "u:" + secret + "@sap.example.com"}), false, "token_url defaults to the host"},
		{"plain bad host is quoted", basic(SAPConfig{Host: "evil.example/"}), false, `host "evil.example/"`},
		{"host loopback literal", basic(SAPConfig{Host: "127.0.0.1:8080"}), false, "private"},
		{"host metadata literal", basic(SAPConfig{Host: "169.254.169.254"}), false, "private"},

		{"token_url over http", oauth(SAPConfig{Host: host, TokenURL: "http://auth.example.com/token"}), false, "token_url"},
		{"token_url loopback", oauth(SAPConfig{Host: host, TokenURL: "https://127.0.0.1/token"}), false, "token_url"},
		{"token_url userinfo", oauth(SAPConfig{Host: host, TokenURL: "https://u:" + secret + "@auth.example.com/token"}), false, "token_url"},
		{"oauth2 default token url from a bad host", oauth(SAPConfig{APIBaseURL: "https://sap.example.com/odata", Host: "x@10.0.0.1/"}), false, "token_url defaults to the host"},
		{"oauth2 default token url with no host", oauth(SAPConfig{APIBaseURL: "https://sap.example.com/odata"}), false, "token_url defaults to the host"},
		{"oauth2 default token url from a private host", oauth(SAPConfig{APIBaseURL: "https://sap.example.com/odata", Host: "10.0.0.1"}), false, "token_url"},

		{"dev flag: http mock-sap", basic(SAPConfig{APIBaseURL: "http://mock-sap:8099/sap/opu/odata/sap/API_SALES_ORDER_SRV"}), true, ""},
		{"dev flag: loopback token url", oauth(SAPConfig{APIBaseURL: "http://127.0.0.1:9000/odata", TokenURL: "http://127.0.0.1:9000/token"}), true, ""},
		{"dev flag does not loosen the host shape", basic(SAPConfig{Host: "x@10.0.0.1/"}), true, "host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check(t, SAP(tt.cfg, tt.allowPrivate), tt.wantErr)
		})
	}
}

func TestBusinessCentral(t *testing.T) {
	tests := []struct {
		name         string
		cfg          BusinessCentralConfig
		allowPrivate bool
		wantErr      string
	}{
		{"defaults", BusinessCentralConfig{AADTenantID: "t1"}, false, ""},
		{"defaults, named environment", BusinessCentralConfig{AADTenantID: "t1", Environment: "Sandbox"}, false, ""},
		{"public overrides", BusinessCentralConfig{AADTenantID: "t1",
			APIBaseURL: "https://bc.example.com:7048/BC/api/v2.0", TokenURL: "https://login.example.com/t1/oauth2/v2.0/token"}, false, ""},

		{"api http", BusinessCentralConfig{APIBaseURL: "http://bc.example.com/api/v2.0"}, false, "api_base_url"},
		{"api loopback", BusinessCentralConfig{APIBaseURL: "https://127.0.0.1/api/v2.0"}, false, "private, loopback"},
		{"api 172.16/12", BusinessCentralConfig{APIBaseURL: "https://172.16.4.2/api/v2.0"}, false, "private, loopback"},
		{"api metadata", BusinessCentralConfig{APIBaseURL: "https://169.254.169.254/latest"}, false, "private, loopback"},
		{"api userinfo", BusinessCentralConfig{APIBaseURL: "https://user:" + secret + "@bc.example.com/api/v2.0"}, false, "user name"},
		{"api query", BusinessCentralConfig{APIBaseURL: "https://bc.example.com/api/v2.0?k=" + secret}, false, "query string"},
		{"token http", BusinessCentralConfig{TokenURL: "http://login.example.com/t/oauth2/v2.0/token"}, false, "token_url"},
		{"token loopback", BusinessCentralConfig{TokenURL: "https://127.0.0.1/token"}, false, "token_url"},
		{"token ipv6 loopback", BusinessCentralConfig{TokenURL: "https://[::1]/token"}, false, "token_url"},
		{"token userinfo", BusinessCentralConfig{TokenURL: "https://u:" + secret + "@login.example.com/token"}, false, "token_url"},
		{"default urls with a query in the tenant", BusinessCentralConfig{AADTenantID: "t1?k=" + secret}, false, "aad_tenant_id"},

		{"dev flag: loopback mock", BusinessCentralConfig{APIBaseURL: "http://127.0.0.1:8080/api/v2.0", TokenURL: "http://127.0.0.1:8080/token"}, true, ""},
		{"dev flag: compose service", BusinessCentralConfig{APIBaseURL: "http://mock-bc:8099/api/v2.0", TokenURL: "http://mock-bc:8099/token"}, true, ""},
		{"dev flag still refuses userinfo", BusinessCentralConfig{APIBaseURL: "http://u:" + secret + "@127.0.0.1/api/v2.0"}, true, "user name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check(t, BusinessCentral(tt.cfg, tt.allowPrivate), tt.wantErr)
		})
	}
}

func TestBrightpearlBaseURL(t *testing.T) {
	tests := []struct{ name, dc, acct, want string }{
		{"eu", "euw1", "acme", "https://euw1.brightpearlconnect.com/public-api/acme"},
		{"us", "use1", "acme", "https://use1.brightpearlconnect.com/public-api/acme"},
		{"old eu code", "eu1", "acme", "https://euw1.brightpearlconnect.com/public-api/acme"},
		{"old us east code", "use", "acme", "https://use1.brightpearlconnect.com/public-api/acme"},
		{"us west legacy host", "usw", "acme", "https://ws-usw.brightpearl.com/public-api/acme"},
		{"us west usw1", "USW1", "acme", "https://ws-usw.brightpearl.com/public-api/acme"},
		{"upper case", "USE1", "acme", "https://use1.brightpearlconnect.com/public-api/acme"},
		{"no datacenter", "", "acme", ""},
		{"no account", "euw1", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := BrightpearlBaseURL(tc.dc, tc.acct); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBrightpearlUSWestHostIsAccepted(t *testing.T) {
	// The legacy US West host must pass the same URL rules as an override.
	if err := Brightpearl(BrightpearlBaseURL("usw", "acme"), "", "", false); err != nil {
		t.Errorf("legacy US West host rejected: %v", err)
	}
	if err := Brightpearl("", "usw", "acme", false); err != nil {
		t.Errorf("usw datacenter rejected: %v", err)
	}
}

func TestNATS(t *testing.T) {
	tests := []struct {
		name         string
		servers      []string
		allowPrivate bool
		wantErr      string
	}{
		{"tls", []string{"tls://nats.example.com:4222"}, false, ""},
		{"nats scheme, TLS enforced by the consumer", []string{"nats://nats.example.com:4222"}, false, ""},
		{"no port", []string{"tls://nats.example.com"}, false, ""},
		{"trailing slash", []string{"tls://nats.example.com:4222/"}, false, ""},
		{"several", []string{"tls://a.example.com:4222", " tls://b.example.com:4222 "}, false, ""},
		{"loopback with the dev flag", []string{"nats://127.0.0.1:4222"}, true, ""},

		{"none", nil, false, "at least one"},
		{"empty string", []string{""}, false, "absolute URL"},
		{"no scheme", []string{"nats.example.com:4222"}, false, "servers"},
		{"http", []string{"http://nats.example.com:4222"}, false, "nats:// or tls://"},
		{"websocket", []string{"wss://nats.example.com"}, false, "nats:// or tls://"},
		{"loopback", []string{"tls://127.0.0.1:4222"}, false, "private"},
		{"metadata service", []string{"nats://169.254.169.254:4222"}, false, "private"},
		{"rfc1918", []string{"tls://10.1.2.3:4222"}, false, "private"},
		{"ipv6 loopback", []string{"tls://[::1]:4222"}, false, "private"},
		{"second server private", []string{"tls://nats.example.com", "tls://192.168.1.1"}, false, "private"},
		{"userinfo", []string{"tls://u:" + secret + "@nats.example.com:4222"}, false, "user name"},
		{"token userinfo", []string{"tls://" + secret + "@nats.example.com:4222"}, false, "user name"},
		{"query", []string{"tls://nats.example.com:4222?k=" + secret}, false, "query"},
		{"fragment", []string{"tls://nats.example.com:4222#" + secret}, false, "fragment"},
		{"path", []string{"tls://nats.example.com:4222/x"}, false, "path"},
		{"bad port", []string{"tls://nats.example.com:99999"}, false, "port"},
		{"comma hides a second server", []string{"tls://a.example.com,127.0.0.1"}, false, "one URL per entry"},
		{"space inside", []string{"tls://a.example.com tls://b.example.com"}, false, "one URL per entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check(t, NATS(tt.servers, tt.allowPrivate), tt.wantErr)
		})
	}

	t.Run("too many", func(t *testing.T) {
		many := make([]string, MaxNATSServers+1)
		for i := range many {
			many[i] = "tls://nats.example.com"
		}
		check(t, NATS(many, false), "at most")
	})
}
