package objectstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

// These tests run gcsStore against fakeGCS, an httptest stand-in for the GCS
// JSON upload API, so the real cloud.google.com/go/storage client decides how
// to upload. The client buffers up to one chunk (16 MiB by default) before it
// sends anything: a body that ends inside the first chunk goes out as a single
// uploadType=multipart request, a longer one as a resumable session with one
// request per chunk. The fake only commits an object when the client finishes
// an upload, which is exactly what a failed body must never do.

const (
	fakeBucket   = "fake-bucket"
	gcsChunkSize = 16 << 20 // storage.Writer's default ChunkSize
)

var errBodyFailed = errors.New("body failed partway")

type fakeObject struct {
	data        []byte
	contentType string
}

type fakeSession struct {
	name        string
	contentType string
	data        []byte
}

// fakeGCS implements the two upload shapes of the GCS JSON API. Objects are
// committed to the map only by a complete multipart request or by the final
// chunk of a resumable session.
type fakeGCS struct {
	mu       sync.Mutex
	objects  map[string]fakeObject // object name -> committed object
	sessions map[string]*fakeSession
	nextID   int
	// counters for asserting which upload shape the client used
	multipartCommits int
	sessionsStarted  int
	chunksReceived   int
	resumableCommits int
}

func newFakeGCS() *fakeGCS {
	return &fakeGCS{objects: map[string]fakeObject{}, sessions: map[string]*fakeSession{}}
}

func (f *fakeGCS) object(name string) (fakeObject, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[name]
	return o, ok
}

func (f *fakeGCS) put(name string, o fakeObject) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[name] = o
}

type fakeCounts struct {
	multipartCommits, sessionsStarted, chunksReceived, resumableCommits int
}

func (f *fakeGCS) counts() fakeCounts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeCounts{f.multipartCommits, f.sessionsStarted, f.chunksReceived, f.resumableCommits}
}

func (f *fakeGCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	want := "/upload/storage/v1/b/" + fakeBucket + "/o"
	if r.URL.Path != want || (r.Method != http.MethodPost && r.Method != http.MethodPut) {
		gcsError(w, http.StatusNotFound, fmt.Sprintf("fake gcs: unhandled %s %s", r.Method, r.URL.Path))
		return
	}
	q := r.URL.Query()
	switch {
	case q.Get("upload_id") != "":
		f.chunk(w, r, q.Get("upload_id"))
	case q.Get("uploadType") == "multipart":
		f.multipart(w, r)
	case q.Get("uploadType") == "resumable":
		f.startSession(w, r)
	default:
		gcsError(w, http.StatusBadRequest, "fake gcs: unknown uploadType "+q.Get("uploadType"))
	}
}

type objectMeta struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
}

// multipart commits only when it read the whole multipart/related body: the
// metadata part, the media part and the closing boundary, without an error.
func (f *fakeGCS) multipart(w http.ResponseWriter, r *http.Request) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/related" {
		gcsError(w, http.StatusBadRequest, "fake gcs: want multipart/related")
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	metaPart, err := mr.NextPart()
	if err != nil {
		gcsError(w, http.StatusBadRequest, "fake gcs: metadata part: "+err.Error())
		return
	}
	var meta objectMeta
	if err := json.NewDecoder(metaPart).Decode(&meta); err != nil {
		gcsError(w, http.StatusBadRequest, "fake gcs: metadata: "+err.Error())
		return
	}
	mediaPart, err := mr.NextPart()
	if err != nil {
		gcsError(w, http.StatusBadRequest, "fake gcs: media part: "+err.Error())
		return
	}
	data, err := io.ReadAll(mediaPart)
	if err != nil {
		gcsError(w, http.StatusBadRequest, "fake gcs: media: "+err.Error())
		return
	}
	if _, err := mr.NextPart(); !errors.Is(err, io.EOF) {
		gcsError(w, http.StatusBadRequest, fmt.Sprintf("fake gcs: body not complete: %v", err))
		return
	}
	name := firstNonEmpty(meta.Name, r.URL.Query().Get("name"))
	ct := firstNonEmpty(meta.ContentType, mediaPart.Header.Get("Content-Type"))

	f.mu.Lock()
	f.objects[name] = fakeObject{data: data, contentType: ct}
	f.multipartCommits++
	f.mu.Unlock()
	writeObject(w, name, ct, len(data))
}

func (f *fakeGCS) startSession(w http.ResponseWriter, r *http.Request) {
	var meta objectMeta
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
		gcsError(w, http.StatusBadRequest, "fake gcs: metadata: "+err.Error())
		return
	}
	s := &fakeSession{
		name:        firstNonEmpty(meta.Name, r.URL.Query().Get("name")),
		contentType: firstNonEmpty(meta.ContentType, r.Header.Get("X-Upload-Content-Type")),
	}
	f.mu.Lock()
	f.nextID++
	id := strconv.Itoa(f.nextID)
	f.sessions[id] = s
	f.sessionsStarted++
	f.mu.Unlock()
	w.Header().Set("Location", "http://"+r.Host+r.URL.Path+"?uploadType=resumable&upload_id="+id)
	w.WriteHeader(http.StatusOK)
}

