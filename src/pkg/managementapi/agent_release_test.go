package managementapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stageAgentRelease points AGENT_RELEASE_DIR at a temp dir holding a fake
// agent binary and version file, and returns the dir and the binary's bytes.
func stageAgentRelease(t *testing.T, version string) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	body := []byte("MZ fake windows agent " + version)
	if err := os.WriteFile(filepath.Join(dir, agentReleaseFile), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, agentReleaseVersion), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(agentReleaseDirEnv, dir)
	return dir, body
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func getRelease(t *testing.T) AgentRelease {
	t.Helper()
	rec := httptest.NewRecorder()
	(&Handler{}).GetAgentRelease(rec, httptest.NewRequest(http.MethodGet, "/api/v1/agents/release", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("release: status %d, body %s", rec.Code, rec.Body.String())
	}
	var env struct{ Data AgentRelease }
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("release JSON: %v", err)
	}
	return env.Data
}

// /release describes exactly the file that /download serves — the installer
// compares the two, so a mismatch here would refuse every install.
func TestAgentRelease_ReportsChecksumVersionAndSize(t *testing.T) {
	_, body := stageAgentRelease(t, "b9548be")
	got := getRelease(t)
	want := AgentRelease{Platform: "windows-amd64", Version: "b9548be", SHA256: sha256Hex(body),
		SizeBytes: int64(len(body)), Filename: "vrsky-agent.exe"}
	if got != want {
		t.Errorf("release = %+v, want %+v", got, want)
	}
}

func TestAgentDownload_ServesExactBytesWithChecksumHeaders(t *testing.T) {
	_, body := stageAgentRelease(t, "b9548be")
	rec := httptest.NewRecorder()
	(&Handler{}).DownloadAgent(rec, httptest.NewRequest(http.MethodGet, "/api/v1/agents/download/windows-amd64", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); string(got) != string(body) {
		t.Errorf("body differs from the staged binary (%d vs %d bytes)", len(got), len(body))
	}
	h := rec.Header()
	if h.Get("X-Checksum-Sha256") != sha256Hex(body) {
		t.Errorf("X-Checksum-Sha256 = %q", h.Get("X-Checksum-Sha256"))
	}
	if h.Get("X-Agent-Version") != "b9548be" {
		t.Errorf("X-Agent-Version = %q", h.Get("X-Agent-Version"))
	}
	if cd := h.Get("Content-Disposition"); !strings.Contains(cd, `filename="vrsky-agent.exe"`) {
		t.Errorf("Content-Disposition = %q — the browser must save it as vrsky-agent.exe", cd)
	}
	if h.Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control = %q; a proxy could serve a previous image's agent", h.Get("Cache-Control"))
	}
}

// A dev server or an image built without the agent must say so, on both
// routes, rather than 500 or serve an empty file.
func TestAgentRelease_MissingBuildIs404OnBothRoutes(t *testing.T) {
	t.Setenv(agentReleaseDirEnv, t.TempDir())
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"release":  (&Handler{}).GetAgentRelease,
		"download": (&Handler{}).DownloadAgent,
	} {
		rec := httptest.NewRecorder()
		call(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "AgentReleaseUnavailable") {
			t.Errorf("%s: status %d body %s, want 404 AgentReleaseUnavailable", name, rec.Code, rec.Body.String())
		}
	}
}

// The checksum is cached; a replaced binary (new image, a dev copying in a
// build) must be re-hashed, or /release would refuse every download of it.
func TestAgentRelease_RecomputesWhenTheFileChanges(t *testing.T) {
	dir, _ := stageAgentRelease(t, "v1")
	first := getRelease(t)

	path := filepath.Join(dir, agentReleaseFile)
	newBody := []byte("MZ a different build entirely")
	if err := os.WriteFile(path, newBody, 0o644); err != nil {
		t.Fatal(err)
	}
	// Same-second writes can share an mtime; make the change unmistakable.
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	second := getRelease(t)
	if second.SHA256 == first.SHA256 {
		t.Fatal("checksum did not change after the binary was replaced")
	}
	if second.SHA256 != sha256Hex(newBody) {
		t.Errorf("checksum = %s, want that of the new file", second.SHA256)
	}
}

// The scripts are served as plain text (so Invoke-RestMethod returns a string)
// and talk to the routes this package actually serves.
func TestAgentScripts_ServedAsTextAndPinnedToRoutes(t *testing.T) {
	h := &Handler{}
	for name, tc := range map[string]struct {
		call  func(http.ResponseWriter, *http.Request)
		wants []string
	}{
		"install": {h.ServeAgentInstallScript, []string{
			"param(", "[string]$Url", "[string]$Token",
			"/api/v1/agents/release", "/api/v1/agents/download/windows-amd64",
			"Get-FileHash", "Unblock-File", "'VRSkyAgent'", "register --url $Url --token $Token",
		}},
		"uninstall": {h.ServeAgentUninstallScript, []string{"param(", "[switch]$Purge", "'VRSkyAgent'", "uninstall"}},
	} {
		rec := httptest.NewRecorder()
		tc.call(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", name, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("%s: Content-Type = %q, want text/plain", name, ct)
		}
		body, _ := io.ReadAll(rec.Body)
		for _, w := range tc.wants {
			if !strings.Contains(string(body), w) {
				t.Errorf("%s script lacks %q", name, w)
			}
		}
	}
}
