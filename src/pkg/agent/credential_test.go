package agent

import (
	"testing"
	"time"
)

func TestCredential_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadCredential(dir); err != ErrNotRegistered {
		t.Fatalf("empty dir: err = %v, want ErrNotRegistered", err)
	}
	in := &Credential{AgentID: "a1", Name: "till", ServerURL: "https://x", Credential: "vrsky_agent_s3cret", RegisteredAt: time.Now().UTC()}
	if err := SaveCredential(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadCredential(dir)
	if err != nil || out.Credential != in.Credential || out.AgentID != "a1" {
		t.Fatalf("load = %+v, %v", out, err)
	}
	assertPrivate(t, dir, credentialPath(dir))
}
