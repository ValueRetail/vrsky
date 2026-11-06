package managementapi

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The Docker Hub mirror guard beyond Dockerfiles (#720, from the review of #694).
//
// TestDockerfilesPullBaseImagesFromTheMirror covers FROM lines and the buildx
// driver. Images also come in through workflow `services:`, `container:` and
// `docker://` steps, `docker run`/`docker pull` in a run script, and the compose
// services a workflow starts. Any of those can drift back to a bare
// `postgres:16` and fail on Docker Hub's pull limit on the day it matters.

var (
	dockerCmdRe  = regexp.MustCompile(`\bdocker\s+(?:(?:container|image)\s+)?(run|pull|create)\b`)
	composeCmdRe = regexp.MustCompile(`\bdocker(?:\s+compose|-compose)\b`)
)

// shellFields splits one command line the way a shell would for our purposes:
// whitespace separates, single and double quotes group, a backslash-newline is
// a continuation. It stops at the end of the command (newline, ;, &&, ||, |).
func shellFields(s string) []string {
	s = strings.ReplaceAll(s, "\\\n", " ")
	var out []string
	var cur strings.Builder
	in := false
	var quote rune
	flush := func() {
		if in {
			out = append(out, cur.String())
			cur.Reset()
			in = false
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == '\n' || r == ';' || r == '|' || r == '&' || r == ')':
			flush()
			return out
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	flush()
	return out
}

// dockerRunValueFlags take their value as the next word. A flag not listed here
// is read as boolean; if that is wrong the guard reports the value as an image,
// which is loud rather than silent.
var dockerRunValueFlags = map[string]bool{
	"-v": true, "--volume": true, "-e": true, "--env": true, "--env-file": true,
	"--network": true, "--net": true, "--name": true, "-p": true, "--publish": true,
	"--user": true, "-u": true, "--entrypoint": true, "--tmpfs": true, "-w": true,
	"--workdir": true, "--platform": true, "--mount": true, "-l": true, "--label": true,
	"--add-host": true, "--cap-add": true, "--cap-drop": true, "--memory": true, "-m": true,
	"--cpus": true, "--restart": true, "--pull": true, "-h": true, "--hostname": true,
	"--health-cmd": true, "--health-interval": true,
}

// scriptCommandImages returns the image of every `docker run|pull|create` in a
// shell script, with "" standing for a command whose image could not be found.
func scriptCommandImages(script string) []string {
	script = commentFree(script)
	flat := strings.ReplaceAll(script, "\\\n", " ")
	var out []string
	for _, loc := range dockerCmdRe.FindAllStringSubmatchIndex(flat, -1) {
		verb := flat[loc[2]:loc[3]]
		words := shellFields(flat[loc[1]:])
		image := ""
		for i := 0; i < len(words); i++ {
			w := words[i]
			if !strings.HasPrefix(w, "-") {
				image = w
				break
			}
			if verb != "pull" && dockerRunValueFlags[w] {
				i++
			}
		}
		out = append(out, image)
	}
	return out
}

// composeValueFlags take their value as the next word, before the verb
// (global options) or after it (run).
var composeValueFlags = map[string]bool{
	"-f": true, "--file": true, "-p": true, "--project-name": true, "--profile": true,
	"--env-file": true, "--project-directory": true, "--ansi": true, "--parallel": true,
	"--progress": true, "-e": true, "--env": true, "-v": true, "--volume": true,
	"-u": true, "--user": true, "-w": true, "--workdir": true, "--entrypoint": true,
	"--name": true, "--pull": true,
}

// composeStarting verbs start or create containers, so the images of the named
// services matter. composeOther verbs never pull an image.
var (
	composeStarting = map[string]bool{"up": true, "pull": true, "run": true, "create": true, "start": true}
	composeOther    = map[string]bool{
		"build": true, "logs": true, "down": true, "ps": true, "config": true, "stop": true,
		"restart": true, "exec": true, "kill": true, "rm": true, "images": true, "ls": true,
		"top": true, "cp": true, "port": true, "pause": true, "unpause": true, "version": true,
		"events": true, "wait": true, "push": true, "watch": true,
	}
)

// scriptComposeServices returns the service names named on each
// `docker compose` / `docker-compose` command that starts containers, with "*"
// for one that names none (all services) and "?" for an invocation whose verb
// it cannot classify (the guard fails on those rather than skip them).
func scriptComposeServices(script string) []string {
	flat := strings.ReplaceAll(commentFree(script), "\\\n", " ")
	var out []string
	for _, loc := range composeCmdRe.FindAllStringIndex(flat, -1) {
		words := shellFields(flat[loc[1]:])
		i := 0
		for ; i < len(words); i++ {
			w := words[i]
			if !strings.HasPrefix(w, "-") {
				break
			}
			if composeValueFlags[w] {
				i++
			}
		}
		if i >= len(words) {
			out = append(out, "?")
			continue
		}
		verb := words[i]
		if composeOther[verb] {
			continue
		}
		if !composeStarting[verb] {
			out = append(out, "?")
			continue
		}
		named := false
		for i++; i < len(words); i++ {
			w := words[i]
			if strings.HasPrefix(w, "-") {
				if composeValueFlags[w] {
					i++
				}
				continue
			}
			out = append(out, w)
			named = true
			if verb == "run" {
				break // the rest is the command to run
			}
		}
		if !named {
			out = append(out, "*")
		}
	}
	return out
}

// commentFree drops whole-line shell comments, which may mention docker run.
func commentFree(script string) string {
	lines := strings.Split(script, "\n")
	keep := lines[:0]
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

func TestScriptCommandImages(t *testing.T) {
	cases := []struct {
		name, script string
		want         []string
	}{
		{"plain", "docker run --rm busybox sh", []string{"busybox"}},
		{"value flags", "docker run --rm --network vrsky-network -v \"$PWD/x\":/x:ro -e A=b mirror.gcr.io/library/golang:1.26 \\\n  go test", []string{"mirror.gcr.io/library/golang:1.26"}},
		{"entrypoint before image", "docker run --rm --entrypoint promtool mirror.gcr.io/prom/prometheus:v3.5.0 check rules", []string{"mirror.gcr.io/prom/prometheus:v3.5.0"}},
		{"quoted value with spaces", "docker run -e \"A=b c\" quay.io/x/y:1 cmd", []string{"quay.io/x/y:1"}},
		{"container run", "docker container run --rm alpine sh", []string{"alpine"}},
		{"image pull", "docker image pull -q grafana/k6", []string{"grafana/k6"}},
		{"pull", "retry docker pull grafana/k6:latest", []string{"grafana/k6:latest"}},
		{"in an if", "if docker run --rm --network n mirror.gcr.io/library/busybox \\\n sh -c \"nc\"; then", []string{"mirror.gcr.io/library/busybox"}},
		{"two commands", "docker run -d --name a -p 1:1 mcr.microsoft.com/azure-storage/azurite:3.34.0 x\ndocker run --rm vrsky-cli:drill", []string{"mcr.microsoft.com/azure-storage/azurite:3.34.0", "vrsky-cli:drill"}},
		{"comment is ignored", "# docker run --rm alpine\necho hi", nil},
		{"variable image", "docker run --rm \"$IMG\" sh", []string{"$IMG"}},
		{"no docker", "go test ./...", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scriptCommandImages(tc.script)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("scriptCommandImages(%q) = %q, want %q", tc.script, got, tc.want)
			}
		})
	}
}

func TestScriptComposeServices(t *testing.T) {
	cases := []struct {
		name, script string
		want         []string
	}{
		{"up with flags", "docker compose up -d --no-build \\\n  minio-test minio-init \\\n  kafka-test", []string{"minio-test", "minio-init", "kafka-test"}},
		{"pull", "retry docker compose pull --quiet nats httpbin", []string{"nats", "httpbin"}},
		{"stops at the end of the command", "docker compose up -d nats && echo done", []string{"nats"}},
		{"all services", "docker compose up -d --build", []string{"*"}},
		{"logs are not started", "docker compose logs --tail=100 management-api", nil},
		{"hyphenated binary", "docker-compose up -d nats", []string{"nats"}},
		{"global -f before the verb", "docker compose -f x.yml up -d nats", []string{"nats"}},
		{"global options before the verb", "docker compose --profile p -p proj --project-name q --env-file e.env pull nats", []string{"nats"}},
		{"run takes one service", "docker compose run --rm -e A=b mock-sap sh -c x", []string{"mock-sap"}},
		{"unknown verb", "docker compose frobnicate nats", []string{"?"}},
		{"no verb", "docker compose --ansi never", []string{"?"}},
		{"build is not a pull", "docker compose build management-api", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scriptComposeServices(tc.script)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("scriptComposeServices(%q) = %q, want %q", tc.script, got, tc.want)
			}
		})
	}
}

