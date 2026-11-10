package objectstore

import "testing"

func TestMoveInsideListing(t *testing.T) {
	tests := []struct {
		name, prefix, movePrefix string
		inside                   bool
	}{
		{"empty move_prefix", "in/", "", false},
		{"equal", "in/", "in", true},
		{"under", "in/", "in/done", true},
		{"empty prefix", "", "processed", true},
		{"prefix as text lists sibling", "in", "in-done/", true},
		{"names files in move_prefix", "data/report_", "data", true},
		{"sibling folder", "in/", "in-done", false},
		{"unrelated", "in/", "processed", false},
		{"bucket root", "in/", "/", false},
		{"one level up", "incoming/a/", "incoming", false},
		{"leading .. kept", "x/", "../x", false},
		{"leading .. under prefix ..", "../", "../x", true},
		{"dot-dot leaves the prefix", "in/", "a/../../in", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MoveInsideListing(tt.prefix, tt.movePrefix)
			if (err != nil) != tt.inside {
				t.Fatalf("MoveInsideListing(%q, %q) = %v, want inside=%v", tt.prefix, tt.movePrefix, err, tt.inside)
			}
		})
	}
}
