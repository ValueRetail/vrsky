package managementapi

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// nginxUID is the uid of the `nginx` user that ui/nginx.conf runs as
// (`user nginx;`) in the nginx:alpine based image built by ui/Dockerfile.
const nginxUID = 101

// pidDirective matches nginx's `pid <path>;` directive at the top level of
// nginx.conf. Comments are stripped before matching.
var pidDirective = regexp.MustCompile(`^\s*pid\s+([^\s;]+)\s*;`)

// nginxPidPath returns the path from the first `pid` directive in an nginx
// config, or "" if there is none.
func nginxPidPath(conf string) string {
	sc := bufio.NewScanner(strings.NewReader(conf))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if m := pidDirective.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// mountCovers reports whether a mount at target contains dir (dir is the
// target itself or sits below it).
func mountCovers(target, dir string) bool {
	target = path.Clean(target)
	dir = path.Clean(dir)
	return dir == target || target == "/" || strings.HasPrefix(dir, target+"/")
}

// checkTmpfsOptions decides whether a tmpfs mounted with these options
// (`uid=101,gid=101,mode=0755`, …) is writable by the nginx user.
//
// A bare tmpfs is not: in the nginx:alpine image /var/run is a symlink to
// /run, which is root-owned with mode 755, and Docker copies that owner and
// mode onto the tmpfs (checked on Docker Desktop for #377: `--tmpfs /var/run`
// gives `mode=755` and uid 101 gets Permission denied). So the tmpfs must
// either be owned by uid 101 with the owner-write bit, or set a mode with the
// world-write bit. A uid other than 101 fails even with a world-writable mode,
// since pinning ownership to anyone else is a mistake waiting for a mode change.
func checkTmpfsOptions(opts string) error {
	uid := -1
	mode := int64(-1)
	for _, o := range strings.Split(opts, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(o), "=")
		switch k {
		case "uid":
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("tmpfs uid=%q is not a number", v)
			}
			uid = n
		case "mode":
			n, err := strconv.ParseInt(v, 8, 32)
			if err != nil {
				return fmt.Errorf("tmpfs mode=%q is not an octal mode", v)
			}
			mode = n
		}
	}
	if uid != -1 && uid != nginxUID {
		return fmt.Errorf("tmpfs is owned by uid %d, but nginx runs as uid %d", uid, nginxUID)
	}
	if mode != -1 && mode&0o002 != 0 {
		return nil // world-writable
	}
	if uid != nginxUID {
		return fmt.Errorf("tmpfs sets neither uid=%d nor a world-writable mode, so it keeps the image's root-owned 755 /run and uid %d can't write it", nginxUID, nginxUID)
	}
	if mode != -1 && mode&0o200 == 0 {
		return fmt.Errorf("tmpfs mode %o does not let its owner (uid %d) write", mode, nginxUID)
	}
	return nil
}

