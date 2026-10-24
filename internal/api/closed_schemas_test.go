package api

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v2"
)

// Structs are closed by the generator. Maps and opaque JSON are deliberate
// escape hatches, so each needs a reason that survives changes to its type.
// This checks the served document, including nested and inline schemas (#658).
func TestGeneratedSchemasAreClosedUnlessRecorded(t *testing.T) {
	document := generatedDocument(t)
	open := map[string]bool{}
	findDocumentSchemas(document, "#", func(path string, schema map[string]interface{}) {
		findOpenSchemas(schema, path, open)
	})
	data, err := os.ReadFile("../../tests/contract/open-schema-exceptions.yml")
	if err != nil {
		t.Fatal(err)
	}
	var exceptions map[string]string
	if err := yaml.Unmarshal(data, &exceptions); err != nil {
		t.Fatal(err)
	}
	var problems []string
	for path := range open {
		if strings.TrimSpace(exceptions[path]) == "" {
			problems = append(problems, path+": open without a recorded reason")
		}
	}
	for path, reason := range exceptions {
		if strings.TrimSpace(reason) == "" {
			problems = append(problems, path+": empty exception reason")
		}
		if !open[path] {
			problems = append(problems, path+": no longer open; remove the exception")
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatal("open schema exceptions disagree with the generated document:\n" + strings.Join(problems, "\n"))
	}
}

func schemaPointer(path, key string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// Only schema-bearing fields are inspected: an example's arbitrary JSON is
// data, and must not be mistaken for another schema.
func findDocumentSchemas(value interface{}, path string, visit func(string, map[string]interface{})) {
	switch value := value.(type) {
	case map[string]interface{}:
		for key, child := range value {
			next := schemaPointer(path, key)
			switch key {
			case "example", "examples", "links":
				continue
			case "schema":
				if schema, ok := child.(map[string]interface{}); ok {
					visit(next, schema)
				}
			case "schemas":
				if schemas, ok := child.(map[string]interface{}); ok {
					for name, entry := range schemas {
						if schema, ok := entry.(map[string]interface{}); ok {
							visit(schemaPointer(next, name), schema)
						}
					}
				}
			default:
				findDocumentSchemas(child, next, visit)
			}
		}
	case []interface{}:
		for i, child := range value {
			findDocumentSchemas(child, fmt.Sprintf("%s/%d", path, i), visit)
		}
	}
}

func findOpenSchemas(schema map[string]interface{}, path string, open map[string]bool) {
	if _, reference := schema["$ref"]; reference {
		return // The referenced component is checked at its own location.
	}
	object := schema["type"] == "object"
	if types, ok := schema["type"].([]interface{}); ok {
		for _, kind := range types {
			object = object || kind == "object"
		}
	}
	_, properties := schema["properties"]
	_, typed := schema["type"]
	_, enum := schema["enum"]
	_, constant := schema["const"]
	_, all := schema["allOf"]
	_, any := schema["anyOf"]
	_, one := schema["oneOf"]
	unconstrained := !typed && !enum && !constant && !all && !any && !one
	if (object || properties || unconstrained) && schema["additionalProperties"] != false {
		open[path] = true
	}
	for key, child := range schema {
		next := schemaPointer(path, key)
		switch key {
		case "properties":
			if properties, ok := child.(map[string]interface{}); ok {
				for name, entry := range properties {
					if nested, ok := entry.(map[string]interface{}); ok {
						findOpenSchemas(nested, schemaPointer(next, name), open)
					}
				}
			}
		case "items", "additionalProperties", "not":
			if nested, ok := child.(map[string]interface{}); ok && len(nested) > 0 {
				findOpenSchemas(nested, next, open)
			}
		case "allOf", "anyOf", "oneOf":
			if variants, ok := child.([]interface{}); ok {
				for i, entry := range variants {
					if nested, ok := entry.(map[string]interface{}); ok {
						findOpenSchemas(nested, fmt.Sprintf("%s/%d", next, i), open)
					}
				}
			}
		}
	}
}

func TestOpenSchemaInventoryFindsLeaksAndIgnoresExampleData(t *testing.T) {
	for _, extra := range []interface{}{nil, true} {
		schema := map[string]interface{}{"type": "object", "additionalProperties": extra}
		open := map[string]bool{}
		findOpenSchemas(schema, "#/body", open)
		if !open["#/body"] {
			t.Errorf("an object with additionalProperties=%v hides extra fields", extra)
		}
	}
	schema := map[string]interface{}{
		"type": "object", "additionalProperties": false,
		"properties": map[string]interface{}{
			"named":  map[string]interface{}{"type": "string"},
			"opaque": map[string]interface{}{"description": "any JSON"},
			"nested": map[string]interface{}{"type": "array", "items": map[string]interface{}{
				"type": []interface{}{"object", "null"}}},
		},
		"example": map[string]interface{}{"schema": map[string]interface{}{}},
	}
	document := map[string]interface{}{"schema": schema, "examples": map[string]interface{}{"schema": map[string]interface{}{}}}
	open := map[string]bool{}
	findDocumentSchemas(document, "#", func(path string, s map[string]interface{}) { findOpenSchemas(s, path, open) })
	if len(open) != 2 || !open["#/schema/properties/opaque"] || !open["#/schema/properties/nested/items"] {
		t.Fatalf("the inventory missed a nested leak or treated example data as schema: %v", open)
	}
}
