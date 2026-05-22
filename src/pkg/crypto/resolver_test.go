package crypto

import (
	"context"
	"errors"
	"testing"
)

type fakeReader struct {
	store map[string]string // id -> ciphertext
}

func (f fakeReader) GetCiphertext(ctx context.Context, tenantID, id string) (string, error) {
	ct, ok := f.store[id]
	if !ok {
		return "", errors.New("not found")
	}
	return ct, nil
}

func newReader(t *testing.T, entries map[string]string) fakeReader {
	store := make(map[string]string, len(entries))
	for id, plain := range entries {
		ct, err := Encrypt(plain, testKey)
		if err != nil {
			t.Fatalf("setup encrypt: %v", err)
		}
		store[id] = ct
	}
	return fakeReader{store: store}
}

func TestResolve_FlatSecretID(t *testing.T) {
	reader := newReader(t, map[string]string{
		"00000000-0000-0000-0000-000000000001": "hunter2",
	})
	config := map[string]any{
		"host":               "localhost",
		"port":               5432,
		"password_secret_id": "00000000-0000-0000-0000-000000000001",
	}
	if err := ResolveSecretsWithKey(context.Background(), reader, "t1", config, testKey); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if config["password"] != "hunter2" {
		t.Fatalf("password not resolved: %+v", config)
	}
	if _, exists := config["password_secret_id"]; exists {
		t.Fatalf("password_secret_id should be removed after resolution")
	}
}

func TestResolve_Idempotent(t *testing.T) {
	reader := newReader(t, map[string]string{
		"00000000-0000-0000-0000-000000000001": "v",
	})
	config := map[string]any{"password_secret_id": "00000000-0000-0000-0000-000000000001"}
	for i := 0; i < 3; i++ {
		if err := ResolveSecretsWithKey(context.Background(), reader, "t1", config, testKey); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
		if config["password"] != "v" {
			t.Fatalf("pass %d: wrong value %v", i, config["password"])
		}
	}
}

func TestResolve_NestedAuth(t *testing.T) {
	reader := newReader(t, map[string]string{
		"00000000-0000-0000-0000-000000000abc": "bearer-token-xyz",
	})
	config := map[string]any{
		"http": map[string]any{
			"auth": map[string]any{
				"type":            "bearer",
				"token_secret_id": "00000000-0000-0000-0000-000000000abc",
			},
		},
	}
	if err := ResolveSecretsWithKey(context.Background(), reader, "t1", config, testKey); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	auth := config["http"].(map[string]any)["auth"].(map[string]any)
	if auth["token"] != "bearer-token-xyz" {
		t.Fatalf("nested token not resolved: %+v", auth)
	}
}

func TestResolve_EndpointArray(t *testing.T) {
	reader := newReader(t, map[string]string{
		"00000000-0000-0000-0000-000000000001": "key1",
		"00000000-0000-0000-0000-000000000002": "key2",
	})
	config := map[string]any{
		"endpoints": []any{
			map[string]any{"path": "/a", "auth_value_secret_id": "00000000-0000-0000-0000-000000000001"},
			map[string]any{"path": "/b", "auth_value_secret_id": "00000000-0000-0000-0000-000000000002"},
		},
	}
	if err := ResolveSecretsWithKey(context.Background(), reader, "t1", config, testKey); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	eps := config["endpoints"].([]any)
	if eps[0].(map[string]any)["auth_value"] != "key1" || eps[1].(map[string]any)["auth_value"] != "key2" {
		t.Fatalf("endpoint auth values not resolved: %+v", eps)
	}
}

func TestResolve_DSNPlaceholder(t *testing.T) {
	reader := newReader(t, map[string]string{
		"11111111-1111-1111-1111-111111111111": "p@ss w/ special",
	})
	config := map[string]any{
		"connection_string": "postgres://app:{secret:11111111-1111-1111-1111-111111111111}@host:5432/db",
	}
	if err := ResolveSecretsWithKey(context.Background(), reader, "t1", config, testKey); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := "postgres://app:p@ss w/ special@host:5432/db"
	if got := config["connection_string"].(string); got != want {
		t.Fatalf("dsn not expanded: got %q want %q", got, want)
	}
}

func TestResolve_NoMatchesIsNoop(t *testing.T) {
	reader := newReader(t, map[string]string{})
	config := map[string]any{
		"host":     "localhost",
		"port":     5432,
		"database": "vrsky",
	}
	if err := ResolveSecretsWithKey(context.Background(), reader, "t1", config, testKey); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(config) != 3 {
		t.Fatalf("config should be unchanged, got %+v", config)
	}
}

func TestResolve_MissingSecretErrors(t *testing.T) {
	reader := newReader(t, map[string]string{})
	config := map[string]any{"password_secret_id": "00000000-0000-0000-0000-deadbeef0000"}
	if err := ResolveSecretsWithKey(context.Background(), reader, "t1", config, testKey); err == nil {
		t.Fatalf("expected error when secret is missing")
	}
}
