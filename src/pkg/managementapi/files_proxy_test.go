package managementapi

import (
	"context"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// newFilesRequest builds a request the way the mux would: tenant in context,
// {id} as a path value.
func newFilesRequest(t *testing.T, method, tenantID, connID, query string, body io.Reader) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/connections/"+connID+"/files"+query, body)
	r.SetPathValue("id", connID)
	return r.WithContext(ContextWithTenantID(context.Background(), tenantID))
}

// pointFileWorkerAt redirects one file-worker allowlist entry at a test server
// for the length of a test. The allowlist is the only source of an upstream
// address, which is what keeps the proxy from being an SSRF primitive — and
// what makes it the natural seam here.
func pointFileWorkerAt(t *testing.T, which *workerEventSource, srv *httptest.Server) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("parse test server URL %q: %v", srv.URL, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	orig := *which
	*which = workerEventSource{service: host, port: port}
	t.Cleanup(func() { *which = orig })
}

// TestProxyListFiles_ForwardsForOwnedConnection is the happy path — and pins
// the contract the worker depends on: the connection id the proxy forwards is
// the one the DATABASE returned for this tenant, not the one in the URL. The
// worker resolves the output directory from it, so a caller-supplied value
// would choose someone else's directory.
func TestProxyListFiles_ForwardsForOwnedConnection(t *testing.T) {
	const (
		tenant = "tenant-a"
		connID = "11111111-1111-1111-1111-111111111111"
	)

	var gotQuery, gotMethod string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotMethod = r.URL.RawQuery, r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[{"name":"a.json"}]}`))
	}))
	defer upstream.Close()
	pointFileWorkerAt(t, &fileWorkers.producer, upstream)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT id::text FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(connID))

	h := &Handler{db: db}
	rec := httptest.NewRecorder()
	h.ProxyListFiles(rec, newFilesRequest(t, http.MethodGet, tenant, connID, "?path=/data/output/bc-items", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if gotMethod != http.MethodGet {
		t.Errorf("upstream method = %q, want GET", gotMethod)
	}
	if !strings.Contains(gotQuery, "connection_id="+connID) {
		t.Errorf("upstream query = %q, want it to carry connection_id=%s", gotQuery, connID)
	}
	if !strings.Contains(gotQuery, "path=") {
		t.Errorf("upstream query = %q, want the caller's path passed through", gotQuery)
	}
	if !strings.Contains(rec.Body.String(), "a.json") {
		t.Errorf("upstream body did not reach the client, got: %q", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db expectations: %v", err)
	}
}

// TestProxyFiles_RefusesForeignConnection is the security case.
//
// file-producer's /files has no tenant check reachable without a connection id,
// and no auth at all when FILE_PRODUCER_AUTH_TOKEN is unset. This ownership
// check is what stands between an authenticated user and another workspace's
// files. If it regresses, the proxy lists and deletes them on request.
func TestProxyFiles_RefusesForeignConnection(t *testing.T) {
	const connID = "22222222-2222-2222-2222-222222222222"

	for _, tc := range []struct {
		name   string
		method string
		call   func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{"list", http.MethodGet, (*Handler).ProxyListFiles},
		{"delete", http.MethodDelete, (*Handler).ProxyDeleteFile},
		{"upload", http.MethodPost, (*Handler).ProxyUploadFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
			}))
			defer upstream.Close()
			pointFileWorkerAt(t, &fileWorkers.producer, upstream)
			pointFileWorkerAt(t, &fileWorkers.consumer, upstream)

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			// The connection exists, but not for this tenant: the scoped query
			// returns nothing.
			mock.ExpectQuery("SELECT id::text FROM connections").
				WithArgs(connID, "tenant-b").
				WillReturnRows(sqlmock.NewRows([]string{"id"}))

			h := &Handler{db: db}
			rec := httptest.NewRecorder()
			tc.call(h, rec, newFilesRequest(t, tc.method, "tenant-b", connID, "?path=/data/output", nil))

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			if reached {
				t.Error("the worker was contacted for a connection the tenant does not own")
			}
			// 404 for a foreign connection, the same as for a missing one:
			// a distinct answer would confirm the ID exists.
			if strings.Contains(strings.ToLower(rec.Body.String()), "forbidden") {
				t.Errorf("response distinguishes foreign from missing: %q", rec.Body.String())
			}
		})
	}
}

// TestProxyUploadFile_StreamsBodyToOwnedConnection checks the multipart body
// and its Content-Type (which carries the boundary — without it the upstream
// cannot parse the form) survive the hop.
func TestProxyUploadFile_StreamsBodyToOwnedConnection(t *testing.T) {
	const (
		tenant = "tenant-a"
		connID = "33333333-3333-3333-3333-333333333333"
	)

	var gotPath, gotCT, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotCT = r.URL.Path, r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer upstream.Close()
	pointFileWorkerAt(t, &fileWorkers.consumer, upstream)

	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "orders.csv")
	_, _ = fw.Write([]byte("id,total\n1,9.99\n"))
	_ = mw.Close()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT id::text FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(connID))

	req := newFilesRequest(t, http.MethodPost, tenant, connID, "/upload", strings.NewReader(buf.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())

	h := &Handler{db: db}
	rec := httptest.NewRecorder()
	h.ProxyUploadFile(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if gotPath != "/upload/"+connID {
		t.Errorf("upstream path = %q, want /upload/%s", gotPath, connID)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data; boundary=") {
		t.Errorf("Content-Type = %q, want the multipart boundary preserved", gotCT)
	}
	if !strings.Contains(gotBody, "1,9.99") {
		t.Errorf("upload body did not reach the worker, got: %q", gotBody)
	}
}

// TestProxyFiles_RelaysUpstreamRefusal checks a worker's own 403 — what it
// answers for a path outside the connection's tenant directory — reaches the
// caller as a 403 rather than being flattened into a 200 or a 502. The panel
// showing "Load failed" with no reason is the failure this whole change is
// about; swallowing the reason here would reproduce it one layer up.
func TestProxyFiles_RelaysUpstreamRefusal(t *testing.T) {
	const (
		tenant = "tenant-a"
		connID = "44444444-4444-4444-4444-444444444444"
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"path is outside this connection's directory"}`))
	}))
	defer upstream.Close()
	pointFileWorkerAt(t, &fileWorkers.producer, upstream)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT id::text FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(connID))

	h := &Handler{db: db}
	rec := httptest.NewRecorder()
	h.ProxyListFiles(rec, newFilesRequest(t, http.MethodGet, tenant, connID, "?path=/etc", nil))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want the upstream 403 relayed", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "outside this connection's directory") {
		t.Errorf("upstream reason lost, got: %q", rec.Body.String())
	}
}
