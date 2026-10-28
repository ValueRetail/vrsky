package managementapi

import (
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

// The promtool unit tests run as matrix legs since #464, each leg naming its
// test groups for `promtool test rules --run`. A group no leg names is never
// tested again, and promtool says SUCCESS when --run matches nothing, so
// nothing would turn red. This checks that every group in
// prometheus-rules-test.yml runs in exactly one leg, that the leg's --run
// selects exactly its groups, and, by running the step with a fake docker,
// that a leg fails when promtool ran fewer groups than it names, and still
// runs `check rules`.
func TestRuleTestGroupsRunOnce(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	// The test groups.
	rtBody, err := os.ReadFile(filepath.Join(root, "infrastructure", "prometheus-rules-test.yml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	var rt struct {
		Tests []struct {
			Name           string `yaml:"name"`
			AlertRuleTest  []any  `yaml:"alert_rule_test"`
			PromqlExprTest []any  `yaml:"promql_expr_test"`
		} `yaml:"tests"`
	}
	if err := yaml.Unmarshal(rtBody, &rt); err != nil {
		t.Fatalf("parse prometheus-rules-test.yml: %v", err)
	}
	if len(rt.Tests) == 0 {
		t.Fatal("prometheus-rules-test.yml has no test groups")
	}
	nameRe := regexp.MustCompile(`^[a-z0-9-]+$`)
	groups := map[string]bool{}
	for i, g := range rt.Tests {
		switch {
		case !nameRe.MatchString(g.Name):
			t.Errorf("test group %d has name %q; a leg can only select it by a unique name of [a-z0-9-]", i+1, g.Name)
		case groups[g.Name]:
			t.Errorf("two test groups are named %q; --run cannot tell them apart", g.Name)
		}
		groups[g.Name] = true
		if len(g.AlertRuleTest)+len(g.PromqlExprTest) == 0 {
			t.Errorf("test group %q has no alert_rule_test or promql_expr_test; it tests nothing", g.Name)
		}
	}

	// The legs.
	wfBody, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "build-push.yml")) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	type leg struct {
		Leg    string   `yaml:"leg"`
		Groups []string `yaml:"groups"`
	}
	var wf struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					Include []leg          `yaml:"include"`
					Other   map[string]any `yaml:",inline"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []ciStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(wfBody, &wf); err != nil {
		t.Fatalf("parse build-push.yml: %v", err)
	}
	job, ok := wf.Jobs["alert-rules"]
	if !ok {
		t.Fatal("build-push.yml has no alert-rules job")
	}
	legs := job.Strategy.Matrix.Include
	if len(legs) == 0 {
		t.Fatal("the alert-rules job has no matrix legs (strategy.matrix.include)")
	}
	if len(job.Strategy.Matrix.Other) > 0 {
		keys := []string{}
		for k := range job.Strategy.Matrix.Other {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Errorf("the alert-rules matrix has %v besides include; that multiplies or drops legs, so a group could run twice or never", keys)
	}
	inLeg := map[string]string{}
	legNames := map[string]bool{}
	for i, l := range legs {
		if l.Leg == "" || legNames[l.Leg] {
			t.Errorf("leg %d is named %q; each leg needs its own name", i+1, l.Leg)
		}
		legNames[l.Leg] = true
		if len(l.Groups) == 0 {
			t.Errorf("leg %q names no test groups", l.Leg)
		}
		for _, g := range l.Groups {
			if !groups[g] {
				t.Errorf("leg %q names %q, which is no test group in prometheus-rules-test.yml", l.Leg, g)
			}
			if prev, dup := inLeg[g]; dup {
				t.Errorf("test group %q is in leg %q and in leg %q; it would run twice", g, prev, l.Leg)
			}
			inLeg[g] = l.Leg
		}
	}
	for _, g := range rt.Tests {
		if _, ok := inLeg[g.Name]; !ok && g.Name != "" {
			t.Errorf("test group %q is in no leg of the alert-rules matrix: CI would never run it", g.Name)
		}
	}

	// The step.
	var testSteps []ciStep
	for _, s := range job.Steps {
		cmds := stripShellComments(s.Run)
		if strings.Contains(cmds, "promtool") && strings.Contains(cmds, "test rules /cfg/prometheus-rules-test.yml") {
			testSteps = append(testSteps, s)
		}
	}
	if len(testSteps) != 1 {
		t.Fatalf("%d alert-rules steps run promtool test rules /cfg/prometheus-rules-test.yml, want exactly 1", len(testSteps))
	}
	st := testSteps[0]
	if st.If != "" {
		t.Errorf("the promtool step has its own if %q; every leg must run it", st.If)
	}
	if t.Failed() {
		return
	}

	bin := t.TempDir()
	writeExecutable(t, bin, "docker", fakeDocker)
	dir := root
	if st.WorkingDirectory != "" {
		dir = filepath.Join(root, st.WorkingDirectory)
	}

	type outcome struct {
		ok      bool
		output  string
		calls   []string
		runArgs []string
	}
	runLeg := func(t *testing.T, l leg, testcases, checkExit, testExit int) outcome {
		t.Helper()
		tmp := t.TempDir()
		vals := map[string]string{
			"join(matrix.groups, ' ')": strings.Join(l.Groups, " "),
			"matrix.leg":               l.Leg,
		}
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

	for _, l := range legs {
		t.Run("leg "+l.Leg, func(t *testing.T) {
			n := len(l.Groups)
			o := runLeg(t, l, n, 0, 0)
			if !o.ok {
				t.Fatalf("the leg fails although promtool passed and ran its %d groups:\n%s", n, o.output)
			}
			checked := false
			for _, c := range o.calls {
				if strings.Contains(c, "promtool") && strings.Contains(c, "check rules /cfg/prometheus-rules.yml") {
					checked = true
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
			for g := range groups {
				if re.MatchString(g) != mine[g] {
					t.Errorf("--run=%q matches group %q: %v, but the leg names it: %v", o.runArgs[0], g, re.MatchString(g), mine[g])
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
				if o := runLeg(t, l, b.testcases, b.checkExit, b.testExit); o.ok {
					t.Errorf("the leg passes when %s:\n%s", b.what, o.output)
				}
			}
		})
	}
}
