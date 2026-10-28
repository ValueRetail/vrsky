package managementapi

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
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

// getEnvName matches the names ui/src/config/env.ts reads, e.g.
// `getEnv('VITE_LOG_LEVEL', 'info')`.
var getEnvName = regexp.MustCompile(`getEnv\(\s*['"](VITE_[A-Z0-9_]+)['"]`)

// composeEnvKeys returns the variable names a compose service sets under
// `environment:`, in either the map form (`KEY: value`) or the list form
// (`- KEY=value`).
func composeEnvKeys(svc map[string]any) ([]string, error) {
	var keys []string
	switch env := svc["environment"].(type) {
	case nil:
	case map[string]any:
		for k := range env {
			keys = append(keys, k)
		}
	case []any:
		for _, e := range env {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("environment entry %v is not a string", e)
			}
			k, _, _ := strings.Cut(s, "=")
			keys = append(keys, k)
		}
	default:
		return nil, fmt.Errorf("environment has unexpected type %T", env)
	}
	return keys, nil
}

// checkUIComposeEnv returns one message per VITE_* variable a service in
// docker-compose.ui.yml sets that can't reach the UI. In `ui` (Vite's dev
// server) a VITE_* name works only if env.ts reads it. In `ui-prod` none
// works: the bundle is built in the image, so Vite has already fixed every
// VITE_* value and nginx reads no environment.
func checkUIComposeEnv(services map[string]map[string]any, read map[string]bool) []string {
	var bad []string
	for _, name := range []string{"ui", "ui-prod"} {
		keys, err := composeEnvKeys(services[name])
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		for _, k := range keys {
			if !strings.HasPrefix(k, "VITE_") {
				continue
			}
			switch {
			case name == "ui-prod":
				bad = append(bad, fmt.Sprintf("ui-prod sets %s, but the image's bundle was built with every VITE_* value already fixed; runtime env can't change it", k))
			case !read[k]:
				bad = append(bad, fmt.Sprintf("ui sets %s, which ui/src/config/env.ts never reads", k))
			}
		}
	}
	sort.Strings(bad)
	return bad
}

// TestCheckUIComposeEnv checks checkUIComposeEnv on small cases, so the guard
// below can't pass for the wrong reason.
func TestCheckUIComposeEnv(t *testing.T) {
	read := map[string]bool{"VITE_API_URL": true}
	cases := []struct {
		name string
		yaml string
		bad  int
	}{
		{"no environment", "ui: {}\nui-prod: {}", 0},
		{"dev sets a name env.ts reads", "ui:\n  environment:\n    VITE_API_URL: x", 0},
		{"dev sets it in list form", "ui:\n  environment:\n    - VITE_API_URL=x", 0},
		{"dev sets a non-VITE name", "ui:\n  environment:\n    NODE_ENV: development", 0},
		{"dev sets an unread name", "ui:\n  environment:\n    VITE_API_BASE_URL: x", 1},
		{"dev sets an unread name in list form", "ui:\n  environment:\n    - VITE_ENVIRONMENT=development", 1},
		{"prod sets a name env.ts reads", "ui-prod:\n  environment:\n    VITE_API_URL: x", 1},
		{"prod sets a non-VITE name", "ui-prod:\n  environment:\n    TZ: UTC", 0},
		{"both wrong", "ui:\n  environment:\n    VITE_ENVIRONMENT: d\nui-prod:\n  environment:\n    VITE_ENVIRONMENT: p", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var services map[string]map[string]any
			if err := yaml.Unmarshal([]byte(tc.yaml), &services); err != nil {
				t.Fatalf("bad test yaml: %v", err)
			}
			if got := checkUIComposeEnv(services, read); len(got) != tc.bad {
				t.Errorf("got %d problems %q; want %d", len(got), got, tc.bad)
			}
		})
	}
}

// TestComposeUIEnvIsRead guards issue #392: docker-compose.ui.yml set
// VITE_API_BASE_URL and VITE_ENVIRONMENT, names the UI never read, which made
// it look like the API URL could be configured there. The names come from
// ui/src/config/env.ts, so reading a new variable there is enough to allow it
// on the dev service.
func TestComposeUIEnvIsRead(t *testing.T) {
	root := filepath.Join("..", "..", "..")

	src, err := os.ReadFile(filepath.Join(root, "ui", "src", "config", "env.ts"))
	if err != nil {
		t.Fatalf("read ui/src/config/env.ts: %v", err)
	}
	read := map[string]bool{}
	for _, m := range getEnvName.FindAllStringSubmatch(string(src), -1) {
		read[m[1]] = true
	}
	if len(read) == 0 {
		t.Fatal("found no getEnv('VITE_…') calls in ui/src/config/env.ts; this guard needs them to know which names the UI reads")
	}

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
	for _, name := range []string{"ui", "ui-prod"} {
		if _, ok := compose.Services[name]; !ok {
			t.Fatalf("docker-compose.ui.yml has no services.%s", name)
		}
	}

	for _, msg := range checkUIComposeEnv(compose.Services, read) {
		t.Errorf("docker-compose.ui.yml: %s", msg)
	}
}

