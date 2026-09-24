package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
)

// deliveryIDPattern bounds the IDs used in temp-file names. The gateway sends
// UUIDs; anything else is refused rather than put into a path.
var deliveryIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// partPrefix marks the agent's temp files; watchers skip them.
const partPrefix = "~vrsky-"

// WriteFile writes one delivered file into a configured write folder and
// returns only once it is complete and verified.
//
// The file is written to a temp name in the SAME folder — same volume, so the
// final rename is atomic — checked against the checksum VRSky sent, synced, and
// only then renamed into place. Whatever reads that folder (SuperPOS, a
// person) never sees a half-written or corrupt file under the real name. Any
// failure removes the temp file.
//
// The folder is looked up by name in the agent's own config and the filename
// is validated again here, so nothing VRSky sends can write outside the
// configured folder.
func WriteFile(cfg *Config, dirName, filename, deliveryID, checksum string, body io.Reader) (string, error) {
	dir, ok := cfg.Directories[dirName]
	if !ok {
		return "", fmt.Errorf("%s: no folder named %q in this agent's config", agentproto.ErrUnknownDirectory, dirName)
	}
	if dir.Mode != agentproto.ModeWrite {
		return "", fmt.Errorf("%s: folder %q is not a write folder", agentproto.ErrUnknownDirectory, dirName)
	}
	if err := agentproto.ValidFilename(filename); err != nil {
		return "", err
	}
	if !deliveryIDPattern.MatchString(deliveryID) {
		return "", fmt.Errorf("refusing delivery id %q", deliveryID)
	}
	root := filepath.Clean(dir.Path)
	final := filepath.Join(root, filename)
	if filepath.Dir(final) != root {
		// Unreachable after ValidFilename; asserted because it is the
		// property everything else here depends on.
		return "", fmt.Errorf("%q resolves outside %s", filename, root)
	}

	tmp := filepath.Join(root, partPrefix+deliveryID+".part")
	_ = os.Remove(tmp) // left by a crash mid-write of this same delivery
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	done := false
	defer func() {
		if !done {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), body); err != nil {
		return "", fmt.Errorf("receive: %w", err)
	}
	if checksum != "" {
		if got := "sha256:" + hex.EncodeToString(h.Sum(nil)); got != checksum {
			return "", fmt.Errorf("checksum mismatch: got %s, want %s", got, checksum)
		}
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := renameWithRetry(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	done = true
	return final, nil
}

// renameWithRetry renames, retrying briefly: on Windows an antivirus scanner
// or indexer commonly holds a freshly written file open for a moment, and the
// rename fails with "being used by another process". os.Rename replaces an
// existing target on both Windows and POSIX.
func renameWithRetry(from, to string) error {
	var err error
	for i := 0; i < 5; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(time.Duration(200*(i+1)) * time.Millisecond)
	}
	return err
}