// workflowImageRefs collects every image reference a workflow or composite
// action names directly, each as "where: reference", and every run script.
func workflowImageRefs(t *testing.T, body []byte) (refs map[string]string, scripts []string) {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	refs = map[string]string{}
	steps := func(v any) {
		list, _ := v.([]any)
		for _, s := range list {
			st, _ := s.(map[string]any)
			if run, ok := st["run"].(string); ok {
				scripts = append(scripts, run)
			}
			if uses, ok := st["uses"].(string); ok && strings.HasPrefix(uses, "docker://") {
				refs["uses "+uses] = strings.TrimPrefix(uses, "docker://")
			}
		}
	}
	if jobs, ok := doc["jobs"].(map[string]any); ok {
		for name, j := range jobs {
			job, _ := j.(map[string]any)
			steps(job["steps"])
			switch c := job["container"].(type) {
			case string:
				refs[name+" container"] = c
			case map[string]any:
				if img, ok := c["image"].(string); ok {
					refs[name+" container"] = img
				}
			}
			svcs, _ := job["services"].(map[string]any)
			for sn, s := range svcs {
				if sm, ok := s.(map[string]any); ok {
					if img, ok := sm["image"].(string); ok {
						refs[name+" services."+sn] = img
					}
				}
			}
		}
	}
	if runs, ok := doc["runs"].(map[string]any); ok {
		steps(runs["steps"])
	}
	return refs, scripts
}

