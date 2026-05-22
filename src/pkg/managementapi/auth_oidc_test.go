package managementapi

import (
	"testing"
)

func TestDomainAllowed(t *testing.T) {
	cases := []struct {
		email   string
		allowed []string
		want    bool
	}{
		// No restriction = everyone in.
		{"a@anywhere.com", nil, true},
		{"a@anywhere.com", []string{}, true},
		// Exact match.
		{"alice@acme.com", []string{"acme.com"}, true},
		// Case insensitive on both sides.
		{"ALICE@ACME.COM", []string{"acme.com"}, true},
		{"alice@acme.com", []string{"ACME.COM"}, true},
		// Wrong domain.
		{"alice@gmail.com", []string{"acme.com"}, false},
		// Allowed list with multiple entries.
		{"bob@dev.acme.com", []string{"acme.com", "dev.acme.com"}, true},
		// Whitespace in allowed entries is trimmed.
		{"alice@acme.com", []string{"  acme.com  "}, true},
		// Malformed email (no @) is rejected.
		{"not-an-email", []string{"acme.com"}, false},
	}
	for _, c := range cases {
		got := domainAllowed(c.email, c.allowed)
		if got != c.want {
			t.Errorf("domainAllowed(%q, %v) = %v, want %v", c.email, c.allowed, got, c.want)
		}
	}
}

func TestPKCEChallenge_StableAndDifferent(t *testing.T) {
	// Same verifier → same challenge.
	v1 := "the-quick-brown-fox-jumps-over-the-lazy-dog"
	c1 := pkceChallenge(v1)
	c2 := pkceChallenge(v1)
	if c1 != c2 {
		t.Errorf("pkceChallenge non-deterministic: %q vs %q", c1, c2)
	}
	// Different verifiers → different challenges.
	c3 := pkceChallenge(v1 + "x")
	if c1 == c3 {
		t.Errorf("pkceChallenge collision: %q", c1)
	}
	// URL-safe — base64 raw URL encoding has no padding nor '+' / '/'.
	for _, r := range c1 {
		if r == '+' || r == '/' || r == '=' {
			t.Errorf("pkceChallenge contains non-URL-safe char %q in %q", r, c1)
		}
	}
}

func TestRandURLSafe(t *testing.T) {
	a := randURLSafe(32)
	b := randURLSafe(32)
	if a == b {
		t.Errorf("randURLSafe collision: both produced %q", a)
	}
	if len(a) == 0 {
		t.Errorf("randURLSafe returned empty string")
	}
}

func TestValidateOIDCUpsert(t *testing.T) {
	good := oidcUpsertRequest{
		IssuerURL:    "https://accounts.google.com",
		ClientID:     "client-123",
		ClientSecret: "secret",
		RedirectURL:  "https://app.vrsky.example/api/v1/auth/oidc/callback",
		Scopes:       []string{"openid", "email", "profile"},
	}
	if err := validateOIDCUpsert(&good); err != nil {
		t.Fatalf("good config rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(r *oidcUpsertRequest)
	}{
		{"no issuer", func(r *oidcUpsertRequest) { r.IssuerURL = "" }},
		{"http issuer", func(r *oidcUpsertRequest) { r.IssuerURL = "http://acme" }},
		{"no client id", func(r *oidcUpsertRequest) { r.ClientID = "" }},
		{"no redirect", func(r *oidcUpsertRequest) { r.RedirectURL = "" }},
		{"http redirect non-localhost", func(r *oidcUpsertRequest) { r.RedirectURL = "http://acme/cb" }},
		{"scopes without openid", func(r *oidcUpsertRequest) { r.Scopes = []string{"email"} }},
		{"allowed domain with @", func(r *oidcUpsertRequest) { r.AllowedDomains = []string{"user@acme.com"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := good
			c.mut(&r)
			if err := validateOIDCUpsert(&r); err == nil {
				t.Errorf("expected validation error for %s", c.name)
			}
		})
	}

	// localhost redirect over http should be allowed (dev convenience).
	dev := good
	dev.RedirectURL = "http://localhost:3000/cb"
	if err := validateOIDCUpsert(&dev); err != nil {
		t.Errorf("localhost http redirect should be allowed in dev, got %v", err)
	}
}
