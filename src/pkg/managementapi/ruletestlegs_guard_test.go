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

// fakeDocker stands in for `docker run … promtool`. It logs each call to
// FAKE_DOCKER_LOG and every --run value to FAKE_DOCKER_RUNS. `check rules`
// exits FAKE_CHECK_EXIT. `test rules` writes a JUnit report with
// FAKE_TESTCASES test cases to wherever --junit points through the -v mounts,
// then exits FAKE_TEST_EXIT.
const fakeDocker = `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"
mounts=()
junit=""
run=""
prev=""
for a in "$@"; do
	case "$prev" in
	-v | --volume) mounts+=("$a") ;;
	--junit) junit="$a" ;;
	--run) run="$a" ;;
	esac
	case "$a" in
	--junit=*) junit="${a#--junit=}" ;;
	--run=*) run="${a#--run=}" ;;
	esac
	prev="$a"
done
case " $* " in
*" check rules "*) exit "${FAKE_CHECK_EXIT:-0}" ;;
*" test rules "*)
	[ -z "$run" ] || printf '%s\n' "$run" >>"$FAKE_DOCKER_RUNS"
	if [ -n "$junit" ]; then
		host=""
		for m in "${mounts[@]}"; do
			src="${m%%:*}"
			dst="${m#*:}"
			dst="${dst%%:*}"
			case "$junit" in "$dst"/*) host="$src${junit#"$dst"}" ;; esac
		done
		[ -n "$host" ] || { echo "fake docker: --junit $junit is not in a mounted directory" >&2; exit 3; }
		{
			echo '<?xml version="1.0" encoding="UTF-8"?>'
			echo '<testsuites><testsuite name="/cfg/prometheus-rules-test.yml">'
			i=0
			while [ "$i" -lt "${FAKE_TESTCASES:-0}" ]; do
				echo "<testcase name=\"group-$i\"></testcase>"
				i=$((i + 1))
			done
			echo '</testsuite></testsuites>'
		} >"$host"
	fi
	exit "${FAKE_TEST_EXIT:-0}"
	;;
esac
echo "fake docker: unexpected call: $*" >&2
exit 3
`

// Where the alert-rules job's inputs live, relative to the repo root. The
// leg lists moved out of the workflow into ruleLegsFile in #495, so adding a
// test group no longer edits .github/.
const (
	ruleTestsFile = "infrastructure/prometheus-rules-test.yml"
	ruleLegsFile  = "infrastructure/prometheus-rules-test-legs.yml"
	buildPushFile = ".github/workflows/build-push.yml"
)

// ruleLeg is one leg of the alert-rules job: its matrix name and the test
// groups the legs file lists for it.
type ruleLeg struct {
	Leg    string
	Groups []string
}

// ruleLegSetup is what ruleLegProblems found: every test group, the legs in
// the workflow's order with their groups from the legs file, and the step
// that runs promtool.
type ruleLegSetup struct {
	groups map[string]bool
	legs   []ruleLeg
	step   ciStep
}

