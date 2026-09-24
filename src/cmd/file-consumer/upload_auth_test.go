package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// /upload publishes caller-supplied bytes into a running pipeline. It used to
// be open with Access-Control-Allow-Origin: *; the token is what stops anything
// on the cluster network from reaching past the management API's proxy.
func TestAuthorizedUpload(t *testing.T) {
	cases := []struct {
		name      string
		token     string
		header    string
		wantAllow bool
	}{
		{"no token configured allows all", "", "", true},
		{"no token ignores any header", "", "Bearer whatever", true},
		{"token set, missing header denied", "secret", "", false},
		{"token set, wrong scheme denied", "secret", "secret", false},
		{"token set, wrong token denied", "secret", "Bearer nope", false},
		{"token set, prefix of the token denied", "secret", "Bearer sec", false},
		{"token set, correct token allowed", "secret", "Bearer secret", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/upload/conn-1", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if got := authorizedUpload(r, tc.token); got != tc.wantAllow {
				t.Errorf("authorizedUpload() = %v, want %v", got, tc.wantAllow)
			}
		})
	}
}

// The wildcard is what let any page on the internet post to this endpoint from
// a visitor's browser. Only the configured origin gets CORS access now.
func TestUploadCORSIsNotAWildcard(t *testing.T) {
	s := &fileConsumer{uploadOrigin: "https://vrsky.example"}

	for _, tc := range []struct {
		name, origin, want string
	}{
		{"configured origin echoed", "https://vrsky.example", "https://vrsky.example"},
		{"other origin gets nothing", "https://evil.example", ""},
		{"no origin gets nothing", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodOptions, "/upload/conn-1", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			s.handleUpload()(rec, r)

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.want {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tc.want)
			}
			if rec.Header().Get("Vary") != "Origin" {
				t.Error("Vary: Origin missing — a shared cache could serve one origin's response to another")
			}
		})
	}
}
