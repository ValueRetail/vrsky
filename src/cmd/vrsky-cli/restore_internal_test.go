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

func TestSameDatabase(t *testing.T) {
	src := "postgres://postgres:management_password@postgres-management:5432/management_db?sslmode=disable"
	cases := []struct {
		other string
		want  bool
	}{
		{src, true}, // identical
		{"postgres://other:pw@postgres-management:5432/management_db", true},                                     // differ only in creds
		{"postgres://postgres:management_password@postgres-management:5432/management_db?sslmode=require", true}, // differ only in query
		{"postgres://postgres:pw@dr-target:5432/management_db?sslmode=disable", false},                           // different host
		{"postgres://postgres:pw@postgres-management:5432/other_db", false},                                      // different db name
	}
	for _, c := range cases {
		if got := sameDatabase(src, c.other); got != c.want {
			t.Errorf("sameDatabase(src, %q) = %v, want %v", c.other, got, c.want)
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
