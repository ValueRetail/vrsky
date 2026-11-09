package managementapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `go build .` inside src/cmd/<name>/ leaves a binary named <name> in that
// directory. Git would list it as untracked, so each command needs an anchored
// /src/cmd/<name>/<name> line in the root .gitignore. Adding a command without
// that line is how a 60 MB binary ends up in a commit.
func TestGitignoreCoversServiceBinaries(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	body, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read root .gitignore: %v", err)
	}
	lines := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		lines[strings.TrimSpace(line)] = true
	}

	entries, err := os.ReadDir(filepath.Join(root, "src", "cmd"))
	if err != nil {
		t.Fatalf("read src/cmd: %v", err)
	}
	found := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		found++
		want := "/src/cmd/" + e.Name() + "/" + e.Name()
		if !lines[want] {
			t.Errorf(".gitignore has no %q line; `go build .` in src/cmd/%s would leave an untracked binary. Add that line to the Build Artifacts & Binaries section", want, e.Name())
		}
	}
	if found == 0 {
		t.Fatal("found no directories in src/cmd; the layout changed, update this test")
	}
}