// composeServices reads docker-compose.yml: each service's image (empty for a
// built one) and its depends_on, so starting a service also checks what it pulls in.
func composeServices(t *testing.T, root string) (image map[string]string, deps map[string][]string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "docker-compose.yml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	var doc struct {
		Services map[string]struct {
			Image     string `yaml:"image"`
			DependsOn any    `yaml:"depends_on"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}
	image, deps = map[string]string{}, map[string][]string{}
	for name, s := range doc.Services {
		image[name] = s.Image
		switch d := s.DependsOn.(type) {
		case []any:
			for _, x := range d {
				deps[name] = append(deps[name], fmt.Sprint(x))
			}
		case map[string]any:
			for k := range d {
				deps[name] = append(deps[name], k)
			}
		}
	}
	return image, deps
}

// Every image a workflow pulls, and every compose service a workflow starts,
// comes from an allowlisted registry (allowedImageRegistries). Exceptions are
// explicit: localCIImages for what the job builds itself.
func TestWorkflowAndComposeImagesPullFromTheMirror(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	files, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatalf("glob workflows: %v", err)
	}
	actions, err := filepath.Glob(filepath.Join(root, ".github", "actions", "*", "action.yml"))
	if err != nil {
		t.Fatalf("glob actions: %v", err)
	}
	files = append(files, actions...)
	if len(files) < 5 {
		t.Fatalf("found %d workflow files, expected the ones under .github; is the glob broken?", len(files))
	}
	composeImage, composeDeps := composeServices(t, root)

	var bad []string
	checkedRuns, checkedServices, checkedCompose := 0, 0, 0
	for _, f := range files {
		body, err := os.ReadFile(f) //nolint:gosec // paths come from a glob over the repo
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		rel, err := filepath.Rel(root, f)
		if err != nil {
			t.Fatalf("relative path of %s: %v", f, err)
		}
		refs, scripts := workflowImageRefs(t, body)
		for where, img := range refs {
			checkedServices++
			if !imageAllowed(img) {
				bad = append(bad, fmt.Sprintf("%s: %s uses %q", rel, where, img))
			}
		}
		started := map[string]bool{}
		for _, sc := range scripts {
			for _, img := range scriptCommandImages(sc) {
				checkedRuns++
				if img == "" || !imageAllowed(img) {
					bad = append(bad, fmt.Sprintf("%s: docker command uses %q", rel, img))
				}
			}
			for _, svc := range scriptComposeServices(sc) {
				if svc == "*" {
					for n := range composeImage {
						started[n] = true
					}
					continue
				}
				if svc == "?" {
					bad = append(bad, rel+": a docker compose command the guard cannot classify")
					continue
				}
				if _, ok := composeImage[svc]; !ok {
					bad = append(bad, fmt.Sprintf("%s: compose service %q is not in docker-compose.yml", rel, svc))
					continue
				}
				started[svc] = true
			}
		}
		// Starting a service starts what it depends on.
		for changed := true; changed; {
			changed = false
			for n := range started {
				for _, d := range composeDeps[n] {
					if !started[d] {
						started[d], changed = true, true
					}
				}
			}
		}
		names := make([]string, 0, len(started))
		for n := range started {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			img := composeImage[n]
			if img == "" {
				continue // built from a Dockerfile, which has its own guard
			}
			checkedCompose++
			if !imageAllowed(img) {
				bad = append(bad, fmt.Sprintf("%s: starts compose service %q with image %q", rel, n, img))
			}
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("these workflow images do not come from an allowlisted registry %v:\n  %s\n\n"+
			"Docker Hub rate-limits pulls even with a login (#692, #720). Use the same image and tag through "+
			"the mirror, e.g. mirror.gcr.io/library/postgres:16-alpine, or add the registry to "+
			"allowedImageRegistries with a reason.", allowedImageRegistries, strings.Join(bad, "\n  "))
	}
	// The guard must not pass because it found nothing.
	if checkedRuns < 5 || checkedServices < 2 || checkedCompose < 5 {
		t.Errorf("checked %d docker commands, %d service/container images, %d compose images; the parsers look broken",
			checkedRuns, checkedServices, checkedCompose)
	}
}
