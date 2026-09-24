// Package agent is the VRSky remote agent: a small program on a customer
// machine that dials out to VRSky over HTTPS and makes that machine usable as a
// pipeline input (it uploads files that appear in a read folder) and output
// (it writes files VRSky sends into a write folder).
//
// The agent decides what VRSky may touch. Its config file names the folders
// and their paths; VRSky only ever refers to those folders by name and never
// sees or sends a path. Operations are a fixed, typed set (pkg/agentproto):
// there is no way to make the agent run a command.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
)

// Config is the agent's config file.
type Config struct {
	// ServerURL is used by `register`; after registering, the agent talks to
	// the URL it registered with (stored beside the credential).
	ServerURL string `json:"server_url,omitempty"`
	// AgentName is the name to register under; VRSky falls back to the
	// hostname.
	AgentName string `json:"agent_name,omitempty"`
	// PollIntervalSeconds is how often read folders are scanned. Default 5.
	PollIntervalSeconds int `json:"poll_interval_seconds,omitempty"`
	// DataDir holds the credential, state and (by default) logs. Default:
	// %ProgramData%\VRSky\agent on Windows, /var/lib/vrsky-agent elsewhere.
	DataDir string `json:"data_dir,omitempty"`
	// AllowInsecureHTTP permits a plain http:// server other than localhost
	// — for a test stack reached over a private network such as Tailscale.
	// The credential then crosses that network unencrypted.
	AllowInsecureHTTP bool `json:"allow_insecure_http,omitempty"`

	Log         LogConfig            `json:"log"`
	Directories map[string]DirConfig `json:"directories"`
}

// LogConfig controls the log file.
type LogConfig struct {
	File      string `json:"file,omitempty"`        // default <data_dir>/logs/agent.log
	MaxSizeMB int    `json:"max_size_mb,omitempty"` // default 20
	MaxFiles  int    `json:"max_files,omitempty"`   // rotated copies kept; default 5
}

// DirConfig is one folder VRSky may use, by the name it is keyed under.
type DirConfig struct {
	Path string `json:"path"`
	Mode string `json:"mode"` // read: files here are uploaded. write: delivered files land here.
}

// DefaultBaseDir is where the agent keeps its files unless configured otherwise.
func DefaultBaseDir() string {
	if runtime.GOOS == "windows" {
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "VRSky", "agent")
	}
	return "/var/lib/vrsky-agent"
}

// DefaultConfigPath is where `run` looks when --config is not given.
func DefaultConfigPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(DefaultBaseDir(), "config.json")
	}
	return "/etc/vrsky-agent/config.json"
}

// LoadConfig reads and validates a config file, filling defaults.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields() // a typo in a key should not silently do nothing
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if err := c.normalise(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) normalise() error {
	if c.PollIntervalSeconds <= 0 {
		c.PollIntervalSeconds = 5
	}
	if c.DataDir == "" {
		c.DataDir = DefaultBaseDir()
	}
	if c.Log.File == "" {
		c.Log.File = filepath.Join(c.DataDir, "logs", "agent.log")
	}
	if c.Log.MaxSizeMB <= 0 {
		c.Log.MaxSizeMB = 20
	}
	if c.Log.MaxFiles <= 0 {
		c.Log.MaxFiles = 5
	}
	if c.ServerURL != "" {
		if err := CheckServerURL(c.ServerURL, c.AllowInsecureHTTP); err != nil {
			return err
		}
	}
	if len(c.Directories) == 0 {
		return errors.New("directories: define at least one folder")
	}
	for name, d := range c.Directories {
		if !agentproto.ValidDirectoryName(name) {
			return fmt.Errorf("directories: %q is not a valid name (letters, digits, - and _, up to 64)", name)
		}
		if !agentproto.ValidMode(d.Mode) {
			return fmt.Errorf("directories.%s: mode must be \"read\" or \"write\"", name)
		}
		if !filepath.IsAbs(d.Path) {
			return fmt.Errorf("directories.%s: path must be absolute, got %q", name, d.Path)
		}
		d.Path = filepath.Clean(d.Path)
		info, err := os.Stat(d.Path)
		if err != nil {
			return fmt.Errorf("directories.%s: %w", name, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("directories.%s: %s is not a folder", name, d.Path)
		}
		if d.Mode == agentproto.ModeWrite {
			if err := probeWritable(d.Path); err != nil {
				return fmt.Errorf("directories.%s: cannot write to %s: %w", name, d.Path, err)
			}
		}
		c.Directories[name] = d
	}
	return nil
}

// probeWritable checks a write folder actually accepts files, so a
// permissions problem shows at startup rather than on the first delivery.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, "~vrsky-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// CheckServerURL requires https, except for loopback or when explicitly allowed.
func CheckServerURL(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("server url %q is not a URL", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" || allowInsecure {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("server url %q uses plain http; use https, or set allow_insecure_http for a private test network", raw)
	default:
		return fmt.Errorf("server url %q must be https", raw)
	}
}

// Announced lists the folders as VRSky sees them: names and modes, sorted.
// Paths are deliberately absent.
func (c *Config) Announced() []agentproto.Directory {
	out := make([]agentproto.Directory, 0, len(c.Directories))
	for name, d := range c.Directories {
		out = append(out, agentproto.Directory{Name: name, Mode: d.Mode})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
