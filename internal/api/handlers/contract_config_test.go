package handlers

import (
	"os"
	"regexp"
	"testing"

	yaml "go.yaml.in/yaml/v2"
)

// The Python gate checks which refusals are excluded, but Python accepts
// lookaround and other syntax that Vertrag's Go regexp engine rejects.
func TestExcludeMatchingUsesGoRegexp(t *testing.T) {
	data, err := os.ReadFile("../../../vertrag.yml")
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
		t.Fatal("vertrag.yml has no exclude-matching patterns")
	}
	for _, pattern := range config.ExcludeMatching {
		if _, err := regexp.Compile(pattern); err != nil {
			t.Errorf("exclude-matching %q: %v", pattern, err)
		}
	}
}
