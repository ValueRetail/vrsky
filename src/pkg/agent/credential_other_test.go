//go:build !windows

package agent

import (
	"os"
	"testing"
)

// assertPrivate: the credential and its folder are the owner's alone.
func assertPrivate(t *testing.T, dir, file string) {
	t.Helper()
	for path, want := range map[string]os.FileMode{dir: 0o700, file: 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}
