package io

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every test that reaches the public internet opts in through
// skipUnlessLiveAPIs, and the met.no host is named in this package's test
// file only: a merge must never wait on, or fail because of, a service
// outside GitHub and the repository's own containers
// (plans/live-api-tests.md). The host is assembled so this file does not
// trip its own check.
func TestLiveAPITestsAreOptIn(t *testing.T) {
	const file = "api_input_test.go"
	liveHost := strings.Join([]string{"api", "met", "no"}, ".")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	funcs := regexp.MustCompile(`(?m)^func (TestRealAPI_\w+)\(t \*testing\.T\) \{\n`).FindAllStringSubmatchIndex(src, -1)
	if len(funcs) < 3 {
		t.Fatalf("found %d TestRealAPI_ functions in %s, expected the three met.no tests", len(funcs), file)
	}
	for _, m := range funcs {
		name := src[m[2]:m[3]]
		rest := src[m[1]:]
		if i := strings.Index(rest, "\nfunc "); i >= 0 {
			rest = rest[:i]
		}
		if !strings.HasPrefix(strings.TrimSpace(rest), "skipUnlessLiveAPIs(t)") {
			t.Errorf("%s does not start with skipUnlessLiveAPIs(t); it would reach %s in CI", name, liveHost)
		}
	}

	root := filepath.Join("..", "..")
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.Base(path) == file && filepath.Base(filepath.Dir(path)) == "io" {
			return nil
		}
		b, err := os.ReadFile(path) //nolint:gosec // paths come from walking the repo
		if err != nil {
			return err
		}
		if strings.Contains(string(b), liveHost) {
			t.Errorf("%s names %s; live tests live in pkg/io/%s behind skipUnlessLiveAPIs", path, liveHost, file)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
