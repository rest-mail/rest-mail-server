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
// must never contribute to a green README badge. The checklist is a partial
// inventory, so the badge says so.
func TestReadmeChecklistBadge(t *testing.T) {
	root := filepath.Join("..", "..")
	file := filepath.Join(root, "CHECKLIST.md")
	var out bytes.Buffer
	if code := run([]string{"-file", file, filepath.Join(root, "pkg", "imap", "rfctest")}, &out, &out); code != 0 {
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
	want := fmt.Sprintf("[![Partial checklist: %d/%d mapped](https://img.shields.io/badge/partial%%20checklist-%d%%2F%d%%20mapped-blue)](CHECKLIST.md)",
		mapped, len(items), mapped, len(items))
	if !strings.Contains(readFile(t, root, "README.md"), want) {
		t.Fatalf("stale or missing README checklist badge; want:\n%s", want)
	}
}

// coverageGate matches the lower bound check_coverage.py enforces on the
// coverage total CI pipes into it.
var coverageGate = regexp.MustCompile(`if not Decimal\(([0-9]+(?:\.[0-9]+)?)\) <= total <= Decimal\(100\):`)

// The coverage badge and COVERAGE.md must name the minimum CI enforces, so a
// changed gate cannot leave either stale.
func TestReadmeCoverageBadge(t *testing.T) {
	root := filepath.Join("..", "..")
	gate, err := enforcedCoverage(readFile(t, root, ".github", "scripts", "check_coverage.py"))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("[![Coverage gate: >=%s%%](https://img.shields.io/badge/coverage%%20gate-%%3E%%3D%s%%25-brightgreen)](COVERAGE.md)", gate, gate)
	if !strings.Contains(readFile(t, root, "README.md"), want) {
		t.Fatalf("stale or missing README coverage badge; want:\n%s", want)
	}
	if doc := fmt.Sprintf("CI requires at least **%s%% statement coverage**", gate); !strings.Contains(readFile(t, root, "COVERAGE.md"), doc) {
		t.Fatalf("COVERAGE.md does not state the enforced minimum; want %q", doc)
	}
}

// Script fixtures: the gate is read from exactly one enforcing comparison.
func TestEnforcedCoverage(t *testing.T) {
	check := "    if not Decimal(95) <= total <= Decimal(100):\n        raise ValueError('below')\n"
	for _, tc := range []struct{ name, script, want, err string }{
		{"single gate", check, "95", ""},
		{"decimal gate", strings.Replace(check, "95", "97.5", 1), "97.5", ""},
		{"no gate", "print(total)\n", "", "no coverage gate"},
		{"two gates", check + strings.Replace(check, "95", "90", 1), "", "2 coverage gates"},
		{"weakened comparison", strings.Replace(check, "<= total", "< total", 1), "", "no coverage gate"},
		{"no upper bound", strings.Replace(check, " <= Decimal(100)", "", 1), "", "no coverage gate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := enforcedCoverage(tc.script)
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

func enforcedCoverage(script string) (string, error) {
	m := coverageGate.FindAllStringSubmatch(script, -1)
	switch len(m) {
	case 0:
		return "", fmt.Errorf("check_coverage.py: no coverage gate matching %s", coverageGate)
	case 1:
		return m[0][1], nil
	default:
		return "", fmt.Errorf("check_coverage.py: %d coverage gates, want exactly one", len(m))
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
