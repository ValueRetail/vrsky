package managementapi

import (
	"fmt"
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
			if goTests, raced := goTestCommands(st.Run); goTests == 0 || raced != goTests {
				t.Errorf("the pkg/io state-store step does not run `go test` with -race on the command (comments removed):\n%s", stripShellComments(st.Run))
			}
		}
	}
	if testStep == nil {
		t.Fatal("no go-test-shards step runs .github/scripts/go-test-shards.sh")
	}
	// Matched against the commands with their comments removed: a comment
	// that mentions -race or the skip check runs nothing.
	testCmds := stripShellComments(testStep.Run)
	for _, want := range []string{"${{ matrix.shard }}", "VRSKY_TEST_POSTGRES_URL not set", "exit 1"} {
		if !strings.Contains(testCmds, want) {
			t.Errorf("the shard test step's commands (comments removed) lack %q:\n%s", want, testCmds)
		}
	}
	if goTests, raced := goTestCommands(testStep.Run); goTests == 0 {
		t.Errorf("the shard test step runs no `go test` command:\n%s", testCmds)
	} else if raced != goTests {
		t.Errorf("%d of the shard test step's %d `go test` commands run without -race on that command:\n%s", goTests-raced, goTests, testCmds)
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

// fakeGo stands in for `go` on PATH. When FAKE_GO_PKG is one of the
// arguments of a `go list`, it fails (printing a package first, as a
// half-finished `go list` can) or prints nothing, per FAKE_GO_MODE, and notes
// the call in FAKE_GO_LOG. Everything else goes to the real go.
const fakeGo = `#!/usr/bin/env bash
if [ "${1:-}" = list ] && [ -n "${FAKE_GO_PKG:-}" ]; then
	for a in "${@:2}"; do
		if [ "$a" = "$FAKE_GO_PKG" ]; then
			echo "go $*" >>"$FAKE_GO_LOG"
			case "$FAKE_GO_MODE" in
			fail)
				echo github.com/ValueRetail/vrsky/pkg/fake
				echo "fake go: list failed" >&2
				exit 1
				;;
			empty) exit 0 ;;
			esac
		fi
	done
fi
exec "$FAKE_GO_REAL" "$@"
`

// go-test-shards.sh feeds `go test` its package list. A `go list` whose
// failure the script swallows leaves a shard testing nothing, or (with no
// packages at all) whatever sits in src/, and the shard stays green: an
// import cycle once printed nothing and exited 0 that way (#443). So every
// `go list` the script runs, for `rest` and for each named shard, must fail
// the script when it fails or finds nothing, and say why on stderr.
func TestGoTestShardScriptFailsWhenGoListDoes(t *testing.T) {
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no go on PATH: %v", err)
	}
	realGo, err = filepath.Abs(realGo)
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", ".github", "scripts", "go-test-shards.sh"))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeExecutable(t, bin, "go", fakeGo)

	modOut, err := exec.Command(realGo, "list", "-m").Output() //nolint:gosec // the go binary found on PATH
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	module := strings.TrimSpace(string(modOut))

	type result struct {
		stdout, stderr string
		calls          string // the faked go list calls
		err            error
	}
	run := func(t *testing.T, pkg, mode string, args ...string) result {
		t.Helper()
		log := filepath.Join(t.TempDir(), "go.log")
		cmd := exec.Command("bash", append([]string{script}, args...)...) //nolint:gosec // fixed script inside the repo
		cmd.Env = append(os.Environ(),
			"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"FAKE_GO_REAL="+realGo, "FAKE_GO_PKG="+pkg, "FAKE_GO_MODE="+mode, "FAKE_GO_LOG="+log)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		calls, _ := os.ReadFile(log) //nolint:gosec // a file in the test's temp dir
		return result{stdout.String(), stderr.String(), string(calls), err}
	}

	// With a working go every shard lists packages, through the fake.
	names := strings.Fields(run(t, "", "", "names").stdout)
	if len(names) < 2 {
		t.Fatalf("go-test-shards.sh names printed %v", names)
	}
	shardDirs := map[string][]string{} // named shard -> its packages as ./dir patterns
	for _, name := range names {
		r := run(t, "", "", name)
		if r.err != nil {
			t.Fatalf("go-test-shards.sh %s with a working go: %v\n%s", name, r.err, r.stderr)
		}
		pkgs := strings.Fields(r.stdout)
		if len(pkgs) == 0 {
			t.Fatalf("go-test-shards.sh %s with a working go printed no packages", name)
		}
		if name == "rest" {
			continue
		}
		for _, p := range pkgs {
			rel := strings.TrimPrefix(p, module+"/")
			if rel == p {
				t.Fatalf("go-test-shards.sh %s printed %q, not a package of %s", name, p, module)
			}
			shardDirs[name] = append(shardDirs[name], "./"+rel)
		}
	}
	if len(shardDirs) == 0 {
		t.Fatal("go-test-shards.sh names lists no named shard besides rest")
	}

	type tc struct {
		shard, pkg, mode string
	}
	cases := []tc{{"rest", "./...", "fail"}, {"rest", "./...", "empty"}}
	for shard, dirs := range shardDirs {
		// Any named shard's go list, not only the last one the script runs.
		for _, d := range dirs {
			cases = append(cases, tc{"rest", d, "fail"})
		}
		cases = append(cases, tc{shard, dirs[0], "fail"}, tc{shard, dirs[0], "empty"})
	}
	for _, c := range cases {
		t.Run(c.shard+"/"+c.mode+" go list "+c.pkg, func(t *testing.T) {
			t.Parallel()
			r := run(t, c.pkg, c.mode, c.shard)
			if r.calls == "" {
				t.Fatalf("go-test-shards.sh %s never ran `go list` with argument %s, so the fake could not break it; this guard assumes the script lists each package by its ./dir", c.shard, c.pkg)
			}
			what := map[string]string{"fail": "failed", "empty": "printed nothing"}[c.mode]
			if r.err == nil {
				t.Fatalf("go-test-shards.sh %s exited 0 although `%s` %s; the shard would run on a wrong package list:\n%s",
					c.shard, strings.TrimSpace(r.calls), what, r.stdout)
			}
			own := strings.TrimSpace(strings.ReplaceAll(r.stderr, "fake go: list failed", ""))
			if own == "" {
				t.Errorf("go-test-shards.sh %s stopped when `%s` %s, but without saying why on stderr", c.shard, strings.TrimSpace(r.calls), what)
			}
		})
	}
}

// The "Go Tests" job is the single result that stands for the shards and the
// lint jobs. It runs with always(), so it starts even when a job it needs
// failed, and only its own step can then turn it red. This runs that step's
// script as the runner would, with each needed job in turn failed, cancelled
// or skipped, and checks the job fails each time and passes when all
// succeeded.
func TestGoTestsAggregatorFailsWhenANeededJobDid(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "build-push.yml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Needs any      `yaml:"needs"`
			Steps []ciStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse build-push.yml: %v", err)
	}
	agg, ok := wf.Jobs["go-tests"]
	if !ok {
		t.Fatal("build-push.yml has no go-tests job")
	}
	needs := needsList(agg.Needs)
	if len(needs) == 0 {
		t.Fatal("the go-tests job needs no jobs")
	}
	var steps []ciStep
	for _, s := range agg.Steps {
		if s.Uses != "" {
			continue
		}
		if s.If != "" {
			t.Errorf("Go Tests step %q has its own if %q; whether it checks the results must not depend on anything else", s.Name, s.If)
		}
		steps = append(steps, s)
	}
	if len(steps) == 0 {
		t.Fatal("the go-tests job runs no script, so nothing compares the results")
	}

	// runJob runs the job's steps in order, as the runner does, and reports
	// whether the job ends green.
	runJob := func(results map[string]string) (bool, string) {
		vals := map[string]string{}
		for j, r := range results {
			vals["needs."+j+".result"] = r
		}
		var out strings.Builder
		for _, s := range steps {
			env := []string{}
			for k, v := range s.Env {
				env = append(env, k+"="+expandExprs(t, "Go Tests step "+s.Name, v, vals))
			}
			stdout, stderr, err := runStepScript(t, s.Shell, expandExprs(t, "Go Tests step "+s.Name, s.Run, vals), t.TempDir(), env...)
			out.WriteString(stdout + stderr)
			if err != nil {
				return false, out.String()
			}
		}
		return true, out.String()
	}

	all := func(r string) map[string]string {
		m := map[string]string{}
		for _, j := range needs {
			m[j] = r
		}
		return m
	}
	if ok, out := runJob(all("success")); !ok {
		t.Fatalf("Go Tests fails although every job it needs succeeded:\n%s", out)
	}
	for _, j := range needs {
		for _, r := range []string{"failure", "cancelled", "skipped"} {
			results := all("success")
			results[j] = r
			if ok, out := runJob(results); ok {
				t.Errorf("Go Tests passes when %s is %s: a red %s would show up as a green Go Tests (does needs.%s.result reach the script?)\n%s", j, r, j, j, out)
			}
		}
	}
}

