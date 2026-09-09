// Package contract holds the machinery for the retail connector contract
// tests.
//
// Each retail integration is two binaries that never call each other. A
// consumer publishes an envelope; a producer, in a different process, consumes
// one. They meet only in that envelope, and until these tests existed both
// sides were only ever exercised against payloads their own tests invented —
// the shape of an integration that passes everywhere and fails in production.
//
// The pattern, per connector:
//
//   - cmd/<name>-consumer/<name>_contract_test.go runs the REAL consumer against
//     a stub vendor API and records the envelopes it publishes to a golden file.
//   - cmd/<name>-producer/<name>_contract_test.go replays exactly those envelopes
//     through the REAL producer and asserts what reaches the wire.
//
// The golden file is the contract. A consumer change that alters the envelope
// shows up there as a diff, and as a failure on the producer side if the
// producer can no longer send it.
//
// Regenerate after an intentional consumer change:
//
//	go test ./cmd/<name>-consumer/ -run Contract -update
package contract

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Update is the -update flag shared by every connector's contract test. It is
// declared here so each test binary gets exactly one definition; declaring it
// per package would collide the moment two of them ended up in one binary.
var Update = flag.Bool("update", false, "rewrite contract golden files from this run")

// Dir is the committed home of the fixtures and goldens, relative to a
// cmd/<connector>/ package.
const Dir = "../../test/fixtures"

// Envelope is the part of an envelope that crosses the wire and matters to the
// producer.
//
// Volatile fields — the generated ID, timestamps — are deliberately absent.
// Pinning them would churn the golden file on every run while saying nothing
// about the contract, and a golden nobody trusts is worse than none.
type Envelope struct {
	// Mode records how the consumer produced this envelope: "poll" for a
	// fetched page, "webhook" for a vendor callback. The two are frequently
	// NOT the same shape, which is exactly the kind of thing these tests are
	// for.
	Mode          string          `json:"mode"`
	TenantID      string          `json:"tenant_id"`
	IntegrationID string          `json:"integration_id"`
	ContentType   string          `json:"content_type"`
	Source        string          `json:"source"`
	StepHistory   []string        `json:"step_history"`
	Metadata      map[string]any  `json:"metadata"`
	Payload       json.RawMessage `json:"payload"`
}

// From projects a live envelope onto the contract subset.
func From(mode string, env *envelope.Envelope) Envelope {
	return Envelope{
		Mode:          mode,
		TenantID:      env.TenantID,
		IntegrationID: env.IntegrationID,
		ContentType:   env.ContentType,
		Source:        env.Source,
		StepHistory:   env.StepHistory,
		Metadata:      env.Metadata,
		Payload:       json.RawMessage(env.Payload),
	}
}

// Golden compares envs against the committed golden for a connector, or
// rewrites it under -update.
//
// name is the connector directory under Dir, e.g. "sitoo".
func Golden(t *testing.T, name string, envs []Envelope) {
	t.Helper()
	path := filepath.Join(Dir, name, "envelopes.golden.json")

	got, err := json.MarshalIndent(envs, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	got = append(got, '\n')

	if *Update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (%v)\nregenerate with: go test ./cmd/%s-consumer/ -run Contract -update", err, name)
	}
	if string(got) != string(want) {
		t.Errorf("the envelopes this consumer publishes no longer match %s.\n\n"+
			"That file is what the %s producer's contract test replays, so this is a change to what the "+
			"two services agree on — not just a test fixture. Check the producer still handles the new "+
			"shape, then regenerate:\n"+
			"  go test ./cmd/%s-consumer/ -run Contract -update\n\ngot:\n%s",
			path, name, name, got)
	}
}

// Load reads a connector's golden envelopes, for the producer side to replay.
func Load(t *testing.T, name string) []Envelope {
	t.Helper()
	path := filepath.Join(Dir, name, "envelopes.golden.json")
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s (%v)\nregenerate with: go test ./cmd/%s-consumer/ -run Contract -update", path, err, name)
	}
	var envs []Envelope
	if err := json.Unmarshal(raw, &envs); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(envs) == 0 {
		t.Fatalf("%s is empty", path)
	}
	return envs
}

// AssertRoutable checks the invariants every producer's Deliver depends on.
//
// Both failures below are SILENT at runtime: Deliver returns nil for a missing
// integration id and drops an invalid payload as Permanent, so a consumer that
// stopped setting either would produce undeliverable traffic with nothing in
// the logs above Debug.
func AssertRoutable(t *testing.T, envs []Envelope) {
	t.Helper()
	for _, e := range envs {
		if !json.Valid(e.Payload) {
			t.Errorf("%s payload is not valid JSON; the producer drops it as Permanent", e.Mode)
		}
		if e.TenantID == "" || e.IntegrationID == "" {
			t.Errorf("%s envelope missing routing ids (tenant=%q integration=%q); the producer cannot look "+
				"up a config without them and returns nil, dropping the record silently",
				e.Mode, e.TenantID, e.IntegrationID)
		}
	}
}
