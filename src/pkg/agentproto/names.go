package agentproto

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// directoryName is what a directory may be called. VRSky refers to an agent's
// directories by these names only; keeping them to a plain slug means a name
// can never be read as a path.
var directoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidDirectoryName reports whether name is a legal directory name.
func ValidDirectoryName(name string) bool { return directoryName.MatchString(name) }

// ValidMode reports whether mode is a legal directory mode.
func ValidMode(mode string) bool { return mode == ModeRead || mode == ModeWrite }

// ErrInvalidFilenameValue is wrapped by every ValidFilename failure.
var ErrInvalidFilenameValue = errors.New("invalid filename")

// maxFilenameBytes is the common per-component limit on Windows (NTFS) and
// Linux (ext4).
const maxFilenameBytes = 255

// windowsReserved are device names Windows refuses as a file's base name, with
// or without an extension: "nul.txt" opens the null device, not a file.
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// ValidFilename reports whether name is safe to create inside a directory on
// both Windows and Linux: a single path component that cannot climb out of
// the directory, name a device, or be silently altered by the filesystem.
//
// Both ends enforce it — the gateway before it offers a delivery, and the agent
// again before it writes — because a filename is built from pipeline data,
// which can carry anything.
func ValidFilename(name string) error {
	bad := func(why string) error { return fmt.Errorf("%w %q: %s", ErrInvalidFilenameValue, name, why) }
	switch {
	case name == "":
		return bad("empty")
	case name == "." || name == "..":
		return bad("is a directory reference")
	case len(name) > maxFilenameBytes:
		return bad("longer than 255 bytes")
	case !utf8.ValidString(name):
		return bad("not valid UTF-8")
	case strings.ContainsAny(name, `/\`):
		return bad("contains a path separator")
	case filepath.Base(name) != name:
		return bad("is not a single path component")
	case strings.ContainsAny(name, `<>:"|?*`):
		return bad(`contains a character Windows forbids (<>:"|?*)`)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return bad("contains a control character")
		}
	}
	// Windows strips trailing dots and spaces, so "report." and "report" would
	// be the same file — and "..." would become "".
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return bad("ends with a dot or space")
	}
	base := name
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if windowsReserved[strings.ToUpper(strings.TrimRight(base, " "))] {
		return bad("is a reserved device name on Windows")
	}
	return nil
}