// ciStep is a workflow step, as the CI guards that run steps need it.
type ciStep struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	Uses             string            `yaml:"uses"`
	If               string            `yaml:"if"`
	Shell            string            `yaml:"shell"`
	WorkingDirectory string            `yaml:"working-directory"`
	Run              string            `yaml:"run"`
	Env              map[string]string `yaml:"env"`
	With             map[string]any    `yaml:"with"`
}

// with returns a `with:` input as a string ("" when it is not set).
func (s ciStep) with(key string) string {
	v, ok := s.With[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// stripShellComments drops shell comments from a script: from an unquoted #
// that starts a word to the end of its line. What is left is what runs.
func stripShellComments(script string) string {
	var b strings.Builder
	var quote byte
	prev := byte('\n')
	comment := false
	for i := 0; i < len(script); i++ {
		c := script[i]
		if comment {
			if c == '\n' {
				comment = false
				b.WriteByte(c)
				prev = c
			}
			continue
		}
		switch {
		case quote == 0 && c == '#' && strings.IndexByte("\n \t;&|()", prev) >= 0:
			comment = true
			continue
		case quote != '\'' && c == '\\' && i+1 < len(script):
			b.WriteByte(c)
			i++
			c = script[i]
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case quote != 0 && c == quote:
			quote = 0
		}
		b.WriteByte(c)
		prev = c
	}
	return b.String()
}

// goTestCommands counts the `go test` commands in a script (comments removed,
// continued lines joined, split at | && || ; and newlines) and how many of
// them carry -race themselves.
func goTestCommands(script string) (goTests, raced int) {
	text := strings.ReplaceAll(stripShellComments(script), "\\\n", " ")
	for _, cmd := range regexp.MustCompile(`\|\|?|&&|;|\n`).Split(text, -1) {
		f := strings.Fields(cmd)
		for i := 0; i+1 < len(f); i++ {
			if f[i] != "go" || f[i+1] != "test" {
				continue
			}
			goTests++
			for _, a := range f[i+2:] {
				if a == "-race" || a == "-race=true" || a == "--race" {
					raced++
					break
				}
			}
			break
		}
	}
	return goTests, raced
}

var ghExpr = regexp.MustCompile(`\$\{\{(.*?)\}\}`)

// expandExprs replaces each ${{ … }} in s with its value from vals, keyed by
// the expression with its whitespace normalised. An expression the caller did
// not supply fails the test: the guard would otherwise run something other
// than what the runner does.
func expandExprs(t *testing.T, where, s string, vals map[string]string) string {
	t.Helper()
	return ghExpr.ReplaceAllStringFunc(s, func(m string) string {
		inner := strings.Join(strings.Fields(ghExpr.FindStringSubmatch(m)[1]), " ")
		v, ok := vals[inner]
		if !ok {
			t.Fatalf("%s uses ${{ %s }}, which this guard cannot evaluate; teach the guard what it means rather than dropping the check", where, inner)
		}
		return v
	})
}

// runStepScript runs a step's run: script the way a GitHub Linux runner
// does: `bash -e {0}` when the step names no shell, `bash --noprofile --norc
// -eo pipefail {0}` for shell: bash.
func runStepScript(t *testing.T, shell, script, dir string, env ...string) (stdout, stderr string, err error) {
	t.Helper()
	var args []string
	switch shell {
	case "":
		args = []string{"-e"}
	case "bash":
		args = []string{"--noprofile", "--norc", "-eo", "pipefail"}
	default:
		t.Fatalf("the step runs under shell %q; this guard only knows how the runner calls bash", shell)
	}
	file := filepath.Join(t.TempDir(), "step.sh")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append(args, file)...) //nolint:gosec // a workflow step from the repo, in a temp file
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var o, e strings.Builder
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	return o.String(), e.String(), err
}

// writeExecutable writes a script into dir under name, executable.
func writeExecutable(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil { //nolint:gosec // a fake tool in the test's temp dir must be executable
		t.Fatal(err)
	}
}
