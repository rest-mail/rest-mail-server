package handlers

import (
	"testing"

	yaml "go.yaml.in/yaml/v2"
)

// A document can contain path, query and header examples of different types.
// Decode their values without forcing every route's examples into uint, then
// require an integer only where a path-ID assertion actually consumes one.
func contractPathExampleID(t *testing.T, value any) uint {
	t.Helper()
	switch v := value.(type) {
	case int:
		if v >= 0 {
			return uint(v)
		}
	case uint:
		return v
	}
	t.Fatalf("documented path ID must be a non-negative integer, got %T", value)
	return 0
}

func TestContractParameterExamplesPreserveTheirDeclaredTypes(t *testing.T) {
	var parameters []struct {
		Name     string `yaml:"name"`
		Examples map[string]struct {
			Value any `yaml:"value"`
		} `yaml:"examples"`
	}
	data := []byte(`
- name: id
  examples: {missing: {value: 2147483647}}
- name: Range
  examples: {range_single: {value: "bytes=0-0"}}
`)
	if err := yaml.Unmarshal(data, &parameters); err != nil {
		t.Fatal(err)
	}
	if len(parameters) != 2 || contractPathExampleID(t, parameters[0].Examples["missing"].Value) != 2147483647 {
		t.Fatalf("path example changed: %+v", parameters)
	}
	if parameters[1].Examples["range_single"].Value != "bytes=0-0" {
		t.Fatalf("header example changed: %+v", parameters[1])
	}
}
