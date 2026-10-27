package api

import (
	"reflect"
	"sort"
	"testing"

	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

// The spec documents an account as the server sends it, in GET and POST
// /api/v1/accounts (#347): exactly the fields it has, all of them required
// except owner_id, which only an owned account carries (#341, Decided 12).
// There is no webmail account any more, and the spec says so by not having
// one.
func TestOpenAPI_AccountMatchesTheServer(t *testing.T) {
	want := []string{"address", "display_name", "id", "is_primary"}
	wantProps := append([]string{}, "address", "display_name", "id", "is_primary", "owner_id")
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string               `yaml:"required"`
				Properties map[string]interface{} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, gone := range []string{"WebmailAccount", "LinkedAccount", "AdminUser"} {
		if _, ok := doc.Components.Schemas[gone]; ok {
			t.Errorf("the spec still documents %s", gone)
		}
	}
	schema, ok := doc.Components.Schemas["Account"]
	if !ok {
		t.Fatal("no Account schema")
	}
	var props []string
	for p := range schema.Properties {
		props = append(props, p)
	}
	sort.Strings(props)
	required := append([]string(nil), schema.Required...)
	sort.Strings(required)
	if !reflect.DeepEqual(props, wantProps) || !reflect.DeepEqual(required, want) {
		t.Errorf("Account: properties %v, required %v; want properties %v, required %v", props, required, wantProps, want)
	}
}
