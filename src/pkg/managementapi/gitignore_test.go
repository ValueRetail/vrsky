package managementapi

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// `go build ./cmd/<name>` without -o, run in src/, writes the binary to
// src/<name>. The root .gitignore lists each of those paths by name. A list
// that drifted from src/cmd/ is how three 34-64 MB binaries ended up
// committed (#360), so every command needs its line.
//
// The lines are anchored names rather than a pattern because a pattern that
// catches any binary at src/ root has to be an allowlist, and that would
// silently ignore the next real file someone adds there. The other direction
// keeps the names honest: every /src/<name> line must be a command, and none
// may name a directory at src/ root, so the list can never hide source.
func TestGitignoreCoversCmdBinaries(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	body, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read root .gitignore: %v", err)
	}
	listed := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^/src/([^/\s]+)$`).FindAllStringSubmatch(string(body), -1) {
		listed[m[1]] = true
	}

	cmdDirs, err := os.ReadDir(filepath.Join(root, "src", "cmd"))
	if err != nil {
		t.Fatalf("read src/cmd: %v", err)
	}
	commands := map[string]bool{}
	for _, d := range cmdDirs {
		if d.IsDir() {
			commands[d.Name()] = true
		}
	}
	if len(commands) == 0 {
		t.Fatal("found no directories in src/cmd; the layout changed, update this test")
	}

	for name := range commands {
		if !listed[name] {
			t.Errorf(".gitignore has no /src/%s line; `go build ./cmd/%s` in src/ would leave an untracked binary", name, name)
		}
	}

	srcEntries, err := os.ReadDir(filepath.Join(root, "src"))
	if err != nil {
		t.Fatalf("read src: %v", err)
	}
	srcDirs := map[string]bool{}
	for _, e := range srcEntries {
		if e.IsDir() {
			srcDirs[e.Name()] = true
		}
	}
	for name := range listed {
		if !commands[name] {
			t.Errorf(".gitignore line /src/%s names no directory in src/cmd; drop it or add the command", name)
		}
		if srcDirs[name] {
			t.Errorf(".gitignore line /src/%s matches the directory src/%s and would hide its files", name, name)
		}
	}
}
