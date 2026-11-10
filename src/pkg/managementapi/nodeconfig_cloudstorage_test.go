package managementapi

import (
	"fmt"
	"strings"
	"testing"
)

// A cloud-storage consumer that refuses its start leaves the connection
// "running" and ingesting nothing, so the start-time check refuses the same
// after_action=move configs (#724).
func TestValidateNodeConfigs_CloudStorageMove(t *testing.T) {
	cs := func(mode, prefix, action, move string) string {
		return fmt.Sprintf(`{"type":"cloud_storage","cloud_storage":{"provider":"s3","bucket":"b","mode":%q,"prefix":%q,"after_action":%q,"move_prefix":%q}}`,
			mode, prefix, action, move)
	}
	tests := []struct {
		name    string
		cfg     string
		wantErr string // substring; empty = accepted
	}{
		{"valid move", cs("poll", "in/", "move", "processed"), ""},
		{"valid move, default mode", cs("", "in/", "move", "in-done"), ""},
		{"no after_action", cs("poll", "in/", "", ""), ""},
		{"delete ignores move_prefix", cs("poll", "", "delete", "processed"), ""},
		{"move without move_prefix", cs("poll", "in/", "move", ""), "move_prefix is required"},
		{"move without move_prefix, event mode", cs("event", "in/", "move", ""), "move_prefix is required"},
		{"move_prefix inside listing", cs("poll", "in/", "move", "in/done"), "inside what prefix"},
		{"move_prefix equals prefix", cs("poll", "in/", "move", "in"), "inside what prefix"},
		{"empty prefix", cs("", "", "move", "processed"), "whole bucket"},
		{"prefix as text lists sibling", cs("poll", "in", "move", "in-done/"), "inside what prefix"},
		{"event mode allows the overlap", cs("event", "in/", "move", "in/done"), ""},
		{"event mode, empty prefix", cs("event", "", "move", "processed"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewValidator().ValidateNodeConfigs(pipeline(tt.cfg, okHTTPDest))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected an error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

// The producer's after_action does not exist; only the consumer is checked.
func TestValidateNodeConfigs_CloudStorageProducerUnchecked(t *testing.T) {
	cfg := `{"type":"cloud_storage","cloud_storage":{"bucket":"b","after_action":"move"}}`
	if err := NewValidator().ValidateNodeConfigs(pipeline(okFileSource, cfg)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
