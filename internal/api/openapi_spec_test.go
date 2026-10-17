package api

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

// The spec documents a webmail account as the server sends it (#358): every
// property it lists is a field the server has. It listed an is_admin that went
// with OSI-14. Both copies are checked: the one at the root, which the webmail's
// types are generated from, and the one served at /api/docs/openapi.yaml.
func TestOpenAPI_WebmailAccountMatchesTheModel(t *testing.T) {
	fields := map[string]bool{}
	typ := reflect.TypeOf(models.WebmailAccount{})
	for i := 0; i < typ.NumField(); i++ {
		if name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			fields[name] = true
		}
	}

	root, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatalf("read the root spec: %v", err)
	}
	served, err := openapiSpec.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the served spec: %v", err)
	}
	for file, spec := range map[string][]byte{"openapi.yaml": root, "internal/api/openapi.yaml": served} {
		var doc struct {
			Components struct {
				Schemas map[string]struct {
					Required   []string               `yaml:"required"`
					Properties map[string]interface{} `yaml:"properties"`
				} `yaml:"schemas"`
			} `yaml:"components"`
		}
		if err := yaml.Unmarshal(spec, &doc); err != nil {
			t.Fatalf("%s: parse: %v", file, err)
		}
		schema, ok := doc.Components.Schemas["WebmailAccount"]
		if !ok {
			t.Fatalf("%s: no WebmailAccount schema", file)
		}
		for prop := range schema.Properties {
			if !fields[prop] {
				t.Errorf("%s: WebmailAccount documents %q, which the server does not send", file, prop)
			}
		}
		for _, prop := range schema.Required {
			if !fields[prop] {
				t.Errorf("%s: WebmailAccount requires %q, which the server does not send", file, prop)
			}
		}
	}
}
