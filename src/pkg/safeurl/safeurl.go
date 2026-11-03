// Package safeurl guards outbound HTTP to a URL a tenant supplied.
//
// A connector that builds a request URL from tenant config, and sends a
// credential with it, is an SSRF primitive unless the destination is checked:
// the tenant can aim it at the cluster network or the cloud metadata service.
// The check that matters is at dial time, on the address actually connected to,
// because a hostname can resolve to a private address (or change from a public
// to a private one between a config-time lookup and the connect, DNS
// rebinding). CheckBaseURL gives early, readable errors for the cases that can
// be seen from the string alone; NewClient is the enforcement.
//
// Dev escape hatch: ALLOW_PRIVATE_UPSTREAMS=true (set by the operator on the
// service, never by a tenant) allows http and private addresses, for a mock
// upstream in docker compose.
package safeurl

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// EnvAllowPrivate is the operator's switch for local development.
const EnvAllowPrivate = "ALLOW_PRIVATE_UPSTREAMS"

// AllowPrivateFromEnv reports whether the operator turned the dev escape hatch on.
func AllowPrivateFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvAllowPrivate))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// blocked lists the ranges a tenant-supplied URL must not reach. Most are also
// caught by the netip predicates in BlockedAddr; the list is explicit so the
// set is readable in one place, and for the ranges the predicates miss.
var blocked = mustPrefixes(
	"0.0.0.0/8",        // "this network"
	"10.0.0.0/8",       // RFC 1918
	"100.64.0.0/10",    // carrier-grade NAT
	"127.0.0.0/8",      // loopback
	"169.254.0.0/16",   // link-local, includes the metadata service 169.254.169.254
	"172.16.0.0/12",    // RFC 1918
	"192.0.0.0/24",     // IETF protocol assignments
	"192.168.0.0/16",   // RFC 1918
	"198.18.0.0/15",    // benchmarking
	"224.0.0.0/4",      // multicast
	"240.0.0.0/4",      // reserved, includes broadcast
	"168.63.129.16/32", // Azure wire server and platform DNS: a public address
	"::/128",           // unspecified
	"::1/128",          // loopback
	"fc00::/7",         // unique local, includes fd00:ec2::254
	"fe80::/10",        // link-local
	"ff00::/8",         // multicast
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// nat64 is the well-known NAT64 prefix; its low 32 bits are an IPv4 address.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// BlockedAddr reports whether a connection to addr is refused.
func BlockedAddr(addr netip.Addr) bool {
	addr = addr.Unmap().WithZone("")
	if !addr.IsValid() {
		return true
	}
	if nat64.Contains(addr) {
		b := addr.As16()
		return BlockedAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	for _, p := range blocked {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// CheckBaseURL checks a configured base URL from the string alone: https (http
// with allowPrivate), a host, no userinfo, query or fragment, and not a
// literal blocked IP. It cannot see where a hostname resolves; NewClient does.
// Its errors name the field "base_url"; CheckURL takes another name.
func CheckBaseURL(raw string, allowPrivate bool) error {
	return CheckURL("base_url", raw, allowPrivate)
}

// CheckURL is CheckBaseURL for a URL the config calls field (token_url, say),
// so the error names the setting the tenant has to fix. Errors never quote
// userinfo, a query or a fragment: they reach logs, activity events and the
// DLQ header.
func CheckURL(field, raw string, allowPrivate bool) error {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("%s %q is not an absolute URL", field, shownBaseURL(raw, u))
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowPrivate:
	default:
		return fmt.Errorf("%s %q must use https", field, shownBaseURL(raw, u))
	}
	if u.User != nil {
		return fmt.Errorf("%s must not carry a user name or password", field)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("%s must not carry a query string or fragment", field)
	}
	if addr, perr := netip.ParseAddr(u.Hostname()); perr == nil && !allowPrivate && BlockedAddr(addr) {
		return fmt.Errorf("%s %q points at a private, loopback or link-local address", field, shownBaseURL(raw, u))
	}
	return nil
}

// shownBaseURL is RedactURL, stricter about "@": a base URL has no business
// holding one outside userinfo, so when url.Parse did not put it in u.User it
// is taken as a password with "?", "#" or "/" in it and nothing is shown.
// RedactURL keeps "@" in a query, since a request URL may well carry an email
// address there.
func shownBaseURL(raw string, u *url.URL) string {
	if strings.Contains(raw, "@") && (u == nil || u.User == nil) {
		return "[invalid URL]"
	}
	return RedactURL(raw)
}

// RedactURL returns raw in a form that is safe to log, show in the activity
// feed or put in an error: a user name or password is replaced with "xxxxx"
// (a user name is often the API key, so Redacted() alone, which keeps it, is
// not enough). A string that does not parse becomes a fixed placeholder, as
// parse errors quote the input. So does one where userinfo may have landed
// somewhere other than u.User: without "//", url.Parse reads
// "user:secret@host/x" as scheme "user" with an opaque rest, and
// "http://user:123/secret@host" as host "user" with "@" in the path. An "@" in
// a real path costs only the display. Same for "@" in the fragment:
// "http://apikey:1234#secret@host" is host "apikey", port 1234 and the rest of
// the password as the fragment. The request itself keeps using the real URL.
func RedactURL(raw string) string {
	const invalid = "[invalid URL]"
	u, err := url.Parse(raw)
	if err != nil {
		return invalid
	}
	if u.Opaque != "" || strings.Contains(u.Path, "@") || strings.Contains(u.Fragment, "@") ||
		(u.Host == "" && u.User == nil && strings.Contains(raw, "@")) {
		return invalid
	}
	if u.User != nil {
		u.User = url.User("xxxxx")
	}
	return u.Redacted()
}

// CheckRequestURL checks a complete request URL for a connector whose target is
// free-form (the API consumer): http or https, a host, and not a literal
// blocked IP. Unlike CheckBaseURL it allows http, a query string and userinfo,
// none of which changes which address is reached; the dial in NewClient is the
// check on that. allowPrivate is the operator's dev switch.
func CheckRequestURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("url %s is not an absolute URL", RedactURL(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url %s must use http or https", RedactURL(raw))
	}
	if addr, perr := netip.ParseAddr(u.Hostname()); perr == nil && !allowPrivate && BlockedAddr(addr) {
		return fmt.Errorf("url %s points at a private, loopback or link-local address", RedactURL(raw))
	}
	return nil
}

// EndpointURL builds the URL an endpoint of the API consumer calls: path is
// used as it is when it is already a full http(s) URL, otherwise it is
// appended to base_url; params is added as the query string. The consumer and
// the management API both use it, so what is checked is what is called.
func EndpointURL(baseURL, path, params string) string {
	var u string
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		u = path
	} else {
		u = strings.TrimSuffix(baseURL, "/") + path
	}
	if params != "" {
		if strings.Contains(u, "?") {
			u += "&" + params
		} else {
			u += "?" + params
		}
	}
	return u
}

// ErrBlockedAddress is returned (wrapped) when a connection is refused.
var ErrBlockedAddress = errors.New("connection to a private, loopback or link-local address refused")

// dialControl runs after name resolution, on each address about to be dialled.
func dialControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: cannot parse %q", ErrBlockedAddress, address)
		}
		if BlockedAddr(ap.Addr()) {
			return fmt.Errorf("%w (%s)", ErrBlockedAddress, ap.Addr())
		}
		return nil
	}
}

