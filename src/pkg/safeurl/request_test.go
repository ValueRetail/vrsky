package safeurl

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Tests for #639: the API consumer's target is free-form, so http is allowed
// and the guard is the dial plus a same-host redirect policy.

// CheckRequestURL is the string check for a free-form target: http is fine, so
// are a query and userinfo; a literal blocked IP is not.
func TestCheckRequestURL(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		allowPrivate bool
		wantErr      string // substring; empty = ok
	}{
		{"https public host", "https://api.met.no/weatherapi/locationforecast/2.0/compact?lat=51.5&lon=-0.1", false, ""},
		{"http public host", "http://api.example.com/v1/items", false, ""},
		{"http public ip", "http://93.184.216.34/x", false, ""},
		{"userinfo is the tenant's own basic auth", "https://u:p@api.example.com/", false, ""},
		{"host with port", "https://api.example.com:8443/", false, ""},
		{"loopback v4", "http://127.0.0.1:8080/", false, "private"},
		{"loopback v6", "http://[::1]/", false, "private"},
		{"private 10", "https://10.0.0.5/", false, "private"},
		{"private 192", "http://192.168.1.1/admin", false, "private"},
		{"metadata", "http://169.254.169.254/latest/meta-data/", false, "private"},
		{"azure wire server", "http://168.63.129.16/", false, "private"},
		{"v4-mapped metadata", "http://[::ffff:169.254.169.254]/", false, "private"},
		{"ula", "http://[fd00::1]/", false, "private"},
		{"cgnat", "http://100.64.0.1/", false, "private"},
		{"ftp", "ftp://api.example.com/", false, "http or https"},
		{"file", "file:///etc/passwd", false, "not an absolute URL"},
		{"no scheme", "api.example.com/v1", false, "not an absolute URL"},
		{"relative path only", "/items", false, "not an absolute URL"},
		{"empty host", "https:///x", false, "not an absolute URL"},
		{"empty", "", false, "not an absolute URL"},
		{"dev flag allows loopback", "http://127.0.0.1:9000/", true, ""},
		{"dev flag allows private", "http://10.0.0.5/", true, ""},
		{"dev flag still wants a host", "http:///x", true, "not an absolute URL"},
		{"dev flag still wants http(s)", "ftp://127.0.0.1/", true, "http or https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckRequestURL(tt.url, tt.allowPrivate)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("CheckRequestURL(%q) = %v, want nil", tt.url, err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("CheckRequestURL(%q) = nil, want error containing %q", tt.url, tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("CheckRequestURL(%q) = %v, want error containing %q", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestEndpointURL(t *testing.T) {
	tests := []struct {
		name               string
		base, path, params string
		want               string
	}{
		{"base and path", "https://api.example.com", "/v1/items", "", "https://api.example.com/v1/items"},
		{"trailing slash on base", "https://api.example.com/", "/v1", "", "https://api.example.com/v1"},
		{"params", "https://api.example.com", "/v1", "a=1&b=2", "https://api.example.com/v1?a=1&b=2"},
		{"params after a query in the path", "https://api.example.com", "/v1?x=1", "a=1", "https://api.example.com/v1?x=1&a=1"},
		{"full https url in path wins", "https://api.example.com", "https://other.example.com/x", "", "https://other.example.com/x"},
		{"full http url in path wins", "https://api.example.com", "http://169.254.169.254/latest", "", "http://169.254.169.254/latest"},
		{"empty base, relative path", "", "/v1", "", "/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EndpointURL(tt.base, tt.path, tt.params); got != tt.want {
				t.Errorf("EndpointURL(%q, %q, %q) = %q, want %q", tt.base, tt.path, tt.params, got, tt.want)
			}
		})
	}
}

