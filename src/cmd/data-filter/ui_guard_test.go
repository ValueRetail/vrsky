package main

// Drift guard between the filter and the builder (#851 review): the editor
// offers the operators in FILTER_OPERATORS (ui/src/components/Pipeline/
// PropertyEditor.tsx), and evaluateRule passes any operator it does not know.
// An operator offered there and missing here would let every message through.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"testing"
)

func TestUIFilterOperatorsMatchEvaluateRule(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "ui", "src", "components", "Pipeline", "PropertyEditor.tsx"))
	if err != nil {
		t.Skipf("UI source not available (%v), drift guard skipped", err)
	}
	block := regexp.MustCompile(`(?s)const FILTER_OPERATORS[^=]*= \[(.*?)\n\]`).FindSubmatch(src)
	if block == nil {
		t.Fatal("could not find FILTER_OPERATORS in PropertyEditor.tsx; the file changed shape, update this test")
	}
	var ui []string
	for _, m := range regexp.MustCompile(`value: '([^']+)'`).FindAllSubmatch(block[1], -1) {
		ui = append(ui, string(m[1]))
	}
	sort.Strings(ui)

	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	var run []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "evaluateRule" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			if sel, ok := sw.Tag.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Operator" {
				return true
			}
			for _, st := range sw.Body.List {
				for _, e := range st.(*ast.CaseClause).List {
					if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						s, _ := strconv.Unquote(lit.Value)
						run = append(run, s)
					}
				}
			}
			return false
		})
	}
	sort.Strings(run)
	if len(run) == 0 {
		t.Fatal("found no `switch rule.Operator` cases in evaluateRule; the code changed shape, update this test")
	}
	if !reflect.DeepEqual(ui, run) {
		t.Errorf("FILTER_OPERATORS in PropertyEditor.tsx = %v, evaluateRule runs %v; keep them the same (an unknown operator passes every message)", ui, run)
	}
}
