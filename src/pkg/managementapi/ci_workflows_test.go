package managementapi

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The CI failure alert must watch every workflow that can go red on main.
//
// .github/workflows/ci-failure-alert.yml subscribes by workflow NAME. Two ways
// that silently stops working: rename a workflow's `name:` and the trigger no
// longer matches it, or add a new workflow that runs on main and forget to list
// it. Neither fails anything. The alert keeps running, keeps reporting success,
// and simply never fires for that workflow again — which is worse than having no
// alert, because the quiet now means "fine" instead of "nobody is watching".
//
// The whole point of the alert is that a red main is otherwise invisible (two
// pushes on 2026-09-09 published no images and went unnoticed), so an alert that
// can lose its subscription without telling anyone defeats it.
//
// pull_request-only workflows are excluded: those failures are already visible
// where someone is looking, on the PR itself.
func TestCIFailureAlertWatchesEveryMainWorkflow(t *testing.T) {
	dir := filepath.Join("..", "..", "..", ".github", "workflows")
	const alertFile = "ci-failure-alert.yml"

	alert, err := os.ReadFile(filepath.Join(dir, alertFile)) //nolint:gosec // fixed repo path
	if err != nil {
		t.Skipf("no CI failure alert (%v) — subscription guard skipped", err)
	}
	watched := map[string]bool{}
	for _, n := range workflowTriggers(t, alert).WorkflowRun.Workflows {
		watched[n] = true
	}
	if len(watched) == 0 {
		t.Fatal("ci-failure-alert.yml watches no workflows — it can never fire")
	}

	paths, err := filepath.Glob(filepath.Join(dir, "*.yml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflows found in %s (%v)", dir, err)
	}

	var unwatched []string
	for _, p := range paths {
		if filepath.Base(p) == alertFile {
			continue // it cannot subscribe to itself
		}
		body, err := os.ReadFile(p) //nolint:gosec // paths come from Glob over the repo
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		var doc struct {
			Name string `yaml:"name"`
		}
		if err := yaml.Unmarshal(body, &doc); err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		if doc.Name == "" {
			t.Errorf("%s has no name: — workflow_run cannot subscribe to it", filepath.Base(p))
			continue
		}
		// Only what can actually run on main. A push trigger reaches main on
		// merge; a schedule always runs on the default branch.
		on := workflowTriggers(t, body)
		if !on.Push && !on.Schedule {
			continue
		}
		if !watched[doc.Name] {
			unwatched = append(unwatched, fmt.Sprintf("%s (%q)", filepath.Base(p), doc.Name))
		}
	}

	if len(unwatched) > 0 {
		sort.Strings(unwatched)
		t.Errorf("these workflows can fail on main but the CI failure alert does not watch them:\n  %s\n\n"+
			"Add the exact name: string to the workflows list in .github/workflows/%s. Until then a "+
			"failure there opens no issue and nothing anywhere turns red.",
			strings.Join(unwatched, "\n  "), alertFile)
	}
}

// triggers is the part of a workflow's `on:` block this suite reasons about.
type triggers struct {
	Push        bool
	Schedule    bool
	WorkflowRun struct {
		Workflows []string
	}
}

// workflowTriggers reads a workflow's `on:` block.
//
// The key needs care: YAML 1.1 resolves a bare `on` to the boolean true, which
// is what GitHub's own workflow files rely on, so it has to be looked up both
// ways rather than as the string "on".
func workflowTriggers(t *testing.T, body []byte) triggers {
	t.Helper()
	var doc map[interface{}]interface{}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	var on interface{}
	for _, k := range []interface{}{"on", true} {
		if v, ok := doc[k]; ok {
			on = v
			break
		}
	}
	block, ok := on.(map[string]interface{})
	if !ok {
		if m, isMap := on.(map[interface{}]interface{}); isMap {
			block = map[string]interface{}{}
			for k, v := range m {
				if ks, isStr := k.(string); isStr {
					block[ks] = v
				}
			}
		} else {
			t.Fatalf("workflow has no usable on: block (got %T)", on)
		}
	}

	var out triggers
	_, out.Push = block["push"]
	_, out.Schedule = block["schedule"]
	if wr, ok := block["workflow_run"].(map[string]interface{}); ok {
		if list, ok := wr["workflows"].([]interface{}); ok {
			for _, w := range list {
				if s, ok := w.(string); ok {
					out.WorkflowRun.Workflows = append(out.WorkflowRun.Workflows, s)
				}
			}
		}
	}
	return out
}

