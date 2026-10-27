package managementapi

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
	"gopkg.in/yaml.v3"
)

// dockerfileCopySources returns the build-context paths that a Dockerfile's
// COPY and ADD instructions read: every argument except flags and the last
// one (the destination). `--from` copies read another stage, not the
// context, and are left out. Two forms this parser can't read stop the test
// instead of passing silently: a COPY/ADD continued on the next line, and an
// ADD that fetches a URL rather than a context path.
func dockerfileCopySources(t *testing.T, dockerfile string) []string {
	t.Helper()
	df, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatalf("read %s: %v", dockerfile, err)
	}
	var srcs []string
	for _, line := range strings.Split(string(df), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (!strings.EqualFold(fields[0], "COPY") && !strings.EqualFold(fields[0], "ADD")) {
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(line), `\`) {
			t.Fatalf("%s has a %s split over several lines (%q); update this test", dockerfile, fields[0], line)
		}
		if len(fields) < 3 {
			continue
		}
		var lineSrcs []string
		fromStage := false
		for _, f := range fields[1 : len(fields)-1] { // the last field is the destination
			if strings.HasPrefix(f, "--from=") {
				fromStage = true
			}
			if !strings.HasPrefix(f, "--") {
				lineSrcs = append(lineSrcs, f)
			}
		}
		if fromStage {
			continue
		}
		for _, s := range lineSrcs {
			if strings.Contains(s, "://") || strings.HasPrefix(s, "git@") {
				t.Fatalf("%s has %s from a remote source (%q); update this test", dockerfile, fields[0], s)
			}
		}
		srcs = append(srcs, lineSrcs...)
	}
	return srcs
}

// newDockerignoreMatcher parses a .dockerignore the way the Docker CLI and
// BuildKit do: ignorefile.ReadAll drops comments and blank lines, cleans
// each pattern with filepath.Clean and strips a leading "/", and
// patternmatcher applies `*`, `?`, `**` and `!` with the last match winning.
func newDockerignoreMatcher(t *testing.T, r io.Reader) *patternmatcher.PatternMatcher {
	t.Helper()
	patterns, err := ignorefile.ReadAll(r)
	if err != nil {
		t.Fatalf("read .dockerignore: %v", err)
	}
	pm, err := patternmatcher.New(patterns)
	if err != nil {
		t.Fatalf("bad .dockerignore pattern: %v", err)
	}
	return pm
}

// dockerignoreExcludes reports whether the build context leaves out rel, a
// path relative to the context root. A path is left out when it, or one of
// its parent directories, matches a pattern and no later `!` pattern takes
// it back; that's how BuildKit filters the context it sends.
func dockerignoreExcludes(t *testing.T, pm *patternmatcher.PatternMatcher, rel string) bool {
	t.Helper()
	excluded, err := pm.MatchesOrParentMatches(filepath.ToSlash(rel))
	if err != nil {
		t.Fatalf("match %q against .dockerignore: %v", rel, err)
	}
	return excluded
}

// contextHasSource reports whether a COPY/ADD source survives the
// .dockerignore: the source, globbed from the context root, must match a
// file the matcher lets through, or a directory with at least one such file
// under it. A wildcard with nothing left and a missing directory fail the
// COPY in Docker; a directory with every file filtered out copies as empty,
// which breaks the build just the same.
func contextHasSource(t *testing.T, pm *patternmatcher.PatternMatcher, root, src string) bool {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, src))
	if err != nil {
		t.Fatalf("bad COPY pattern %q: %v", src, err)
	}
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			t.Fatalf("stat %s: %v", m, err)
		}
		if !info.IsDir() {
			rel, err := filepath.Rel(root, m)
			if err != nil {
				t.Fatalf("relative path of %s: %v", m, err)
			}
			if !dockerignoreExcludes(t, pm, rel) {
				return true
			}
			continue
		}
		found := false
		err = filepath.WalkDir(m, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			if !dockerignoreExcludes(t, pm, rel) {
				found = true
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", m, err)
		}
		if found {
			return true
		}
	}
	return false
}

// excludedCopySources returns "<dockerfile>: COPY <src>" for every COPY/ADD
// source of the given Dockerfiles (paths relative to root) that the matcher
// keeps out of a build context rooted at root.
func excludedCopySources(t *testing.T, pm *patternmatcher.PatternMatcher, root string, dockerfiles []string) []string {
	t.Helper()
	var bad []string
	for _, df := range dockerfiles {
		srcs := dockerfileCopySources(t, filepath.Join(root, df))
		if len(srcs) == 0 {
			t.Fatalf("found no COPY sources in %s; the format changed, update this test", df)
		}
		for _, s := range srcs {
			if !contextHasSource(t, pm, root, s) {
				bad = append(bad, df+": COPY "+s)
			}
		}
	}
	return bad
}

// repoRootDockerfiles lists the Dockerfiles built with the repo root as the
// build context, relative to root: ui/Dockerfile and src/cmd/*/Dockerfile
// (what infrastructure/azure/build-push-acr.sh and the build-images job in
// build-push.yml pass to `az acr build --file ... .`), and every Dockerfile a
// `build:` in a docker-compose*.yml names. A compose build with another
// context, or a <Dockerfile>.dockerignore that BuildKit would read instead
// of the root file, stops the test: this guard only knows the root file.
func repoRootDockerfiles(t *testing.T, root string) []string {
	t.Helper()
	set := map[string]bool{"ui/Dockerfile": true}
	cmd, err := filepath.Glob(filepath.Join(root, "src", "cmd", "*", "Dockerfile"))
	if err != nil || len(cmd) == 0 {
		t.Fatalf("found no src/cmd/*/Dockerfile (%v); the layout changed, update this test", err)
	}
	for _, p := range cmd {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatalf("relative path of %s: %v", p, err)
		}
		set[filepath.ToSlash(rel)] = true
	}

	composeFiles, err := filepath.Glob(filepath.Join(root, "docker-compose*.yml"))
	if err != nil || len(composeFiles) == 0 {
		t.Fatalf("found no docker-compose*.yml at the repo root (%v); update this test", err)
	}
	for _, cf := range composeFiles {
		raw, err := os.ReadFile(cf)
		if err != nil {
			t.Fatalf("read %s: %v", cf, err)
		}
		var compose struct {
			Services map[string]struct {
				Build any `yaml:"build"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(raw, &compose); err != nil {
			t.Fatalf("parse %s: %v", cf, err)
		}
		for name, svc := range compose.Services {
			buildCtx, dockerfile := ".", "Dockerfile"
			switch b := svc.Build.(type) {
			case nil:
				continue
			case string:
				buildCtx = b
			case map[string]any:
				if c, ok := b["context"].(string); ok {
					buildCtx = c
				}
				if d, ok := b["dockerfile"].(string); ok {
					dockerfile = d
				}
			default:
				t.Fatalf("%s: service %s has a `build:` this test can't read (%T); update this test", cf, name, b)
			}
			if filepath.Clean(buildCtx) != "." {
				t.Fatalf("%s: service %s builds with context %q, not the repo root; this guard only checks the root .dockerignore, update it",
					filepath.Base(cf), name, buildCtx)
			}
			set[filepath.ToSlash(filepath.Clean(dockerfile))] = true
		}
	}

	var out []string
	for df := range set {
		if _, err := os.Stat(filepath.Join(root, df)); err != nil {
			t.Fatalf("Dockerfile %s: %v", df, err)
		}
		own := filepath.Join(root, df+".dockerignore")
		if _, err := os.Stat(own); err == nil {
			t.Fatalf("%s.dockerignore exists; BuildKit reads it instead of the root .dockerignore (az acr build doesn't), update this test", df)
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("stat %s: %v", own, err)
		}
		out = append(out, df)
	}
	sort.Strings(out)
	return out
}