// ruleLegProblems checks the alert-rules setup from the three files' bodies
// (legsFile nil: the file does not exist). Every test group must be in
// exactly one leg, the legs file must hold exactly the workflow's matrix legs,
// none of them empty, and the job and its promtool step must not be able to
// pass on a failure. It is a function of the bodies so the guard can feed it
// broken copies (TestRuleLegGuardCatchesMutations).
func ruleLegProblems(rulesTest, legsFile, workflow []byte) (ruleLegSetup, []string) {
	var setup ruleLegSetup
	var problems []string
	fail := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	// The test groups.
	var rt struct {
		Tests []struct {
			Name           string `yaml:"name"`
			AlertRuleTest  []any  `yaml:"alert_rule_test"`
			PromqlExprTest []any  `yaml:"promql_expr_test"`
		} `yaml:"tests"`
	}
	if err := yaml.Unmarshal(rulesTest, &rt); err != nil {
		fail("parse %s: %v", ruleTestsFile, err)
		return setup, problems
	}
	if len(rt.Tests) == 0 {
		fail("%s has no test groups", ruleTestsFile)
		return setup, problems
	}
	nameRe := regexp.MustCompile(`^[a-z0-9-]+$`)
	setup.groups = map[string]bool{}
	for i, g := range rt.Tests {
		switch {
		case !nameRe.MatchString(g.Name):
			fail("test group %d has name %q; a leg can only select it by a unique name of [a-z0-9-]", i+1, g.Name)
		case setup.groups[g.Name]:
			fail("two test groups are named %q; --run cannot tell them apart", g.Name)
		}
		setup.groups[g.Name] = true
		if len(g.AlertRuleTest)+len(g.PromqlExprTest) == 0 {
			fail("test group %q has no alert_rule_test or promql_expr_test; it tests nothing", g.Name)
		}
	}

	// The workflow: the matrix names the legs and nothing else.
	type ruleStep struct {
		ciStep          `yaml:",inline"`
		ContinueOnError any `yaml:"continue-on-error"`
	}
	var wf struct {
		Jobs map[string]struct {
			ContinueOnError any `yaml:"continue-on-error"`
			Strategy        struct {
				Matrix map[string]any `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []ruleStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(workflow, &wf); err != nil {
		fail("parse %s: %v", buildPushFile, err)
		return setup, problems
	}
	job, ok := wf.Jobs["alert-rules"]
	if !ok {
		fail("%s has no alert-rules job", buildPushFile)
		return setup, problems
	}
	if job.ContinueOnError != nil {
		fail("the alert-rules job has continue-on-error: %v; a red leg would not stop Go Tests or the deploy", job.ContinueOnError)
	}
	var wfLegs []string
	for k, v := range job.Strategy.Matrix {
		if k != "leg" {
			fail("the alert-rules matrix has %q besides leg; that adds, multiplies or drops legs, so a group could run twice or never", k)
			continue
		}
		list, ok := v.([]any)
		if !ok {
			fail("the alert-rules matrix leg is %v, want a list of leg names", v)
			continue
		}
		for _, l := range list {
			s, ok := l.(string)
			if !ok {
				fail("the alert-rules matrix leg %v is not a string; quote it", l)
				continue
			}
			wfLegs = append(wfLegs, s)
		}
	}
	if len(wfLegs) == 0 {
		fail("the alert-rules job has no matrix legs (strategy.matrix.leg)")
	}

	// The legs file: exactly the workflow's legs, every group once.
	if legsFile == nil {
		fail("%s is missing; the alert-rules legs read their test groups from it", ruleLegsFile)
		return setup, problems
	}
	var fileLegs map[string][]string
	if err := yaml.Unmarshal(legsFile, &fileLegs); err != nil {
		fail("parse %s: %v", ruleLegsFile, err)
		return setup, problems
	}
	inLeg := map[string]string{}
	seen := map[string]bool{}
	for _, name := range wfLegs {
		if name == "" || seen[name] {
			fail("the alert-rules matrix names leg %q twice or empty; each leg needs its own name", name)
			continue
		}
		seen[name] = true
		groups, ok := fileLegs[name]
		if !ok {
			fail("the workflow runs leg %q but %s has no entry for it; the leg would fail", name, ruleLegsFile)
			continue
		}
		if len(groups) == 0 {
			fail("leg %q lists no test groups in %s", name, ruleLegsFile)
		}
		for _, g := range groups {
			if !setup.groups[g] {
				fail("leg %q lists %q, which is no test group in %s", name, g, ruleTestsFile)
			}
			if prev, dup := inLeg[g]; dup {
				fail("test group %q is in leg %q and in leg %q; it would run twice", g, prev, name)
			}
			inLeg[g] = name
		}
		setup.legs = append(setup.legs, ruleLeg{Leg: name, Groups: groups})
	}
	var extra []string
	for name := range fileLegs {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		fail("%s lists leg %q, which is no leg of the alert-rules matrix: its groups would never run", ruleLegsFile, name)
	}
	for _, g := range rt.Tests {
		if _, ok := inLeg[g.Name]; !ok && g.Name != "" {
			fail("test group %q is in no leg of %s: CI would never run it", g.Name, ruleLegsFile)
		}
	}

	// The step.
	var testSteps []ruleStep
	for _, s := range job.Steps {
		cmds := stripShellComments(s.Run)
		if strings.Contains(cmds, "promtool") && strings.Contains(cmds, "test rules /cfg/prometheus-rules-test.yml") {
			testSteps = append(testSteps, s)
		}
	}
	if len(testSteps) != 1 {
		fail("%d alert-rules steps run promtool test rules /cfg/prometheus-rules-test.yml, want exactly 1", len(testSteps))
		return setup, problems
	}
	st := testSteps[0]
	if st.If != "" {
		fail("the promtool step has its own if %q; every leg must run it", st.If)
	}
	if st.ContinueOnError != nil {
		fail("the promtool step has continue-on-error: %v; a failing rule test would pass", st.ContinueOnError)
	}
	setup.step = st.ciStep
	return setup, problems
}

// readRuleLegFiles reads the three files ruleLegProblems checks. The legs
// file comes back nil when it does not exist.
func readRuleLegFiles(t *testing.T, root string) (rulesTest, legsFile, workflow []byte) {
	t.Helper()
	read := func(rel string, mayBeMissing bool) []byte {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // fixed paths inside the repo
		if err != nil {
			if mayBeMissing && os.IsNotExist(err) {
				return nil
			}
			t.Fatal(err)
		}
		return b
	}
	return read(ruleTestsFile, false), read(ruleLegsFile, true), read(buildPushFile, false)
}

// The promtool unit tests run as matrix legs since #464; each leg reads its
// test groups from prometheus-rules-test-legs.yml since #495 and passes them
// to `promtool test rules --run`. A group no leg names is never tested again,
// and promtool says SUCCESS when --run matches nothing, so nothing would turn
// red. This checks that every group in prometheus-rules-test.yml runs in
// exactly one leg, and, by running the step with a fake docker, that the
// leg's --run selects exactly its groups, that it still runs `check rules`
// with the checkout mounted read-only, and that it fails when promtool ran
// fewer groups than it names or when its entry in the legs file is missing,
// empty or unreadable.
func TestRuleTestGroupsRunOnce(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	rulesTest, legsFile, workflow := readRuleLegFiles(t, root)
	setup, problems := ruleLegProblems(rulesTest, legsFile, workflow)
	for _, p := range problems {
		t.Error(p)
	}
	if t.Failed() {
		return
	}
	st := setup.step

	bin := t.TempDir()
	writeExecutable(t, bin, "docker", fakeDocker)

	type outcome struct {
		ok      bool
		output  string
		calls   []string
		runArgs []string
	}
	// runLeg runs the step for leg in a fresh checkout that holds only the
	// legs file, with the given body (nil: no legs file). The fake docker
	// needs none of the other files, and the step must need no other input
	// to pick its groups.
	runLeg := func(t *testing.T, leg string, legsBody []byte, testcases, checkExit, testExit int) outcome {
		t.Helper()
		tmp := t.TempDir()
		checkout := filepath.Join(tmp, "checkout")
		if err := os.MkdirAll(filepath.Join(checkout, "infrastructure"), 0o750); err != nil {
			t.Fatal(err)
		}
		if legsBody != nil {
			if err := os.WriteFile(filepath.Join(checkout, filepath.FromSlash(ruleLegsFile)), legsBody, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		dir := checkout
		if st.WorkingDirectory != "" {
			dir = filepath.Join(checkout, st.WorkingDirectory)
		}
		vals := map[string]string{"matrix.leg": leg}
		where := "the promtool step"
		env := []string{
			"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"RUNNER_TEMP=" + t.TempDir(),
			"FAKE_DOCKER_LOG=" + filepath.Join(tmp, "calls"),
			"FAKE_DOCKER_RUNS=" + filepath.Join(tmp, "runs"),
			"FAKE_TESTCASES=" + strconv.Itoa(testcases),
			"FAKE_CHECK_EXIT=" + strconv.Itoa(checkExit),
			"FAKE_TEST_EXIT=" + strconv.Itoa(testExit),
		}
		for k, v := range st.Env {
			env = append(env, k+"="+expandExprs(t, where, v, vals))
		}
		stdout, stderr, err := runStepScript(t, st.Shell, expandExprs(t, where, st.Run, vals), dir, env...)
		calls, _ := os.ReadFile(filepath.Join(tmp, "calls")) //nolint:gosec // a file in the test's temp dir
		runs, _ := os.ReadFile(filepath.Join(tmp, "runs"))   //nolint:gosec // a file in the test's temp dir
		return outcome{
			ok:      err == nil,
			output:  stdout + stderr,
			calls:   strings.Split(strings.TrimSpace(string(calls)), "\n"),
			runArgs: strings.Fields(string(runs)),
		}
	}

	// replaceEntry returns the legs file with leg's entry replaced by repl
	// (its key line and every line under it, up to the next top-level line).
	entryRe := func(leg string) *regexp.Regexp {
		return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(leg) + `:\n(?:[ \t].*\n|\n)*`)
	}
	replaceEntry := func(t *testing.T, leg, repl string) []byte {
		t.Helper()
		re := entryRe(leg)
		if !re.Match(legsFile) {
			t.Fatalf("cannot find leg %q's entry in %s to break it", leg, ruleLegsFile)
		}
		return re.ReplaceAll(legsFile, []byte(repl))
	}

	roMount := regexp.MustCompile(`(?:^| )-v \S*/infrastructure:/cfg:ro(?: |$)`)
	for _, l := range setup.legs {
		t.Run("leg "+l.Leg, func(t *testing.T) {
			n := len(l.Groups)
			o := runLeg(t, l.Leg, legsFile, n, 0, 0)
			if !o.ok {
				t.Fatalf("the leg fails although promtool passed and ran its %d groups:\n%s", n, o.output)
			}
			checked := false
			for _, c := range o.calls {
				if strings.Contains(c, "promtool") && strings.Contains(c, "check rules /cfg/prometheus-rules.yml") {
					checked = true
				}
				if strings.Contains(c, "/cfg") && !roMount.MatchString(c) {
					t.Errorf("a docker run mounts /cfg without infrastructure:/cfg:ro; the container could change the checkout:\n%s", c)
				}
			}
			if !checked {
				t.Errorf("the leg does not run promtool check rules /cfg/prometheus-rules.yml; a broken rule file would pass. Calls:\n%s", strings.Join(o.calls, "\n"))
			}

			// --run selects exactly this leg's groups, and is anchored: promtool
			// matches it against each group name with Go's regexp, unanchored.
			if len(o.runArgs) != 1 {
				t.Fatalf("promtool test rules got %d --run values %v, want one: without it every leg runs every group", len(o.runArgs), o.runArgs)
			}
			re, err := regexp.Compile(o.runArgs[0])
			if err != nil {
				t.Fatalf("--run=%q is not a valid regexp: %v", o.runArgs[0], err)
			}
			mine := map[string]bool{}
			loose := []string{}
			for _, g := range l.Groups {
				mine[g] = true
				if re.MatchString("x"+g) || re.MatchString(g+"x") {
					loose = append(loose, g)
				}
			}
			if len(loose) > 0 {
				t.Errorf("--run=%q is not anchored: it also matches longer names that contain %v, so a new group could run in two legs", o.runArgs[0], loose)
			}
			for g := range setup.groups {
				if re.MatchString(g) != mine[g] {
					t.Errorf("--run=%q matches group %q: %v, but the legs file lists it for this leg: %v", o.runArgs[0], g, re.MatchString(g), mine[g])
				}
			}

			// Fewer groups than named (a name that matches nothing, or none at
			// all), or a failing promtool, fails the leg.
			bad := []struct {
				what                           string
				testcases, checkExit, testExit int
			}{
				{"promtool ran one group fewer than the leg names", n - 1, 0, 0},
				{"promtool ran no group", 0, 0, 0},
				{"promtool test rules failed", n, 0, 1},
				{"promtool check rules failed", n, 1, 0},
			}
			for _, b := range bad {
				if o := runLeg(t, l.Leg, legsFile, b.testcases, b.checkExit, b.testExit); o.ok {
					t.Errorf("the leg passes when %s:\n%s", b.what, o.output)
				}
			}

			// A broken legs file fails the leg loudly, even when promtool then
			// "ran" as many groups as the step thought it named: with none
			// named, zero of zero would otherwise pass.
			first := l.Groups[0]
			broken := []struct {
				what      string
				body      []byte
				testcases int
			}{
				{"the legs file is missing", nil, 0},
				{"the leg's entry is empty", replaceEntry(t, l.Leg, l.Leg+":\n\n"), 0},
				{"the leg has no entry", replaceEntry(t, l.Leg, ""), 0},
				{"the leg's key is quoted, which the step does not read", replaceEntry(t, l.Leg, `"`+l.Leg+`":`+"\n  - "+strings.Join(l.Groups, "\n  - ")+"\n\n"), 0},
				{"a line under the leg is no plain item", replaceEntry(t, l.Leg, l.Leg+":\n  - "+strings.Join(l.Groups, "\n  - ")+"\n  - "+first+" # trailing comment\n\n"), n},
			}
			for _, b := range broken {
				if o := runLeg(t, l.Leg, b.body, b.testcases, 0, 0); o.ok {
					t.Errorf("the leg passes when %s:\n%s", b.what, o.output)
				}
			}
		})
	}
}

// Each way of breaking the setup that #495 names, applied to the real files,
// must be reported by ruleLegProblems. A mutation that no longer changes the
// file fails too, so this cannot pass by matching nothing.
func TestRuleLegGuardCatchesMutations(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	rulesTest, legsFile, workflow := readRuleLegFiles(t, root)
	setup, problems := ruleLegProblems(rulesTest, legsFile, workflow)
	if len(problems) > 0 || len(setup.legs) < 2 {
		t.Fatalf("the real files must pass with at least two legs before mutating them: %v", problems)
	}
	firstLeg, secondLeg := setup.legs[0], setup.legs[1]
	moved := firstLeg.Groups[0]

	jobHead := "  alert-rules:\n    name: Alert rules (promtool ${{ matrix.leg }})\n"
	stepHead := "      - name: Alert rules (promtool)\n"
	matrixLeg := regexp.MustCompile(`(?m)^        leg: \[.*\]\n`)

	type files struct{ legs, workflow []byte }
	replace := func(b []byte, old, repl string) []byte {
		return []byte(strings.Replace(string(b), old, repl, 1))
	}
	entry := func(leg string) *regexp.Regexp {
		return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(leg) + `:\n(?:[ \t].*\n|\n)*`)
	}
	mutations := []struct {
		name   string
		mutate func(f files) files
		want   string
	}{
		{"a group missing from the legs file", func(f files) files {
			f.legs = replace(f.legs, "  - "+moved+"\n", "")
			return f
		}, "is in no leg"},
		{"a group listed in two legs", func(f files) files {
			f.legs = replace(f.legs, secondLeg.Leg+":\n", secondLeg.Leg+":\n  - "+moved+"\n")
			return f
		}, "it would run twice"},
		{"a leg left empty", func(f files) files {
			f.legs = entry(secondLeg.Leg).ReplaceAll(f.legs, []byte(secondLeg.Leg+":\n\n"))
			return f
		}, "lists no test groups"},
		{"the legs file deleted", func(f files) files {
			f.legs = nil
			return f
		}, "is missing"},
		{"a leg in the file that the workflow does not run", func(f files) files {
			f.legs = append(append([]byte{}, f.legs...), []byte("\n9/9:\n  - "+moved+"\n")...)
			return f
		}, "no leg of the alert-rules matrix"},
		{"a workflow leg the file has no entry for", func(f files) files {
			f.legs = entry(secondLeg.Leg).ReplaceAll(f.legs, nil)
			return f
		}, "has no entry for it"},
		{"continue-on-error on the job", func(f files) files {
			f.workflow = replace(f.workflow, jobHead, jobHead+"    continue-on-error: true\n")
			return f
		}, "job has continue-on-error"},
		{"continue-on-error on the step", func(f files) files {
			f.workflow = replace(f.workflow, stepHead, stepHead+"        continue-on-error: true\n")
			return f
		}, "step has continue-on-error"},
		{"an if on the step", func(f files) files {
			f.workflow = replace(f.workflow, stepHead, stepHead+"        if: matrix.leg != '"+secondLeg.Leg+"'\n")
			return f
		}, "has its own if"},
		{"include added to the matrix", func(f files) files {
			f.workflow = matrixLeg.ReplaceAllFunc(f.workflow, func(m []byte) []byte {
				return append(append([]byte{}, m...), []byte("        include:\n          - leg: 9/9\n")...)
			})
			return f
		}, `"include" besides leg`},
		{"a leg dropped from the matrix", func(f files) files {
			f.workflow = matrixLeg.ReplaceAllFunc(f.workflow, func(m []byte) []byte {
				return []byte(strings.Replace(string(m), "'"+secondLeg.Leg+"', ", "", 1))
			})
			return f
		}, "no leg of the alert-rules matrix"},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			got := m.mutate(files{legs: append([]byte{}, legsFile...), workflow: append([]byte{}, workflow...)})
			if string(got.legs) == string(legsFile) && string(got.workflow) == string(workflow) && got.legs != nil {
				t.Fatal("the mutation changed nothing; update it to the files' current layout")
			}
			_, problems := ruleLegProblems(rulesTest, got.legs, got.workflow)
			for _, p := range problems {
				if strings.Contains(p, m.want) {
					return
				}
			}
			t.Errorf("ruleLegProblems does not catch %s (want a problem containing %q), got %v", m.name, m.want, problems)
		})
	}
}