var contentRangeRE = regexp.MustCompile(`^bytes (?:(\d+)-(\d+)|\*)/(\d+|\*)$`)

// chunk appends one chunk to a resumable session. The object is committed only
// when the Content-Range carries the total size and the session holds exactly
// that many bytes.
func (f *fakeGCS) chunk(w http.ResponseWriter, r *http.Request, id string) {
	m := contentRangeRE.FindStringSubmatch(r.Header.Get("Content-Range"))
	if m == nil {
		gcsError(w, http.StatusBadRequest, "fake gcs: bad Content-Range "+r.Header.Get("Content-Range"))
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		gcsError(w, http.StatusBadRequest, "fake gcs: chunk body: "+err.Error())
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunksReceived++
	s, ok := f.sessions[id]
	if !ok {
		gcsError(w, http.StatusNotFound, "fake gcs: no session "+id)
		return
	}
	if m[1] != "" {
		first, _ := strconv.Atoi(m[1])
		last, _ := strconv.Atoi(m[2])
		if first != len(s.data) || last-first+1 != len(data) {
			gcsError(w, http.StatusBadRequest, fmt.Sprintf("fake gcs: range %d-%d does not follow %d bytes or match %d sent", first, last, len(s.data), len(data)))
			return
		}
		s.data = append(s.data, data...)
	}
	if m[3] == "*" {
		// More to come. The client sends X-GUploader-No-308, so "incomplete"
		// is a 200 with the 308 in an override header.
		w.Header().Set("X-Http-Status-Code-Override", "308")
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(s.data)-1))
		w.WriteHeader(http.StatusOK)
		return
	}
	total, _ := strconv.Atoi(m[3])
	if total != len(s.data) {
		gcsError(w, http.StatusBadRequest, fmt.Sprintf("fake gcs: total %d but session holds %d", total, len(s.data)))
		return
	}
	f.objects[s.name] = fakeObject{data: s.data, contentType: s.contentType}
	delete(f.sessions, id)
	f.resumableCommits++
	writeObject(w, s.name, s.contentType, len(s.data))
}

func writeObject(w http.ResponseWriter, name, contentType string, size int) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"kind":        "storage#object",
		"bucket":      fakeBucket,
		"name":        name,
		"contentType": contentType,
		"size":        strconv.Itoa(size),
	})
}

func gcsError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// startFakeGCS serves a fresh fakeGCS and returns a gcsStore pointed at it.
// The caller closes both (tests that count goroutines close them explicitly).
func startFakeGCS(t *testing.T) (*fakeGCS, *httptest.Server, ObjectStore) {
	t.Helper()
	fake := newFakeGCS()
	srv := httptest.NewServer(fake)
	// An Endpoint turns on WithoutAuthentication; uploads resolve to
	// <host>/upload/storage/v1/..., which is where the fake listens.
	store, err := newGCSStore(context.Background(), &Config{
		Provider: ProviderGCS,
		Bucket:   fakeBucket,
		Endpoint: srv.URL + "/storage/v1/",
	})
	if err != nil {
		srv.Close()
		t.Fatalf("newGCSStore: %v", err)
	}
	return fake, srv, store
}

// payload returns n deterministic, non-repeating-looking bytes.
func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i/251)
	}
	return b
}

// failingBody yields sent bytes and then fails with errBodyFailed, like a
// pg_dump that dies halfway through a backup.
func failingBody(sent int) io.Reader {
	return io.MultiReader(bytes.NewReader(payload(sent)), iotest.ErrReader(errBodyFailed))
}

// putStreamWithin runs PutStream and fails the test if it does not return
// within d, so a hung upload shows up as a failure rather than a test timeout.
func putStreamWithin(t *testing.T, d time.Duration, store ObjectStore, key string, body io.Reader, ct string) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- store.PutStream(context.Background(), key, body, ct) }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("PutStream(%q) did not return within %v", key, d)
		return nil
	}
}

func TestGCSPutStream_GoodBodyCommitsExactBytes(t *testing.T) {
	tests := []struct {
		name          string
		size          int
		contentType   string
		wantResumable bool
	}{
		{name: "small body, one multipart request", size: 3 << 10, contentType: "application/json"},
		{name: "two chunks, resumable session", size: gcsChunkSize + 64<<10, contentType: "application/gzip", wantResumable: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake, srv, store := startFakeGCS(t)
			defer srv.Close()
			defer store.Close()

			want := payload(tc.size)
			if err := putStreamWithin(t, 30*time.Second, store, "backups/good.tar.gz", bytes.NewReader(want), tc.contentType); err != nil {
				t.Fatalf("PutStream: %v", err)
			}

			got, ok := fake.object("backups/good.tar.gz")
			if !ok {
				t.Fatal("no object committed")
			}
			if !bytes.Equal(got.data, want) {
				t.Errorf("committed %d bytes, want the %d bytes sent", len(got.data), len(want))
			}
			if got.contentType != tc.contentType {
				t.Errorf("content type = %q, want %q", got.contentType, tc.contentType)
			}
			c := fake.counts()
			if tc.wantResumable && (c.resumableCommits != 1 || c.chunksReceived < 2) {
				t.Errorf("want one resumable commit over at least two chunks, got %+v", c)
			}
			if !tc.wantResumable && c.multipartCommits != 1 {
				t.Errorf("want one multipart commit, got %+v", c)
			}
		})
	}
}