// writableMountFor looks through a compose service definition for a mount
// that makes dir writable by uid 101 and returns a short description of it.
// It accepts:
//
//   - `tmpfs:` as a string or a list, each entry `target[:options]`, with
//     options checked by checkTmpfsOptions
//   - `volumes:` long syntax `{type: tmpfs, target, tmpfs: {mode}}` with a
//     world-writable mode (long syntax has no uid option)
//
// Any other volume covering dir fails: a named or anonymous volume copies the
// image's root-owned /run (checked for #377), and a bind mount's owner depends
// on the host.
func writableMountFor(svc map[string]any, dir string) (string, error) {
	var tmpfs []string
	switch v := svc["tmpfs"].(type) {
	case nil:
	case string:
		tmpfs = []string{v}
	case []any:
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return "", fmt.Errorf("tmpfs entry %v is not a string", e)
			}
			tmpfs = append(tmpfs, s)
		}
	default:
		return "", fmt.Errorf("tmpfs has unexpected type %T", v)
	}
	for _, e := range tmpfs {
		target, opts, _ := strings.Cut(e, ":")
		if !mountCovers(target, dir) {
			continue
		}
		if err := checkTmpfsOptions(opts); err != nil {
			return "", fmt.Errorf("tmpfs %q covers %s but: %w", e, dir, err)
		}
		return "tmpfs " + e, nil
	}

	vols, _ := svc["volumes"].([]any)
	for _, e := range vols {
		switch v := e.(type) {
		case string:
			parts := strings.Split(v, ":")
			target := parts[0]
			if len(parts) > 1 {
				target = parts[1]
			}
			if mountCovers(target, dir) {
				return "", fmt.Errorf("volume %q covers %s, but a named or anonymous volume copies the image's root-owned /run and a bind mount's owner depends on the host; use a tmpfs", v, dir)
			}
		case map[string]any:
			target, _ := v["target"].(string)
			if target == "" || !mountCovers(target, dir) {
				continue
			}
			if v["type"] != "tmpfs" {
				return "", fmt.Errorf("volume %v covers %s, but only a tmpfs gives uid %d a writable directory there", v, dir, nginxUID)
			}
			if ro, _ := v["read_only"].(bool); ro {
				return "", fmt.Errorf("volume %v covers %s but is read_only", v, dir)
			}
			// Long-syntax tmpfs has mode but no uid, so it needs a
			// world-writable mode. yaml.v3 (which compose-go also uses)
			// reads 0755 and 0o755 as octal, so m holds the mode bits.
			opts := ""
			if t, ok := v["tmpfs"].(map[string]any); ok {
				if m, ok := t["mode"].(int); ok {
					opts = "mode=" + strconv.FormatInt(int64(m), 8)
				}
			}
			if err := checkTmpfsOptions(opts); err != nil {
				return "", fmt.Errorf("volume %v covers %s but: %w", v, dir, err)
			}
			return fmt.Sprintf("volume %v", v), nil
		}
	}
	return "", fmt.Errorf("no tmpfs covers %s", dir)
}

// TestComposeWritableMountMatcher checks writableMountFor on small compose
// snippets, so the guard below can't pass for the wrong reason.
func TestComposeWritableMountMatcher(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		ok   bool
	}{
		{"tmpfs string owned by nginx", `tmpfs: /var/run:uid=101`, true},
		{"tmpfs list with options", "tmpfs:\n  - /tmp\n  - /var/run:uid=101,gid=101", true},
		{"tmpfs on a parent dir", `tmpfs: /var:uid=101`, true},
		{"tmpfs with trailing slash", `tmpfs: /var/run/:uid=101`, true},
		{"bare tmpfs keeps the image's root 755", `tmpfs: /var/run`, false},
		{"tmpfs with only gid", `tmpfs: /var/run:gid=101`, false},
		{"tmpfs owned by root", `tmpfs: /var/run:uid=0`, false},
		{"tmpfs owned by root, world-writable", `tmpfs: /var/run:uid=0,mode=1777`, false},
		{"tmpfs world-writable mode", `tmpfs: /var/run:mode=1777`, true},
		{"tmpfs owned by nginx, mode 0755", `tmpfs: /var/run:uid=101,mode=0755`, true},
		{"tmpfs owned by nginx, no owner write", `tmpfs: /var/run:uid=101,mode=0555`, false},
		{"tmpfs not world-writable, no uid", `tmpfs: /var/run:mode=0755`, false},
		{"tmpfs on a sibling with a shared prefix", `tmpfs: /var/ru:uid=101`, false},
		{"tmpfs on an unrelated path", `tmpfs: /tmp:uid=101`, false},
		{"rw bind volume", "volumes:\n  - ./run:/var/run", false},
		{"ro bind volume", "volumes:\n  - ./run:/var/run:ro", false},
		{"anonymous volume", "volumes:\n  - /var/run", false},
		{"long named volume", "volumes:\n  - type: volume\n    source: run\n    target: /var/run", false},
		{"long tmpfs without mode", "volumes:\n  - type: tmpfs\n    target: /var/run", false},
		{"long tmpfs world-writable mode", "volumes:\n  - type: tmpfs\n    target: /var/run\n    tmpfs:\n      mode: 01777", true},
		{"long tmpfs restrictive mode", "volumes:\n  - type: tmpfs\n    target: /var/run\n    tmpfs:\n      mode: 0755", false},
		{"long tmpfs read_only", "volumes:\n  - type: tmpfs\n    target: /var/run\n    read_only: true\n    tmpfs:\n      mode: 01777", false},
		{"unrelated volume", "volumes:\n  - ./ui:/app", false},
		{"nothing mounted", `image: nginx`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var svc map[string]any
			if err := yaml.Unmarshal([]byte(tc.yaml), &svc); err != nil {
				t.Fatalf("bad test yaml: %v", err)
			}
			got, err := writableMountFor(svc, "/var/run")
			if tc.ok && err != nil {
				t.Errorf("want a writable mount, got error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Errorf("want an error, got mount %q", got)
			}
		})
	}
}