// Every Dockerfile and CI workflow must build with a Go at least as new as
// go.mod requires.
//
// This exists because of a specific mistake. Bumping the toolchain to 1.26, I
// replaced the literal string "golang:1.22" everywhere, grepped for it, found
// none left, and called it done. Five Dockerfiles were on golang:1.24 and had
// never contained "1.22" — so they kept an older Go against a go.mod now
// requiring 1.26, and `go mod download` refused outright.
//
// The verification was the flaw, not the edit: grepping for the OLD value
// proves it is gone, not that the NEW one is present everywhere. This asserts
// the property instead of the absence of a string.
//
// A newer Go than go.mod requires is fine — Go is forward compatible. An older
// one is a hard build failure, and it fails in the image build, which is the
// slowest place to find out.
func TestBuildToolchainsSatisfyGoMod(t *testing.T) {
	root := filepath.Join("..", "..", "..")

	gomod, err := os.ReadFile(filepath.Join(root, "src", "go.mod"))
	if err != nil {
		t.Skipf("go.mod not readable (%v)", err)
	}
	m := regexp.MustCompile(`(?m)^go (\d+)\.(\d+)`).FindStringSubmatch(string(gomod))
	if m == nil {
		t.Fatal("no `go X.Y` directive in go.mod")
	}
	wantMajor, wantMinor := atoi(t, m[1]), atoi(t, m[2])
	want := fmt.Sprintf("%d.%d", wantMajor, wantMinor)

	// (file, version) pairs found across the repo.
	type ref struct {
		where   string
		version string
		major   int
		minor   int
	}
	var refs []ref

	add := func(where, v string) {
		p := strings.SplitN(v, ".", 3)
		if len(p) < 2 {
			t.Errorf("%s: unparseable Go version %q", where, v)
			return
		}
		refs = append(refs, ref{where, v, atoi(t, p[0]), atoi(t, p[1])})
	}

	dockerfiles, _ := filepath.Glob(filepath.Join(root, "src", "cmd", "*", "Dockerfile"))
	fromGolang := regexp.MustCompile(`FROM golang:(\d+\.\d+)`)
	for _, p := range dockerfiles {
		body, err := os.ReadFile(p) //nolint:gosec // paths come from Glob over the repo
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		for _, mm := range fromGolang.FindAllStringSubmatch(string(body), -1) {
			add(filepath.Join("cmd", filepath.Base(filepath.Dir(p)), "Dockerfile"), mm[1])
		}
	}

	workflows, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	setupGo := regexp.MustCompile(`go-version:\s*'?(\d+\.\d+)'?`)
	inContainer := regexp.MustCompile(`golang:(\d+\.\d+)`)
	for _, p := range workflows {
		body, err := os.ReadFile(p) //nolint:gosec // paths come from Glob over the repo
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		for _, re := range []*regexp.Regexp{setupGo, inContainer} {
			for _, mm := range re.FindAllStringSubmatch(string(body), -1) {
				add(filepath.Base(p), mm[1])
			}
		}
	}

	if len(refs) == 0 {
		t.Fatal("found no Go toolchain references — the Dockerfile/workflow format changed; update this test")
	}

	for _, r := range refs {
		if r.major < wantMajor || (r.major == wantMajor && r.minor < wantMinor) {
			t.Errorf("%s builds with Go %s but go.mod requires %s — `go mod download` refuses outright, "+
				"and it fails inside the image build, which is the slowest place to find out",
				r.where, r.version, want)
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("not a number: %q", s)
	}
	return n
}
