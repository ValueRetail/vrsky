package managementapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The Go tests run as a matrix of shards (#436,
// plans/issue-436-shard-go-tests.md), and .github/scripts/go-test-shards.sh
// decides which packages each shard tests. A package that no shard picks up
// is never tested again and nothing turns red, so this checks that the
// matrix and the script name the same shards, that together they cover what
// `go list ./...` reports, each package exactly once, and that every shard
// still runs with -race, against Postgres, with the skipped-DB-test check.
func TestGoTestShardsCoverEveryPackage(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "build-push.yml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	type step struct {
		Name string            `yaml:"name"`
		If   string            `yaml:"if"`
		Run  string            `yaml:"run"`
		Env  map[string]string `yaml:"env"`
	}
	var wf struct {
		Jobs map[string]struct {
			Name     string         `yaml:"name"`
			Needs    any            `yaml:"needs"`
			If       string         `yaml:"if"`
			Services map[string]any `yaml:"services"`
			Strategy struct {
				Matrix struct {
					Shard []string `yaml:"shard"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse build-push.yml: %v", err)
	}
	shards, ok := wf.Jobs["go-test-shards"]
	if !ok {
		t.Fatal("build-push.yml has no go-test-shards job")
	}
	matrix := append([]string(nil), shards.Strategy.Matrix.Shard...)
	sort.Strings(matrix)

	script := filepath.Join(root, ".github", "scripts", "go-test-shards.sh")
	run := func(args ...string) []string {
		t.Helper()
		out, err := exec.Command("bash", append([]string{script}, args...)...).Output() //nolint:gosec // fixed script inside the repo
		if err != nil {
			stderr := ""
			if ee, ok := err.(*exec.ExitError); ok {
				stderr = string(ee.Stderr)
			}
			t.Fatalf("go-test-shards.sh %s: %v\n%s", strings.Join(args, " "), err, stderr)
		}
		return strings.Fields(string(out))
	}

	names := run("names")
	sort.Strings(names)
	if strings.Join(names, " ") != strings.Join(matrix, " ") {
		t.Fatalf("the matrix runs shards %v but go-test-shards.sh defines %v; a shard the matrix leaves out is never tested", matrix, names)
	}

	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	all := strings.Fields(string(out))
	if len(all) < 50 {
		t.Fatalf("go list ./... found only %d packages; is this running from the module?", len(all))
	}

	owner := map[string]string{}
	for _, shard := range names {
		pkgs := run(shard)
		if len(pkgs) == 0 {
			t.Errorf("shard %s has no packages", shard)
		}
		for _, p := range pkgs {
			if prev, dup := owner[p]; dup {
				t.Errorf("%s is in shard %s and in shard %s; it would be tested twice", p, prev, shard)
			}
			owner[p] = shard
		}
	}
	for _, p := range all {
		if _, ok := owner[p]; !ok {
			t.Errorf("%s is in no shard: CI would never test it", p)
		}
	}
	if len(owner) != len(all) {
		t.Errorf("the shards list %d packages, go list ./... has %d", len(owner), len(all))
	}

	// What each shard runs: the script's packages, with -race, against the
	// Postgres service, failing on a skipped database test.
	if _, ok := shards.Services["postgres"]; !ok {
		t.Error("the go-test-shards job has no postgres service; the database tests would skip")
	}
	var testStep *step
	ioShard := ""
	for i, st := range shards.Steps {
		if strings.Contains(st.Run, "go-test-shards.sh") {
			testStep = &shards.Steps[i]
		}
		if strings.Contains(st.Run, "-tags=integration") && strings.Contains(st.Run, "./pkg/io/") {
			m := regexp.MustCompile(`matrix\.shard == '([^']+)'`).FindStringSubmatch(st.If)
			if m == nil {
				t.Errorf("the pkg/io state-store step runs in every shard (if: %q); it belongs in the shard that holds pkg/io", st.If)
			} else {
				ioShard = m[1]
			}
		}
	}
	if testStep == nil {
		t.Fatal("no go-test-shards step runs .github/scripts/go-test-shards.sh")
	}
	for _, want := range []string{"${{ matrix.shard }}", "go test ", "-race", "VRSKY_TEST_POSTGRES_URL not set", "exit 1"} {
		if !strings.Contains(testStep.Run, want) {
			t.Errorf("the shard test step lacks %q:\n%s", want, testStep.Run)
		}
	}
	if !strings.Contains(testStep.Env["VRSKY_TEST_POSTGRES_URL"], "localhost:5432") {
		t.Errorf("the shard test step does not point VRSKY_TEST_POSTGRES_URL at the service: %q", testStep.Env["VRSKY_TEST_POSTGRES_URL"])
	}
	if ioShard == "" {
		t.Error("no step runs the pkg/io state-store tests (go test -tags=integration ./pkg/io/)")
	} else if owner["github.com/ValueRetail/vrsky/pkg/io"] != ioShard {
		t.Errorf("the pkg/io state-store tests run in shard %q, but pkg/io is in shard %q", ioShard, owner["github.com/ValueRetail/vrsky/pkg/io"])
	}

	// One "Go Tests" result that is green only when everything passed, and the
	// rollout waits for it as well as for the jobs themselves.
	agg, ok := wf.Jobs["go-tests"]
	if !ok || agg.Name != "Go Tests" {
		t.Fatal(`no go-tests job named "Go Tests"`)
	}
	aggNeeds := strings.Join(needsList(agg.Needs), " ")
	for _, j := range []string{"go-test-shards", "go-lint", "alert-rules"} {
		if !strings.Contains(aggNeeds, j) {
			t.Errorf("Go Tests does not need %s", j)
		}
	}
	if !strings.Contains(agg.If, "always()") {
		t.Errorf("Go Tests has no always(); a failed shard would skip it instead of failing it: %q", agg.If)
	}
	planNeeds := needsList(wf.Jobs["plan"].Needs)
	for _, j := range []string{"go-tests", "go-test-shards", "go-lint", "alert-rules"} {
		found := false
		for _, n := range planNeeds {
			if n == j {
				found = true
			}
		}
		if !found {
			t.Errorf("the plan job does not need %s: a red %s would not stop a deploy", j, j)
		}
	}
}
