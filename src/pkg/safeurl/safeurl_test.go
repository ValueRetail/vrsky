package safeurl

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestBlockedAddr(t *testing.T) {
	tests := []struct {
		addr    string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"127.1.2.3", true},
		{"::1", true},
		{"10.0.0.5", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},
		{"168.63.129.16", true},
		{"100.64.0.1", true},
		{"0.0.0.0", true},
		{"::", true},
		{"224.0.0.1", true},
		{"255.255.255.255", true},
		{"fe80::1", true},
		{"fc00::1", true},
		{"fd00:ec2::254", true},
		{"::ffff:10.0.0.1", true},        // v4-mapped private
		{"::ffff:169.254.169.254", true}, // v4-mapped metadata
		{"64:ff9b::a00:1", true},         // NAT64 wrapping 10.0.0.1
		{"fe80::1%eth0", true},           // zone is ignored
		{"93.184.216.34", false},
		{"8.8.8.8", false},
		{"172.32.0.1", false}, // just outside 172.16/12
		{"2606:4700:4700::1111", false},
		{"::ffff:8.8.8.8", false},
		{"64:ff9b::808:808", false}, // NAT64 wrapping 8.8.8.8
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			addr, err := netip.ParseAddr(tt.addr)
			if err != nil {
				t.Fatalf("parse %q: %v", tt.addr, err)
			}
			if got := BlockedAddr(addr); got != tt.blocked {
				t.Errorf("BlockedAddr(%s) = %v, want %v", tt.addr, got, tt.blocked)
			}
		})
	}
}

func TestBlockedAddr_Invalid(t *testing.T) {
	if !BlockedAddr(netip.Addr{}) {
		t.Error("the zero Addr must be refused")
	}
}

func TestCheckBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		allowPrivate bool
		wantErr      string // substring; empty = ok
	}{
		{"default sitoo", "https://api.mysitoo.com/v2", false, ""},
		{"public host with port", "https://sitoo.example.com:8443/v2", false, ""},
		{"public ip", "https://93.184.216.34/v2", false, ""},
		{"http rejected", "http://api.mysitoo.com/v2", false, "must use https"},
		{"ftp rejected", "ftp://api.mysitoo.com/v2", false, "must use https"},
		{"no scheme", "api.mysitoo.com/v2", false, "not an absolute URL"},
		{"empty host", "https:///v2", false, "not an absolute URL"},
		{"loopback v4", "https://127.0.0.1/v2", false, "private"},
		{"loopback v6", "https://[::1]/v2", false, "private"},
		{"private 10", "https://10.1.2.3/v2", false, "private"},
		{"private 172", "https://172.20.0.1/v2", false, "private"},
		{"private 192", "https://192.168.0.10:8443/v2", false, "private"},
		{"metadata", "https://169.254.169.254/latest/meta-data", false, "private"},
		{"azure wire server", "https://168.63.129.16/", false, "private"},
		{"v4-mapped metadata", "https://[::ffff:169.254.169.254]/", false, "private"},
		{"ula", "https://[fd00::1]/", false, "private"},
		{"userinfo", "https://user:pw@api.mysitoo.com/v2", false, "user name"},
		{"query", "https://api.mysitoo.com/v2?x=1", false, "query"},
		{"fragment", "https://api.mysitoo.com/v2#x", false, "query string or fragment"},
		{"dev flag allows http", "http://sitoo-mock:8080/v2", true, ""},
		{"dev flag allows loopback", "https://127.0.0.1:9999/v2", true, ""},
		{"dev flag still wants a host", "http:///v2", true, "not an absolute URL"},
		{"dev flag still refuses userinfo", "http://u:p@mock/v2", true, "user name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckBaseURL(tt.url, tt.allowPrivate)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("CheckBaseURL(%q) = %v, want nil", tt.url, err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("CheckBaseURL(%q) = nil, want error containing %q", tt.url, tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("CheckBaseURL(%q) = %v, want error containing %q", tt.url, err, tt.wantErr)
			}
		})
	}
}

