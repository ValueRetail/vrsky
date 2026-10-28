package managementapi

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// fileServices are the compose services that mount the host's files folder.
var fileServices = []string{"file-consumer", "file-producer"}

// filesHostDirVar is the .env variable that picks the host folder behind the
// file connectors' /data/input (#422).
const filesHostDirVar = "VRSKY_FILES_HOST_DIR"

// windowsDrive matches a path that starts with a drive letter, `C:` or `c:\`.
var windowsDrive = regexp.MustCompile(`^[A-Za-z]:`)

// composeMount is one entry of a service's `volumes:`, in either syntax.
type composeMount struct {
	Raw    string
	Source string
	Target string
}

// splitShortVolume splits a short-syntax volume, `source:target[:mode]` or a
// bare `target`. A `${VAR:-default}` in the source keeps its own colon, so the
// source runs to the first colon outside braces.
func splitShortVolume(spec string) (source, target string) {
	depth := 0
	for i, r := range spec {
		switch r {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth > 0 {
				continue
			}
			// A drive letter (`C:\x:/data`) is part of the source.
			if i == 1 && windowsDrive.MatchString(spec) {
				continue
			}
			rest := spec[i+1:]
			// The target is everything up to the next colon outside braces
			// (the mode, `:ro`, follows it).
			d := 0
			for j, r2 := range rest {
				switch r2 {
				case '{':
					d++
				case '}':
					if d > 0 {
						d--
					}
				case ':':
					if d == 0 {
						return spec[:i], rest[:j]
					}
				}
			}
			return spec[:i], rest
		}
	}
	return "", spec
}

// composeMounts returns a service's volumes in both syntaxes.
func composeMounts(svc map[string]any) ([]composeMount, error) {
	vols, ok := svc["volumes"].([]any)
	if !ok && svc["volumes"] != nil {
		return nil, fmt.Errorf("volumes has unexpected type %T", svc["volumes"])
	}
	var out []composeMount
	for _, e := range vols {
		switch v := e.(type) {
		case string:
			src, tgt := splitShortVolume(v)
			out = append(out, composeMount{Raw: v, Source: src, Target: tgt})
		case map[string]any:
			src, _ := v["source"].(string)
			tgt, _ := v["target"].(string)
			out = append(out, composeMount{Raw: fmt.Sprint(v), Source: src, Target: tgt})
		default:
			return nil, fmt.Errorf("volume entry %v has unexpected type %T", e, e)
		}
	}
	return out, nil
}

