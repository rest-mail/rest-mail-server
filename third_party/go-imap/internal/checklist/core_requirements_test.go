package main

import (
	"encoding/csv"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAC01CoreRequirementInventory(t *testing.T) {
	data, err := os.ReadFile("testdata/core-requirements.tsv")
	if err != nil {
		t.Fatal(err)
	}
	counts, err := auditCoreRequirements(string(data))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d requirements: %d supported, %d refused, %d out of scope",
		counts["supported"]+counts["refused"]+counts["out_of_scope"], counts["supported"], counts["refused"], counts["out_of_scope"])
	for _, control := range []struct{ name, old, replacement, want string }{
		{"missing verdict", "\tsupported\t", "\t\t", "invalid verdict"},
		{"missing citation", "\twire/session_test.go#TestGreeting\t", "\t\t", "missing citation"},
		{"missing reason", "The session accepts a connected byte stream and writes an untagged greeting before reading commands.", "", "missing reason"},
		{"stale test citation", "#TestGreeting\t", "#TestInventedGreeting\t", "missing symbol"},
		{"omitted section", "3501:2.1;9051:2.1", "3501:2.2;9051:2.2", "missing disposition for RFC 3501 §2.1"},
	} {
		t.Run(control.name, func(t *testing.T) {
			mutated := strings.Replace(string(data), control.old, control.replacement, 1)
			if mutated == string(data) {
				t.Fatal("control did not change the inventory")
			}
			if _, err := auditCoreRequirements(mutated); err == nil || !strings.Contains(err.Error(), control.want) {
				t.Fatalf("want %q, got %v", control.want, err)
			}
		})
	}
}

func auditCoreRequirements(data string) (map[string]int, error) {
	reader := csv.NewReader(strings.NewReader(data))
	reader.Comma = '\t'
	reader.Comment = '#'
	reader.FieldsPerRecord = 5
	header, err := reader.Read()
	if err != nil || !slices.Equal(header, []string{"rfc_sections", "requirement", "verdict", "evidence", "reason"}) {
		return nil, fmt.Errorf("invalid inventory header: %v (%v)", header, err)
	}
	sections := map[string]string{"3501": rfc3501Sections, "9051": rfc9051Sections}
	seenRequirements := map[string]bool{}
	seenSections := map[string]bool{}
	counts := map[string]int{}
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		for _, field := range row {
			if strings.TrimSpace(field) != field {
				return nil, fmt.Errorf("unexpected surrounding whitespace in %q", field)
			}
		}
		source, requirement, verdict, evidence, reason := row[0], row[1], row[2], row[3], row[4]
		if requirement == "" || seenRequirements[requirement] || reason == "" {
			return nil, fmt.Errorf("missing or duplicate requirement or missing reason: %q", row)
		}
		seenRequirements[requirement] = true
		switch verdict {
		case "supported", "refused", "out_of_scope":
			counts[verdict]++
		default:
			return nil, fmt.Errorf("%s: invalid verdict %q", requirement, verdict)
		}
		for _, locators := range strings.Split(source, ";") {
			rfc, locators, _ := strings.Cut(locators, ":")
			for _, section := range strings.Split(locators, ",") {
				if !slices.Contains(strings.Fields(sections[rfc]), section) {
					return nil, fmt.Errorf("%s: unknown RFC section %s:%s", requirement, rfc, section)
				}
				seenSections[rfc+":"+section] = true
			}
		}
		if evidence == "" {
			return nil, fmt.Errorf("%s: missing citation", requirement)
		}
		var hasTest bool
		for _, ref := range strings.Split(evidence, ";") {
			if ref == "rfc" && verdict == "out_of_scope" {
				continue
			}
			isTest, err := checkCoreEvidence(ref)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", requirement, err)
			}
			hasTest = hasTest || isTest
		}
		if verdict == "supported" && !hasTest {
			return nil, fmt.Errorf("%s: supported verdict needs a test citation", requirement)
		}
	}
	for _, rfc := range []string{"3501", "9051"} {
		for _, section := range strings.Fields(sections[rfc]) {
			if !seenSections[rfc+":"+section] {
				return nil, fmt.Errorf("missing disposition for RFC %s §%s", rfc, section)
			}
		}
	}
	return counts, nil
}

func checkCoreEvidence(ref string) (bool, error) {
	area, address, _ := strings.Cut(ref, "/")
	file, symbol, _ := strings.Cut(address, "#")
	paths := map[string]string{
		"wire": "../../pkg/imap/rfctest", "engine": "../../pkg/imap/server", "checklist": ".",
		"gateway": "../../../../internal/gateway/imap", "api": "../../../../internal/api",
		"handlers": "../../../../internal/api/handlers",
	}
	if paths[area] == "" || filepath.Base(file) != file || !strings.HasSuffix(file, ".go") || symbol == "" {
		return false, fmt.Errorf("invalid citation %q", ref)
	}
	source, err := parser.ParseFile(token.NewFileSet(), filepath.Join(paths[area], file), nil, parser.SkipObjectResolution)
	if err != nil {
		return false, fmt.Errorf("citation %s: %w", ref, err)
	}
	for _, decl := range source.Decls {
		if function, ok := decl.(*ast.FuncDecl); ok && function.Name.Name == symbol {
			return function.Recv == nil && strings.HasPrefix(symbol, "Test") && strings.HasSuffix(file, "_test.go"), nil
		}
	}
	return false, fmt.Errorf("missing symbol in citation %q", ref)
}