// apiProxyEnv is the variable ui/vite.config.ts reads for its /api proxy
// target. It has no VITE_ prefix on purpose: only the dev server reads it,
// and Vite would hand any VITE_* value to the browser bundle.
const apiProxyEnv = "API_PROXY_TARGET"

// apiService is the docker-compose.yml service the dev UI's proxy must reach.
const apiService = "management-api"

// viteReadsProxyEnv matches `process.env.API_PROXY_TARGET` in vite.config.ts.
var viteReadsProxyEnv = regexp.MustCompile(`process\.env\.` + apiProxyEnv + `\b`)

// composeFile is the part of a compose file the dev-proxy guard reads.
type composeFile struct {
	Services map[string]map[string]any `yaml:"services"`
	Networks map[string]map[string]any `yaml:"networks"`
}

// composeEnvValue returns the value a compose service sets for key under
// `environment:`, in map or list form.
func composeEnvValue(svc map[string]any, key string) (string, bool) {
	switch env := svc["environment"].(type) {
	case map[string]any:
		v, ok := env[key]
		if !ok {
			return "", false
		}
		if v == nil {
			return "", true
		}
		return fmt.Sprint(v), true
	case []any:
		for _, e := range env {
			s, _ := e.(string)
			if k, v, _ := strings.Cut(s, "="); k == key {
				return v, true
			}
		}
	}
	return "", false
}

// composeServiceNetworks returns the network keys a service joins, from the
// list form (`- net`) or the map form (`net: {aliases: …}`).
func composeServiceNetworks(svc map[string]any) []string {
	var nets []string
	switch n := svc["networks"].(type) {
	case []any:
		for _, e := range n {
			if s, ok := e.(string); ok {
				nets = append(nets, s)
			}
		}
	case map[string]any:
		for k := range n {
			nets = append(nets, k)
		}
	}
	sort.Strings(nets)
	return nets
}

// dockerNetworkName returns the Docker name of a network a compose file
// declares under key: its `name:` if set, else the key for an external
// network. A non-external network without `name:` gets a project prefix, so
// another project can't count on its name; that returns "".
func dockerNetworkName(key string, def map[string]any) string {
	if n, ok := def["name"].(string); ok && n != "" {
		return n
	}
	if ext, _ := def["external"].(bool); ext {
		return key
	}
	return ""
}

// checkDevUIProxy returns what stops the dev `ui` service in
// docker-compose.ui.yml from reaching the API through Vite's proxy: the
// target must be http://<apiService>:<its listen port>, and `ui` must join,
// as an external network, a network apiService is on in docker-compose.yml.
func checkDevUIProxy(uiFile, core composeFile) []string {
	var bad []string
	ui, ok := uiFile.Services["ui"]
	if !ok {
		return []string{"docker-compose.ui.yml has no services.ui"}
	}
	api, ok := core.Services[apiService]
	if !ok {
		return []string{"docker-compose.yml has no services." + apiService}
	}

	target, ok := composeEnvValue(ui, apiProxyEnv)
	if !ok {
		bad = append(bad, fmt.Sprintf("ui doesn't set %s, so Vite proxies /api to localhost:3000, which inside the container is the container itself", apiProxyEnv))
	} else {
		listen, _ := composeEnvValue(api, "MGMT_API_LISTEN")
		_, port, err := net.SplitHostPort(listen)
		if err != nil || port == "" {
			bad = append(bad, fmt.Sprintf("docker-compose.yml: %s sets no MGMT_API_LISTEN port to check %s against", apiService, apiProxyEnv))
		} else if want := "http://" + apiService + ":" + port; strings.TrimRight(target, "/") != want {
			bad = append(bad, fmt.Sprintf("ui sets %s=%q; want %q (%s's service name and MGMT_API_LISTEN port)", apiProxyEnv, target, want, apiService))
		}
	}

	apiNets := map[string]bool{}
	for _, k := range composeServiceNetworks(api) {
		if n := dockerNetworkName(k, core.Networks[k]); n != "" {
			apiNets[n] = true
		}
	}
	shared := false
	for _, k := range composeServiceNetworks(ui) {
		def := uiFile.Networks[k]
		if ext, _ := def["external"].(bool); ext && apiNets[dockerNetworkName(k, def)] {
			shared = true
		}
	}
	if !shared {
		bad = append(bad, fmt.Sprintf("ui joins no external network that %s is on in docker-compose.yml, so it can't resolve %s", apiService, apiService))
	}
	return bad
}

