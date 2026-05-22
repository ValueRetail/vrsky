package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthorizedFileRequest(t *testing.T) {
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
		{"token set, correct token allowed", "secret", "Bearer secret", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodDelete, "/files?path=/data/output/x", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if got := authorizedFileRequest(r, tc.token); got != tc.wantAllow {
				t.Errorf("authorizedFileRequest() = %v, want %v", got, tc.wantAllow)
			}
		})
	}
}
