package managementapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The address a limit is keyed on, through the proxies prod actually has.
// Every case is a request as management-api sees it: RemoteAddr is the last
// hop that connected to it.
func TestClientAddr(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		xff        []string
		want       string // the limiter key
		wantIP     string // what audit rows store
	}{
		{"prod: ingress-nginx then the UI pod's nginx",
			"10.244.1.5:51000", []string{"203.0.113.9, 10.240.0.4"}, "203.0.113.9", "203.0.113.9"},
		{"a spoofed leftmost entry is ignored",
			"10.244.1.5:51000", []string{"198.51.100.77, 203.0.113.9, 10.240.0.4"}, "203.0.113.9", "203.0.113.9"},
		{"a spoofed private leftmost entry is ignored too",
			"10.244.1.5:51000", []string{"10.0.0.1, 203.0.113.9, 10.240.0.4"}, "203.0.113.9", "203.0.113.9"},
		{"straight from one proxy (the webhooks ingress)",
			"10.240.0.4:40000", []string{"203.0.113.9"}, "203.0.113.9", "203.0.113.9"},
		{"the header split over two lines",
			"10.244.1.5:51000", []string{"203.0.113.9", "10.240.0.4"}, "203.0.113.9", "203.0.113.9"},
		{"no proxy at all",
			"203.0.113.9:40000", nil, "203.0.113.9", "203.0.113.9"},
		{"the dev stack: every hop private, the furthest wins",
			"172.18.0.9:40000", []string{"172.18.0.1"}, "172.18.0.1", "172.18.0.1"},
		{"IPv6 client is keyed by its /64, stored whole",
			"10.244.1.5:51000", []string{"2001:db8:1:2::1, 10.240.0.4"}, "2001:db8:1:2::/64", "2001:db8:1:2::1"},
		{"bracketed IPv6 with a port",
			"[2001:db8:1:2::1]:40000", nil, "2001:db8:1:2::/64", "2001:db8:1:2::1"},
		{"IPv4-mapped is IPv4",
			"10.244.1.5:51000", []string{"::ffff:203.0.113.9, 10.240.0.4"}, "203.0.113.9", "203.0.113.9"},
		{"junk entries are skipped",
			"10.244.1.5:51000", []string{"unknown, 203.0.113.9, 10.240.0.4"}, "203.0.113.9", "203.0.113.9"},
		{"nothing usable",
			"", []string{"unknown"}, "unknown", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
			req.RemoteAddr = tc.remoteAddr
			for _, v := range tc.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			// X-Real-IP is the ingress pod at this service; it must not win.
			req.Header.Set("X-Real-IP", "10.240.0.4")
			if got := clientAddr(req); got != tc.want {
				t.Errorf("clientAddr = %q, want %q", got, tc.want)
			}
			if got := clientIPString(req); got != tc.wantIP {
				t.Errorf("clientIPString = %q, want %q", got, tc.wantIP)
			}
		})
	}
}
