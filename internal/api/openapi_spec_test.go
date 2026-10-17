package api

import (
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

// The spec documents a webmail account as the server sends it (#358): every
// property it lists is a field the server has. It listed an is_admin that went
// with OSI-14. There is one spec, the root openapi.yaml: the API serves it and
// the webmail's types are generated from it.
func TestOpenAPI_WebmailAccountMatchesTheModel(t *testing.T) {
	fields := map[string]bool{}
	typ := reflect.TypeOf(models.WebmailAccount{})
	for i := 0; i < typ.NumField(); i++ {
		if name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			fields[name] = true
		}
	}

	spec, err := restmail.OpenAPI.ReadFile("openapi.yaml")
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
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	schema, ok := doc.Components.Schemas["WebmailAccount"]
	if !ok {
		t.Fatal("no WebmailAccount schema")
	}
	for prop := range schema.Properties {
		if !fields[prop] {
			t.Errorf("WebmailAccount documents %q, which the server does not send", prop)
		}
	}
	for _, prop := range schema.Required {
		if !fields[prop] {
			t.Errorf("WebmailAccount requires %q, which the server does not send", prop)
		}
	}
}