// NewClient returns an HTTP client that refuses blocked destinations at dial
// time. It ignores proxy environment variables (a proxy would turn the check
// into one on the proxy) and does not follow redirects, so a credential is
// never forwarded to a host the configured URL did not name.
func NewClient(timeout time.Duration, allowPrivate bool) *http.Client {
	return NewClientWith(timeout, Options{AllowPrivate: allowPrivate})
}

// maxRedirects bounds a same-host redirect chain.
const maxRedirects = 5

// Options tune NewClientWith.
type Options struct {
	// AllowPrivate is the operator's dev switch (see AllowPrivateFromEnv).
	AllowPrivate bool
	// SameHostRedirects follows up to five redirects that stay on the host
	// the request started on (an http to https upgrade on the default ports
	// counts as the same host), never https to http. Any other redirect is an
	// error. Without it no redirect is followed and the 3xx is returned.
	SameHostRedirects bool
	// TLSConfig, when set, is the transport's client TLS configuration (a
	// client certificate and a pinned CA, for mTLS). The dial check is
	// unchanged: it sits below TLS, on the TCP connect.
	TLSConfig *tls.Config
}

// NewClientWith is NewClient with options. The dial check applies on every
// hop whatever the options.
func NewClientWith(timeout time.Duration, o Options) *http.Client {
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: dialControl(o.AllowPrivate)}
	check := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if o.SameHostRedirects {
		check = sameHostRedirect(o.AllowPrivate)
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return d.DialContext(ctx, network, addr)
			},
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
			TLSClientConfig:       o.TLSConfig,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: check,
	}
}

// sameHostRedirect is the CheckRedirect for Options.SameHostRedirects. Every
// hop is checked as a request URL again; the dial check covers the address.
func sameHostRedirect(allowPrivate bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if err := CheckRequestURL(req.URL.String(), allowPrivate); err != nil {
			return fmt.Errorf("redirect refused: %w", err)
		}
		first, prev := via[0].URL, via[len(via)-1].URL
		if prev.Scheme == "https" && req.URL.Scheme == "http" {
			return fmt.Errorf("redirect from https to http refused (%s)", RedactURL(req.URL.String()))
		}
		sameHost := strings.EqualFold(first.Hostname(), req.URL.Hostname())
		samePort := effectivePort(prev) == effectivePort(req.URL) || isHTTPUpgrade(prev, req.URL)
		if !sameHost || !samePort {
			return fmt.Errorf("redirect to another host refused (%s to %s)", first.Host, req.URL.Host)
		}
		return nil
	}
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// isHTTPUpgrade reports an http to https hop between the default ports.
func isHTTPUpgrade(from, to *url.URL) bool {
	return from.Scheme == "http" && to.Scheme == "https" &&
		effectivePort(from) == "80" && effectivePort(to) == "443"
}
