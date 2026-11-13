package schema

import (
	"reflect"
	"strconv"
	"strings"
)

// JSONSchema describes the current document format as a JSON Schema
// (draft 2020-12), for editors to check and complete documents with.
func JSONSchema() map[string]any {
	s := structSchema(reflect.TypeOf(Document{}))
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["title"] = "Super Matrix schema"
	props := s["properties"].(map[string]any)
	props["version"] = map[string]any{"type": "integer", "minimum": MinVersion, "maximum": CurrentVersion, "description": "Document format version."}
	s["required"] = []string{"version"}
	return s
}

var (
	listType = reflect.TypeOf(List{})
	hashType = reflect.TypeOf(Hash{})
)

type mapper interface{ mapItem() any }

func typeSchema(t reflect.Type, f reflect.StructField) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := map[string]any{}
	switch {
	case t == listType:
		out["type"] = "array"
		out["items"] = map[string]any{"type": "string"}
	case t == hashType:
		out = structSchema(t)
		out["required"] = []string{"hash"}
	case reflect.PointerTo(t).Implements(reflect.TypeOf((*mapper)(nil)).Elem()):
		item := reflect.New(t).Interface().(mapper).mapItem()
		out["type"] = "object"
		out["properties"] = map[string]any{Spread: map[string]any{"type": "boolean", "description": "Keep items added in the app."}}
		out["additionalProperties"] = structSchema(reflect.TypeOf(item).Elem())
	case t.Kind() == reflect.Struct:
		out = structSchema(t)
	case t.Kind() == reflect.Slice:
		out["type"] = "array"
		out["items"] = map[string]any{"type": "string"}
	case t.Kind() == reflect.String:
		out["type"] = "string"
	case t.Kind() == reflect.Int:
		out["type"] = "integer"
	case t.Kind() == reflect.Bool:
		out["type"] = "boolean"
	}
	if d := f.Tag.Get("desc"); d != "" {
		out["description"] = d
	}
	if e := f.Tag.Get("enum"); e != "" {
		var vals []any
		for _, v := range strings.Split(e, ",") {
			if n, err := strconv.Atoi(v); err == nil && out["type"] == "integer" {
				vals = append(vals, n)
			} else {
				vals = append(vals, v)
			}
		}
		out["enum"] = vals
	}
	for _, k := range []string{"min", "max"} {
		if v, err := strconv.Atoi(f.Tag.Get(k)); err == nil {
			out[map[string]string{"min": "minimum", "max": "maximum"}[k]] = v
		}
	}
	if d, ok := f.Tag.Lookup("default"); ok {
		switch out["type"] {
		case "boolean":
			out["default"] = d == "true"
		case "integer":
			n, _ := strconv.Atoi(d)
			out["default"] = n
		default:
			out["default"] = d
		}
	}
	return out
}

func structSchema(t reflect.Type) map[string]any {
	props := map[string]any{}
	var fields []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := jsonName(f)
		if name == "" {
			continue
		}
		props[name] = typeSchema(f.Type, f)
		if name != "ui" && f.Tag.Get("key") != "true" {
			fields = append(fields, name)
		}
	}
	if ui, ok := props["ui"].(map[string]any); ok {
		ui["items"] = map[string]any{"type": "string", "enum": fields}
	}
	return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
}