func TestGCSPutStream_FailedBodyLeavesNoObject(t *testing.T) {
	older := fakeObject{data: []byte("last night's good backup"), contentType: "application/gzip"}
	tests := []struct {
		name string
		sent int // bytes the body yields before it fails
		// a resumable session must have sent at least one chunk before the
		// failure, so the abort is tested after data reached the server
		wantChunkSent bool
		prior         *fakeObject
	}{
		// the old code committed an empty object here
		{name: "fails before the first byte", sent: 0},
		{name: "fails within the first chunk", sent: 4 << 10},
		{name: "fails within the first chunk, older object kept", sent: 4 << 10, prior: &older},
		{name: "fails after the first resumable chunk", sent: gcsChunkSize + 64<<10, wantChunkSent: true},
		{name: "fails after the first resumable chunk, older object kept", sent: gcsChunkSize + 64<<10, wantChunkSent: true, prior: &older},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake, srv, store := startFakeGCS(t)
			defer srv.Close()
			defer store.Close()

			const key = "backups/2026-10-29.tar.gz"
			if tc.prior != nil {
				fake.put(key, *tc.prior)
			}

			err := putStreamWithin(t, 30*time.Second, store, key, failingBody(tc.sent), "application/gzip")
			if err == nil {
				t.Fatal("PutStream returned nil for a body that failed")
			}
			if !errors.Is(err, errBodyFailed) {
				t.Errorf("PutStream error %v does not wrap the body error", err)
			}

			got, ok := fake.object(key)
			switch {
			case tc.prior == nil && ok:
				t.Errorf("a %d-byte partial object was committed under %q", len(got.data), key)
			case tc.prior != nil && !ok:
				t.Errorf("the older object under %q is gone", key)
			case tc.prior != nil && (!bytes.Equal(got.data, tc.prior.data) || got.contentType != tc.prior.contentType):
				t.Errorf("the older object was replaced by %d bytes (%q)", len(got.data), got.contentType)
			}

			c := fake.counts()
			if c.multipartCommits != 0 || c.resumableCommits != 0 {
				t.Errorf("an upload was finalised: %+v", c)
			}
			if tc.wantChunkSent && (c.sessionsStarted != 1 || c.chunksReceived < 1) {
				t.Errorf("want a resumable session with at least one chunk sent, got %+v", c)
			}

			if leaked := waitNoWriterGoroutines(2 * time.Second); leaked != "" {
				t.Errorf("upload goroutines still running after PutStream returned:\n%s", leaked)
			}
		})
	}
}

// TestGCSPutStream_FailedBodyLeavesNoGoroutines counts every goroutine, not
// just the writer's: after the store and the server are closed the process
// must be back where it started.
func TestGCSPutStream_FailedBodyLeavesNoGoroutines(t *testing.T) {
	for _, sent := range []int{4 << 10, gcsChunkSize + 64<<10} {
		t.Run(fmt.Sprintf("%d bytes sent", sent), func(t *testing.T) {
			before := runtime.NumGoroutine()

			_, srv, store := startFakeGCS(t)
			err := putStreamWithin(t, 30*time.Second, store, "backups/leak.tar.gz", failingBody(sent), "application/gzip")
			if !errors.Is(err, errBodyFailed) {
				t.Errorf("PutStream error = %v, want the body error", err)
			}
			_ = store.Close()
			srv.Close()

			deadline := time.Now().Add(5 * time.Second)
			for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if after := runtime.NumGoroutine(); after > before {
				t.Errorf("%d goroutines before, %d after:\n%s", before, after, allStacks())
			}
		})
	}
}

// writerFrames name the goroutines a storage.Writer starts for one upload.
var writerFrames = []string{
	"cloud.google.com/go/storage.(*httpStorageClient).OpenWriter",
	"cloud.google.com/go/storage.(*Writer).monitorCancel",
	"google.golang.org/api/internal/gensupport",
}

// waitNoWriterGoroutines waits up to d for the writer's goroutines to exit and
// returns the stacks of any still running ("" when none are).
func waitNoWriterGoroutines(d time.Duration) string {
	deadline := time.Now().Add(d)
	for {
		var leaked []string
		for _, g := range strings.Split(allStacks(), "\n\n") {
			for _, f := range writerFrames {
				if strings.Contains(g, f) {
					leaked = append(leaked, g)
					break
				}
			}
		}
		if len(leaked) == 0 {
			return ""
		}
		if time.Now().After(deadline) {
			return strings.Join(leaked, "\n\n")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func allStacks() string {
	buf := make([]byte, 1<<20)
	return string(buf[:runtime.Stack(buf, true)])
}
