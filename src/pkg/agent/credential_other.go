//go:build !windows

package agent

import "os"

// ensurePrivateDir creates dir readable by its owner only.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// restrictFile makes a file readable and writable by its owner only.
func restrictFile(path string) error { return os.Chmod(path, 0o600) }