// TestDockerignoreKeepsDockerfileSources guards issue #385: the root
// .dockerignore allowlists what the images COPY (`*`, then `!` re-includes),
// and every image builds with the repo root as context. Narrowing the
// allowlist (say `!ui` to `!ui/src`) still left every source on disk, so the
// old guard passed while `COPY ui/package*.json` failed, first seen as a red
// ACR build on main after the merge. This applies the root .dockerignore
// the way Docker does and checks that each COPY/ADD source of every
// repo-root Dockerfile still reaches the build context. It doesn't need
// Docker.
func TestDockerignoreKeepsDockerfileSources(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	f, err := os.Open(filepath.Join(root, ".dockerignore"))
	if err != nil {
		t.Fatalf("open the root .dockerignore: %v", err)
	}
	defer func() { _ = f.Close() }()
	pm := newDockerignoreMatcher(t, f)

	for _, bad := range excludedCopySources(t, pm, root, repoRootDockerfiles(t, root)) {
		t.Errorf("%s, but the root .dockerignore keeps it out of the build context", bad)
	}
}

// TestDockerignoreGuardCatchesMutations checks that the guard above catches
// the narrowings issue #385 is about. It edits the real root .dockerignore
// in memory (the file on disk is never touched), builds the matcher from
// the edited text and runs it over the real repo-root Dockerfiles. The
// unedited file must report nothing; every mutation must report the source
// it breaks. An edit that no longer applies to the file fails the test, so
// a reworded .dockerignore can't turn these cases into silent passes.
func TestDockerignoreGuardCatchesMutations(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, ".dockerignore"))
	if err != nil {
		t.Fatalf("read the root .dockerignore: %v", err)
	}
	dockerfiles := repoRootDockerfiles(t, root)

	// replaceLine swaps the one line equal to oldLine for newLine; an empty
	// newLine drops the line. It fails unless exactly one line matches.
	replaceLine := func(t *testing.T, text, oldLine, newLine string) string {
		t.Helper()
		hits := 0
		var out []string
		for _, l := range strings.Split(text, "\n") {
			if strings.TrimSpace(l) != oldLine {
				out = append(out, l)
				continue
			}
			hits++
			if newLine != "" {
				out = append(out, newLine)
			}
		}
		if hits != 1 {
			t.Fatalf("stale mutation: want exactly one %q line in the root .dockerignore, found %d", oldLine, hits)
		}
		return strings.Join(out, "\n")
	}

	tests := []struct {
		name       string
		oldLine    string // line to replace or drop; "" for none
		newLine    string // its replacement; "" drops oldLine
		appendLine string // line added at the end; "" for none
		want       []string
	}{
		{name: "unmodified file passes"},
		{
			name:    "!ui narrowed to !ui/src",
			oldLine: "!ui",
			newLine: "!ui/src",
			want: []string{
				"ui/Dockerfile: COPY ui/package*.json",
				"ui/Dockerfile: COPY ui/nginx.conf",
			},
		},
		{
			name:       "src/go.sum excluded at the end",
			appendLine: "src/go.sum",
			want:       []string{"src/cmd/management-api/Dockerfile: COPY src/go.sum"},
		},
		{
			name:       "infrastructure/migrations excluded at the end",
			appendLine: "infrastructure/migrations",
			want:       []string{"src/cmd/management-api/Dockerfile: COPY infrastructure/migrations"},
		},
		{
			name:    "!infrastructure/migrations dropped",
			oldLine: "!infrastructure/migrations",
			want:    []string{"src/cmd/management-api/Dockerfile: COPY infrastructure/migrations"},
		},
		{
			name:    "!src/pkg dropped",
			oldLine: "!src/pkg",
			want:    []string{"src/cmd/management-api/Dockerfile: COPY src/pkg/"},
		},
		{
			name:       "every ui json file excluded with **",
			appendLine: "ui/**/*.json",
			want: []string{
				"ui/Dockerfile: COPY ui/package*.json",
				"ui/Dockerfile: COPY ui/tsconfig*.json",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text := string(raw)
			if tc.oldLine != "" {
				text = replaceLine(t, text, tc.oldLine, tc.newLine)
			}
			if tc.appendLine != "" {
				text = strings.TrimRight(text, "\n") + "\n" + tc.appendLine + "\n"
			}
			pm := newDockerignoreMatcher(t, strings.NewReader(text))
			bad := excludedCopySources(t, pm, root, dockerfiles)

			if len(tc.want) == 0 {
				if len(bad) != 0 {
					t.Fatalf("the unmodified .dockerignore should pass, got:\n%s", strings.Join(bad, "\n"))
				}
				return
			}
			got := map[string]bool{}
			for _, b := range bad {
				got[b] = true
			}
			for _, w := range tc.want {
				if !got[w] {
					t.Errorf("guard did not report %q; it reported:\n%s", w, strings.Join(bad, "\n"))
				}
			}
		})
	}
}

