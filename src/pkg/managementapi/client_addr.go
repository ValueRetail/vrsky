package managementapi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/failurelimit"
)

// clientIPAddr is the address a request came from, as far as it can be known.
//
// In production a request passes two proxies before it reaches this service:
// ingress-nginx, then the UI pod's nginx for /api/. So X-Real-IP here is the
// ingress pod, and X-Forwarded-For reads "<client>, <ingress pod>". The entry
// to trust is found by walking the hops from the right — RemoteAddr first,
// then X-Forwarded-For from its last entry backwards — and skipping every
// address that is one of ours: private, loopback or link-local. The first
// public address is the client. That holds whether a proxy overwrites the
// header or appends to it, through one proxy or two, because a client on the
// internet cannot have a private source address; and it cannot be dodged by
// sending an X-Forwarded-For of one's own, which only adds entries further to
// the left. Reading the leftmost entry, as this code once did, trusts exactly
// that.
//
// When no hop is public (the dev stack, tests) the furthest hop is the client.
func clientIPAddr(r *http.Request) (netip.Addr, bool) {
	var hops []netip.Addr
	if addr, ok := parseHop(r.RemoteAddr); ok {
		hops = append(hops, addr)
	}
	for _, h := range r.Header.Values("X-Forwarded-For") {
		entries := strings.Split(h, ",")
		for i := len(entries) - 1; i >= 0; i-- {
			if addr, ok := parseHop(entries[i]); ok {
				hops = append(hops, addr)
			}
		}
	}
	if len(hops) == 0 {
		return netip.Addr{}, false
	}
	for _, addr := range hops {
		if !ownProxy(addr) {
			return addr, true
		}
	}
	return hops[len(hops)-1], true
}

// parseHop reads one address as it appears in RemoteAddr or a forwarded
// header: with or without a port, brackets or surrounding space.
func parseHop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.TrimPrefix(strings.TrimSuffix(s, "]"), "[")
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}

// ownProxy is true for an address that can only be one of our own hops.
func ownProxy(addr netip.Addr) bool {
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified()
}

// clientAddr is the limiter key for the request's address: the address
// itself for IPv4, its /64 for IPv6. "unknown" when there is none.
func clientAddr(r *http.Request) string {
	addr, ok := clientIPAddr(r)
	if !ok {
		return "unknown"
	}
	return failurelimit.AddressKey(addr)
}

// clientIPString is the client address for audit rows and sessions, in the
// form Postgres inet accepts; empty when unknown.
func clientIPString(r *http.Request) string {
	addr, ok := clientIPAddr(r)
	if !ok {
		return ""
	}
	return addr.String()
}
