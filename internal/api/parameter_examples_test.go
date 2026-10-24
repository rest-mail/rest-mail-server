package api

import (
	"sort"
	"testing"
)

// Vertrag builds no request for a required parameter that has no example, and refuses
// the whole description over one: E2E run 40111450987 (2026-10-24) sent nothing after
// 2,876 such refusals (#670). Every required path, query and header parameter of the
// generated description carries an example, from its field's example tag or from a
// request pair in openapi-example.json, with the value openapi.yaml gives it.
func TestEveryRequiredParameterHasAnExample(t *testing.T) {
	doc := generatedDocument(t)
	paths, _ := doc["paths"].(map[string]interface{})
	var missing []string
	for _, op := range operationsOf(doc) {
		item, _ := paths[op.path].(map[string]interface{})
		for key, param := range parametersOf(doc, item, op.operation) {
			if !truthy(param["required"]) || hasParameterExample(param) {
				continue
			}
			missing = append(missing, op.id+": "+key)
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("%s is required and has no example: give its field an example tag", m)
	}
}

func hasParameterExample(param map[string]interface{}) bool {
	if _, ok := param["example"]; ok {
		return true
	}
	if examples, ok := param["examples"].(map[string]interface{}); ok && len(examples) > 0 {
		return true
	}
	schema, _ := param["schema"].(map[string]interface{})
	if _, ok := schema["default"]; ok {
		return true
	}
	return false
}