// TestCheckDevUIProxy checks checkDevUIProxy on small compose files, so the
// guard below can't pass for the wrong reason.
func TestCheckDevUIProxy(t *testing.T) {
	core := `
services:
  management-api:
    environment:
      MGMT_API_LISTEN: ":3000"
    networks:
      - vrsky-network
networks:
  vrsky-network:
    name: vrsky-network
`
	good := `
services:
  ui:
    environment:
      API_PROXY_TARGET: http://management-api:3000
    networks:
      - core
networks:
  core:
    external: true
    name: vrsky-network
`
	cases := []struct {
		name string
		ui   string
		core string
		bad  int
	}{
		{"service name, port and external network", good, core, 0},
		{"list-form env and map-form networks", `
services:
  ui:
    environment:
      - API_PROXY_TARGET=http://management-api:3000/
    networks:
      vrsky-network: {}
networks:
  vrsky-network:
    external: true
`, core, 0},
		{"no target", `
services:
  ui:
    networks: [core]
networks:
  core: {external: true, name: vrsky-network}
`, core, 1},
		{"localhost target", `
services:
  ui:
    environment: {API_PROXY_TARGET: "http://localhost:3000"}
    networks: [core]
networks:
  core: {external: true, name: vrsky-network}
`, core, 1},
		{"wrong port", `
services:
  ui:
    environment: {API_PROXY_TARGET: "http://management-api:8080"}
    networks: [core]
networks:
  core: {external: true, name: vrsky-network}
`, core, 1},
		{"container name instead of service name", `
services:
  ui:
    environment: {API_PROXY_TARGET: "http://vrsky-management-api:3000"}
    networks: [core]
networks:
  core: {external: true, name: vrsky-network}
`, core, 1},
		{"own bridge network", `
services:
  ui:
    environment: {API_PROXY_TARGET: "http://management-api:3000"}
    networks: [vrsky]
networks:
  vrsky: {driver: bridge}
`, core, 1},
		{"same key but not external", `
services:
  ui:
    environment: {API_PROXY_TARGET: "http://management-api:3000"}
    networks: [vrsky-network]
networks:
  vrsky-network: {driver: bridge}
`, core, 1},
		{"external network the API isn't on", `
services:
  ui:
    environment: {API_PROXY_TARGET: "http://management-api:3000"}
    networks: [other]
networks:
  other: {external: true, name: some-other-network}
`, core, 1},
		{"no networks at all", `
services:
  ui:
    environment: {API_PROXY_TARGET: "http://management-api:3000"}
`, core, 1},
		{"API listens elsewhere", good, `
services:
  management-api:
    environment: {MGMT_API_LISTEN: ":4000"}
    networks: [vrsky-network]
networks:
  vrsky-network: {name: vrsky-network}
`, 1},
		{"API network has a project-prefixed name", good, `
services:
  management-api:
    environment: {MGMT_API_LISTEN: ":3000"}
    networks: [vrsky-network]
networks:
  vrsky-network: {driver: bridge}
`, 1},
		{"no ui service", "services: {}", core, 1},
		{"no API service", good, "services: {}", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ui, c composeFile
			if err := yaml.Unmarshal([]byte(tc.ui), &ui); err != nil {
				t.Fatalf("bad ui yaml: %v", err)
			}
			if err := yaml.Unmarshal([]byte(tc.core), &c); err != nil {
				t.Fatalf("bad core yaml: %v", err)
			}
			if got := checkDevUIProxy(ui, c); len(got) != tc.bad {
				t.Errorf("got %d problems %q; want %d", len(got), got, tc.bad)
			}
		})
	}
}

// TestComposeUIDevReachesAPI guards issue #399: the dev `ui` service ran Vite
// with its /api proxy hardcoded to http://localhost:3000, which inside the
// container is the container itself, on its own bridge network, so every API
// call failed. vite.config.ts now reads the target from API_PROXY_TARGET, and
// the service must point it at the management API on the core stack's
// network.
func TestComposeUIDevReachesAPI(t *testing.T) {
	root := filepath.Join("..", "..", "..")

	vite, err := os.ReadFile(filepath.Join(root, "ui", "vite.config.ts"))
	if err != nil {
		t.Fatalf("read ui/vite.config.ts: %v", err)
	}
	if !viteReadsProxyEnv.Match(vite) {
		t.Errorf("ui/vite.config.ts doesn't read process.env.%s, so setting it in docker-compose.ui.yml changes nothing", apiProxyEnv)
	}

	read := func(name string) composeFile {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var f composeFile
		if err := yaml.Unmarshal(raw, &f); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return f
	}
	for _, msg := range checkDevUIProxy(read("docker-compose.ui.yml"), read("docker-compose.yml")) {
		t.Errorf("docker-compose.ui.yml: %s", msg)
	}
}
