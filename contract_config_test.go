package restmail_test

import (
	"os"
	"regexp"
	"testing"

	yaml "go.yaml.in/yaml/v2"
)

// Vertrag compiles exclusions with Go's regexp engine. Python's re accepts
// constructs such as lookahead that would stop the contract run before HTTP.
func TestContractExclusionsCompile(t *testing.T) {
	data, err := os.ReadFile("vertrag.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		ExcludeMatching []string `yaml:"exclude-matching"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.ExcludeMatching) == 0 {
		t.Fatal("contract config has no exclusion patterns")
	}
	for _, pattern := range config.ExcludeMatching {
		if _, err := regexp.Compile(pattern); err != nil {
			t.Errorf("contract exclusion %q: %v", pattern, err)
		}
	}
}