// The dial control is where the guard lives: it sees the resolved address.
func TestDialControl(t *testing.T) {
	tests := []struct {
		name         string
		address      string
		allowPrivate bool
		wantBlocked  bool
	}{
		{"public v4", "93.184.216.34:443", false, false},
		{"public v6", "[2606:4700:4700::1111]:443", false, false},
		{"loopback", "127.0.0.1:443", false, true},
		{"loopback v6", "[::1]:443", false, true},
		{"private", "10.0.0.7:443", false, true},
		{"metadata", "169.254.169.254:80", false, true},
		{"v4-mapped private", "[::ffff:192.168.1.1]:443", false, true},
		{"unparseable", "not-an-address", false, true},
		{"dev flag lets private through", "10.0.0.7:443", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := dialControl(tt.allowPrivate)("tcp", tt.address, nil)
			if tt.wantBlocked != (err != nil) {
				t.Fatalf("dialControl(%q) = %v, blocked want %v", tt.address, err, tt.wantBlocked)
			}
			if err != nil && !errors.Is(err, ErrBlockedAddress) {
				t.Errorf("error %v does not wrap ErrBlockedAddress", err)
			}
		})
	}
}

// A hostname passes CheckBaseURL (it is not a literal IP); only the dial-time
// check sees that it resolves to loopback. This is the DNS-name SSRF case.
func TestClient_RefusesHostnameResolvingToLoopback(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	_, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")

	target := "http://localhost:" + port + "/"
	if err := CheckBaseURL(strings.Replace(target, "http://", "https://", 1), false); err != nil {
		t.Fatalf("a hostname is not caught at config time, got %v", err)
	}
	resp, err := NewClient(5*time.Second, false).Get(target)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("request to a name resolving to loopback succeeded")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("error %v does not wrap ErrBlockedAddress", err)
	}
	if hit {
		t.Error("the server was reached")
	}
}

func TestClient_LiteralPrivateIPRefusedAtDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	resp, err := NewClient(5*time.Second, false).Get(srv.URL) // http://127.0.0.1:port
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("request to 127.0.0.1 succeeded")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("error %v does not wrap ErrBlockedAddress", err)
	}
}

func TestClient_DevFlagReachesLocalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	resp, err := NewClient(5*time.Second, true).Get(srv.URL)
	if err != nil {
		t.Fatalf("with the dev flag: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestClient_DoesNotFollowRedirects(t *testing.T) {
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()
	resp, err := NewClient(5*time.Second, true).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || followed {
		t.Errorf("status %d, followed=%v; a redirect must be surfaced, not followed", resp.StatusCode, followed)
	}
}

func TestAllowPrivateFromEnv(t *testing.T) {
	tests := map[string]bool{"": false, "true": true, "TRUE": true, "1": true, "yes": true, "false": false, "0": false, "nope": false}
	for v, want := range tests {
		t.Run("value="+v, func(t *testing.T) {
			t.Setenv(EnvAllowPrivate, v)
			if got := AllowPrivateFromEnv(); got != want {
				t.Errorf("AllowPrivateFromEnv() with %q = %v, want %v", v, got, want)
			}
		})
	}
}

// NewDialer is the dial check for clients that are not HTTP. A hostname that
// resolves to loopback is refused after the lookup; the dev flag lets it through.
func TestNewDialer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	for _, host := range []string{"127.0.0.1", "localhost"} {
		_, err := NewDialer(2*time.Second, false).Dial("tcp", net.JoinHostPort(host, port))
		if !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("dial %s: error %v does not wrap ErrBlockedAddress", host, err)
		}
	}
	c, err := NewDialer(2*time.Second, true).Dial("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatalf("dev flag: dial refused: %v", err)
	}
	_ = c.Close()
}
