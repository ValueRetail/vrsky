package managementapi

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// makeRule is one rule parsed from a Makefile: its documentation (the text
// after `## ` on the rule line, which `make help` prints) and its recipe lines.
type makeRule struct {
	doc    string
	recipe []string
}

// ruleLine matches a rule line: one or more target names, then a colon that
// is not the start of `:=` or `::=`. Variable assignments (`X := ...`,
// `X ?= ...`, `X = ...`) and recipe lines (which start with a tab) don't match.
var ruleLine = regexp.MustCompile(`^([A-Za-z0-9_.-]+(?:[ \t]+[A-Za-z0-9_.-]+)*)[ \t]*:([^=:].*)?$`)

// forwardLine matches a recipe that hands the target to src/Makefile:
// `$(MAKE) -C src <target>`, with or without a leading `@`. The target is a
// name or `$@` (the rule's own name).
var forwardLine = regexp.MustCompile(`^\t@?\$\(MAKE\)\s+-C\s+src\s+([A-Za-z0-9_.-]+|\$@)\s*$`)

// parseMakefile reads a Makefile and returns its rules by target name.
// Special targets such as .PHONY and .DEFAULT_GOAL are left out.
func parseMakefile(t *testing.T, path string) map[string]*makeRule {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }() // read-only; a close error changes nothing

	rules := map[string]*makeRule{}
	var current []*makeRule // the rule(s) the following recipe lines belong to
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "\t") {
			for _, r := range current {
				r.recipe = append(r.recipe, line)
			}
			continue
		}
		m := ruleLine.FindStringSubmatch(line)
		if m == nil {
			if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "#") {
				current = nil // a variable or directive ends the recipe
			}
			continue
		}
		doc := ""
		if i := strings.Index(m[2], "## "); i >= 0 {
			doc = strings.TrimSpace(m[2][i+len("## "):])
		}
		current = nil
		for _, name := range strings.Fields(m[1]) {
			if strings.HasPrefix(name, ".") {
				continue
			}
			r := &makeRule{doc: doc}
			rules[name] = r
			current = append(current, r)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(rules) == 0 {
		t.Fatalf("parsed no rules from %s; the format changed, update this test", path)
	}
	return rules
}

