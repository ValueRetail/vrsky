package managementapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGenerateOpenAPI(t *testing.T) {
	raw, err := GenerateOpenAPI()
	if err != nil {
		t.Fatalf("GenerateOpenAPI: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("spec is not valid JSON: %v", err)
	}

	if v, _ := doc["openapi"].(string); !strings.HasPrefix(v, "3.0") {
		t.Errorf("openapi version = %q, want 3.0.x", v)
	}

	paths, ok := doc["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		t.Fatal("spec has no paths")
	}

	// Every registry route appears under paths[path][method].
	for _, r := range apiRoutes {
		methods, ok := paths[r.Path].(map[string]any)
		if !ok {
			t.Errorf("path %q missing from spec", r.Path)
			continue
		}
		if _, ok := methods[r.Method]; !ok {
			t.Errorf("operation %s %s missing from spec", strings.ToUpper(r.Method), r.Path)
		}
	}

	// Every $ref resolves to a defined component schema.
	comps, _ := doc["components"].(map[string]any)
	schemas, _ := comps["schemas"].(map[string]any)
	for _, ref := range collectRefs(doc) {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		if _, ok := schemas[name]; !ok {
			t.Errorf("dangling $ref: %s (schema not defined)", ref)
		}
	}

	// The error envelope must be present (default response references it).
	if _, ok := schemas["ErrorResponse"]; !ok {
		t.Error("ErrorResponse schema not generated")
	}
}

// collectRefs walks the decoded spec and returns every $ref string.
func collectRefs(v any) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "$ref" {
				if s, ok := val.(string); ok {
					out = append(out, s)
				}
				continue
			}
			out = append(out, collectRefs(val)...)
		}
	case []any:
		for _, e := range t {
			out = append(out, collectRefs(e)...)
		}
	}
	return out
}

func TestOpenAPIRouteCoverageMatchesPatterns(t *testing.T) {
	// Guards the registry against drift in this package's own view: every
	// distinct mux Pattern in the registry must carry a non-empty summary/tag.
	for _, r := range apiRoutes {
		if r.Pattern == "" || r.Method == "" || r.Path == "" {
			t.Errorf("incomplete route entry: %+v", r)
		}
		if r.Summary == "" || r.Tag == "" {
			t.Errorf("route %s %s missing summary/tag", r.Method, r.Path)
		}
	}
}
