package objectstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Tests for #718: DeleteIf and CopyIf send each provider's condition on the
// object's version, and map its answers: 412 to ErrVersionChanged, a missing
// object to ErrNotFound. Each backend runs its real SDK client against an
// httptest server that answers the way the provider does.

// condFake is one object, key "in/a" at version cur (missing when gone), as
// the provider's API serves it. It records the condition each request carried.
type condFake struct {
	provider string
	cur      string
	gone     bool

	mu   sync.Mutex
	sent []string // "<method> <condition>" of each conditional request
}

func (f *condFake) record(method, cond string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, method+" "+cond)
}

func (f *condFake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

// check answers a conditional request on the source object: missing, refused
// or fine (true).
func (f *condFake) check(w http.ResponseWriter, cond string) bool {
	switch {
	case f.gone:
		switch f.provider {
		case "s3":
			nfS3Error(404, "NoSuchKey")(w, nil)
		case "azure":
			nfAzureError(404, "BlobNotFound")(w, nil)
		default:
			nfGCSError(404)(w, nil)
		}
		return false
	case cond != f.want():
		switch f.provider {
		case "s3":
			nfS3Error(412, "PreconditionFailed")(w, nil)
		case "azure":
			nfAzureError(412, "ConditionNotMet")(w, nil)
		default:
			nfGCSError(412)(w, nil)
		}
		return false
	}
	return true
}

// want is the condition a request for version cur carries: S3 and Azure quote
// the ETag, GCS sends the generation as is.
func (f *condFake) want() string {
	if f.provider == "gcs" {
		return f.cur
	}
	return `"` + f.cur + `"`
}

func (f *condFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch f.provider {
	case "s3":
		f.serveS3(w, r)
	case "azure":
		f.serveAzure(w, r)
	default:
		f.serveGCS(w, r)
	}
}

func (f *condFake) serveS3(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodDelete:
		cond := r.Header.Get("If-Match")
		f.record("DELETE", cond)
		if f.check(w, cond) {
			w.WriteHeader(http.StatusNoContent)
		}
	case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
		cond := r.Header.Get("X-Amz-Copy-Source-If-Match")
		f.record("COPY", cond)
		if f.check(w, cond) {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><CopyObjectResult><ETag>"x"</ETag><LastModified>2026-01-02T03:04:05.000Z</LastModified></CopyObjectResult>`)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *condFake) serveAzure(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodDelete:
		cond := r.Header.Get("If-Match")
		f.record("DELETE", cond)
		if f.check(w, cond) {
			w.WriteHeader(http.StatusAccepted)
		}
	case http.MethodGet: // the copy's download of the source
		cond := r.Header.Get("If-Match")
		f.record("GET", cond)
		if f.check(w, cond) {
			w.Header().Set("Content-Length", "1")
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("ETag", f.want())
			w.Header().Set("x-ms-blob-type", "BlockBlob")
			_, _ = io.WriteString(w, "x")
		}
	case http.MethodPut: // the copy's upload to the destination
		w.Header().Set("ETag", `"dst"`)
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *condFake) serveGCS(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodDelete:
		cond := r.URL.Query().Get("ifGenerationMatch")
		f.record("DELETE", cond)
		if f.check(w, cond) {
			w.WriteHeader(http.StatusNoContent)
		}
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/rewriteTo/"):
		cond := r.URL.Query().Get("ifSourceGenerationMatch")
		f.record("COPY", cond)
		if f.check(w, cond) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"kind":"storage#rewriteResponse","totalBytesRewritten":"1","objectSize":"1","done":true,`+
				`"resource":{"kind":"storage#object","name":"done/a","bucket":"b","size":"1","generation":"9"}}`)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// version is what the caller read as the object's Version.
func condVersion(provider string) string {
	if provider == "gcs" {
		return "1"
	}
	return "v1"
}

func TestConditional_PerProvider(t *testing.T) {
	cases := []struct {
		name    string
		cur     string // the object's version now, as S3/Azure ETag or GCS generation
		gcsCur  string
		gone    bool
		wantErr error // nil, ErrVersionChanged or ErrNotFound
	}{
		{name: "unchanged", cur: "v1", gcsCur: "1"},
		{name: "written again", cur: "v2", gcsCur: "2", wantErr: ErrVersionChanged},
		{name: "gone", cur: "v1", gcsCur: "1", gone: true, wantErr: ErrNotFound},
	}
	ops := []struct {
		name string
		// method is how the fake records the conditional request.
		method map[string]string
		run    func(c Conditional, version string) error
	}{
		{"DeleteIf", map[string]string{"s3": "DELETE", "azure": "DELETE", "gcs": "DELETE"},
			func(c Conditional, v string) error { return c.DeleteIf(context.Background(), "in/a", v) }},
		{"CopyIf", map[string]string{"s3": "COPY", "azure": "GET", "gcs": "COPY"},
			func(c Conditional, v string) error { return c.CopyIf(context.Background(), "in/a", "done/a", v) }},
	}
	for _, be := range nfBackends {
		for _, op := range ops {
			for _, tc := range cases {
				t.Run(be.name+"/"+op.name+"/"+tc.name, func(t *testing.T) {
					f := &condFake{provider: be.name, cur: tc.cur, gone: tc.gone}
					if be.name == "gcs" {
						f.cur = tc.gcsCur
					}
					store := be.build(t, f.ServeHTTP)
					c, ok := store.(Conditional)
					if !ok {
						t.Fatalf("%s store does not implement Conditional", be.name)
					}
					version := condVersion(be.name)
					err := op.run(c, version)
					switch {
					case tc.wantErr == nil && err != nil:
						t.Fatalf("%s: %v", op.name, err)
					case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
						t.Fatalf("%s = %v, want an error wrapping %v", op.name, err, tc.wantErr)
					}
					if tc.wantErr != ErrVersionChanged && errors.Is(err, ErrVersionChanged) {
						t.Errorf("%s: %v also reads as a changed version", op.name, err)
					}
					if tc.wantErr != ErrNotFound && errors.Is(err, ErrNotFound) {
						t.Errorf("%s: %v also reads as not found", op.name, err)
					}
					want := op.method[be.name] + " " + version
					if be.name != "gcs" {
						want = op.method[be.name] + ` "` + version + `"`
					}
					got := f.requests()
					if len(got) != 1 || got[0] != want {
						t.Errorf("conditional requests = %q, want [%q]", got, want)
					}
				})
			}
		}
	}
}

// An S3-compatible store without the conditional headers (501 NotImplemented)
// gets the plain request, so its after-action still works.
func TestConditional_S3NotImplementedFallsBack(t *testing.T) {
	for _, op := range []string{"delete", "copy"} {
		t.Run(op, func(t *testing.T) {
			var mu sync.Mutex
			var conds []string
			store := nfStore(t, &Config{Provider: ProviderS3, Bucket: "b", Region: "us-east-1", AccessKeyID: "k", SecretAccessKey: "s"},
				func(w http.ResponseWriter, r *http.Request) {
					cond := r.Header.Get("If-Match") + r.Header.Get("X-Amz-Copy-Source-If-Match")
					mu.Lock()
					conds = append(conds, cond)
					mu.Unlock()
					if cond != "" {
						nfS3Error(501, "NotImplemented")(w, r)
						return
					}
					if r.Method == http.MethodDelete {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					w.Header().Set("Content-Type", "application/xml")
					_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><CopyObjectResult><ETag>"x"</ETag></CopyObjectResult>`)
				})
			c := store.(Conditional)
			var err error
			if op == "delete" {
				err = c.DeleteIf(context.Background(), "in/a", "v1")
			} else {
				err = c.CopyIf(context.Background(), "in/a", "done/a", "v1")
			}
			if err != nil {
				t.Fatalf("%s with the fallback: %v", op, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(conds) != 2 || conds[0] != `"v1"` || conds[1] != "" {
				t.Errorf("requests' conditions = %q, want the conditional one, then a plain one", conds)
			}
		})
	}
}

// A GCS version that is not a generation is refused before any request: a
// plain delete would drop the condition without saying so.
func TestConditional_GCSBadVersion(t *testing.T) {
	f := &condFake{provider: "gcs", cur: "1"}
	var store ObjectStore
	for _, be := range nfBackends {
		if be.name == "gcs" {
			store = be.build(t, f.ServeHTTP)
		}
	}
	c := store.(Conditional)
	if err := c.DeleteIf(context.Background(), "in/a", "etag-not-a-number"); err == nil {
		t.Error("DeleteIf with a non-numeric generation worked")
	}
	if err := c.CopyIf(context.Background(), "in/a", "done/a", "etag-not-a-number"); err == nil {
		t.Error("CopyIf with a non-numeric generation worked")
	}
	if got := f.requests(); len(got) != 0 {
		t.Errorf("requests sent: %q", got)
	}
}
