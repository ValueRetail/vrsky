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
