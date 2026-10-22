package handlers

// SendMessageRequest as the contract tests (#388) probe it: vertrag 0.29 sent
// no boundary probe for sendMessage while the body schema held a nested $ref,
// and the schema must say what SendMessage enforces: a non-empty to list and
// an optional subject.

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

func TestSendMessageRequest_ContractSchema(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	schema, ok := doc.Components.Schemas["SendMessageRequest"]
	if !ok {
		t.Fatal("no SendMessageRequest schema")
	}

	var refs []string
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[any]any:
			for key, child := range v {
				if key == "$ref" {
					refs = append(refs, child.(string))
				}
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(schema)
	if len(refs) > 0 {
		t.Errorf("SendMessageRequest has nested $ref %s: vertrag drops every probe of the body", strings.Join(refs, ", "))
	}

	body := schema.(map[any]any)
	var required []string
	for _, field := range body["required"].([]any) {
		required = append(required, field.(string))
	}
	sort.Strings(required)
	if !reflect.DeepEqual(required, []string{"from", "to"}) {
		t.Errorf("required = %v, want [from to] (SendMessage sends an absent subject as an empty one)", required)
	}

	to := body["properties"].(map[any]any)["to"].(map[any]any)
	if to["minItems"] != 1 {
		t.Errorf("to minItems = %v, want 1 (SendMessage refuses an empty to list)", to["minItems"])
	}
}
