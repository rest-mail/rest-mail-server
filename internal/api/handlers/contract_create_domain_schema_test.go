package handlers

import (
	"reflect"
	"testing"

	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

// A nested reference makes vertrag 0.29 silently discard every body probe,
// including probes for unrelated fields such as name and default_quota_bytes.
func TestCreateDomainRequest_ContractSchema(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]map[interface{}]interface{} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	schema, ok := doc.Components.Schemas["CreateDomainRequest"]
	if !ok {
		t.Fatal("missing CreateDomainRequest")
	}
	var walk func(interface{})
	walk = func(node interface{}) {
		switch node := node.(type) {
		case map[interface{}]interface{}:
			for key, value := range node {
				if key == "$ref" {
					t.Errorf("CreateDomainRequest contains $ref %v; vertrag drops its body probes", value)
				}
				walk(value)
			}
		case []interface{}:
			for _, value := range node {
				walk(value)
			}
		}
	}
	walk(schema)

	properties, ok := schema["properties"].(map[interface{}]interface{})
	if !ok {
		t.Fatal("missing CreateDomainRequest properties")
	}
	policy, ok := properties["sending_policy"].(map[interface{}]interface{})
	if !ok {
		t.Fatal("missing sending_policy schema")
	}
	shared := doc.Components.Schemas["DomainSendingPolicy"]
	if policy["type"] != "string" || !reflect.DeepEqual(policy["enum"], shared["enum"]) {
		t.Fatalf("sending_policy = %v, want string enum %v", policy, shared["enum"])
	}
	values, ok := policy["enum"].([]interface{})
	if !ok || len(values) == 0 {
		t.Fatal("missing sending_policy enum")
	}
	for _, value := range values {
		policy, ok := value.(string)
		if !ok || !validSendingPolicy(policy) {
			t.Errorf("documented sending policy %v is refused by the handler", value)
		}
	}
}
