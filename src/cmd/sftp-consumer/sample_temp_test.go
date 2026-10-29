package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// orderedSFTP lists its files in a fixed order, which fakeSFTP's map does not,
// so "first match" is something a test can pin down.
type orderedSFTP struct {
	*fakeSFTP
	order []string
}

func (o *orderedSFTP) List(string) ([]remoteFile, error) {
	out := make([]remoteFile, 0, len(o.order))
	for _, n := range o.order {
		out = append(out, remoteFile{Name: n, Size: int64(len(o.files[n]))})
	}
	return out, nil
}

// TestSampleData_SkipsTempNames: the sample-data preview skips the temp names
// the poller skips (#590) unless file_pattern names them, so it shows a file
// that will be published (#601).
func TestSampleData_SkipsTempNames(t *testing.T) {
	order := []string{".hidden.json", "~lock.json", "x.csv.part", "X.TMP", "real.json"}
	files := map[string][]byte{
		".hidden.json": []byte(`{"from":"dotfile"}`),
		"~lock.json":   []byte(`{"from":"tilde"}`),
		"x.csv.part":   []byte(`{"from":"part"}`),
		"X.TMP":        []byte(`{"from":"tmp"}`),
		"real.json":    []byte(`{"from":"real"}`),
	}
	tests := []struct {
		name      string
		order     []string
		pattern   string
		wantFile  string // empty: expect ok=false
		wantError string
	}{
		{name: "no pattern skips dotfile, tilde, .part and .tmp", order: order, wantFile: "real.json"},
		{name: "star pattern does not name temp names", order: order, pattern: "*", wantFile: "real.json"},
		{name: "json pattern skips dotfile and tilde matches", order: order, pattern: "*.json", wantFile: "real.json"},
		{name: "part pattern samples the part file", order: order, pattern: "*.part", wantFile: "x.csv.part"},
		{name: "dot pattern samples the dotfile", order: order, pattern: ".*", wantFile: ".hidden.json"},
		{name: "tilde pattern samples the tilde file", order: order, pattern: "~*", wantFile: "~lock.json"},
		{name: "only temp names and no pattern: nothing to sample", order: order[:4], wantError: "no files"},
		{name: "temp names matched by a plain pattern: nothing to sample", order: order[:4], pattern: "*.json", wantError: "no files"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &orderedSFTP{fakeSFTP: &fakeSFTP{files: files}, order: tc.order}
			s := &sftpConsumer{dial: func(*SFTPConfig) (sftpConn, error) { return fake, nil }}
			body, _ := json.Marshal(map[string]any{
				"host": "h", "username": "u", "remote_dir": "/in", "file_pattern": tc.pattern,
			})
			rec := httptest.NewRecorder()
			s.handleSampleData()(rec, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(string(body))))

			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("response is not JSON: %v: %s", err, rec.Body.String())
			}
			if tc.wantError != "" {
				if resp["ok"] != false || !strings.Contains(resp["error"].(string), tc.wantError) {
					t.Fatalf("want ok=false with error containing %q, got %v", tc.wantError, resp)
				}
				return
			}
			if resp["ok"] != true || resp["filename"] != tc.wantFile {
				t.Fatalf("sampled %v (ok=%v), want %q", resp["filename"], resp["ok"], tc.wantFile)
			}
		})
	}
}