// TestComposeNginxPidPath checks the pid directive parser.
func TestComposeNginxPidPath(t *testing.T) {
	cases := map[string]string{
		"user nginx;\npid /var/run/nginx.pid;\n":   "/var/run/nginx.pid",
		"  pid   /tmp/nginx.pid ;  # moved\n":      "/tmp/nginx.pid",
		"# pid /var/run/nginx.pid;\npid /run/x;\n": "/run/x",
		"user nginx;\nworker_processes auto;\n":    "",
		"pidfile /x;\n":                            "",
	}
	for conf, want := range cases {
		if got := nginxPidPath(conf); got != want {
			t.Errorf("nginxPidPath(%q) = %q; want %q", conf, got, want)
		}
	}
}

// TestComposeUIProdPidDirWritable guards issue #377: `docker compose -f
// docker-compose.ui.yml --profile prod up ui-prod` exited at once because the
// image runs nginx as uid 101 and nginx couldn't create its pid file in the
// image's root-owned /var/run. Production mounts an emptyDir there
// (infrastructure/kubernetes/ui/deployment.yaml) with fsGroup 101; the compose
// service needs a tmpfs there that uid 101 can write. The pid path comes from ui/nginx.conf, so
// moving the pid file moves what this checks.
//
// The files are part of this repo, so a missing one fails rather than skips.
func TestComposeUIProdPidDirWritable(t *testing.T) {
	root := filepath.Join("..", "..", "..")

	conf, err := os.ReadFile(filepath.Join(root, "ui", "nginx.conf"))
	if err != nil {
		t.Fatalf("read ui/nginx.conf: %v", err)
	}
	pid := nginxPidPath(string(conf))
	if pid == "" {
		t.Fatal("ui/nginx.conf has no `pid` directive; this guard needs one to know which directory must be writable")
	}
	if !path.IsAbs(pid) {
		t.Fatalf("ui/nginx.conf pid %q is not absolute; this guard only handles absolute paths", pid)
	}
	dir := path.Dir(pid)

	raw, err := os.ReadFile(filepath.Join(root, "docker-compose.ui.yml"))
	if err != nil {
		t.Fatalf("read docker-compose.ui.yml: %v", err)
	}
	var compose struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatalf("parse docker-compose.ui.yml: %v", err)
	}
	svc, ok := compose.Services["ui-prod"]
	if !ok {
		t.Fatal("docker-compose.ui.yml has no services.ui-prod")
	}

	if _, err := writableMountFor(svc, dir); err != nil {
		t.Errorf("ui-prod in docker-compose.ui.yml can't write nginx's pid file %s: %v.\n"+
			"nginx runs as uid %d and the image's %s is not writable by it, so the container exits at start (#377). "+
			"Mount a tmpfs there, e.g. `tmpfs: [\"%s:uid=%d,gid=%d\"]`, as production does with an emptyDir.",
			pid, err, nginxUID, dir, dir, nginxUID, nginxUID)
	}
}
