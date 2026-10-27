package managementapi

import (
	"bufio"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// copyCommand matches a shell copy (`cp`, Windows `copy`, PowerShell
// `Copy-Item`) and captures its first path argument, skipping flags.
var copyCommand = regexp.MustCompile("(?i)(?:^|[\\s;&|`(])(?:cp|copy|copy-item)(?:\\s+-{1,2}[A-Za-z-]+)*\\s+[\"'`]?([^\\s\"'`]+)")

// copiesRootEnvExample reports whether a line tells the reader to copy the
// repo-root .env.example. A copy of ui/.env.example or any other
// subdirectory's file doesn't count.
func copiesRootEnvExample(line string) bool {
	for _, m := range copyCommand.FindAllStringSubmatch(line, -1) {
		src := strings.TrimPrefix(strings.TrimPrefix(m[1], "./"), `.\`)
		if src == ".env.example" {
			return true
		}
	}
	return false
}

// envAssignment matches `NAME=value` with an optional leading `export `.
var envAssignment = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)

// parseEnvExample returns the variables a .env file assigns, and the line
// numbers of non-blank, non-comment lines that aren't assignments.
func parseEnvExample(body string) (vars []string, bad []int) {
	sc := bufio.NewScanner(strings.NewReader(body))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := envAssignment.FindStringSubmatch(line); m != nil {
			vars = append(vars, m[1])
		} else {
			bad = append(bad, n)
		}
	}
	return vars, bad
}

// composeVarRef matches a compose interpolation: `$NAME`, `${NAME}`,
// `${NAME:-x}`, `${NAME-x}`, `${NAME:?x}` and so on.
var composeVarRef = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// composeInterpolatedVars returns the variables a compose file reads from the
// environment (and so from .env). `$$` is compose's escape for a literal `$`
// and whole-line YAML comments are not read, so both are dropped first.
func composeInterpolatedVars(body string) map[string]bool {
	vars := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		line = strings.ReplaceAll(line, "$$", "")
		for _, m := range composeVarRef.FindAllStringSubmatch(line, -1) {
			vars[m[1]] = true
		}
	}
	return vars
}

// gitignoreIgnoresRootFile reports whether the root .gitignore ignores a
// file at the repo root. It handles the rules that can hit a root file: plain
// globs, a leading `/` or `**/`, and `!` negation (the last match wins).
// Directory-only rules (trailing `/`) and rules with an inner `/` can't match
// a root file and are skipped.
func gitignoreIgnoresRootFile(gitignore, name string) (bool, string) {
	ignored, rule := false, ""
	for _, raw := range strings.Split(gitignore, "\n") {
		p := strings.TrimSpace(raw)
		if p == "" || strings.HasPrefix(p, "#") || strings.HasSuffix(p, "/") {
			continue
		}
		negate := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		p = strings.TrimPrefix(strings.TrimPrefix(p, "/"), "**/")
		if strings.Contains(p, "/") {
			continue
		}
		if ok, err := path.Match(p, name); err == nil && ok {
			ignored, rule = !negate, raw
		}
	}
	return ignored, rule
}

// TestEnvExampleHelpers checks the parsers on small inputs, so the guards
// below can't pass for the wrong reason.
func TestEnvExampleHelpers(t *testing.T) {
	copies := map[string]bool{
		"cp .env.example .env":                        true,
		"cp .env.example .env          # set the key": true,
		"copy .env.example .env":                      true,
		"Copy-Item .env.example .env":                 true,
		"cp -n ./.env.example .env":                   true,
		"`cp .env.example .env`":                      true,
		"cd vrsky && cp .env.example .env":            true,
		"cp ui/.env.example ui/.env":                  false,
		"cp src/cmd/x/.env.example .env":              false,
		"copy ui\\.env.example ui\\.env":              false,
		"the passwords from `.env.example`":           false,
		"cp .env.example.bak .env":                    false,
		"scp .env.example host:":                      false,
	}
	for line, want := range copies {
		if got := copiesRootEnvExample(line); got != want {
			t.Errorf("copiesRootEnvExample(%q) = %v; want %v", line, got, want)
		}
	}

	vars, bad := parseEnvExample("# c\n\nA=1\nexport B=\n  C = x\nnot an assignment\n#D=1\n")
	if strings.Join(vars, ",") != "A,B,C" || len(bad) != 1 || bad[0] != 6 {
		t.Errorf("parseEnvExample = %v, bad lines %v; want [A B C], bad [6]", vars, bad)
	}

	compose := strings.Join([]string{
		`a: "${A}"`, `b: "${B:-x}"`, `c: "${C-x}"`, `d: "${D:?need D}"`,
		`e: $E`, `f: "$${F}"`, `# g: ${G}`, `h: "${H:+set}"`,
	}, "\n")
	got := composeInterpolatedVars(compose)
	for _, v := range []string{"A", "B", "C", "D", "E", "H"} {
		if !got[v] {
			t.Errorf("composeInterpolatedVars missed %s", v)
		}
	}
	for _, v := range []string{"F", "G"} {
		if got[v] {
			t.Errorf("composeInterpolatedVars counted %s, which compose doesn't read", v)
		}
	}

	ignoreCases := []struct {
		gitignore string
		want      bool
	}{
		{".env\n.env.local\n.env.*.local\n", false},
		{".env*\n", true},
		{"/.env.*\n", true},
		{"**/.env.example\n", true},
		{".env*\n!.env.example\n", false},
		{"!.env.example\n.env*\n", true},
		{"ui/.env.example\n", false},
		{".env.example/\n", false},
	}
	for _, tc := range ignoreCases {
		if got, _ := gitignoreIgnoresRootFile(tc.gitignore, ".env.example"); got != tc.want {
			t.Errorf("gitignoreIgnoresRootFile(%q) = %v; want %v", tc.gitignore, got, tc.want)
		}
	}
}

// TestEnvExampleExistsWhenDocsCopyIt: README and the operator docs tell
// people to run `cp .env.example .env` (#381). That command fails on a fresh
// clone unless the file is in the repo root. Copies of ui/.env.example and
// other subdirectory files are not checked here.
func TestEnvExampleExistsWhenDocsCopyIt(t *testing.T) {
	root := filepath.Join("..", "..", "..")

	docs := []string{"README.md"}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			docs = append(docs, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs/: %v", err)
	}
	if len(docs) < 2 {
		t.Fatal("found no .md files under docs/; the layout changed, update this test")
	}

	_, statErr := os.Stat(filepath.Join(root, ".env.example"))
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		t.Fatalf("stat .env.example: %v", statErr)
	}

	for _, doc := range docs {
		body, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if copiesRootEnvExample(line) && !exists {
				t.Errorf("%s:%d tells people to copy .env.example, but there is no .env.example in the repo root. "+
					"Add it (variables docker-compose.yml reads, dev placeholders only) or fix the doc.",
					filepath.ToSlash(doc), i+1)
			}
		}
	}
}