// TestDockerignoreMatcherFollowsDockerDocs pins newDockerignoreMatcher and
// dockerignoreExcludes to the examples in Docker's .dockerignore docs
// (https://docs.docker.com/build/concepts/context/#dockerignore-files), plus
// the directory-then-child re-include the root file relies on.
func TestDockerignoreMatcherFollowsDockerDocs(t *testing.T) {
	tests := []struct {
		name     string
		ignore   string
		excluded map[string]bool // path relative to the context root -> left out?
	}{
		{
			name:   "comment lines are not patterns",
			ignore: "# comment\n",
			excluded: map[string]bool{
				"# comment": false,
				"comment":   false,
			},
		},
		{
			name:   "*/temp* matches temp* one directory down only",
			ignore: "*/temp*\n",
			excluded: map[string]bool{
				"somedir/temporary.txt":        true,
				"somedir/temp":                 true,
				"somedir/temp/inside.txt":      true, // a matched directory takes its files with it
				"temporary.txt":                false,
				"somedir/subdir/temporary.txt": false,
			},
		},
		{
			name:   "*/*/temp* matches temp* two directories down only",
			ignore: "*/*/temp*\n",
			excluded: map[string]bool{
				"somedir/subdir/temporary.txt": true,
				"somedir/temporary.txt":        false,
				"a/b/c/temporary.txt":          false,
			},
		},
		{
			name:   "temp? matches one extra character in the root",
			ignore: "temp?\n",
			excluded: map[string]bool{
				"tempa":         true,
				"tempb":         true,
				"tempb/file":    true,
				"temp":          false,
				"tempab":        false,
				"somedir/tempa": false,
			},
		},
		{
			name:   "**/*.go matches .go files at any depth, root included",
			ignore: "**/*.go\n",
			excluded: map[string]bool{
				"main.go":       true,
				"a/b/c/main.go": true,
				"a/b/c/main.md": false,
			},
		},
		{
			name:   "*.md then !README.md keeps only README.md",
			ignore: "*.md\n!README.md\n",
			excluded: map[string]bool{
				"README.md":  false,
				"CHANGES.md": true,
			},
		},
		{
			name:   "*.md, !README*.md, README-secret.md: the secret stays out",
			ignore: "*.md\n!README*.md\nREADME-secret.md\n",
			excluded: map[string]bool{
				"README.md":        false,
				"README-dev.md":    false,
				"README-secret.md": true,
				"CHANGES.md":       true,
			},
		},
		{
			name:   "*.md, README-secret.md, !README*.md: the last match lets the secret in",
			ignore: "*.md\nREADME-secret.md\n!README*.md\n",
			excluded: map[string]bool{
				"README.md":        false,
				"README-secret.md": false,
				"CHANGES.md":       true,
			},
		},
		{
			name:   "leading and trailing slashes are dropped",
			ignore: "/foo/bar/\n",
			excluded: map[string]bool{
				"foo/bar":       true,
				"foo/bar/x":     true,
				"foo/barbaz":    false,
				"other/foo/bar": false,
			},
		},
		{
			name:   "patterns go through filepath.Clean",
			ignore: "foo/./bar/../baz\n",
			excluded: map[string]bool{
				"foo/baz":   true,
				"foo/baz/x": true,
				"foo/bar":   false,
			},
		},
		{
			name:   "an excluded directory with a later ! re-include of a child",
			ignore: "dir\n!dir/keep\n",
			excluded: map[string]bool{
				"dir/keep":       false,
				"dir/keep/a.txt": false,
				"dir/drop.txt":   true,
				"dir/sub/x":      true,
				"other":          false,
			},
		},
		{
			name:   "allowlist: *, !dir, dir/*, !dir/keep",
			ignore: "*\n!dir\ndir/*\n!dir/keep\n",
			excluded: map[string]bool{
				"dir/keep":       false,
				"dir/keep/a.txt": false,
				"dir/drop":       true,
				"dir/drop/a.txt": true,
				"elsewhere.txt":  true,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := newDockerignoreMatcher(t, strings.NewReader(tc.ignore))
			for path, want := range tc.excluded {
				if got := dockerignoreExcludes(t, pm, path); got != want {
					t.Errorf("%q with .dockerignore %q: excluded = %v, want %v", path, tc.ignore, got, want)
				}
			}
		})
	}
}

