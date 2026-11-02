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
func CheckBaseURL(raw string, allowPrivate bool) error {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("base_url %q is not an absolute URL", raw)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowPrivate:
	default:
		return fmt.Errorf("base_url %q must use https", raw)
	}
	if u.User != nil {
		return fmt.Errorf("base_url %q must not carry a user name or password", raw)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("base_url %q must not carry a query string or fragment", raw)
	}
	if addr, perr := netip.ParseAddr(u.Hostname()); perr == nil && !allowPrivate && BlockedAddr(addr) {
		return fmt.Errorf("base_url %q points at a private, loopback or link-local address", raw)
	}
	return nil
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
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: dialControl(allowPrivate)}
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
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
