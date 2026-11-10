package objectstore

import (
	"fmt"
	"path"
	"strings"
)

// MoveInsideListing reports an after_action=move whose moved objects the
// listing of prefix would select again (#567, #594, #724). The consumer
// refuses such a start in poll mode, and the management API refuses it when a
// connection is started, so both use this one rule.
//
// The providers match prefix as a plain string, not a folder: prefix "in"
// lists "in-done/a.csv" too. A move writes path.Join(move_prefix, base name),
// so every destination is dir+b with b a base name (not empty, no "/"). One of
// them is selected when dir starts with prefix (equal, under it, or an empty
// prefix), or when prefix starts with dir and the rest of prefix has no "/"
// (a base name starting with that rest is selected). Leading slashes are
// ignored on both sides, which refuses more rather than less.
//
// It returns nil when movePrefix is empty; a missing move_prefix is a
// separate refusal.
func MoveInsideListing(prefix, movePrefix string) error {
	if movePrefix == "" {
		return nil
	}
	p := strings.TrimLeft(prefix, "/")
	dir := strings.TrimLeft(path.Clean(movePrefix), "/")
	if dir == "." {
		dir = ""
	}
	if dir != "" {
		dir += "/"
	}
	rest, under := strings.CutPrefix(p, dir)
	if !strings.HasPrefix(dir, p) && (!under || strings.Contains(rest, "/")) {
		return nil
	}
	if prefix == "" {
		return fmt.Errorf("move_prefix %q is inside prefix \"\" (the whole bucket): moved objects would be published again", movePrefix)
	}
	return fmt.Errorf("move_prefix %q is inside what prefix %q lists (prefix is matched as text, not as a folder; end it with \"/\" to read one folder): moved objects would be published again", movePrefix, prefix)
}
