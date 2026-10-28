package managementapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The lint checks in build-push.yml are spread over more than one job since
// #446 (plans/issue-446-faster-go-lint.md): promtool runs in its own job
// beside Go lint. Moving a step between jobs is exactly when one gets lost, or
// lands in a job nothing waits for. This checks that every check still runs on
// pushes and pull requests, cannot be ignored (no continue-on-error), and sits
// in a job that both the "Go Tests" aggregator and the rollout plan need.
// Steps are matched on their commands with shell comments removed (#462), so
// a commented-out command does not count as a check that runs.
func TestEveryLintCheckStillGates(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "build-push.yml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	type step struct {
		Name            string            `yaml:"name"`
		Uses            string            `yaml:"uses"`
		Run             string            `yaml:"run"`
		If              string            `yaml:"if"`
		With            map[string]string `yaml:"with"`
		Env             map[string]string `yaml:"env"`
		ContinueOnError any               `yaml:"continue-on-error"`
	}
	type job struct {
		Name            string `yaml:"name"`
		Needs           any    `yaml:"needs"`
		If              string `yaml:"if"`
		ContinueOnError any    `yaml:"continue-on-error"`
		Steps           []step `yaml:"steps"`
	}
	var wf struct {
		Jobs map[string]job `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse build-push.yml: %v", err)
	}

	checks := []struct {
		name  string
		match func(step) bool
	}{
		{"gofmt", func(s step) bool { return strings.Contains(s.Run, "gofmt -l .") && strings.Contains(s.Run, "exit 1") }},
		{"golangci-lint", func(s step) bool { return strings.HasPrefix(s.Uses, "golangci/golangci-lint-action@") }},
		{"OpenAPI lint", func(s step) bool { return strings.Contains(s.Run, "./cmd/lint-openapi") }},
		{"tenant-filter lint", func(s step) bool { return strings.Contains(s.Run, "make lint-tenant") }},
		{"connector lint", func(s step) bool { return strings.Contains(s.Run, "make lint-connector") }},
		{"promtool check rules", func(s step) bool {
			return strings.Contains(s.Run, "promtool") && strings.Contains(s.Run, "check rules /cfg/prometheus-rules.yml")
		}},
		{"promtool test rules", func(s step) bool {
			return strings.Contains(s.Run, "promtool") && strings.Contains(s.Run, "test rules /cfg/prometheus-rules-test.yml")
		}},
	}

	aggNeeds := needsList(wf.Jobs["go-tests"].Needs)
	planNeeds := needsList(wf.Jobs["plan"].Needs)
	has := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}

	for _, c := range checks {
		found := 0
		for jobID, j := range wf.Jobs {
			for _, s := range j.Steps {
				s.Run = stripShellComments(s.Run)
				if !c.match(s) {
					continue
				}
				found++
				where := jobID + " step " + s.Name
				if !strings.Contains(j.If, "'push'") || !strings.Contains(j.If, "'pull_request'") {
					t.Errorf("%s (%s): job if %q does not run on both pushes and pull requests", c.name, where, j.If)
				}
				if s.If != "" {
					t.Errorf("%s (%s): the step has its own if %q; it must run every time", c.name, where, s.If)
				}
				if j.ContinueOnError != nil || s.ContinueOnError != nil {
					t.Errorf("%s (%s): continue-on-error lets a finding pass", c.name, where)
				}
				if !has(aggNeeds, jobID) {
					t.Errorf("%s (%s): the Go Tests aggregator does not need job %s", c.name, where, jobID)
				}
				if !has(planNeeds, jobID) {
					t.Errorf("%s (%s): the plan job does not need job %s, so a red check would not stop a deploy", c.name, where, jobID)
				}
				if c.name == "golangci-lint" {
					if s.With["working-directory"] != "src" {
						t.Errorf("golangci-lint runs in %q, not src, so it does not read src/.golangci.yml", s.With["working-directory"])
					}
					if v := s.With["only-new-issues"]; v != "" && v != "false" {
						t.Errorf("golangci-lint has only-new-issues: %s; findings in untouched code would pass", v)
					}
					for _, flag := range []string{"--new", "--disable", "--enable-only", "--fast", "--issues-exit-code", "--config", "--no-config"} {
						if strings.Contains(s.With["args"], flag) {
							t.Errorf("golangci-lint args %q contain %s, which narrows what it reports", s.With["args"], flag)
						}
					}
				}
			}
		}
		if found == 0 {
			t.Errorf("no step in build-push.yml runs %s", c.name)
		}
	}

	// The aggregator must actually compare each job's result to success, not
	// only wait for it: needs alone lets a failed job through once always()
	// is set.
	for _, jobID := range aggNeeds {
		checked := false
		for _, s := range wf.Jobs["go-tests"].Steps {
			for name, v := range s.Env {
				if strings.Contains(v, "needs."+jobID+".result") && strings.Contains(s.Run, `"$`+name+`" = success`) {
					checked = true
				}
			}
		}
		if !checked {
			t.Errorf("the Go Tests aggregator needs %s but never checks that needs.%s.result is success", jobID, jobID)
		}
	}

	// golangci-lint's own linter set: the standard set, nothing switched off
	// wholesale. Narrower settings belong in a reviewed change to this guard.
	cfgBody, err := os.ReadFile(filepath.Join("..", "..", ".golangci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Linters struct {
			Default string   `yaml:"default"`
			Disable []string `yaml:"disable"`
		} `yaml:"linters"`
		Issues struct {
			New bool `yaml:"new"`
		} `yaml:"issues"`
	}
	if err := yaml.Unmarshal(cfgBody, &cfg); err != nil {
		t.Fatalf("parse .golangci.yml: %v", err)
	}
	if cfg.Linters.Default != "standard" {
		t.Errorf(".golangci.yml linters.default is %q, want standard", cfg.Linters.Default)
	}
	if len(cfg.Linters.Disable) > 0 {
		t.Errorf(".golangci.yml disables linters %v", cfg.Linters.Disable)
	}
	if cfg.Issues.New {
		t.Error(".golangci.yml sets issues.new: only new findings would fail the build")
	}
}