// sortedTargets returns the rule names in order, so failures read the same
// on every run.
func sortedTargets(rules map[string]*makeRule) []string {
	names := make([]string, 0, len(rules))
	for name := range rules {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// makefiles parses the root Makefile and src/Makefile.
func makefiles(t *testing.T) (root, src map[string]*makeRule) {
	t.Helper()
	repo := filepath.Join("..", "..", "..")
	return parseMakefile(t, filepath.Join(repo, "Makefile")),
		parseMakefile(t, filepath.Join(repo, "src", "Makefile"))
}

// TestRootMakefileForwardsOnlyExistingSrcTargets guards issue #361: a root
// rule whose recipe is `$(MAKE) -C src <target>` fails with "No rule to make
// target" when src/Makefile has no such rule. Every forward must point at a
// target src/Makefile defines.
func TestRootMakefileForwardsOnlyExistingSrcTargets(t *testing.T) {
	root, src := makefiles(t)

	forwards := 0
	for _, name := range sortedTargets(root) {
		for _, line := range root[name].recipe {
			m := forwardLine.FindStringSubmatch(line)
			if m == nil {
				// Anything else that calls make or reaches into src is a
				// forward this test can't read; skipping it would hide a
				// broken one.
				if strings.Contains(line, "$(MAKE)") || strings.Contains(line, "-C src") {
					t.Errorf("root Makefile target %q has an unrecognised forward %q; update this test",
						name, strings.TrimSpace(line))
				}
				continue
			}
			forwards++
			target := m[1]
			if target == "$@" {
				target = name
			}
			if _, ok := src[target]; !ok {
				t.Errorf("root Makefile target %q forwards to `make -C src %s`, but src/Makefile has no %q rule",
					name, target, target)
			}
		}
	}
	if forwards == 0 {
		t.Fatal("found no `$(MAKE) -C src <target>` forwards in the root Makefile; the format changed, update this test")
	}
}

// TestRootMakefileHelpTargetsAreRunnable guards issue #361: root `make help`
// prints src/Makefile's `##`-documented targets and then the root Makefile's
// own. Anything it lists must run from the repo root, so every documented
// target in either file needs a rule in the root Makefile.
func TestRootMakefileHelpTargetsAreRunnable(t *testing.T) {
	root, src := makefiles(t)

	for _, mf := range []struct {
		file  string
		rules map[string]*makeRule
	}{
		{"src/Makefile", src},
		{"Makefile", root},
	} {
		for _, name := range sortedTargets(mf.rules) {
			if mf.rules[name].doc == "" {
				continue
			}
			if _, ok := root[name]; !ok {
				t.Errorf("root `make help` lists %q (documented in %s), but the root Makefile has no %q rule",
					name, mf.file, name)
			}
		}
	}
}

// TestRootMakefileHelpListsCoreStackTargets guards issue #361: the core
// compose targets are the entry point for running VRSky on a laptop, so root
// `make help` must list each one with a one-line description (a `## ` comment
// on its rule line in the root Makefile).
func TestRootMakefileHelpListsCoreStackTargets(t *testing.T) {
	root, _ := makefiles(t)

	for _, name := range []string{"up-core", "up-core-remote", "down-core"} {
		t.Run(name, func(t *testing.T) {
			r, ok := root[name]
			if !ok {
				t.Fatalf("root Makefile has no %q rule", name)
			}
			if r.doc == "" {
				t.Errorf("root Makefile rule %q has no `## description`, so `make help` doesn't list it", name)
			}
		})
	}
}

// ansiEscape matches the colour codes `make help` wraps target names in.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// TestRootMakefileHelpOutputListsDocumentedTargets guards issue #361 on the
// outcome rather than the comments: it runs root `make help` and checks that
// every `##`-documented target in src/Makefile and the root Makefile shows up
// as a listed entry. Dropping the root help's own grep, for instance, hides
// up-core and friends while their `##` comments stay in place.
func TestRootMakefileHelpOutputListsDocumentedTargets(t *testing.T) {
	makeBin, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is not on PATH, so root `make help` can't run here; the static checks above still apply")
	}
	root, src := makefiles(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, makeBin, "-C", filepath.Join("..", "..", ".."), "help").CombinedOutput()
	if err != nil {
		t.Fatalf("root `make help` failed: %v\n%s", err, out)
	}

	// A listed entry is an indented line whose first word is the target.
	listed := map[string]bool{}
	for _, line := range strings.Split(ansiEscape.ReplaceAllString(string(out), ""), "\n") {
		if !strings.HasPrefix(line, " ") {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 1 {
			listed[fields[0]] = true
		}
	}

	for _, mf := range []struct {
		file  string
		rules map[string]*makeRule
	}{
		{"src/Makefile", src},
		{"Makefile", root},
	} {
		for _, name := range sortedTargets(mf.rules) {
			if mf.rules[name].doc != "" && !listed[name] {
				t.Errorf("%q is documented in %s, but root `make help` doesn't list it; output:\n%s",
					name, mf.file, out)
			}
		}
	}
}

// TestMakefileDockerBuildUIContextHoldsDockerfileSources guards issue #369:
// `make docker-build-ui` passed `../ui` as the build context while
// ui/Dockerfile copies `ui/...` paths from the repo root, so the build
// failed at the first COPY. This runs `make -n docker-build-ui` in src/,
// resolves the Dockerfile and the context the printed `docker build` uses,
// and checks every COPY and ADD source (other than `--from` stage copies)
// matches a file under that context. It doesn't need Docker, and it doesn't
// apply the root .dockerignore (plans/backlog.md item 22).
func TestMakefileDockerBuildUIContextHoldsDockerfileSources(t *testing.T) {
	makeBin, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is not on PATH, so `make -n docker-build-ui` can't run here")
	}
	srcDir := filepath.Join("..", "..")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, makeBin, "-n", "-C", srcDir, "docker-build-ui").CombinedOutput()
	if err != nil {
		t.Fatalf("`make -n docker-build-ui` failed: %v\n%s", err, out)
	}

	var args []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "docker" && fields[1] == "build" {
			if args != nil {
				t.Fatalf("`make -n docker-build-ui` printed more than one `docker build`; update this test:\n%s", out)
			}
			args = fields[2:]
		}
	}
	if args == nil {
		t.Fatalf("`make -n docker-build-ui` printed no `docker build` command:\n%s", out)
	}

	// The context is the one positional argument; -f names the Dockerfile,
	// which defaults to <context>/Dockerfile.
	var buildCtx, dockerfile string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-f" || a == "--file":
			i++
			if i < len(args) {
				dockerfile = args[i]
			}
		case strings.HasPrefix(a, "--file="):
			dockerfile = strings.TrimPrefix(a, "--file=")
		case a == "-t" || a == "--tag" || a == "--build-arg" || a == "--target" || a == "--platform":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			if buildCtx != "" {
				t.Fatalf("two build contexts (%q, %q) in `docker build %s`; update this test",
					buildCtx, a, strings.Join(args, " "))
			}
			buildCtx = a
		}
	}
	if buildCtx == "" {
		t.Fatalf("no build context in `docker build %s`", strings.Join(args, " "))
	}
	buildCtx = filepath.Join(srcDir, buildCtx) // make runs the recipe in src/
	if dockerfile == "" {
		dockerfile = filepath.Join(buildCtx, "Dockerfile")
	} else {
		dockerfile = filepath.Join(srcDir, dockerfile)
	}

	df, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatalf("read the Dockerfile `docker build` names: %v", err)
	}
	copies := 0
	for _, line := range strings.Split(string(df), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (!strings.EqualFold(fields[0], "COPY") && !strings.EqualFold(fields[0], "ADD")) {
			continue
		}
		// ADD is checked like COPY. Two forms this parser can't read get a
		// hard stop instead of a silent pass: a COPY/ADD continued on the
		// next line, and an ADD that fetches a URL rather than a context path.
		if strings.HasSuffix(strings.TrimSpace(line), `\`) {
			t.Fatalf("%s has a %s split over several lines (%q); update this test", dockerfile, fields[0], line)
		}
		if len(fields) < 3 {
			continue
		}
		var srcs []string
		fromStage := false
		for _, f := range fields[1 : len(fields)-1] { // the last field is the destination
			if strings.HasPrefix(f, "--from=") {
				fromStage = true
			}
			if !strings.HasPrefix(f, "--") {
				srcs = append(srcs, f)
			}
		}
		if fromStage {
			continue
		}
		for _, s := range srcs {
			if strings.Contains(s, "://") || strings.HasPrefix(s, "git@") {
				t.Fatalf("%s has %s from a remote source (%q); update this test", dockerfile, fields[0], s)
			}
			copies++
			matches, err := filepath.Glob(filepath.Join(buildCtx, s))
			if err != nil {
				t.Fatalf("bad COPY pattern %q in %s: %v", s, dockerfile, err)
			}
			if len(matches) == 0 {
				t.Errorf("%s copies %q, but the build context `make docker-build-ui` uses (%s) has nothing there",
					dockerfile, s, buildCtx)
			}
		}
	}
	if copies == 0 {
		t.Fatalf("found no COPY sources in %s; the format changed, update this test", dockerfile)
	}
}
