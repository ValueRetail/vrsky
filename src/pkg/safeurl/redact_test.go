package safeurl

import (
	"strings"
	"testing"
)

// CheckRequestURL names the URL in its errors, and those reach logs, the
// activity feed and dead-letter headers, so userinfo must not be in them (#689).
func TestCheckRequestURL_ErrorsDoNotQuoteUserinfo(t *testing.T) {
	for _, raw := range []string{
		"ftp://user:secret@example.com/x",
		"http://user:secret@10.0.0.5/x",
		"http://secret@169.254.169.254/",
		"http://user:secret@host:80/%zz",
		"http://user:secret@/x",
		// No "//", so url.Parse never fills u.User: the credential sits in
		// Opaque, Path or a host:port that is really user:port.
		"user:secret@host/x",
		"https:user:secret@host/x",
		"secret@host/x",
		// Parsed as host "user", port 123, path "/secret@host". With http it
		// passes this check (TestRedactURL covers it); ftp gets the scheme error.
		"ftp://user:123/secret@host",
	} {
		err := CheckRequestURL(raw, false)
		if err == nil {
			t.Fatalf("CheckRequestURL(%q) = nil, want an error", raw)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("CheckRequestURL(%q) error %q contains the secret", raw, err)
		}
	}
}

// CheckBaseURL's errors reach logs, activity events and the DLQ header, so no
// path may quote userinfo (#700). Every row is userinfo-bearing input that
// fails one check; "secret" stands for the credential.
func TestCheckBaseURL_ErrorsDoNotQuoteUserinfo(t *testing.T) {
	tests := []struct {
		name, raw    string
		allowPrivate bool
		wantErr      string
	}{
		{"not absolute: bad escape", "https://user:secret@host/%zz", false, "not an absolute URL"},
		{"not absolute: no host", "https://user:secret@/x", false, "not an absolute URL"},
		{"not absolute: no //", "user:secret@host/x", false, "not an absolute URL"},
		{"not absolute: user name only, no //", "secret@host/x", false, "not an absolute URL"},
		{"scheme: http", "http://user:secret@api.example.com/v2", false, "must use https"},
		{"scheme: ftp", "ftp://secret@api.example.com/v2", false, "must use https"},
		{"scheme: password in the path", "ftp://user:123/secret@host", false, "must use https"},
		{"scheme: password with ?", "http://apikey:1234?secret@host", false, "must use https"},
		// url.Parse reads the user name as the host; it is often the key.
		{"scheme: password with ?, user name is the key", "http://secret:1234?pw@host", false, "must use https"},
		{"scheme: password with #, user name is the key", "http://secret:1234#pw@host", false, "must use https"},
		{"scheme: key in the query", "http://host/x?key=secret", false, "must use https"},
		{"scheme: key in the fragment", "http://host/x#secret", false, "must use https"},
		{"not absolute: key in the query", "https:///x?key=secret", false, "not an absolute URL"},
		{"userinfo", "https://user:secret@api.example.com/v2", false, "user name"},
		{"userinfo: user name is the key", "https://secret@api.example.com/v2", false, "user name"},
		{"userinfo: dev flag", "http://user:secret@mock/v2", true, "user name"},
		{"query", "https://api.example.com/v2?key=secret", false, "query"},
		{"query: password with ?", "https://apikey:1234?secret@host/v2", false, "query"},
		{"fragment: password with #", "https://apikey:1234#secret@host/v2", false, "fragment"},
		{"private: password with /", "https://10.0.0.1:123/secret@host/v2", false, "private"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckBaseURL(tt.raw, tt.allowPrivate)
			if err == nil {
				t.Fatalf("CheckBaseURL(%q) = nil, want an error containing %q", tt.raw, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("CheckBaseURL(%q) = %v, want an error containing %q", tt.raw, err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("CheckBaseURL(%q) error %q contains the secret", tt.raw, err)
			}
		})
	}
}

// A password containing "#" leaves url.Parse reading the user name as the host
// and the rest of the password as the fragment (#700).
func TestRedactURL_FragmentWithAt(t *testing.T) {
	tests := []struct{ in, want string }{
		{"http://apikey:1234#secret@host", "[invalid URL]"},
		{"https://apikey:1234/x#secret@host", "[invalid URL]"},
		{"https://host/x#a%40b", "[invalid URL]"},
		{"https://host/x#section", "https://host/x#section"},
		{"https://user:pw@host/x#section", "https://xxxxx@host/x#section"},
	}
	for _, tt := range tests {
		if got := RedactURL(tt.in); got != tt.want {
			t.Errorf("RedactURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// CheckURL names the field it was given, at the start of every message, and
// never "base_url" (#700, backlog 163).
func TestCheckURL_NamesTheField(t *testing.T) {
	for _, raw := range []string{
		"not a url",
		"http://login.example.com/token",
		"https://u:p@login.example.com/token",
		"https://login.example.com/token?x=1",
		"https://127.0.0.1/token",
	} {
		err := CheckURL("token_url", raw, false)
		if err == nil {
			t.Fatalf("CheckURL(%q) = nil, want an error", raw)
		}
		if !strings.HasPrefix(err.Error(), "token_url ") || strings.Contains(err.Error(), "base_url") {
			t.Errorf("CheckURL(%q) = %q, want it to start with token_url and not name base_url", raw, err)
		}
	}
	if err := CheckURL("token_url", "https://login.example.com/token", false); err != nil {
		t.Errorf("CheckURL on a good URL = %v, want nil", err)
	}
}

// A password containing "?" lands in the query (#717, backlog 176). With no
// path before the "?" that is what "@" in a query means; with a path it is a
// plain query (an email address, say) and stays visible.
func TestRedactURL_QueryWithAt(t *testing.T) {
	tests := []struct{ in, want string }{
		{"http://apikey:1234?secret@host", "[invalid URL]"},
		{"http://secret?pw@host", "[invalid URL]"},
		{"https://apikey:1234?secret@host/v2", "[invalid URL]"},
		{"http://host/x?email=a@b", "http://host/x?email=a@b"},
		{"http://host:8080/?email=a@b", "http://host:8080/?email=a@b"},
		{"http://host?a=1", "http://host?a=1"},
		{"http://user:pw@host?a=1", "http://xxxxx@host?a=1"},
	}
	for _, tt := range tests {
		if got := RedactURL(tt.in); got != tt.want {
			t.Errorf("RedactURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	for _, raw := range []string{"http://apikey:1234?secret@host", "https://apikey:1234?secret@host/v2"} {
		if err := CheckRequestURL(raw, true); err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("CheckRequestURL(%q) = %v, quotes the password", raw, err)
		}
	}
}
