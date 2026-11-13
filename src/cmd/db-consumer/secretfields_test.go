package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A stored secret in a setting that is not a credential (#853): refused or
// dropped, and its value comes back in no answer, error or log line.

func TestResolveSecretsInNodes_OnlyOwnNodeAndOnlyCredentials(t *testing.T) {
	db, _ := harness.LeakSecretsDB(t)
	c := &dbConsumer{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// Our node, the secret in a setting that is not a credential: refused.
	ours := `[{"id":"src","type":"consumer","config":{"type":"database","database":{"host":"h","query":"` + harness.LeakPlaceholder + `"}}}]`
	_, err := c.resolveSecretsInNodes(json.RawMessage(ours), "tenant-1")
	if err == nil || !strings.Contains(err.Error(), "database.query holds a {secret:…} placeholder") || strings.Contains(err.Error(), harness.LeakSecret) {
		t.Fatalf("err = %v", err)
	}

	// The same as a reference: dropped, never resolved.
	ref := strings.Replace(ours, `":"`+harness.LeakPlaceholder+`"`, `_secret_id":"`+harness.LeakSecretID+`"`, 1)
	out, err := c.resolveSecretsInNodes(json.RawMessage(ref), "tenant-1")
	if err != nil || strings.Contains(string(out), harness.LeakSecret) || strings.Contains(string(out), harness.LeakSecretID) {
		t.Fatalf("reference: %s, %v", out, err)
	}

	// Another connector's node is left alone, placeholder and all.
	foreign := `[{"id":"dst","type":"producer","config":{"type":"kafka","kafka":{"topic":"` + harness.LeakPlaceholder + `"}}}]`
	out, err = c.resolveSecretsInNodes(json.RawMessage(foreign), "tenant-1")
	if err != nil || strings.Contains(string(out), harness.LeakSecret) {
		t.Fatalf("foreign node: %s, %v", out, err)
	}

	// A credential still resolves.
	cred := `[{"id":"src","type":"consumer","config":{"type":"database","database":{"host":"h","password_secret_id":"` + harness.LeakSecretID + `"}}}]`
	out, err = c.resolveSecretsInNodes(json.RawMessage(cred), "tenant-1")
	if err != nil || !strings.Contains(string(out), harness.LeakSecret) {
		t.Fatalf("credential: %s, %v", out, err)
	}
}
