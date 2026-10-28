package main

// Drift guard between the converter and the builder's view of it (#458). The
// converter editor offers the mapping types in CONVERTER_MAPPING_TYPES, edits
// the FieldMapping keys named in the ConverterMapping type, and the template
// checks accept the node keys in CONNECTOR_KEYS.converter, all in
// ui/src/types/nodeConfig.ts. A type or key added here and not there is one
// the editor shows wrongly or drops (backlog item 63: `each` showed as
// "Rename"); one there and not here is one the converter skips without a word.

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
	"strings"
	"testing"
)

func readUINodeConfig(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "ui", "src", "types", "nodeConfig.ts"))
	if err != nil {
		t.Skipf("UI source not available (%v), drift guard skipped", err)
	}
	return string(b)
}

// uiMatch returns the first capture of re in src, failing when the markup the
// regexp expects is gone.
func uiMatch(t *testing.T, src, what string, re *regexp.Regexp) string {
	t.Helper()
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("could not find %s in nodeConfig.ts; the file changed shape, update this test", what)
	}
	return m[1]
}

var quoted = regexp.MustCompile(`'([^']*)'`)

func quotedStrings(s string) []string {
	var out []string
	for _, m := range quoted.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// jsonTags lists the json names of a struct's fields.
func jsonTags(v interface{}) []string {
	var out []string
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		name := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// mappingTypesRun reads the case labels of the `switch m.Type` in
// applyMappings, so the list comes from the code that runs, not a comment.
func mappingTypesRun(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	var out []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "applyMappings" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			sel, ok := sw.Tag.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Type" {
				return true
			}
			for _, st := range sw.Body.List {
				for _, e := range st.(*ast.CaseClause).List {
					if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, _ := strconv.Unquote(lit.Value); s != "" { // "" is rename
							out = append(out, s)
						}
					}
				}
			}
			return false
		})
	}
	if len(out) == 0 {
		t.Fatal("found no `switch m.Type` cases in applyMappings; the code changed shape, update this test")
	}
	sort.Strings(out)
	return out
}

func TestUIMappingTypesMatchConverter(t *testing.T) {
	src := readUINodeConfig(t)
	ui := quotedStrings(uiMatch(t, src, "CONVERTER_MAPPING_TYPES",
		regexp.MustCompile(`(?s)CONVERTER_MAPPING_TYPES: readonly string\[\] = \[(.*?)\]`)))
	if got := mappingTypesRun(t); !reflect.DeepEqual(ui, got) {
		t.Errorf("CONVERTER_MAPPING_TYPES in nodeConfig.ts = %v, applyMappings runs %v; keep them the same", ui, got)
	}
}

func TestUIConverterKeysMatchConfig(t *testing.T) {
	src := readUINodeConfig(t)
	ui := quotedStrings(uiMatch(t, src, "CONNECTOR_KEYS.converter",
		regexp.MustCompile(`(?s)\n  converter: \{\s*block: '',\s*keys: \[(.*?)\]`)))
	if got := jsonTags(ConverterNodeConfig{}); !reflect.DeepEqual(ui, got) {
		t.Errorf("CONNECTOR_KEYS.converter.keys in nodeConfig.ts = %v, ConverterNodeConfig reads %v; keep them the same", ui, got)
	}
}

func TestUIConverterMappingMatchesFieldMapping(t *testing.T) {
	src := readUINodeConfig(t)
	body := uiMatch(t, src, "the ConverterMapping type",
		regexp.MustCompile(`(?s)export type ConverterMapping = \{(.*?)\n\}`))
	var ui []string
	for _, m := range regexp.MustCompile(`(?m)^\s*(\w+)\??:`).FindAllStringSubmatch(body, -1) {
		ui = append(ui, m[1])
	}
	sort.Strings(ui)
	if got := jsonTags(FieldMapping{}); !reflect.DeepEqual(ui, got) {
		t.Errorf("ConverterMapping in nodeConfig.ts has %v, FieldMapping reads %v; keep them the same", ui, got)
	}
}
