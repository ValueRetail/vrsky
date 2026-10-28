package main

import (
	"os"
	"testing"
)

// TestHostOwner pins who written files end up owned by. The review of #422
// found the regression: HOST_HOME now names the repo's data/input, which
// dockerd creates as root:root on a fresh Linux clone, and following that
// owner made every output root-owned.
func TestHostOwner(t *testing.T) {
	probeOf := func(uid, gid int) func() (int, int, bool) {
		return func() (int, int, bool) { return uid, gid, true }
	}
	noProbe := func() (int, int, bool) { return 0, 0, false }

	tests := []struct {
		name             string
		uidStr, gidStr   string
		probe            func() (int, int, bool)
		wantUID, wantGID int
	}{
		{"root-owned host folder keeps FILE_OWNER", "1000", "1000", probeOf(0, 0), 1000, 1000},
		{"root-owned host folder, no FILE_OWNER: leave alone", "", "", probeOf(0, 0), -1, -1},
		{"host folder owner wins over compose's 1000 default", "1000", "1000", probeOf(501, 20), 501, 20},
		{"host folder owner matches FILE_OWNER", "1000", "1000", probeOf(1000, 1001), 1000, 1000},
		{"no host folder: FILE_OWNER", "1000", "1000", noProbe, 1000, 1000},
		{"no FILE_OWNER: host folder owner", "", "", probeOf(501, 20), 501, 20},
		{"unparsable FILE_OWNER: host folder owner", "x", "1000", probeOf(501, 20), 501, 20},
		{"nothing known: leave alone", "", "", noProbe, -1, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			uid, gid := hostOwner(tc.uidStr, tc.gidStr, tc.probe)
			if uid != tc.wantUID || gid != tc.wantGID {
				t.Errorf("hostOwner(%q, %q) = %d, %d; want %d, %d",
					tc.uidStr, tc.gidStr, uid, gid, tc.wantUID, tc.wantGID)
			}
		})
	}
}

// TestProbeHostHomeOwner checks the probe reads the folder's real owner and
// reports nothing for an unset or missing HOST_HOME.
func TestProbeHostHomeOwner(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("HOST_HOME", dir)
	uid, _, ok := probeHostHomeOwner()
	if !ok || uid != os.Getuid() {
		t.Errorf("probeHostHomeOwner() on a temp dir = uid %d, ok %v; want uid %d, ok true", uid, ok, os.Getuid())
	}

	t.Setenv("HOST_HOME", dir+"/missing")
	if _, _, ok := probeHostHomeOwner(); ok {
		t.Error("probeHostHomeOwner() on a missing dir reported ok")
	}

	t.Setenv("HOST_HOME", "")
	if _, _, ok := probeHostHomeOwner(); ok {
		t.Error("probeHostHomeOwner() with HOST_HOME unset reported ok")
	}
}
