package managementapi

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
