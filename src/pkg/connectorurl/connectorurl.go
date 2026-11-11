// Package connectorurl holds the URL checks for connectors whose target comes
// from tenant config and receives a credential: Brightpearl, Front Systems,
// SAP S/4HANA, Business Central and the NATS consumer's servers. Each connector calls its function at start
// and before it sends anything, and the management API calls the same one when
// a connection starts, so the two cannot drift (#708). Visma has its own
// package, pkg/vismaurl.
//
// These are the early, readable errors from the strings alone. The dial check
// in safeurl.NewClient is the enforcement: it is the only one that sees where
// a host name resolves. allowPrivate is the operator's dev switch
// (safeurl.AllowPrivateFromEnv). Errors name the setting to fix and never
// quote userinfo, a query or a fragment.
package connectorurl

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
)

var (
	brightpearlDatacenter  = regexp.MustCompile(`(?i)^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	brightpearlAccountCode = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// BrightpearlBaseURL is the default API root for a datacenter and account
// code: https://{datacenter}.brightpearlconnect.com/public-api/{account_code}
// (#732, https://help.brightpearl.com/hc/en-us/articles/212645003-URI-syntax).
// Configs written for the legacy ws-{code}.brightpearl.com hosts use the old
// codes: eu1 is euw1 now and use is use1. US West (usw, usw1) has no
// documented brightpearlconnect.com name, and ws-usw.brightpearl.com still
// serves the API, so it keeps the legacy host. It returns "" when either part
// is empty.
func BrightpearlBaseURL(datacenter, accountCode string) string {
	if datacenter == "" || accountCode == "" {
		return ""
	}
	dc := strings.ToLower(datacenter)
	switch dc {
	case "usw", "usw1":
		return "https://ws-usw.brightpearl.com/public-api/" + accountCode
	case "eu1":
		dc = "euw1"
	case "use":
		dc = "use1"
	}
	return "https://" + dc + ".brightpearlconnect.com/public-api/" + accountCode
}

// Brightpearl checks where the staff token goes (#667). A base_url override
// is checked as a URL. Without one the connector calls
// https://{datacenter}.brightpearlconnect.com/public-api/{account_code}, so
// datacenter must be one host label and account_code one path segment;
// otherwise "x@10.0.0.1/" as a datacenter moves the host.
func Brightpearl(baseURL, datacenter, accountCode string, allowPrivate bool) error {
	if baseURL != "" {
		return safeurl.CheckBaseURL(baseURL, allowPrivate)
	}
	if !brightpearlDatacenter.MatchString(datacenter) {
		return fmt.Errorf("%s must be a single host label such as euw1 or use1", shown("datacenter", datacenter))
	}
	if !brightpearlAccountCode.MatchString(accountCode) {
		return fmt.Errorf("%s may only hold letters, digits, - and _", shown("account_code", accountCode))
	}
	return nil
}

// FrontSystems checks the base_url both API keys are sent to (#677). It is
// required: there is no default, each partner has its own Azure APIM host.
func FrontSystems(baseURL string, allowPrivate bool) error {
	if strings.TrimSpace(baseURL) == "" {
		return errors.New("base_url is required (the partner's Azure APIM host)")
	}
	return safeurl.CheckBaseURL(baseURL, allowPrivate)
}

// SAPConfig is the part of an SAP S/4HANA node that decides where requests go.
type SAPConfig struct {
	Host       string // e.g. my347623.s4hana.ondemand.com, optionally :port
	Service    string // OData service, used with Host
	APIBaseURL string // full service root; overrides Host + Service
	TokenURL   string // oauth2; default https://{host}/sap/bc/sec/oauth2/token
	AuthType   string // "oauth2", anything else is basic
}

const sapHostLabel = `[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?`

var (
	sapHostName = regexp.MustCompile(`^` + sapHostLabel + `(\.` + sapHostLabel + `)*$`)
	sapHostPort = regexp.MustCompile(`^[0-9]{1,5}$`)
)

// SAP checks the service root and, for oauth2, the token URL: both carry the
// tenant's credential (#678). host goes into the default URLs as it is, so it
// must be one host name with an optional numeric port; a value such as
// "x@10.0.0.1/" or "sap.example.com/admin?" would otherwise change the host or
// the path while the node looks like it uses the default.
func SAP(cfg SAPConfig, allowPrivate bool) error {
	if cfg.APIBaseURL != "" {
		if err := safeurl.CheckURL("api_base_url", strings.TrimRight(cfg.APIBaseURL, "/"), allowPrivate); err != nil {
			return err
		}
	} else {
		if err := sapCheckHost(cfg.Host); err != nil {
			return err
		}
		root := fmt.Sprintf("https://%s/sap/opu/odata/sap/%s", cfg.Host, cfg.Service)
		if err := safeurl.CheckURL("host", root, allowPrivate); err != nil {
			return err
		}
	}
	if strings.ToLower(strings.TrimSpace(cfg.AuthType)) != "oauth2" {
		return nil
	}
	if cfg.TokenURL != "" {
		return safeurl.CheckURL("token_url", cfg.TokenURL, allowPrivate)
	}
	if err := sapCheckHost(cfg.Host); err != nil {
		return fmt.Errorf("token_url defaults to the host: %w", err)
	}
	return safeurl.CheckURL("token_url", fmt.Sprintf("https://%s/sap/bc/sec/oauth2/token", cfg.Host), allowPrivate)
}

func sapCheckHost(host string) error {
	name, port, hasPort := strings.Cut(host, ":")
	ok := host != "" && len(name) <= 253 && sapHostName.MatchString(name)
	if ok && hasPort {
		n, err := strconv.Atoi(port)
		ok = sapHostPort.MatchString(port) && err == nil && n >= 1 && n <= 65535
	}
	if !ok {
		return fmt.Errorf("%s must be a host name such as my347623.s4hana.ondemand.com, optionally with :port, and no scheme, path or user name", shown("host", host))
	}
	return nil
}

// BusinessCentralConfig is the part of a Business Central node that decides
// where requests go.
type BusinessCentralConfig struct {
	APIBaseURL  string // API v2.0 root; default is the cloud API for AADTenantID
	TokenURL    string // default is Entra's token endpoint for AADTenantID
	AADTenantID string
	Environment string // default Production
}

// BusinessCentral checks the API root, which gets the bearer token, and the
// token URL, which gets the client secret (#686). Unset, both are Microsoft
// hosts with aad_tenant_id (and environment) in the path, and an error then
// names those fields.
func BusinessCentral(cfg BusinessCentralConfig, allowPrivate bool) error {
	apiField, api := "api_base_url", cfg.APIBaseURL
	if api == "" {
		env := cfg.Environment
		if env == "" {
			env = "Production"
		}
		apiField = "aad_tenant_id or environment"
		api = fmt.Sprintf("https://api.businesscentral.dynamics.com/v2.0/%s/%s/api/v2.0", cfg.AADTenantID, env)
	}
	if err := safeurl.CheckURL(apiField, api, allowPrivate); err != nil {
		return err
	}
	tokenField, token := "token_url", cfg.TokenURL
	if token == "" {
		tokenField = "aad_tenant_id"
		token = fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", cfg.AADTenantID)
	}
	return safeurl.CheckURL(tokenField, token, allowPrivate)
}

// shown names a refused field with its value quoted, unless the value may hold
// a credential: "@" (userinfo), "?" or "#" (a query or fragment, which may
// carry a key). Then only the field is named. Like safeurl's errors, these
// reach the start answer, logs, the activity feed and the DLQ header.
func shown(field, value string) string {
	if strings.ContainsAny(value, "@?#") {
		return field
	}
	return fmt.Sprintf("%s %q", field, value)
}

// MaxNATSServers bounds the server list of one NATS source.
const MaxNATSServers = 16

// NATS checks the server URLs of a NATS source (#814). Each must be
// nats://host[:port] or tls://host[:port]: no user name or password (those
// come from stored secrets), no path, query or fragment, and not a literal
// private, loopback or link-local address. The scheme does not decide TLS:
// the consumer requires it unless allowPrivate is set, since a nats:// server
// upgrades to TLS when asked. The consumer's dialer (safeurl.NewDialer) checks
// where a host name resolves.
func NATS(servers []string, allowPrivate bool) error {
	if len(servers) == 0 {
		return errors.New("servers: at least one server URL is required (tls://host:4222)")
	}
	if len(servers) > MaxNATSServers {
		return fmt.Errorf("servers: at most %d server URLs", MaxNATSServers)
	}
	for _, raw := range servers {
		if err := natsServer(strings.TrimSpace(raw), allowPrivate); err != nil {
			return err
		}
	}
	return nil
}

func natsServer(raw string, allowPrivate bool) error {
	const field = "servers"
	// nats.go splits a server string on commas: one entry must stay one
	// server, or a second one would slip past this check.
	if strings.ContainsAny(raw, ", \t\r\n") {
		return fmt.Errorf("%s: one URL per entry, without commas or spaces", field)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("%s is not an absolute URL (tls://host:4222)", shown(field, raw))
	}
	switch strings.ToLower(u.Scheme) {
	case "nats", "tls":
	default:
		return fmt.Errorf("%s must use nats:// or tls://", shown(field, raw))
	}
	if u.User != nil || strings.Contains(raw, "@") {
		return fmt.Errorf("%s must not carry a user name or password; use the stored credentials", field)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("%s must not carry a query string or fragment", field)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("%s must not carry a path", shown(field, raw))
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%s has an invalid port", shown(field, raw))
		}
	}
	if addr, perr := netip.ParseAddr(u.Hostname()); perr == nil && !allowPrivate && safeurl.BlockedAddr(addr) {
		return fmt.Errorf("%s points at a private, loopback or link-local address", shown(field, raw))
	}
	return nil
}
