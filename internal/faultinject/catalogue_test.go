package faultinject

// No build tag: this runs in every build, and holds the fault code to the
// catalogue and out of normal builds.

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v2"
)

type catalogueEntry struct {
	Name      string `yaml:"name"`
	BuildTags string `yaml:"build_tags"`
	Operation string `yaml:"operation"`
	Path      string `yaml:"path"`
	Phase     string `yaml:"phase"`
	KnownGap  string `yaml:"known_gap"`
	Breaks    string `yaml:"breaks"`
}

// The phases tests/contract/faults.py can run and judge.
var judgedPhases = map[string]bool{"examples": true}

func readCatalogue(t *testing.T) []catalogueEntry {
	t.Helper()
	raw, err := os.ReadFile("../../tests/contract/faults.yml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Faults []catalogueEntry `yaml:"faults"`
	}
	if err := yaml.UnmarshalStrict(raw, &doc); err != nil {
		t.Fatalf("tests/contract/faults.yml: %v", err)
	}
	if len(doc.Faults) == 0 {
		t.Fatal("tests/contract/faults.yml lists no fault")
	}
	return doc.Faults
}

func buildLine(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadString('\n')
	return strings.TrimSpace(line)
}

// Every fault in the catalogue has its switch file, built only with its tags,
// and names an operation and a phase the fault run can judge.
func TestEveryCatalogueFaultHasItsSwitch(t *testing.T) {
	spec, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range readCatalogue(t) {
		if f.Name == "" || seen[f.Name] {
			t.Fatalf("a fault without a name, or named twice: %q", f.Name)
		}
		seen[f.Name] = true
		if want := "faultinject,fault_" + f.Name; f.BuildTags != want {
			t.Errorf("%s: build_tags %q, want %q", f.Name, f.BuildTags, want)
		}
		file := "fault_" + f.Name + ".go"
		if got, want := buildLine(t, file), "//go:build faultinject && fault_"+f.Name; got != want {
			t.Errorf("%s: first line %q, want %q", file, got, want)
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`Name:\s+"` + regexp.QuoteMeta(f.Name) + `"`).Match(body) {
			t.Errorf("%s does not register a fault named %s", file, f.Name)
		}
		if !regexp.MustCompile(`Path:\s+"` + regexp.QuoteMeta(f.Path) + `"`).Match(body) {
			t.Errorf("%s does not break the catalogue's path %q", file, f.Path)
		}
		if !judgedPhases[f.Phase] {
			t.Errorf("%s: phase %q is not one the fault run judges", f.Name, f.Phase)
		}
		if !strings.Contains(string(spec), "operationId: "+f.Operation+"\n") {
			t.Errorf("%s: operation %q is not in openapi.yaml", f.Name, f.Operation)
		}
		if strings.TrimSpace(f.Breaks) == "" {
			t.Errorf("%s says nothing of what it breaks", f.Name)
		}
	}
	switches, err := filepath.Glob("fault_*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range switches {
		name := strings.TrimSuffix(strings.TrimPrefix(file, "fault_"), ".go")
		if !seen[name] {
			t.Errorf("%s has no entry in tests/contract/faults.yml", file)
		}
	}
}

// Every source file of this package needs the faultinject tag, so a normal
// build compiles none of it.
func TestNoFaultCodeInANormalBuild(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		if line := buildLine(t, file); !strings.HasPrefix(line, "//go:build faultinject") {
			t.Errorf("%s starts %q; it must start //go:build faultinject", file, line)
		}
	}
}
