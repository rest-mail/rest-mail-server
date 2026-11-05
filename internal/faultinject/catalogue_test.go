package faultinject

// No build tag: this runs in every build, and holds the fault code to the
// catalogue and out of normal builds.

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v2"
)

type catalogueEntry struct {
	Name            string `yaml:"name"`
	BuildTags       string `yaml:"build_tags"`
	Operation       string `yaml:"operation"`
	Path            string `yaml:"path"`
	Phase           string `yaml:"phase"`
	KnownGap        string `yaml:"known_gap"`
	Breaks          string `yaml:"breaks"`
	ExpectedFailure string `yaml:"expected_failure"`
	Patch           string `yaml:"patch"`
	Test            string `yaml:"test"`
	Subtest         string `yaml:"subtest"`
}

// The phases tests/contract/faults.py can run and judge.
var judgedPhases = map[string]bool{"examples": true, "body_cap": true, "auth_throttle": true, "secret_scan": true, "browser_session": true, "pairs": true}

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
		if f.Patch != "" {
			checkPatchFault(t, f)
			continue
		}
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

// A patch fault is its catalogue entry alone. Its diff must still apply to the
// code it breaks, so a change to that code says so here rather than a night
// later. Its witness must be a pair of the operation, sent by a test the gate
// chore contract:pairs requires, or a red run of it would prove nothing.
func checkPatchFault(t *testing.T, f catalogueEntry) {
	t.Helper()
	if f.Phase != "pairs" || f.BuildTags != "" {
		t.Errorf("%s: a patch fault has phase pairs and no build tags, got %q and %q", f.Name, f.Phase, f.BuildTags)
	}
	if f.ExpectedFailure == "" || strings.TrimSpace(f.Breaks) == "" {
		t.Errorf("%s: a patch fault says what it breaks and the failure it must cause", f.Name)
	}
	apply := exec.Command("git", "apply", "--check", "-")
	apply.Dir = "../.."
	apply.Stdin = strings.NewReader(f.Patch)
	if out, err := apply.CombinedOutput(); err != nil {
		t.Errorf("%s: its patch no longer applies, so update it to break this code the same way: %v\n%s", f.Name, err, out)
	}
	chores, err := os.ReadFile("../../chores.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`example_pairs\.sh -run '.*\b` + regexp.QuoteMeta(f.Test) + `\b`).Match(chores) {
		t.Errorf("%s: %s is not a test chore contract:pairs requires", f.Name, f.Test)
	}
	raw, err := os.ReadFile("../../openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	var pairs map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &pairs); err != nil {
		t.Fatal(err)
	}
	pair := f.Subtest[strings.LastIndex(f.Subtest, "/")+1:]
	if _, ok := pairs[f.Operation][pair]; !ok {
		t.Errorf("%s: subtest %q names no example pair of %s", f.Name, f.Subtest, f.Operation)
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

// What the Go toolchain actually compiles for a build without the tags: the API
// does not link this package, and takes the no-op hooks in cmd/api/nofaults.go.
func TestANormalAPIBuildLinksNoFaultCode(t *testing.T) {
	goList := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("go", append([]string{"list"}, args...)...)
		cmd.Dir = "../.."
		// This test also runs in the build with every fault's tag.
		cmd.Env = append(os.Environ(), "GOFLAGS=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	for _, dep := range strings.Fields(goList("-deps", "./cmd/api")) {
		if strings.HasSuffix(dep, "/internal/faultinject") {
			t.Fatalf("a normal build of cmd/api links %s", dep)
		}
	}
	files := strings.Fields(goList("-f", `{{join .GoFiles " "}}`, "./cmd/api"))
	if !slices.Contains(files, "nofaults.go") || slices.Contains(files, "faults.go") {
		t.Fatalf("a normal build of cmd/api compiles %v", files)
	}
}
