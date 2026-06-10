package main

import "testing"

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"postgres://user:secret@host:5432/db": "postgres://***@host:5432/db",
		"postgres://host:5432/db":             "postgres://host:5432/db", // no creds
		"postgres://u:p@h/db?sslmode=disable": "postgres://***@h/db?sslmode=disable",
		"":                                    "",
	}
	for in, want := range cases {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGzipRoundTrip(t *testing.T) {
	orig := []byte("the quick brown fox dumps the lazy database")
	gz, err := gzipBytes(orig)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	out, err := gunzipBytes(gz)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	if string(out) != string(orig) {
		t.Errorf("round-trip = %q, want %q", out, orig)
	}
}
