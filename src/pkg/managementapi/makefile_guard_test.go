package managementapi

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
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
// `$(MAKE) -C src <target>`, with or without a leading `@`.
var forwardLine = regexp.MustCompile(`^\t@?\$\(MAKE\)\s+-C\s+src\s+([A-Za-z0-9_.-]+)\s*$`)

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
				continue
			}
			forwards++
			if _, ok := src[m[1]]; !ok {
				t.Errorf("root Makefile target %q forwards to `make -C src %s`, but src/Makefile has no %q rule",
					name, m[1], m[1])
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