// TestDockerfileCopySourcesSkipsStageCopies checks that `--from` copies,
// which read another build stage and not the context, are left out, while
// other flags are dropped and the context sources kept.
func TestDockerfileCopySourcesSkipsStageCopies(t *testing.T) {
	df := filepath.Join(t.TempDir(), "Dockerfile")
	content := strings.Join([]string{
		"FROM golang AS builder",
		"COPY --from=builder /app/dist /usr/share/nginx/html",
		"COPY --chown=1000:1000 --from=builder /build/app .",
		"COPY --chown=1000:1000 a.txt b.txt /dst/",
		"ADD c.tar.gz /dst/",
		"copy d.txt /dst/",
		"",
	}, "\n")
	if err := os.WriteFile(df, []byte(content), 0o600); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	got := dockerfileCopySources(t, df)
	want := []string{"a.txt", "b.txt", "c.tar.gz", "d.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("dockerfileCopySources = %v, want %v", got, want)
	}
}

// TestExcludedCopySourcesOnFakeContext runs excludedCopySources against a
// build context in a temp dir, for cases the real repo can't show on
// demand: a directory whose every file is ignored, a wildcard with nothing
// left, a missing source, and a `--from` copy of a path the context lacks.
func TestExcludedCopySourcesOnFakeContext(t *testing.T) {
	tests := []struct {
		name       string
		files      []string // created under the context root
		dockerfile string
		ignore     string
		want       []string
	}{
		{
			name:       "directory with every file ignored is reported",
			files:      []string{"assets/a.txt", "assets/sub/b.txt"},
			dockerfile: "COPY assets ./assets\n",
			ignore:     "assets/**/*.txt\n",
			want:       []string{"Dockerfile: COPY assets"},
		},
		{
			name:       "directory with one file left passes",
			files:      []string{"assets/a.txt", "assets/sub/b.json"},
			dockerfile: "COPY assets ./assets\n",
			ignore:     "assets/**/*.txt\n",
		},
		{
			name:       "wildcard with every match ignored is reported",
			files:      []string{"package.json", "package-lock.json"},
			dockerfile: "COPY package*.json ./\n",
			ignore:     "*\n!src\n",
			want:       []string{"Dockerfile: COPY package*.json"},
		},
		{
			name:       "missing source is reported",
			files:      []string{"keep.txt"},
			dockerfile: "COPY keep.txt ./\nCOPY gone.txt ./\n",
			want:       []string{"Dockerfile: COPY gone.txt"},
		},
		{
			name:       "--from copy of a path not in the context is skipped",
			files:      []string{"keep.txt"},
			dockerfile: "COPY keep.txt ./\nCOPY --from=builder /app/dist ./dist\n",
			ignore:     "*\n!keep.txt\n",
		},
		{
			name:       "child re-included under an excluded directory passes",
			files:      []string{"dir/drop.txt", "dir/keep/a.txt"},
			dockerfile: "COPY dir/keep ./keep\n",
			ignore:     "dir\n!dir/keep\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, f := range tc.files {
				p := filepath.Join(root, filepath.FromSlash(f))
				if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
					t.Fatalf("mkdir for %s: %v", f, err)
				}
				if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
					t.Fatalf("write %s: %v", f, err)
				}
			}
			dockerfile := []byte("FROM scratch\n" + tc.dockerfile)
			if err := os.WriteFile(filepath.Join(root, "Dockerfile"), dockerfile, 0o600); err != nil {
				t.Fatalf("write Dockerfile: %v", err)
			}
			pm := newDockerignoreMatcher(t, strings.NewReader(tc.ignore))
			got := excludedCopySources(t, pm, root, []string{"Dockerfile"})
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("excludedCopySources = %q, want %q", got, tc.want)
			}
		})
	}
}
