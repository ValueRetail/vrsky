// Package tenantpath confines a connection's file paths to its own tenant's
// subtree.
//
// # WHY IT EXISTS
//
// file-consumer and file-producer share one ReadWriteMany volume in the cluster
// (vrsky-files), and both took the path straight from the connection config:
// the consumer watched whatever directory the user typed, the producer wrote to
// whatever directory the user typed as long as it sat under a mounted root.
// Neither root had a tenant component. Two consequences, both live:
//
//   - Cross-tenant READ. A tenant points a file source at the shared output
//     root and ingests every other tenant's produced files.
//   - Cross-tenant OVERWRITE. The producer preserves the incoming filename when
//     the envelope carries metadata.filename — which a file→file pipeline
//     always does — so two tenants processing "orders.csv" write the same path.
//     Last writer wins, silently.
//
// Resolve is the single place that decides where a connection's files live, so
// the rule is stated once and both connectors enforce it. It is enforced in the
// CONNECTORS rather than only at validation because connectors read their
// config from the database directly; a validator can be bypassed by any write
// that does not go through the API.
//
// # WHAT IT DOES NOT DO
//
// It does not resolve symlinks. A symlink already inside a tenant root that
// points outside it would defeat this, and defending against that means
// touching the filesystem on every resolve. The volume is written only by these
// connectors, which never create symlinks, so the exposure is a hostile
// filesystem rather than a hostile connection config — a different threat and a
// different fix (mount options, or a resolve-and-recheck at open time).
package tenantpath

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrEscapesTenantRoot is returned when a configured path resolves outside the
// tenant's own subtree. Callers should surface it to the operator rather than
// silently relocating the path: a pipeline quietly writing somewhere other than
// where its config says is the failure mode this package exists to end.
var ErrEscapesTenantRoot = errors.New("path escapes the tenant's directory")

// ErrBadTenantID is returned for a tenant identifier that cannot be a single
// path segment. A tenant ID reaching this package comes from the database, not
// from a request, but it lands in a filesystem path either way and one
// containing a separator would silently widen every root built from it.
var ErrBadTenantID = errors.New("tenant id is not usable as a path segment")

// Root is the directory that holds everything belonging to one tenant.
func Root(baseDir, tenantID string) (string, error) {
	if err := checkTenantID(tenantID); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(baseDir), tenantID), nil
}

// Resolve maps a user-configured path onto an absolute path inside the tenant's
// root.
//
//	""                        -> <base>/<tenant>
//	"orders"                  -> <base>/<tenant>/orders
//	"<base>/orders"           -> <base>/<tenant>/orders   (base prefix stripped)
//	"<base>/<tenant>/orders"  -> <base>/<tenant>/orders   (already correct)
//	"/etc/passwd"             -> ErrEscapesTenantRoot
//	"../../etc"               -> ErrEscapesTenantRoot
//
// The base-prefix strip is what keeps the existing user experience working. The
// UI has always asked for a directory and people type the mounted root they can
// see ("/data/output"); rewriting that to the tenant's own copy of it is both
// what they meant and the only safe reading. Anything genuinely outside the
// mounted tree is refused rather than relocated, because a path that lands
// somewhere other than where the config says is how this class of bug hides.
func Resolve(baseDir, tenantID, configured string) (string, error) {
	root, err := Root(baseDir, tenantID)
	if err != nil {
		return "", err
	}
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return root, nil
	}

	base := filepath.Clean(baseDir)
	p := filepath.Clean(configured)

	var candidate string
	switch {
	case within(p, root):
		// Already tenant-qualified; leave it alone.
		candidate = p
	case within(p, base):
		// Under the mounted root but not tenant-qualified: re-home it into
		// this tenant's copy of that subtree.
		rel, relErr := filepath.Rel(base, p)
		if relErr != nil {
			return "", fmt.Errorf("%w: %s", ErrEscapesTenantRoot, configured)
		}
		candidate = filepath.Join(root, rel)
	case !filepath.IsAbs(p):
		// A relative path is relative to the tenant's root, never to the
		// process working directory.
		candidate = filepath.Join(root, p)
	default:
		return "", fmt.Errorf("%w: %q is not under %s", ErrEscapesTenantRoot, configured, base)
	}

	// Belt and braces. filepath.Join cleans, so "a/../../b" has already
	// collapsed by here — but this is the invariant the whole package is for,
	// so it is asserted rather than assumed.
	if !within(candidate, root) {
		return "", fmt.Errorf("%w: %q resolves to %s, outside %s",
			ErrEscapesTenantRoot, configured, candidate, root)
	}
	return candidate, nil
}

// within reports whether p is dir or sits underneath it, comparing whole path
// segments. A plain strings.HasPrefix would accept "/data/input-other" as being
// under "/data/input".
func within(p, dir string) bool {
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	if p == dir {
		return true
	}
	return strings.HasPrefix(p, dir+string(filepath.Separator))
}

func checkTenantID(tenantID string) error {
	t := strings.TrimSpace(tenantID)
	if t == "" {
		return fmt.Errorf("%w: empty", ErrBadTenantID)
	}
	if t == "." || t == ".." {
		return fmt.Errorf("%w: %q", ErrBadTenantID, tenantID)
	}
	if strings.ContainsRune(t, '/') || strings.ContainsRune(t, filepath.Separator) {
		return fmt.Errorf("%w: %q contains a path separator", ErrBadTenantID, tenantID)
	}
	if t != tenantID {
		return fmt.Errorf("%w: %q has surrounding whitespace", ErrBadTenantID, tenantID)
	}
	return nil
}
