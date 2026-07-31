package oauthcc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestToken_FetchesAndCaches verifies the token is fetched once and then served
// from cache, and that the client_credentials form + scope are sent.
func TestToken_FetchesAndCaches(t *testing.T) {
	var calls int
	var gotGrant, gotID, gotScope string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		gotGrant = r.Form.Get("grant_type")
		gotID = r.Form.Get("client_id")
		gotScope = r.Form.Get("scope")
		fmt.Fprint(w, `{"access_token":"tok-123","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "cid", "secret", "https://api.example/.default").WithHTTPClient(srv.Client())

	for i := 0; i < 3; i++ {
		tok, err := c.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "tok-123" {
			t.Fatalf("token = %q, want tok-123", tok)
		}
	}
	if calls != 1 {
		t.Errorf("token endpoint called %d times, want 1 (cached)", calls)
	}
	if gotGrant != "client_credentials" || gotID != "cid" || gotScope != "https://api.example/.default" {
		t.Errorf("form = grant:%q id:%q scope:%q", gotGrant, gotID, gotScope)
	}
}

// TestToken_ErrorOnNon2xx surfaces a token-endpoint failure.
func TestToken_ErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, "cid", "bad", "").WithHTTPClient(srv.Client())
	if _, err := c.Token(context.Background()); err == nil {
		t.Fatal("expected error on 401 token response")
	}
}
