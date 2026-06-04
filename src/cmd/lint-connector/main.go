// lint-connector — #98 static check (deferred from #83).
//
// Enforces that every cmd/* worker built on the Connector SDK is wired the way
// the SDK expects, so nobody half-migrates a connector or quietly reaches
// around the runner:
//
//  1. Run wiring — a package that imports pkg/sdk MUST hand control to the
//     runner by calling one of sdk.RunProducer / RunConsumer / RunFilter /
//     RunConverter. A worker that embeds sdk.Base* but never calls Run* would
//     compile yet never start its subscription/health/lifecycle.
//
//  2. No SDK bypass — an SDK connector MUST NOT also import pkg/messaging
//     directly. The SDK owns NATS/JetStream; connectors publish through the
//     injected publish closure and subscribe via res.NATS. Importing the
//     low-level publisher/subscriber is the pre-SDK pattern this migration
//     removed.
//
// Escape hatch: a file in the package containing the comment
// "lint:connector-ok" exempts that package from rule 2 — for the rare
// legitimate case (e.g. tenant-consumer's cross-tenant bridge, which owns its
// own durable subscription by design).
//
// Packages that do not import pkg/sdk are ignored: they are either not
// connectors (management-api, the lint tools, migrate-secrets) or workers not
// yet migrated (tracked separately). This check is about the SDK contract, not
// about forcing migration.
//
// Usage:
//
//	go run ./cmd/lint-connector
//
// Exits non-zero on any violation. Wire into Makefile / CI.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	sdkPkg       = "github.com/ValueRetail/vrsky/pkg/sdk"
	messagingPkg = "github.com/ValueRetail/vrsky/pkg/messaging"
	suppress     = "lint:connector-ok"
)

func main() {
	root := os.Getenv("LINT_CONNECTOR_ROOT")
	if root == "" {
		root = "cmd"
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-connector: cannot read %s: %v\n", root, err)
		os.Exit(2)
	}

	var violations []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		info, err := analyzeDir(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lint-connector: failed to analyze %s: %v\n", dir, err)
			os.Exit(2)
		}
		violations = append(violations, info.violations(e.Name())...)
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		fmt.Fprintln(os.Stderr, "connector lint failed:")
		for _, v := range violations {
			fmt.Fprintln(os.Stderr, "  -", v)
		}
		os.Exit(1)
	}
	fmt.Println("connector lint: ok")
}

// pkgInfo is the analysis result for one cmd/<name> package.
type pkgInfo struct {
	importsSDK       bool
	importsMessaging bool
	runCalled        bool
	suppressed       bool
}

// violations returns the lint failures for a package (empty if it passes or is
// not an SDK connector).
func (p pkgInfo) violations(name string) []string {
	if !p.importsSDK {
		return nil // not an SDK connector — out of scope for this check
	}
	var out []string
	if !p.runCalled {
		out = append(out, fmt.Sprintf("cmd/%s imports pkg/sdk but never calls sdk.Run{Producer,Consumer,Filter,Converter} — hand control to the runner in main()", name))
	}
	if p.importsMessaging && !p.suppressed {
		out = append(out, fmt.Sprintf("cmd/%s is an SDK connector but imports pkg/messaging directly — publish via the injected closure and subscribe via res.NATS, or add a // %s comment for a deliberate exception", name, suppress))
	}
	return out
}

// analyzeDir parses every non-test .go file in dir and aggregates the signals.
func analyzeDir(dir string) (pkgInfo, error) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return pkgInfo{}, err
	}

	var info pkgInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		// ParseComments so suppression is honoured only in real comments, not
		// in string literals that happen to contain the token.
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return pkgInfo{}, fmt.Errorf("parse %s: %w", path, err)
		}
		analyzeFile(f, &info)
	}
	return info, nil
}

// analyzeFile folds one file's imports, sdk.Run* calls, and suppression
// comments into info.
func analyzeFile(f *ast.File, info *pkgInfo) {
	// Suppression marker — only when it appears in an actual comment.
	for _, cg := range f.Comments {
		if strings.Contains(cg.Text(), suppress) {
			info.suppressed = true
			break
		}
	}

	sdkAliases := map[string]bool{}
	dotImportedSDK := false
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		switch path {
		case sdkPkg:
			info.importsSDK = true
			switch {
			case imp.Name == nil:
				sdkAliases["sdk"] = true
			case imp.Name.Name == ".":
				dotImportedSDK = true // sdk.RunX → bare RunX(...)
			default:
				sdkAliases[imp.Name.Name] = true
			}
		case messagingPkg:
			info.importsMessaging = true
		}
	}
	if len(sdkAliases) == 0 && !dotImportedSDK {
		return // no sdk import in this file → no Run* call to find here
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr: // sdk.RunConsumer(...)
			if pkgIdent, ok := fun.X.(*ast.Ident); ok &&
				sdkAliases[pkgIdent.Name] && strings.HasPrefix(fun.Sel.Name, "Run") {
				info.runCalled = true
			}
		case *ast.Ident: // dot-imported: RunConsumer(...)
			if dotImportedSDK && strings.HasPrefix(fun.Name, "Run") {
				info.runCalled = true
			}
		}
		return true
	})
}
