package managementapi

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The Windows agent, downloadable from VRSky itself (#266).
//
// Settings → Remote agents shows one PowerShell command. It fetches
// install.ps1 from here, which downloads the agent from here, checks its
// SHA-256 against /release, and registers, installs and starts it. The files
// come from AGENT_RELEASE_DIR, which the management-api image fills at build
// time with an agent cross-compiled from the same commit — so the agent
// Settings offers always matches the server.
//
// These routes are PUBLIC: no session, no X-Tenant-ID (TenantIDMiddleware
// exempts them). The machine being set up has no login, and nothing here is
// secret — the binary is the same one CI publishes as an artifact. What
// protects a workspace is the single-use registration token, which the
// operator passes to the script; it never appears in these files.

//go:embed agent_install.ps1
var agentInstallScript []byte

//go:embed agent_uninstall.ps1
var agentUninstallScript []byte

const (
	agentReleaseDirEnv     = "AGENT_RELEASE_DIR"
	agentReleaseDirDefault = "/app/agent"
	agentReleasePlatform   = "windows-amd64"
	agentReleaseFile       = "vrsky-agent-windows-amd64.exe"
	agentReleaseVersion    = "version.txt"
	agentDownloadName      = "vrsky-agent.exe"
)

// AgentRelease describes the agent build this server offers for download.
type AgentRelease struct {
	Platform  string `json:"platform"`
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Filename  string `json:"filename"`
}

var errAgentReleaseMissing = errors.New("agent release not present")

func agentReleaseDir() string {
	if d := os.Getenv(agentReleaseDirEnv); d != "" {
		return d
	}
	return agentReleaseDirDefault
}

// The checksum of a 7 MB file is cached per path and recomputed only when the
// file's size or modification time changes, so /release stays cheap and a
// replaced file (a new image, a dev copying in a build) is never misreported.
type agentReleaseEntry struct {
	info    AgentRelease
	modTime time.Time
	size    int64
}

var agentReleaseCache = struct {
	mu     sync.Mutex
	byPath map[string]agentReleaseEntry
}{byPath: map[string]agentReleaseEntry{}}

// loadAgentRelease returns the release info and the path of the binary.
func loadAgentRelease(dir string) (AgentRelease, string, error) {
	path := filepath.Join(dir, agentReleaseFile)
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return AgentRelease{}, "", errAgentReleaseMissing
	}

	agentReleaseCache.mu.Lock()
	defer agentReleaseCache.mu.Unlock()
	if e, ok := agentReleaseCache.byPath[path]; ok && e.modTime.Equal(st.ModTime()) && e.size == st.Size() {
		return e.info, path, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return AgentRelease{}, "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return AgentRelease{}, "", err
	}
	version := "unknown"
	if raw, err := os.ReadFile(filepath.Join(dir, agentReleaseVersion)); err == nil {
		if v := strings.TrimSpace(string(raw)); v != "" {
			version = v
		}
	}
	info := AgentRelease{
		Platform:  agentReleasePlatform,
		Version:   version,
		SHA256:    hex.EncodeToString(h.Sum(nil)),
		SizeBytes: st.Size(),
		Filename:  agentDownloadName,
	}
	agentReleaseCache.byPath[path] = agentReleaseEntry{info: info, modTime: st.ModTime(), size: st.Size()}
	return info, path, nil
}

func writeAgentReleaseMissing(w http.ResponseWriter) {
	_ = writeError(w, http.StatusNotFound, "AgentReleaseUnavailable",
		fmt.Sprintf("this server was built without the Windows agent (no %s in %s)", agentReleaseFile, agentReleaseDir()), nil)
}

// GetAgentRelease reports the downloadable agent's version and checksum.
//
// GET /api/v1/agents/release
func (h *Handler) GetAgentRelease(w http.ResponseWriter, _ *http.Request) {
	info, _, err := loadAgentRelease(agentReleaseDir())
	if err != nil {
		writeAgentReleaseMissing(w)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	_ = writeJSON(w, http.StatusOK, map[string]any{"data": info})
}

// DownloadAgent serves the Windows agent binary.
//
// GET /api/v1/agents/download/windows-amd64
func (h *Handler) DownloadAgent(w http.ResponseWriter, r *http.Request) {
	info, path, err := loadAgentRelease(agentReleaseDir())
	if err != nil {
		writeAgentReleaseMissing(w)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeAgentReleaseMissing(w)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeAgentReleaseMissing(w)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.microsoft.portable-executable")
	w.Header().Set("Content-Disposition", `attachment; filename="`+agentDownloadName+`"`)
	w.Header().Set("X-Checksum-Sha256", info.SHA256)
	w.Header().Set("X-Agent-Version", info.Version)
	// A proxy must never hand out a previous image's binary after an upgrade.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, agentDownloadName, st.ModTime(), f)
}

// ServeAgentInstallScript serves the PowerShell installer.
//
// GET /api/v1/agents/install.ps1
func (h *Handler) ServeAgentInstallScript(w http.ResponseWriter, _ *http.Request) {
	serveScript(w, agentInstallScript)
}

// ServeAgentUninstallScript serves the PowerShell uninstaller.
//
// GET /api/v1/agents/uninstall.ps1
func (h *Handler) ServeAgentUninstallScript(w http.ResponseWriter, _ *http.Request) {
	serveScript(w, agentUninstallScript)
}

func serveScript(w http.ResponseWriter, script []byte) {
	// text/plain so Invoke-RestMethod hands the script back as one string,
	// which the one-liner turns into a script block.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(script)
}
