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
// Docker's tmpfs defaults to mode 1777, so a bare `/var/run` tmpfs is already
// writable by uid 101. A uid= option is how someone pins ownership, though,
// and pinning it to anything but nginx's uid is a mistake even while the
// default mode happens to hide it, so a uid other than 101 fails. A mode= that
// drops the world-write bit is only fine when the tmpfs is owned by uid 101
// and keeps the owner-write bit.
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
	if mode != -1 && mode&0o002 == 0 && (uid != nginxUID || mode&0o200 == 0) {
		return fmt.Errorf("tmpfs mode %o does not let uid %d write", mode, nginxUID)
	}
	return nil
}

// writableMountFor looks through a compose service definition for a mount
// that makes dir writable and returns a short description of it. It accepts:
//
//   - `tmpfs:` as a string or a list, each entry `target[:options]`
//   - `volumes:` short syntax `[source:]target[:options]`, unless options has `ro`
//   - `volumes:` long syntax `{type, target, read_only}`, unless read_only is
//     true; a long-syntax tmpfs has its `tmpfs.mode` checked like the above
//
// A bind or named volume only proves the path is mounted read-write, not that
// uid 101 owns it; the fix uses tmpfs, which this checks properly.
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
			var target, opts string
			switch len(parts) {
			case 1:
				target = parts[0]
			case 2:
				target = parts[1]
			default:
				target, opts = parts[1], parts[2]
			}
			if !mountCovers(target, dir) {
				continue
			}
			for _, o := range strings.Split(opts, ",") {
				if o == "ro" {
					return "", fmt.Errorf("volume %q covers %s but is read-only", v, dir)
				}
			}
			return "volume " + v, nil
		case map[string]any:
			target, _ := v["target"].(string)
			if target == "" || !mountCovers(target, dir) {
				continue
			}
			if ro, _ := v["read_only"].(bool); ro {
				return "", fmt.Errorf("volume %v covers %s but is read_only", v, dir)
			}
			if v["type"] == "tmpfs" {
				if t, ok := v["tmpfs"].(map[string]any); ok {
					if m, ok := t["mode"].(int); ok {
						// yaml.v3 (which compose-go also uses) already reads
						// 0755 and 0o755 as octal, so m holds the mode bits.
						if err := checkTmpfsOptions("mode=" + strconv.FormatInt(int64(m), 8)); err != nil {
							return "", fmt.Errorf("volume %v covers %s but: %w", v, dir, err)
						}
					}
				}
			}
			return fmt.Sprintf("volume %v", v), nil
		}
	}
	return "", fmt.Errorf("no tmpfs or read-write volume covers %s", dir)
}

// TestComposeWritableMountMatcher checks writableMountFor on small compose
// snippets, so the guard below can't pass for the wrong reason.
func TestComposeWritableMountMatcher(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		ok   bool
	}{
		{"tmpfs string", `tmpfs: /var/run`, true},
		{"tmpfs list with options", "tmpfs:\n  - /tmp\n  - /var/run:uid=101,gid=101", true},
		{"tmpfs on a parent dir", `tmpfs: /var`, true},
		{"tmpfs with trailing slash", `tmpfs: /var/run/`, true},
		{"tmpfs owned by root", `tmpfs: /var/run:uid=0`, false},
		{"tmpfs not world-writable, owned by nginx", `tmpfs: /var/run:uid=101,mode=0755`, true},
		{"tmpfs not world-writable, owned by root", `tmpfs: /var/run:mode=0755`, false},
		{"tmpfs on a sibling with a shared prefix", `tmpfs: /var/ru`, false},
		{"tmpfs on an unrelated path", `tmpfs: /tmp`, false},
		{"rw bind volume", "volumes:\n  - ./run:/var/run", true},
		{"rw bind volume explicit", "volumes:\n  - ./run:/var/run:rw", true},
		{"ro bind volume", "volumes:\n  - ./run:/var/run:ro", false},
		{"anonymous volume", "volumes:\n  - /var/run", true},
		{"long volume", "volumes:\n  - type: volume\n    source: run\n    target: /var/run", true},
		{"long volume read_only", "volumes:\n  - type: bind\n    source: ./run\n    target: /var/run\n    read_only: true", false},
		{"long tmpfs", "volumes:\n  - type: tmpfs\n    target: /var/run", true},
		{"long tmpfs world-writable mode", "volumes:\n  - type: tmpfs\n    target: /var/run\n    tmpfs:\n      mode: 01777", true},
		{"long tmpfs restrictive mode", "volumes:\n  - type: tmpfs\n    target: /var/run\n    tmpfs:\n      mode: 0755", false},
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
// (infrastructure/kubernetes/ui/deployment.yaml); the compose service needs a
// writable mount at the same place. The pid path comes from ui/nginx.conf, so
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
