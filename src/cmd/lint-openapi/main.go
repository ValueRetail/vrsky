// Command lint-openapi enforces that every HTTP route registered on the
// management-API mux is documented in the OpenAPI registry (#94). It mirrors the
// lint-tenant-filter / lint-connector custom-linter pattern: pure AST analysis,
// run via `go run ./cmd/lint-openapi`, and wired into `make lint` + CI so adding
// a route without a corresponding apiRoutes entry fails the build.
//
// It compares two sets of route-pattern strings, both taken verbatim from
// source so the match is exact:
//   - the first string arg of every mux.Handle / mux.HandleFunc call in
//     pkg/managementapi/handler.go (the routes actually served), and
//   - the Pattern (first field) of every entry in apiRoutes in
//     pkg/managementapi/openapi_registry.go (the routes documented).
//
// Any served pattern missing from the registry is an error.
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
	pkgDir       = "pkg/managementapi"
	registryFile = "pkg/managementapi/openapi_registry.go"
)

// Meta endpoints that serve the docs themselves — not part of the documented
// API surface, so they don't need a registry entry.
var exempt = map[string]bool{
	"GET /openapi.json": true,
	"GET /docs":         true,
	"GET /status":       true, // HTML status page; /status.json is the documented API
}

func main() {
	served, err := muxPatterns(pkgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-openapi: %v\n", err)
		os.Exit(2)
	}
	documented, err := registryPatterns(registryFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-openapi: %v\n", err)
		os.Exit(2)
	}

	var missing []string
	for p := range served {
		if exempt[p] {
			continue
		}
		if !documented[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		fmt.Fprintln(os.Stderr, "lint-openapi: these routes are served but missing from the OpenAPI registry (pkg/managementapi/openapi_registry.go):")
		for _, p := range missing {
			fmt.Fprintf(os.Stderr, "  - %q\n", p)
		}
		fmt.Fprintln(os.Stderr, "Add an apiRoutes entry (Pattern must match the mux string exactly).")
		os.Exit(1)
	}
	fmt.Printf("lint-openapi: ok (%d routes served, all documented)\n", len(served))
}

// muxPatterns returns the set of first-string-arg patterns from every
// mux.Handle / mux.HandleFunc call across all non-test .go files in dir. It
// scans the whole package (not just handler.go) so routes registered in other
// files — e.g. RegisterAPIConsumerRoutes in api_consumer_handler.go — are also
// enforced against the OpenAPI registry.
func muxPatterns(dir string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, _, err := parseFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok || recv.Name != "mux" {
				return true
			}
			if sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc" {
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			if lit := stringLit(call.Args[0]); lit != "" {
				out[lit] = true
			}
			return true
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no mux route registrations found in %s (parser drift?)", dir)
	}
	return out, nil
}

// registryPatterns returns the set of Pattern (first field) values from the
// apiRoutes composite literal.
func registryPatterns(path string) (map[string]bool, error) {
	f, _, err := parseFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if name.Name != "apiRoutes" || i >= len(vs.Values) {
				continue
			}
			lit, ok := vs.Values[i].(*ast.CompositeLit) // []apiRoute{...}
			if !ok {
				continue
			}
			for _, el := range lit.Elts {
				entry, ok := el.(*ast.CompositeLit) // {Pattern, Method, ...}
				if !ok || len(entry.Elts) == 0 {
					continue
				}
				if p := stringLit(entry.Elts[0]); p != "" {
					out[p] = true
				}
			}
		}
		return true
	})
	if len(out) == 0 {
		return nil, fmt.Errorf("no apiRoutes entries found in %s (parser drift?)", path)
	}
	return out, nil
}

func stringLit(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}

func parseFile(path string) (*ast.File, *token.FileSet, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return f, fset, nil
}