// checkLinuxContainerPath says why p can't be a path inside the Linux
// container on every host, or returns nil. A path that comes from a host
// variable fails: on Windows that value is `C:\Users\you` (Git Bash hands
// docker.exe its HOME converted to that form too), which the Linux engine
// rejects as a mount target.
func checkLinuxContainerPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("is empty")
	case strings.Contains(p, "$"):
		return fmt.Errorf("%q is read from a variable, so a Windows host turns it into a Windows path; use a fixed Linux path", p)
	case windowsDrive.MatchString(p) || strings.Contains(p, `\`):
		return fmt.Errorf("%q is a Windows path", p)
	case !path.IsAbs(p):
		return fmt.Errorf("%q is not an absolute Linux path", p)
	}
	return nil
}

// TestComposeFilesMountHelpers checks the parsers on small inputs, so the
// guard below can't pass for the wrong reason.
func TestComposeFilesMountHelpers(t *testing.T) {
	split := []struct {
		spec, source, target string
	}{
		{"./data/input:/data/input", "./data/input", "/data/input"},
		{"./data/output:/data/output:ro", "./data/output", "/data/output"},
		{"${HOME:-./data/input}:${HOME:-/data/input}", "${HOME:-./data/input}", "${HOME:-/data/input}"},
		{"${VRSKY_FILES_HOST_DIR:-./data/input}:/data/input", "${VRSKY_FILES_HOST_DIR:-./data/input}", "/data/input"},
		{`C:\Users\x\files:/data/input`, `C:\Users\x\files`, "/data/input"},
		{"C:/Users/x/files:/data/input:rw", "C:/Users/x/files", "/data/input"},
		{"/c/Users/x/files:/data/input", "/c/Users/x/files", "/data/input"},
		{"named-volume:/data", "named-volume", "/data"},
		{"/anonymous", "", "/anonymous"},
	}
	for _, tc := range split {
		src, tgt := splitShortVolume(tc.spec)
		if src != tc.source || tgt != tc.target {
			t.Errorf("splitShortVolume(%q) = (%q, %q); want (%q, %q)", tc.spec, src, tgt, tc.source, tc.target)
		}
	}

	paths := map[string]bool{
		"/data/input":            true,
		"/data/output":           true,
		"${HOME:-/data/input}":   false,
		"${HOME}":                false,
		"$HOME/files":            false,
		`C:\Users\x`:             false,
		"C:/Users/x":             false,
		"data/input":             false,
		"":                       false,
		"/data/${TENANT}/input":  false,
		`/data\input`:            false,
		"/c/Users/x/files/input": true, // a fixed Linux path, if an odd one
	}
	for p, ok := range paths {
		if err := checkLinuxContainerPath(p); (err == nil) != ok {
			t.Errorf("checkLinuxContainerPath(%q) = %v; want ok=%v", p, err, ok)
		}
	}

	var svc map[string]any
	if err := yaml.Unmarshal([]byte(`
volumes:
  - "${X:-./a}:/data/input"
  - type: bind
    source: ./b
    target: /data/output
`), &svc); err != nil {
		t.Fatal(err)
	}
	mounts, err := composeMounts(svc)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 2 || mounts[0].Target != "/data/input" || mounts[1].Source != "./b" || mounts[1].Target != "/data/output" {
		t.Errorf("composeMounts = %+v", mounts)
	}
}

// TestComposeFilesMountTargetIsFixed: file-consumer and file-producer used to
// mount `${HOME:-./data/input}` at `${HOME:-/data/input}`, the same path on
// both sides. On Windows HOME is `C:\Users\you`, which the Linux engine
// rejects as a target ("mount path must be absolute"), so the file
// connectors couldn't start (#422). The host folder is now a source only,
// mounted at a fixed Linux path, and HOST_HOME (what "~" in a node path
// expands to) names that fixed path.
func TestComposeFilesMountTargetIsFixed(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	var compose struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}

	for _, name := range fileServices {
		svc, ok := compose.Services[name]
		if !ok {
			t.Errorf("docker-compose.yml has no services.%s; update fileServices if it was renamed", name)
			continue
		}
		mounts, err := composeMounts(svc)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}

		// Every mount target is a fixed Linux path.
		var hostDirTarget string
		for _, m := range mounts {
			if err := checkLinuxContainerPath(m.Target); err != nil {
				t.Errorf("%s mounts %s: target %v (#422)", name, m.Raw, err)
			}
			if strings.Contains(m.Source, filesHostDirVar) {
				hostDirTarget = m.Target
			}
		}
		if hostDirTarget == "" {
			t.Errorf("%s doesn't mount ${%s}; .env.example and docs/operator/windows.md tell people to set it", name, filesHostDirVar)
			continue
		}

		// "~" expands to HOST_HOME inside the container, so it must name the
		// folder that is actually mounted there.
		home, ok := composeEnvValue(svc, "HOST_HOME")
		if !ok {
			t.Errorf("%s sets no HOST_HOME; it must name the mounted host folder (the source's \"~\", the destination's owner lookup)", name)
			continue
		}
		if err := checkLinuxContainerPath(home); err != nil {
			t.Errorf("%s HOST_HOME %v (#422)", name, err)
		} else if path.Clean(home) != path.Clean(hostDirTarget) {
			t.Errorf("%s HOST_HOME is %q, but ${%s} is mounted at %q; \"~\" in a node path would not reach the host folder",
				name, home, filesHostDirVar, hostDirTarget)
		}

		// The file source only reads under its base dir (pkg/tenantpath), so
		// the host folder has to be that base dir or nothing in it is reachable.
		if base, ok := composeEnvValue(svc, "FILE_CONSUMER_BASE_DIR"); ok && path.Clean(base) != path.Clean(hostDirTarget) {
			t.Errorf("%s reads files under FILE_CONSUMER_BASE_DIR=%q, but ${%s} is mounted at %q, where no pipeline can reach it",
				name, base, filesHostDirVar, hostDirTarget)
		}
	}
}
