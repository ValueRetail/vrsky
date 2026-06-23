package managementapi

import (
	"encoding/json"
	"reflect"
	"strings"
)

// Reflection-based JSON-Schema generator for the OpenAPI document (#94). Given a
// Go request/response type it emits an OpenAPI 3.0 schema, registering named
// structs under components/schemas and returning a $ref to them. Kept
// deliberately small — it covers the shapes the management-API DTOs actually
// use (structs, scalars, slices, maps, pointers, time.Time, json.RawMessage,
// interface{}). Unknown/!exported shapes degrade to an open object.

var rawJSONType = reflect.TypeOf(json.RawMessage(nil))

// schemaFor returns the OpenAPI schema for t, registering any named struct it
// encounters in defs (keyed by the Go type name).
func schemaFor(t reflect.Type, defs map[string]map[string]any) map[string]any {
	if t == nil {
		return map[string]any{}
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Special-cases.
	if t.PkgPath() == "time" && t.Name() == "Time" {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if t == rawJSONType {
		return map[string]any{"type": "object", "description": "arbitrary JSON"}
	}

	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 { // []byte
			return map[string]any{"type": "string", "format": "byte"}
		}
		return map[string]any{"type": "array", "items": schemaFor(t.Elem(), defs)}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaFor(t.Elem(), defs)}
	case reflect.Interface:
		return map[string]any{} // any
	case reflect.Struct:
		return structSchema(t, defs)
	default:
		return map[string]any{}
	}
}

// structSchema registers t under components/schemas and returns a $ref to it.
func structSchema(t reflect.Type, defs map[string]map[string]any) map[string]any {
	name := t.Name()
	if name == "" {
		// Anonymous struct — inline it (no $ref).
		return objectSchema(t, defs)
	}
	ref := map[string]any{"$ref": "#/components/schemas/" + name}
	if _, done := defs[name]; done {
		return ref
	}
	defs[name] = map[string]any{} // reserve to break recursion
	defs[name] = objectSchema(t, defs)
	return ref
}

func objectSchema(t reflect.Type, defs map[string]map[string]any) map[string]any {
	props := map[string]any{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous { // unexported
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		jsonName := strings.Split(tag, ",")[0]
		if f.Anonymous && jsonName == "" {
			// Embedded struct: flatten its properties.
			embedded := objectSchema(deref(f.Type), defs)
			if ep, ok := embedded["properties"].(map[string]any); ok {
				for k, v := range ep {
					props[k] = v
				}
			}
			continue
		}
		if jsonName == "" {
			jsonName = f.Name
		}
		props[jsonName] = schemaFor(f.Type, defs)
	}
	return map[string]any{"type": "object", "properties": props}
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}
