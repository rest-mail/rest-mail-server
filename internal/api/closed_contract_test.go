package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/restmail/restmail/internal/api/endpoint"
)

func contractDocument(t *testing.T, generator *oapifly.Generator) map[string]interface{} {
	t.Helper()
	data, err := json.Marshal(generator.Generate())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func pointerPart(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

// visitContractSchemas visits schemas, rather than arbitrary JSON objects such
// as examples or property-name dictionaries. Components cover shared types;
// request, parameter, response and header sites cover inline declarations too.
func visitContractSchemas(doc map[string]interface{}, visit func(map[string]interface{}, string)) {
	var schema func(interface{}, string)
	schema = func(value interface{}, path string) {
		s, ok := value.(map[string]interface{})
		if !ok {
			return
		}
		visit(s, path)
		if props, ok := s["properties"].(map[string]interface{}); ok {
			for name, property := range props {
				schema(property, path+"/properties/"+pointerPart(name))
			}
		}
		for _, key := range []string{"items", "additionalProperties", "not"} {
			// An open dictionary's value is constrained by the recorded exception
			// for the dictionary; its {} value is not an extra independent schema.
			if key == "additionalProperties" {
				if object, ok := s[key].(map[string]interface{}); ok && len(object) == 0 {
					continue
				}
			}
			schema(s[key], path+"/"+key)
		}
		for _, key := range []string{"allOf", "anyOf", "oneOf"} {
			if s["type"] == "object" && s["additionalProperties"] == false {
				// The alternatives of a closed object only refine its declared fields
				// (go-oapifly AnyOf); the object itself admits no other field.
				continue
			}
			if alternatives, ok := s[key].([]interface{}); ok {
				for i, alternative := range alternatives {
					schema(alternative, fmt.Sprintf("%s/%s/%d", path, key, i))
				}
			}
		}
	}
	if components, ok := doc["components"].(map[string]interface{}); ok {
		if schemas, ok := components["schemas"].(map[string]interface{}); ok {
			for name, value := range schemas {
				schema(value, "#/components/schemas/"+pointerPart(name))
			}
		}
	}
	var sites func(interface{}, string)
	sites = func(value interface{}, path string) {
		switch value := value.(type) {
		case map[string]interface{}:
			for key, child := range value {
				if key == "example" || key == "examples" {
					continue
				}
				childPath := path + "/" + pointerPart(key)
				if key == "schema" {
					schema(child, childPath)
				} else {
					sites(child, childPath)
				}
			}
		case []interface{}:
			for i, child := range value {
				sites(child, fmt.Sprintf("%s/%d", path, i))
			}
		}
	}
	sites(doc["paths"], "#/paths")
}

func schemaIsOpen(schema map[string]interface{}) bool {
	if len(schema) == 0 {
		return true // an untyped value accepts an object with arbitrary fields too
	}
	_, properties := schema["properties"]
	_, additional := schema["additionalProperties"]
	return (schema["type"] == "object" || properties || additional) && schema["additionalProperties"] != false
}

func unrecordedSchemaPaths(doc map[string]interface{}, exceptions map[string]string) []string {
	var paths []string
	visitContractSchemas(doc, func(schema map[string]interface{}, path string) {
		if schemaIsOpen(schema) && len(strings.TrimSpace(exceptions[path])) < 30 {
			paths = append(paths, path)
		}
	})
	sort.Strings(paths)
	return paths
}

func TestEveryGeneratedObjectIsClosedOrHasARecordedReason(t *testing.T) {
	data, err := os.ReadFile("../../tests/contract/open-object-exceptions.json")
	if err != nil {
		t.Fatal(err)
	}
	var exceptions map[string]string
	if err := json.Unmarshal(data, &exceptions); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, generator := range gapGenerators(t) {
		doc := contractDocument(t, generator)
		for _, path := range unrecordedSchemaPaths(doc, exceptions) {
			t.Errorf("%s accepts undeclared fields without a recorded reason", path)
		}
		visitContractSchemas(doc, func(schema map[string]interface{}, path string) {
			if !schemaIsOpen(schema) {
				return
			}
			seen[path] = true
		})
	}
	for path := range exceptions {
		if !seen[path] {
			t.Errorf("stale open-object exception %s; remove or correct its reason", path)
		}
	}
	t.Logf("%d explicit open-object exceptions; every other generated object is closed", len(seen))
}

func TestClosureGuardCatchesAnOpenedResourceOrUntypedField(t *testing.T) {
	data, err := os.ReadFile("../../tests/contract/open-object-exceptions.json")
	if err != nil {
		t.Fatal(err)
	}
	var exceptions map[string]string
	if err := json.Unmarshal(data, &exceptions); err != nil {
		t.Fatal(err)
	}
	generator := gapGenerators(t)[0]
	for _, fault := range []string{"open-resource", "untyped-field"} {
		t.Run(fault, func(t *testing.T) {
			doc := contractDocument(t, generator)
			if got := unrecordedSchemaPaths(doc, exceptions); len(got) != 0 {
				t.Fatalf("baseline is not clean: %v", got)
			}
			domain := doc["components"].(map[string]interface{})["schemas"].(map[string]interface{})["Domain"].(map[string]interface{})
			want := "#/components/schemas/Domain"
			if fault == "open-resource" {
				domain["additionalProperties"] = true
			} else {
				domain["properties"].(map[string]interface{})["unexpected_payload"] = map[string]interface{}{}
				want += "/properties/unexpected_payload"
			}
			if got := unrecordedSchemaPaths(doc, exceptions); len(got) != 1 || got[0] != want {
				t.Fatalf("guard missed the real response-schema fault: %v, want %s", got, want)
			}
		})
	}
}

func objectSchema(doc map[string]interface{}, schema map[string]interface{}) map[string]interface{} {
	if ref, ok := schema["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		return objectSchema(doc, doc["components"].(map[string]interface{})["schemas"].(map[string]interface{})[name].(map[string]interface{}))
	}
	if alternatives, ok := schema["oneOf"].([]interface{}); ok {
		for _, alternative := range alternatives {
			candidate := objectSchema(doc, alternative.(map[string]interface{}))
			if candidate["type"] == "object" {
				return candidate
			}
		}
	}
	return schema
}

// This exercises the actual registered request types, not a second list of test
// DTOs. The shared endpoint's binding refuses these violations before invoking
// any handler. Authentication/database failures are therefore irrelevant here.
func TestAllRegisteredJSONRequestTypesRejectUnknownFields(t *testing.T) {
	const unknown = "__undeclared_field_658"
	checked := map[string]bool{}
	open := map[string]bool{}
	for _, routers := range gapRouters(t) {
		doc := contractDocument(t, routers.generator("binding"))
		for _, operation := range routers.Registry.Endpoints() {
			path := doc["paths"].(map[string]interface{})[operation.Path].(map[string]interface{})
			described := path[strings.ToLower(operation.Method)].(map[string]interface{})
			body, ok := described["requestBody"].(map[string]interface{})
			if !ok {
				continue
			}
			media, ok := body["content"].(map[string]interface{})["application/json"].(map[string]interface{})
			if !ok {
				continue
			}
			schema := objectSchema(doc, media["schema"].(map[string]interface{}))
			if schema["type"] != "object" {
				t.Errorf("%s JSON binding needs an explicit non-object probe", operation.OperationID)
				continue
			}
			if schemaIsOpen(schema) {
				open[operation.OperationID] = true // reason checked against the full schema above
				continue
			}
			t.Run(operation.OperationID, func(t *testing.T) {
				if operation.Request == nil {
					t.Fatal("described JSON body has no registered request type")
				}
				request := reflect.New(operation.Request).Interface()
				err := endpoint.BindBody(request, []byte(`{"`+unknown+`":true}`))
				var refusal *endpoint.Error
				if !errors.As(err, &refusal) || refusal.Status != http.StatusUnprocessableEntity || refusal.Code != "validation_failed" || refusal.Fields[unknown] == "" {
					t.Fatalf("registered request does not name its refused unknown field: %v", err)
				}
				checked[operation.OperationID] = true
			})
		}
	}
	if len(checked) == 0 {
		t.Fatal("no JSON request bodies were exercised")
	}
	ids := make([]string, 0, len(open))
	for id := range open {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	t.Logf("%d closed registered JSON bodies reject unknown fields; explicit open formats: %v", len(checked), ids)
}
