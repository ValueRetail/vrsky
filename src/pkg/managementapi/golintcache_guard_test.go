package managementapi

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Go lint keeps its own cache entry since #463: setup-go's shared one is
// saved by whichever Go job finishes first, usually a -race test shard whose
// build output golangci-lint cannot reuse, so lint rebuilt everything on
// every run. This checks the job restores modules, the Go build cache and
// golangci-lint's cache under a key of its own before linting, that the key
// moves with the Go version, go.sum and time (so the entry is saved again
// rather than frozen at its first save), and that nothing else competes for
// the entry or the cache directory.
func TestGoLintKeepsItsOwnBuildCache(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "build-push.yml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []ciStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse build-push.yml: %v", err)
	}
	job, ok := wf.Jobs["go-lint"]
	if !ok {
		t.Fatal("build-push.yml has no go-lint job")
	}
	steps := job.Steps
	find := func(match func(ciStep) bool) int {
		for i, s := range steps {
			if match(s) {
				return i
			}
		}
		return -1
	}
	setup := find(func(s ciStep) bool { return strings.HasPrefix(s.Uses, "actions/setup-go@") })
	restore := find(func(s ciStep) bool {
		return strings.HasPrefix(s.Uses, "actions/cache@") || strings.HasPrefix(s.Uses, "actions/cache/restore@")
	})
	lint := find(func(s ciStep) bool { return strings.HasPrefix(s.Uses, "golangci/golangci-lint-action@") })
	mods := find(func(s ciStep) bool { return s.Uses == "./.github/actions/go-modules" })
	if setup < 0 || restore < 0 || lint < 0 {
		t.Fatalf("go-lint needs actions/setup-go (step %d), an actions/cache step (step %d) and golangci-lint-action (step %d)", setup, restore, lint)
	}

	if v := steps[setup].with("cache"); v != "false" {
		t.Errorf("setup-go in go-lint has cache: %q, want false: its shared entry holds a -race shard's build output, which golangci-lint cannot reuse", v)
	}
	if v := steps[lint].with("skip-cache"); v != "true" {
		t.Errorf("golangci-lint-action has skip-cache: %q, want true: its own small cache would compete for ~/.cache/golangci-lint", v)
	}

	cache := steps[restore]
	if restore < setup {
		t.Errorf("the lint cache step (%d) comes before setup-go (%d), whose Go version its key needs", restore, setup)
	}
	if restore > lint {
		t.Errorf("the lint cache step (%d) comes after golangci-lint (%d); lint would start cold", restore, lint)
	}
	if mods >= 0 && restore > mods {
		t.Errorf("the lint cache step (%d) comes after the module download (%d), which then fetches every module again", restore, mods)
	}
	paths := map[string]bool{}
	for _, p := range strings.Split(cache.with("path"), "\n") {
		paths[strings.TrimSpace(p)] = true
	}
	for _, want := range []string{"~/.cache/go-build", "~/.cache/golangci-lint", "~/go/pkg/mod"} {
		if !paths[want] {
			t.Errorf("the lint cache does not hold %s (paths: %q)", want, cache.with("path"))
		}
	}

	// The key: this job's own, by Go version and go.sum, plus something that
	// changes over time.
	key := cache.with("key")
	if !strings.Contains(key, "lint") {
		t.Errorf("the lint cache key %q does not say lint; it would share an entry with other Go jobs", key)
	}
	exprIn := func(s, re string) bool {
		return regexp.MustCompile(`\$\{\{\s*` + re + `\s*\}\}`).MatchString(s)
	}
	if id := steps[setup].ID; id == "" || !exprIn(key, `steps\.`+regexp.QuoteMeta(id)+`\.outputs\.go-version`) {
		t.Errorf("the lint cache key %q does not include setup-go's go-version output (setup-go id %q); a Go upgrade would restore build output of the old one", key, id)
	}
	if !exprIn(key, `hashFiles\(\s*'src/go\.sum'\s*\)`) {
		t.Errorf("the lint cache key %q does not include hashFiles('src/go.sum')", key)
	}
	timeExpr := ""
	for _, m := range ghExpr.FindAllStringSubmatch(key, -1) {
		e := strings.TrimSpace(m[1])
		switch {
		case e == "github.run_id" || e == "github.run_number" || e == "github.run_attempt":
			timeExpr = m[0]
		case strings.HasPrefix(e, "steps."):
			parts := strings.Split(e, ".")
			if len(parts) != 4 || parts[2] != "outputs" {
				continue
			}
			for i, s := range steps {
				if s.ID != parts[1] || !strings.Contains(stripShellComments(s.Run), "date") {
					continue
				}
				if i > restore {
					t.Errorf("step %q, which the lint cache key reads, runs after the cache step", s.ID)
				}
				out := filepath.Join(t.TempDir(), "github_output")
				if _, stderr, err := runStepScript(t, s.Shell, s.Run, t.TempDir(), "GITHUB_OUTPUT="+out); err != nil {
					t.Fatalf("step %q: %v\n%s", s.ID, err, stderr)
				}
				got, _ := os.ReadFile(out) //nolint:gosec // a file in the test's temp dir
				for _, line := range strings.Split(string(got), "\n") {
					if v, ok := strings.CutPrefix(line, parts[3]+"="); ok && strings.TrimSpace(v) != "" {
						timeExpr = m[0]
					}
				}
				if timeExpr == "" {
					t.Errorf("step %q does not write a non-empty %s to GITHUB_OUTPUT (wrote %q); the key's time part would be empty and the entry frozen", s.ID, parts[3], got)
				}
			}
		}
	}
	if timeExpr == "" {
		t.Errorf("nothing in the lint cache key %q changes over time; actions/cache never saves over an existing key, so the entry would stay as first saved", key)
	}

	restoreKeys := []string{}
	for _, k := range strings.Split(cache.with("restore-keys"), "\n") {
		if k = strings.TrimSpace(k); k != "" {
			restoreKeys = append(restoreKeys, k)
		}
	}
	if len(restoreKeys) == 0 {
		t.Errorf("the lint cache has no restore-keys; each new key (every week, every go.sum change) would start cold")
	}
	fallsBack := false
	for _, k := range restoreKeys {
		if !strings.HasPrefix(key, k) || k == key {
			t.Errorf("restore key %q is not a shorter prefix of the key %q", k, key)
		}
		if timeExpr != "" && !strings.Contains(k, timeExpr) {
			fallsBack = true
		}
	}
	if len(restoreKeys) > 0 && timeExpr != "" && !fallsBack {
		t.Errorf("every restore key keeps the time part of the key; a new period would start cold")
	}

	// Nothing else uses a go-lint- entry, in any workflow or local action.
	literal, _, _ := strings.Cut(key, "${{")
	files, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if err != nil {
		t.Fatal(err)
	}
	actions, err := filepath.Glob(filepath.Join(root, ".github", "actions", "*", "action.y*ml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range append(files, actions...) {
		b, err := os.ReadFile(f) //nolint:gosec // a workflow or action file inside the repo
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Jobs map[string]struct {
				Steps []ciStep `yaml:"steps"`
			} `yaml:"jobs"`
			Runs struct {
				Steps []ciStep `yaml:"steps"`
			} `yaml:"runs"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		check := func(where string, ss []ciStep) {
			for _, s := range ss {
				if !strings.HasPrefix(s.Uses, "actions/cache") {
					continue
				}
				for _, k := range append([]string{s.with("key")}, strings.Split(s.with("restore-keys"), "\n")...) {
					k = strings.TrimSpace(k)
					if k == "" {
						continue
					}
					other, _, _ := strings.Cut(k, "${{")
					if strings.Contains(k, "go-lint-") || (other != "" && strings.HasPrefix(literal, other)) {
						t.Errorf("%s step %q uses cache key %q, which reaches Go lint's entry (%q)", where, s.Name, k, key)
					}
				}
			}
		}
		for id, j := range doc.Jobs {
			if filepath.Base(f) == "build-push.yml" && id == "go-lint" {
				continue
			}
			check(filepath.Base(f)+" job "+id, j.Steps)
		}
		check(f, doc.Runs.Steps)
	}
}
