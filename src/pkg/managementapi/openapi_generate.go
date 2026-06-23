package managementapi

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
)

// GenerateOpenAPI builds the OpenAPI 3.0 document for the management API from
// apiRoutes (#94). It is deterministic (sorted keys) so the served spec and any
// checked-in snapshot are stable. Schemas are reflected from the route DTOs.
func GenerateOpenAPI() ([]byte, error) {
	defs := map[string]map[string]any{}
	// Always expose the standard error envelope and reference it as the default.
	_ = schemaFor(reflect.TypeOf(ErrorResponse{}), defs)

	paths := map[string]map[string]any{}
	tagSet := map[string]struct{}{}

	for _, r := range apiRoutes {
		tagSet[r.Tag] = struct{}{}

		op := map[string]any{
			"tags":        []string{r.Tag},
			"summary":     r.Summary,
			"operationId": operationID(r.Method, r.Path),
		}
		if params := pathParams(r.Path); len(params) > 0 {
			op["parameters"] = params
		}
		if r.Request != nil {
			op["requestBody"] = map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": map[string]any{"schema": schemaFor(r.Request, defs)},
				},
			}
		}

		success := map[string]any{"description": "Success"}
		if r.Response != nil {
			success["content"] = map[string]any{
				"application/json": map[string]any{"schema": schemaFor(r.Response, defs)},
			}
		}
		op["responses"] = map[string]any{
			"200": success,
			"default": map[string]any{
				"description": "Error",
				"content": map[string]any{
					"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/ErrorResponse"}},
				},
			},
		}

		if paths[r.Path] == nil {
			paths[r.Path] = map[string]any{}
		}
		paths[r.Path][r.Method] = op
	}

	tags := make([]map[string]any, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, map[string]any{"name": t})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i]["name"].(string) < tags[j]["name"].(string) })

	doc := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "VRSky Management API",
			"version":     "1.0.0",
			"description": "REST API for the VRSky integration platform. Most endpoints require a session cookie (set by POST /api/v1/auth/login) and an X-Tenant-ID header selecting the active workspace.",
		},
		"servers": []map[string]any{{"url": "/", "description": "Same origin as the UI / gateway"}},
		"tags":    tags,
		"paths":   paths,
		"components": map[string]any{
			"schemas": defs,
			"securitySchemes": map[string]any{
				"sessionCookie": map[string]any{"type": "apiKey", "in": "cookie", "name": "vrsky_session"},
				"tenantHeader":  map[string]any{"type": "apiKey", "in": "header", "name": "X-Tenant-ID"},
			},
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}

var pathParamRe = regexp.MustCompile(`\{([^}]+)\}`)

func pathParams(path string) []map[string]any {
	var out []map[string]any
	for _, m := range pathParamRe.FindAllStringSubmatch(path, -1) {
		out = append(out, map[string]any{
			"name":     m[1],
			"in":       "path",
			"required": true,
			"schema":   map[string]any{"type": "string"},
		})
	}
	return out
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func operationID(method, path string) string {
	return method + nonAlnum.ReplaceAllString(path, "_")
}