// TestEnvExampleVarsReadByCompose: .env.example is the template for the .env
// that docker-compose.yml reads. A variable compose never interpolates does
// nothing, so someone edits it in .env and wonders why the stack ignores it.
func TestEnvExampleVarsReadByCompose(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	body, err := os.ReadFile(filepath.Join(root, ".env.example"))
	if os.IsNotExist(err) {
		t.Skip("no .env.example in the repo root")
	}
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	vars, bad := parseEnvExample(string(body))
	for _, n := range bad {
		t.Errorf(".env.example:%d is neither a comment nor NAME=value; compose's .env parser expects NAME=value", n)
	}
	if len(vars) == 0 {
		t.Fatal(".env.example assigns no variables; either it lost its content or this parser broke")
	}

	compose, err := os.ReadFile(filepath.Join(root, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	read := composeInterpolatedVars(string(compose))

	var unread []string
	for _, v := range vars {
		if !read[v] {
			unread = append(unread, v)
		}
	}
	sort.Strings(unread)
	for _, v := range unread {
		t.Errorf(".env.example sets %s, but docker-compose.yml never reads ${%s}; "+
			"use it in compose (e.g. \"${%s:-default}\") or drop it from .env.example", v, v, v)
	}
}

// TestEnvExampleNotGitignored: .env is ignored so real secrets stay out of
// git, but .env.example has to be committed or the docs' `cp` step fails on a
// fresh clone. A broad rule like `.env*` would hide it without any error.
func TestEnvExampleNotGitignored(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	if _, err := os.Stat(filepath.Join(root, ".env.example")); os.IsNotExist(err) {
		t.Skip("no .env.example in the repo root")
	}
	body, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read root .gitignore: %v", err)
	}
	if ignored, rule := gitignoreIgnoresRootFile(string(body), ".env.example"); ignored {
		t.Errorf("root .gitignore rule %q ignores .env.example; add `!.env.example` after it so the template gets committed", rule)
	}
}
