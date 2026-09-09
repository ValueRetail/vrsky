package tenantpath

import (
	"errors"
	"strings"
	"testing"
)

const (
	base = "/data/files"
	tA   = "tenant-a"
	tB   = "tenant-b"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		want       string
	}{
		{"empty falls back to the tenant root", "", "/data/files/tenant-a"},
		{"relative path hangs off the tenant root", "orders", "/data/files/tenant-a/orders"},
		{"nested relative path", "orders/2026", "/data/files/tenant-a/orders/2026"},

		// The compatibility case. People type the mounted root they can see,
		// because that is what the UI has always asked for.
		{"mounted root is re-homed into the tenant", "/data/files", "/data/files/tenant-a"},
		{"path under the mounted root is re-homed", "/data/files/orders", "/data/files/tenant-a/orders"},

		{"an already tenant-qualified path is left alone", "/data/files/tenant-a/orders", "/data/files/tenant-a/orders"},
		{"trailing slash and dot segments are cleaned", "/data/files/orders/./", "/data/files/tenant-a/orders"},
		{"surrounding whitespace is ignored", "  orders  ", "/data/files/tenant-a/orders"},

		// Traversal that stays inside is fine — only escaping is an error.
		{"traversal that stays inside is allowed", "orders/../invoices", "/data/files/tenant-a/invoices"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(base, tA, tc.configured)
			if err != nil {
				t.Fatalf("Resolve(%q) = error %v, want %q", tc.configured, err, tc.want)
			}
			if got != tc.want {
				t.Errorf("Resolve(%q) = %q, want %q", tc.configured, got, tc.want)
			}
		})
	}
}

// TestResolve_RefusesEscape is the reason the package exists. Each of these
// would have been accepted verbatim by file-consumer before this change.
func TestResolve_RefusesEscape(t *testing.T) {
	for _, configured := range []string{
		"/etc/passwd",              // somewhere else entirely
		"/data/files-other/orders", // adjacent directory a prefix check would accept
		"../../etc",                // climb out of the tenant root
		"orders/../../../etc",      // climb out after descending
		"/",                        // the filesystem root
	} {
		t.Run(configured, func(t *testing.T) {
			got, err := Resolve(base, tA, configured)
			if err == nil {
				t.Fatalf("Resolve(%q) = %q, want an error — that path is outside the tenant's directory", configured, got)
			}
			if !errors.Is(err, ErrEscapesTenantRoot) {
				t.Errorf("error = %v, want ErrEscapesTenantRoot so callers can branch on it", err)
			}
		})
	}
}

// TestResolve_KeepsTenantsApart is the cross-tenant case in one assertion: the
// same configured path, resolved for two tenants, must never collide.
//
// This is the live defect it prevents — the producer preserves an incoming
// filename, so two tenants both processing "orders.csv" wrote the same file and
// the last one silently won.
func TestResolve_KeepsTenantsApart(t *testing.T) {
	for _, configured := range []string{"", "orders", "/data/files", "/data/files/orders"} {
		a, err := Resolve(base, tA, configured)
		if err != nil {
			t.Fatalf("Resolve(%q) for %s: %v", configured, tA, err)
		}
		b, err := Resolve(base, tB, configured)
		if err != nil {
			t.Fatalf("Resolve(%q) for %s: %v", configured, tB, err)
		}
		if a == b {
			t.Errorf("both tenants resolve %q to %s — one would read or overwrite the other's files", configured, a)
		}
		if within(a, b) || within(b, a) {
			t.Errorf("tenant trees overlap: %s and %s", a, b)
		}
	}
}

// TestResolve_DoesNotExpandTilde records an ordering requirement for callers:
// "~" is not special here, so a caller that supports it must expand it BEFORE
// resolving. Expanded first, "~/x" becomes an absolute path outside the mounted
// base and is refused; left unexpanded it is merely a directory named "~"
// inside the tenant root — contained, but not what the user meant.
func TestResolve_DoesNotExpandTilde(t *testing.T) {
	got, err := Resolve(base, tA, "~/orders")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/data/files/tenant-a/~/orders"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// And once a caller has expanded it, it is outside and refused.
	if _, err := Resolve(base, tA, "/home/someone/orders"); !errors.Is(err, ErrEscapesTenantRoot) {
		t.Errorf("expanded home path error = %v, want ErrEscapesTenantRoot", err)
	}
}

// TestResolve_TenantCannotReachAnotherTenant: naming another tenant explicitly
// must not escape. It stays contained inside the caller's own root rather than
// reaching the real one.
func TestResolve_TenantCannotReachAnotherTenant(t *testing.T) {
	got, err := Resolve(base, tA, "/data/files/tenant-b/orders")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	victim, _ := Root(base, tB)
	if within(got, victim) {
		t.Fatalf("tenant-a resolved into tenant-b's tree at %s", got)
	}
	if want := "/data/files/tenant-a/tenant-b/orders"; got != want {
		t.Errorf("got %q, want %q — contained inside the caller's own root", got, want)
	}
}

func TestRoot_RejectsUnusableTenantIDs(t *testing.T) {
	for _, id := range []string{"", "  ", ".", "..", "a/b", "../escape", " padded"} {
		if _, err := Root(base, id); err == nil {
			t.Errorf("Root(%q) succeeded; a tenant id that is not one path segment widens every root built from it", id)
		} else if !errors.Is(err, ErrBadTenantID) {
			t.Errorf("Root(%q) error = %v, want ErrBadTenantID", id, err)
		}
	}
}

// within is the containment test the whole package rests on; a naive
// strings.HasPrefix would accept an adjacent directory.
func TestWithin(t *testing.T) {
	cases := map[string]struct {
		p, dir string
		want   bool
	}{
		"same path":          {"/data/files", "/data/files", true},
		"child":              {"/data/files/a", "/data/files", true},
		"grandchild":         {"/data/files/a/b", "/data/files", true},
		"adjacent sibling":   {"/data/files-other", "/data/files", false},
		"prefix but not dir": {"/data/filesX", "/data/files", false},
		"parent":             {"/data", "/data/files", false},
		"unrelated":          {"/etc", "/data/files", false},
	}
	for name, tc := range cases {
		if got := within(tc.p, tc.dir); got != tc.want {
			t.Errorf("%s: within(%q, %q) = %v, want %v", name, tc.p, tc.dir, got, tc.want)
		}
	}
}

func TestResolve_ErrorMentionsTheOffendingPath(t *testing.T) {
	_, err := Resolve(base, tA, "/etc/passwd")
	if err == nil {
		t.Fatal("expected an error")
	}
	// The operator has to be able to see which config value was rejected.
	if !strings.Contains(err.Error(), "/etc/passwd") {
		t.Errorf("error %q does not name the rejected path", err)
	}
}
