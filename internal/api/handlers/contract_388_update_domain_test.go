package handlers

// UpdateDomainRequest as the contract tests (#388) probe it: vertrag 0.29 sent
// no boundary probe for updateDomain while the body schema held a nested $ref
// (sending_policy to DomainSendingPolicy), and the inlined policy must still
// be the values Update accepts.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

func TestUpdateDomainRequest_ContractSchema(t *testing.T) {
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
	schema, ok := doc.Components.Schemas["UpdateDomainRequest"]
	if !ok {
		t.Fatal("no UpdateDomainRequest schema")
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
		t.Errorf("UpdateDomainRequest has nested $ref %s: vertrag drops every probe of the body", strings.Join(refs, ", "))
	}

	policy := schema.(map[any]any)["properties"].(map[any]any)["sending_policy"].(map[any]any)
	shared := doc.Components.Schemas["DomainSendingPolicy"].(map[any]any)
	if !reflect.DeepEqual(policy["enum"], shared["enum"]) {
		t.Errorf("sending_policy enum = %v, want DomainSendingPolicy's %v", policy["enum"], shared["enum"])
	}
	for _, value := range policy["enum"].([]any) {
		if !validSendingPolicy(value.(string)) {
			t.Errorf("sending_policy enum offers %q, which Update refuses", value)
		}
	}
}
