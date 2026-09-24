package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, v any) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	var raw []byte
	if s, ok := v.(string); ok {
		raw = []byte(s)
	} else {
		raw, _ = json.Marshal(v)
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// testConfig builds a valid config over real temp folders.
func testConfig(t *testing.T) *Config {
	t.Helper()
	base := t.TempDir()
	in, out := filepath.Join(base, "SECRET-PATH-read-7f3a"), filepath.Join(base, "SECRET-PATH-write-9c1b")
	for _, d := range []string{in, out} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := &Config{
		DataDir: filepath.Join(base, "data"),
		Directories: map[string]DirConfig{
			"inbox":  {Path: in, Mode: "read"},
			"outbox": {Path: out, Mode: "write"},
		},
	}
	if err := c.normalise(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConfig_LoadsValidAndFillsDefaults(t *testing.T) {
	c := testConfig(t)
	p := writeConfig(t, map[string]any{"directories": map[string]any{
		"inbox": map[string]any{"path": c.Directories["inbox"].Path, "mode": "read"}}})
	got, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.PollIntervalSeconds != 5 || got.Log.MaxSizeMB != 20 || got.Log.MaxFiles != 5 || got.DataDir == "" {
		t.Errorf("defaults not filled: %+v", got)
	}
}

func TestConfig_RejectsRelativePathsAndBadNames(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	_ = os.WriteFile(file, nil, 0o600)
	cases := map[string]any{
		"relative path":  map[string]any{"directories": map[string]any{"in": map[string]any{"path": "relative/dir", "mode": "read"}}},
		"bad name":       map[string]any{"directories": map[string]any{"../x": map[string]any{"path": dir, "mode": "read"}}},
		"bad mode":       map[string]any{"directories": map[string]any{"in": map[string]any{"path": dir, "mode": "readwrite"}}},
		"missing folder": map[string]any{"directories": map[string]any{"in": map[string]any{"path": filepath.Join(dir, "nope"), "mode": "read"}}},
		"path is a file": map[string]any{"directories": map[string]any{"in": map[string]any{"path": file, "mode": "read"}}},
		"no folders":     map[string]any{"directories": map[string]any{}},
		"plain http":     map[string]any{"server_url": "http://vrsky.example", "directories": map[string]any{"in": map[string]any{"path": dir, "mode": "read"}}},
		"typo in a key":  `{"directorys": {}}`,
		"not a url":      map[string]any{"server_url": "vrsky", "directories": map[string]any{"in": map[string]any{"path": dir, "mode": "read"}}},
	}
	for name, v := range cases {
		if _, err := LoadConfig(writeConfig(t, v)); err == nil {
			t.Errorf("%s: loaded, want an error", name)
		}
	}
}

func TestCheckServerURL(t *testing.T) {
	for _, ok := range []string{"https://vrsky.example", "http://localhost:9330", "http://127.0.0.1:9330", "http://[::1]:9330"} {
		if err := CheckServerURL(ok, false); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	if err := CheckServerURL("http://100.77.75.49:9330", false); err == nil {
		t.Error("plain http to a non-loopback host allowed without opting in")
	}
	if err := CheckServerURL("http://100.77.75.49:9330", true); err != nil {
		t.Errorf("allow_insecure_http not honoured: %v", err)
	}
}

// VRSky must learn folder names and directions — never paths.
func TestConfig_AnnouncedCarriesNoPaths(t *testing.T) {
	c := testConfig(t)
	raw, _ := json.Marshal(c.Announced())
	for _, d := range c.Directories {
		if strings.Contains(string(raw), "SECRET-PATH") || strings.Contains(string(raw), filepath.Base(d.Path)) {
			t.Fatalf("announced folders leak a path: %s", raw)
		}
	}
	if len(c.Announced()) != 2 || c.Announced()[0].Name != "inbox" {
		t.Errorf("announced = %+v", c.Announced())
	}
}
