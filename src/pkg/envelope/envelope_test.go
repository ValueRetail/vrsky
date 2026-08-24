package envelope

import "testing"

// The envelope ID is the JetStream dedup key and the claim-check object key, so
// "every new envelope has a unique ID" is a correctness invariant, not a nicety:
// an empty ID disables duplicate suppression and makes every offloaded payload
// on a connection collide at spill/<tenant>/<connection>/.
func TestNew_GeneratesUniqueID(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		e := New()
		if e.ID == "" {
			t.Fatal("New() must generate an ID")
		}
		if seen[e.ID] {
			t.Fatalf("duplicate ID generated: %s", e.ID)
		}
		seen[e.ID] = true
	}
}

func TestNew_IDSurvivesRoundTrip(t *testing.T) {
	e := New()
	e.TenantID = "tenant-x"
	b, err := Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := Unmarshal(b)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.ID != e.ID {
		t.Errorf("ID = %q after round-trip, want %q", got.ID, e.ID)
	}
}

// Callers may substitute a source's natural key so dedup keys off it.
func TestNew_IDIsOverridable(t *testing.T) {
	e := New()
	e.ID = "order-42"
	if e.ID != "order-42" {
		t.Errorf("ID = %q", e.ID)
	}
}
