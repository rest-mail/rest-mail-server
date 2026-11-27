package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Check the real mappings before counting them: a row naming a missing test
// must never contribute to a green README badge.
func TestReadmeChecklistBadge(t *testing.T) {
	root := filepath.Join("..", "..")
	file := filepath.Join(root, "CHECKLIST.md")
	var out bytes.Buffer
	if code := run([]string{"-file", file, filepath.Join(root, "pkg", "pop3", "rfctest")}, &out, &out); code != 0 {
		t.Fatalf("invalid checklist (exit %d):\n%s", code, out.String())
	}
	items, err := parseChecklist(file, func(format string, args ...any) {
		t.Errorf(format, args...)
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped := 0
	for _, it := range items {
		if !it.na {
			mapped++
		}
	}
	want := fmt.Sprintf("[![RFC checklist: %d/%d mapped](https://img.shields.io/badge/RFC%%20checklist-%d%%2F%d%%20mapped-blue)](CHECKLIST.md)",
		mapped, len(items), mapped, len(items))
	if !strings.Contains(readFile(t, root, "README.md"), want) {
		t.Fatalf("stale or missing README checklist badge; want:\n%s", want)
	}
}

// coverageGate matches the CI step that fails the build below the minimum.
var coverageGate = regexp.MustCompile(`awk -v t="\$total" 'BEGIN \{ exit !\(t \+ 0 >= ([0-9]+(?:\.[0-9]+)?)\) \}'`)

// The coverage badge and COVERAGE.md must name the minimum CI enforces, so a
// changed gate cannot leave either stale.
func TestReadmeCoverageBadge(t *testing.T) {
	root := filepath.Join("..", "..")
	// Vendored in rest-mail-server: the gate is in that repository's workflow.
	gate, err := enforcedCoverage(readFile(t, root, "..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("[![Coverage gate: >=%s%%](https://img.shields.io/badge/coverage%%20gate-%%3E%%3D%s%%25-brightgreen)](COVERAGE.md)", gate, gate)
	if !strings.Contains(readFile(t, root, "README.md"), want) {
		t.Fatalf("stale or missing README coverage badge; want:\n%s", want)
	}
	if doc := fmt.Sprintf("CI fails when statement coverage falls below %s%%.", gate); !strings.Contains(readFile(t, root, "COVERAGE.md"), doc) {
		t.Fatalf("COVERAGE.md does not state the enforced minimum; want %q", doc)
	}
}

// Workflow fixtures: the gate is read from exactly one enforcing step.
func TestEnforcedCoverage(t *testing.T) {
	step := `awk -v t="$total" 'BEGIN { exit !(t + 0 >= 95) }'`
	for _, tc := range []struct{ name, workflow, want, err string }{
		{"single gate", "run: |\n  " + step + "\n", "95", ""},
		{"decimal gate", strings.Replace(step, "95", "97.5", 1), "97.5", ""},
		{"no gate", "run: go test ./...\n", "", "no coverage gate"},
		{"two gates", step + "\n" + strings.Replace(step, "95", "90", 1), "", "2 coverage gates"},
		{"weakened comparison", strings.Replace(step, ">=", ">", 1), "", "no coverage gate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := enforcedCoverage(tc.workflow)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("enforcedCoverage = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func enforcedCoverage(workflow string) (string, error) {
	m := coverageGate.FindAllStringSubmatch(workflow, -1)
	switch len(m) {
	case 0:
		return "", fmt.Errorf("ci.yml: no coverage gate matching %s", coverageGate)
	case 1:
		return m[0][1], nil
	default:
		return "", fmt.Errorf("ci.yml: %d coverage gates, want exactly one", len(m))
	}
}

func readFile(t *testing.T, elem ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(elem...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