func newReq(t *testing.T, raw string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The redirect policy, hop by hop. via is the chain so far, the last entry
// being the request that was redirected.
func TestSameHostRedirect(t *testing.T) {
	five := []string{"https://a.example.com/1", "https://a.example.com/2", "https://a.example.com/3", "https://a.example.com/4", "https://a.example.com/5"}
	tests := []struct {
		name         string
		via          []string
		next         string
		allowPrivate bool
		wantErr      string // substring; empty = followed
	}{
		{"same host, other path", []string{"https://api.example.com/old"}, "https://api.example.com/new", false, ""},
		{"host name case", []string{"https://API.example.com/old"}, "https://api.example.com/new", false, ""},
		{"http to https upgrade, default ports", []string{"http://api.example.com/x"}, "https://api.example.com/x", false, ""},
		{"same explicit port", []string{"https://api.example.com:8443/a"}, "https://api.example.com:8443/b", false, ""},
		{"https to http downgrade", []string{"https://api.example.com/x"}, "http://api.example.com/x", false, "https to http"},
		{"other host", []string{"https://api.example.com/x"}, "https://evil.example.net/x", false, "another host"},
		{"subdomain is another host", []string{"https://api.example.com/x"}, "https://www.api.example.com/x", false, "another host"},
		{"same host, other port", []string{"https://api.example.com/x"}, "https://api.example.com:8443/x", false, "another host"},
		{"http to https on a non-default port", []string{"http://api.example.com:8080/x"}, "https://api.example.com/x", false, "another host"},
		{"metadata service", []string{"https://api.example.com/x"}, "http://169.254.169.254/latest/meta-data/", false, "private"},
		{"private literal even on the same host", []string{"http://10.0.0.5/x"}, "http://10.0.0.5/y", false, "private"},
		{"private literal, dev flag", []string{"http://10.0.0.5/x"}, "http://10.0.0.5/y", true, ""},
		{"not http", []string{"https://api.example.com/x"}, "ftp://api.example.com/x", false, "http or https"},
		{"fifth hop", five, "https://a.example.com/6", false, ""},
		{"sixth hop", append(append([]string{}, five...), "https://a.example.com/6"), "https://a.example.com/7", false, "redirects"},
		{"the first host is the reference", []string{"https://a.example.com/1", "https://a.example.com/2"}, "https://b.example.com/3", false, "another host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var via []*http.Request
			for _, v := range tt.via {
				via = append(via, newReq(t, v))
			}
			err := sameHostRedirect(tt.allowPrivate)(newReq(t, tt.next), via)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("redirect to %s = %v, want it followed", tt.next, err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("redirect to %s followed, want an error containing %q", tt.next, tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("redirect to %s = %v, want error containing %q", tt.next, err, tt.wantErr)
			}
		})
	}
}

// With real servers: a same-host redirect is followed, a redirect to another
// host is not, and the other host is never contacted. 127.0.0.1 and localhost
// are one machine but two hosts to the policy.
func TestClientWith_SameHostRedirects(t *testing.T) {
	var otherHits int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherHits++ }))
	defer other.Close()
	_, otherPort, _ := strings.Cut(strings.TrimPrefix(other.URL, "http://"), ":")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
		case "/new":
			_, _ = w.Write([]byte("arrived"))
		case "/away":
			http.Redirect(w, r, "http://localhost:"+otherPort+"/", http.StatusFound)
		case "/metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer srv.Close()

	c := NewClientWith(5*time.Second, Options{AllowPrivate: true, SameHostRedirects: true})

	resp, err := c.Get(srv.URL + "/old")
	if err != nil {
		t.Fatalf("same-host redirect: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "arrived" {
		t.Errorf("body after a same-host redirect = %q, want it followed", body)
	}

	for _, path := range []string{"/away", "/metadata", "/loop"} {
		resp, err := c.Get(srv.URL + path)
		if err == nil {
			_ = resp.Body.Close()
			t.Errorf("GET %s succeeded, want the redirect refused", path)
		}
	}
	if otherHits != 0 {
		t.Errorf("the other host received %d requests, want none", otherHits)
	}

	// Without the option nothing is followed, as for NewClient.
	plain := NewClientWith(5*time.Second, Options{AllowPrivate: true})
	resp, err = plain.Get(srv.URL + "/old")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("status %d, want the 301 surfaced when SameHostRedirects is off", resp.StatusCode)
	}
}

// The dial check holds with redirects on.
func TestClientWith_RedirectsStillDialChecked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := NewClientWith(5*time.Second, Options{SameHostRedirects: true})
	resp, err := c.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("request to loopback succeeded")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("error %v does not wrap ErrBlockedAddress", err)
	}
}
