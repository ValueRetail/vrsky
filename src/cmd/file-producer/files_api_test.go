package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// newFilesAPI builds a file-producer whose output root is a temp dir, with the
// connection→tenant lookup mocked.
func newFilesAPI(t *testing.T, connID, tenantID string) (*fileProducer, string) {
	t.Helper()
	dir := t.TempDir()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery("SELECT tenant_id::text FROM connections").
		WithArgs(connID).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow(tenantID)).
		RowsWillBeClosed()

	p := &fileProducer{
		db:               db,
		defaultOutputDir: dir,
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return p, dir
}

// TestFilesAPI_ListsOnlyTheConnectionsOwnTenant is the bug the file manager hit
// in production: the pipeline wrote to <root>/<tenant>/bc-items while the panel
// listed <root>/bc-items and showed nothing. The caller still sends the path it
// typed; the tenant segment is added here.
func TestFilesAPI_ListsOnlyTheConnectionsOwnTenant(t *testing.T) {
	const (
		connID = "11111111-1111-1111-1111-111111111111"
		tenant = "tenant-a"
	)
	p, root := newFilesAPI(t, connID, tenant)

	mine := filepath.Join(root, tenant, "bc-items")
	if err := os.MkdirAll(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "items.json"), []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/files?connection_id="+connID+"&path="+filepath.Join(root, "bc-items"), nil)
	p.filesHandler("", "http://localhost:5173")(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Files []fileEntry `json:"files"`
		Path  string      `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if len(got.Files) != 1 || got.Files[0].Name != "items.json" {
		t.Fatalf("listed %+v, want the one file under the tenant's own directory", got.Files)
	}
	if got.Path != mine {
		t.Errorf("listed path = %q, want it re-homed to %q", got.Path, mine)
	}
}

// TestFilesAPI_CannotReachAnotherTenantsDirectory is the security case.
//
// Before this, the path was checked only against the output root, so naming
// another tenant's directory under it was allowed — a cross-tenant read on the
// shared volume, and with DELETE, a cross-tenant wipe.
//
// Note what the fix does: tenantpath RE-HOMES such a path into the caller's own
// subtree rather than rejecting it, which is its documented behaviour (people
// type the mounted root they can see, and rewriting that to their own copy of
// it is what they meant). So the assertion here is the outcome that matters —
// the other tenant's file is neither listed nor deleted — not the status code,
// which would pin a mechanism that is allowed to change.
func TestFilesAPI_CannotReachAnotherTenantsDirectory(t *testing.T) {
	const (
		connID = "22222222-2222-2222-2222-222222222222"
		tenant = "tenant-a"
		other  = "tenant-b"
	)

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			p, root := newFilesAPI(t, connID, tenant)

			victim := filepath.Join(root, other, "payroll")
			if err := os.MkdirAll(victim, 0o755); err != nil {
				t.Fatal(err)
			}
			secret := filepath.Join(victim, "secret.json")
			if err := os.WriteFile(secret, []byte(`{"salary":1}`), 0o644); err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			r := httptest.NewRequest(method, "/files?connection_id="+connID+"&path="+victim, nil)
			p.filesHandler("", "http://localhost:5173")(rec, r)

			if strings.Contains(rec.Body.String(), "secret.json") {
				t.Errorf("another tenant's file was listed: %s", rec.Body.String())
			}
			if _, err := os.Stat(secret); err != nil {
				t.Errorf("another tenant's file was deleted: %v", err)
			}
			// Whatever it did, it did inside the caller's own root.
			if body := rec.Body.String(); strings.Contains(body, filepath.Join(root, other)) {
				t.Errorf("response names a path outside the caller's root: %s", body)
			}
		})
	}
}

// TestFilesAPI_RefusesEscapeAboveTheOutputRoot covers the traversal case
// separately: ".." is collapsed by tenantpath, not by the filesystem.
func TestFilesAPI_RefusesEscapeAboveTheOutputRoot(t *testing.T) {
	const (
		connID = "33333333-3333-3333-3333-333333333333"
		tenant = "tenant-a"
	)
	p, root := newFilesAPI(t, connID, tenant)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/files?connection_id="+connID+"&path="+filepath.Join(root, "..", "..", "etc"), nil)
	p.filesHandler("", "http://localhost:5173")(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestFilesAPI_RefusesDeletingTheTenantRoot: an empty path resolves to the
// tenant's own root, so without this guard a delete with a missing parameter
// empties the whole tenant in one call.
func TestFilesAPI_RefusesDeletingTheTenantRoot(t *testing.T) {
	const (
		connID = "44444444-4444-4444-4444-444444444444"
		tenant = "tenant-a"
	)
	p, root := newFilesAPI(t, connID, tenant)

	mine := filepath.Join(root, tenant, "bc-items")
	if err := os.MkdirAll(mine, 0o755); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/files?connection_id="+connID, nil)
	p.filesHandler("", "http://localhost:5173")(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("the tenant's directory was removed: %v", err)
	}
}

// TestFilesAPI_RequiresConnectionID: without one there is no tenant to resolve,
// so the request must be refused rather than fall back to the shared root.
func TestFilesAPI_RequiresConnectionID(t *testing.T) {
	p, _ := newFilesAPI(t, "unused", "tenant-a")

	rec := httptest.NewRecorder()
	p.filesHandler("", "http://localhost:5173")(rec,
		httptest.NewRequest(http.MethodGet, "/files?path=/data/output", nil))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestFilesAPI_DeletesWithinTheTenant is the happy path for delete, so the
// guards above are not passing by refusing everything.
func TestFilesAPI_DeletesWithinTheTenant(t *testing.T) {
	const (
		connID = "55555555-5555-5555-5555-555555555555"
		tenant = "tenant-a"
	)
	p, root := newFilesAPI(t, connID, tenant)

	mine := filepath.Join(root, tenant, "bc-items")
	if err := os.MkdirAll(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(mine, "items.json")
	if err := os.WriteFile(target, []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	p.filesHandler("", "http://localhost:5173")(rec,
		httptest.NewRequest(http.MethodDelete, "/files?connection_id="+connID+"&path="+target, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("file still present after delete: %v", err)
	}
}
