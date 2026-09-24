package agentproto

import (
	"errors"
	"strings"
	"testing"
)

// ValidFilename is the last check between pipeline data and a file on a
// customer's disk. Every case below is something a pipeline could produce.
func TestValidFilename_RejectsTraversalAndReserved(t *testing.T) {
	bad := []string{
		"", ".", "..",
		"../x", "..\\x", "a/b", `a\b`, "/etc/passwd", `C:\Windows\x`,
		"x:y", "a<b", "a>b", `a"b`, "a|b", "a?b", "a*b",
		"CON", "con", "nul.txt", "COM1.log", "lpt9", "AUX .txt",
		"trailing.", "trailing ", "tab\there", "nl\nhere",
		strings.Repeat("a", 256),
		"\xff\xfe",
	}
	for _, name := range bad {
		if err := ValidFilename(name); err == nil {
			t.Errorf("ValidFilename(%q) = nil, want an error", name)
		} else if !errors.Is(err, ErrInvalidFilenameValue) {
			t.Errorf("ValidFilename(%q) error does not wrap ErrInvalidFilenameValue: %v", name, err)
		}
	}
	good := []string{
		"orders.json", "orders-20260924-101500.json", "ÆØÅ-kvittering.xml",
		".hidden", "a..b", "console.log", "nullable.csv", strings.Repeat("a", 255),
	}
	for _, name := range good {
		if err := ValidFilename(name); err != nil {
			t.Errorf("ValidFilename(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidDirectoryName(t *testing.T) {
	for _, ok := range []string{"inbox", "superpos-out", "A_1", "x"} {
		if !ValidDirectoryName(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "-lead", "_lead", "a/b", "a.b", "..", "a b", strings.Repeat("a", 65)} {
		if ValidDirectoryName(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
