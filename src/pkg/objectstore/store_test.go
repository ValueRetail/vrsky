package objectstore

import (
	"context"
	"strings"
	"testing"
)

func TestNew_UnknownProvider(t *testing.T) {
	_, err := New(context.Background(), &Config{Provider: "dropbox", Bucket: "b"})
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("want unknown-provider error, got %v", err)
	}
}

func TestNew_NotImplementedProviders(t *testing.T) {
	for _, p := range []string{ProviderAzure, ProviderGCS} {
		if _, err := New(context.Background(), &Config{Provider: p, Bucket: "b"}); err == nil {
			t.Errorf("provider %q: want not-implemented error, got nil", p)
		}
	}
}

func TestNew_NilConfig(t *testing.T) {
	if _, err := New(context.Background(), nil); err == nil {
		t.Fatal("want error for nil config")
	}
}

func TestNew_S3RequiresBucket(t *testing.T) {
	if _, err := New(context.Background(), &Config{Provider: ProviderS3}); err == nil {
		t.Fatal("want error when bucket is empty")
	}
}

func TestNew_S3DefaultsAndEndpoint(t *testing.T) {
	// Empty provider defaults to S3; a custom endpoint (MinIO) must not error at
	// construction time (no network call is made until a request runs).
	store, err := New(context.Background(), &Config{
		Bucket:          "test-bucket",
		Region:          "us-east-1",
		Endpoint:        "http://localhost:9000",
		AccessKeyID:     "minioadmin",
		SecretAccessKey: "minioadmin",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if store == nil {
		t.Fatal("New returned nil store")
	}
}

func TestEscapeKey(t *testing.T) {
	cases := map[string]string{
		"a/b/c.json":          "a/b/c.json",
		"orders/2024 01.json": "orders/2024%2001.json",
		"weird+name&x.csv":    "weird+name&x.csv", // PathEscape leaves these unescaped
	}
	for in, want := range cases {
		if got := escapeKey(in); got != want {
			t.Errorf("escapeKey(%q) = %q, want %q", in, got, want)
		}
	}
}
