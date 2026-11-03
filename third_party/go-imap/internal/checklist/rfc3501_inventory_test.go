package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const rfc3501Sections = `1 1.1 1.2 1.3 2 2.1 2.2 2.2.1 2.2.2 2.3 2.3.1 2.3.1.1 2.3.1.2 2.3.2 2.3.3 2.3.4 2.3.5 2.3.6 2.4 3 3.1 3.2 3.3 3.4 4 4.1 4.2 4.3 4.3.1 4.4 4.5 5 5.1 5.1.1 5.1.2 5.1.3 5.2 5.3 5.4 5.5 6 6.1 6.1.1 6.1.2 6.1.3 6.2 6.2.1 6.2.2 6.2.3 6.3 6.3.1 6.3.2 6.3.3 6.3.4 6.3.5 6.3.6 6.3.7 6.3.8 6.3.9 6.3.10 6.3.11 6.4 6.4.1 6.4.2 6.4.3 6.4.4 6.4.5 6.4.6 6.4.7 6.4.8 6.5 6.5.1 7 7.1 7.1.1 7.1.2 7.1.3 7.1.4 7.1.5 7.2 7.2.1 7.2.2 7.2.3 7.2.4 7.2.5 7.2.6 7.3 7.3.1 7.3.2 7.4 7.4.1 7.4.2 7.5 8 9 10 11 11.1 11.2 12 A B C`

var inventoryReference = regexp.MustCompile(`IMAP(?:2)?-[A-Z0-9]+(?:-[A-Z0-9]+)*(?:-\*)?`)

const rfc3501SourceHash = "4437974dde85859f2391f32215295e7b20ce157c3bd2adbe7e5b443e990f15cf"

func TestRFC3501Inventory(t *testing.T) {
	document, source, items := rfc3501Fixture(t)
	if err := auditRFC3501Inventory(document, source, items); err != nil {
		t.Fatal(err)
	}
}

func TestRFC3501InventoryDetectsOmissions(t *testing.T) {
	document, source, items := rfc3501Fixture(t)
	var sectionRow, grammarRow string
	for _, line := range strings.Split(document, "\n") {
		if strings.HasPrefix(line, "| 7.2.4 |") {
			sectionRow = line
		}
		if strings.HasPrefix(line, "| `address`") {
			grammarRow = line
		}
	}
	if sectionRow == "" || grammarRow == "" {
		t.Fatal("missing real inventory rows")
	}
	for _, fixture := range []struct{ name, before, after, want string }{
		{"missing section", sectionRow + "\n", "", "missing section 7.2.4"},
		{"duplicate section", sectionRow, sectionRow + "\n" + sectionRow, "duplicate section 7.2.4"},
		{"invented section", "| 7.2.4 |", "| 7.2.99 |", "unknown section 7.2.99"},
		{"unknown row", sectionRow, strings.Replace(sectionRow, "IMAP-STATUS-*", "IMAP-NONEXISTENT-01", 1), "unknown checklist reference"},
		{"excluded command", sectionRow, "| 7.2.4 | STATUS Response | n/a: ignore the STATUS requirements. |", "cannot exclude requirement section"},
		{"missing production", "`addr-adl`, ", "", "missing production addr-adl"},
		{"duplicate production", "`addr-adl`, ", "`addr-adl`, `addr-adl`, ", "duplicate production addr-adl"},
		{"unknown production", "`addr-adl`", "`invented-production`", "unknown production invented-production"},
		{"unknown boundary", grammarRow, strings.Replace(grammarRow, "IMAP-INV-ABNF-01", "IMAP-NONEXISTENT-01", 1), "unknown checklist reference"},
		{"source mismatch", rfc3501SourceHash, strings.Repeat("0", 64), "source hash"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			mutated := strings.Replace(document, fixture.before, fixture.after, 1)
			if mutated == document {
				t.Fatal("mutation did not change the real inventory")
			}
			if err := auditRFC3501Inventory(mutated, source, items); err == nil || !strings.Contains(err.Error(), fixture.want) {
				t.Fatalf("got %v, want %q", err, fixture.want)
			}
		})
	}
}

func rfc3501Fixture(t *testing.T) (string, grammarSource, map[string]*item) {
	t.Helper()
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join("testdata", "core-grammar-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index grammarIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Sources) == 0 || index.Sources[0].RFC != 3501 {
		t.Fatal("missing RFC3501 production source")
	}
	items, err := parseChecklist(filepath.Join(root, "CHECKLIST.md"), func(format string, args ...any) { t.Errorf(format, args...) })
	if err != nil {
		t.Fatal(err)
	}
	return readFile(t, root, "CHECKLIST.md"), index.Sources[0], items
}

func auditRFC3501Inventory(document string, source grammarSource, items map[string]*item) error {
	if !strings.Contains(document, "`"+rfc3501SourceHash+"`") {
		return fmt.Errorf("missing RFC3501 source hash")
	}
	sectionText, found := inventoryBetween(document, "## RFC 3501: complete section index", "### RFC 3501 section 9: every production disposition")
	if !found {
		return fmt.Errorf("missing section index")
	}
	knownSections := map[string]bool{}
	for _, section := range strings.Fields(rfc3501Sections) {
		knownSections[section] = true
	}
	exclusions := " 1 1.1 1.3 2 2.3 5 8 10 12 A B C "
	seenSections := map[string]bool{}
	for _, line := range strings.Split(sectionText, "\n") {
		cells := splitRow(line)
		if !strings.HasPrefix(line, "|") || len(cells) != 3 || cells[0] == "Section" || isSeparator(cells) {
			continue
		}
		section := cells[0]
		if !knownSections[section] {
			return fmt.Errorf("unknown section %s", section)
		}
		if seenSections[section] {
			return fmt.Errorf("duplicate section %s", section)
		}
		seenSections[section] = true
		if cells[1] == "" || cells[2] == "" {
			return fmt.Errorf("empty section %s disposition", section)
		}
		if strings.HasPrefix(cells[2], "n/a:") {
			if !strings.Contains(exclusions, " "+section+" ") {
				return fmt.Errorf("cannot exclude requirement section %s", section)
			}
			if len(strings.TrimSpace(strings.TrimPrefix(cells[2], "n/a:"))) < 15 {
				return fmt.Errorf("section %s has no exclusion reason", section)
			}
		} else if err := auditInventoryReferences(cells[2], items); err != nil {
			return fmt.Errorf("section %s: %w", section, err)
		}
	}
	for _, section := range strings.Fields(rfc3501Sections) {
		if !seenSections[section] {
			return fmt.Errorf("missing section %s", section)
		}
	}
	grammarText, found := inventoryBetween(document, "### RFC 3501 section 9: every production disposition", "## RFC 3501: connection, any-state commands and LOGIN")
	if !found {
		return fmt.Errorf("missing production dispositions")
	}
	knownProductions := map[string]bool{}
	for _, name := range source.Productions {
		knownProductions[name] = true
	}
	seenProductions := map[string]bool{}
	for _, line := range strings.Split(grammarText, "\n") {
		cells := splitRow(line)
		if !strings.HasPrefix(line, "| `") || len(cells) != 3 {
			continue
		}
		for _, match := range grammarName.FindAllStringSubmatch(cells[0], -1) {
			name := match[1]
			if !knownProductions[name] {
				return fmt.Errorf("unknown production %s", name)
			}
			if seenProductions[name] {
				return fmt.Errorf("duplicate production %s", name)
			}
			seenProductions[name] = true
		}
		for _, cell := range cells[1:] {
			if err := auditInventoryReferences(cell, items); err != nil {
				return err
			}
		}
	}
	for _, name := range source.Productions {
		if !seenProductions[name] {
			return fmt.Errorf("missing production %s", name)
		}
	}
	return nil
}

func inventoryBetween(document, start, end string) (string, bool) {
	_, tail, found := strings.Cut(document, start)
	if !found {
		return "", false
	}
	body, _, found := strings.Cut(tail, end)
	return body, found
}

func auditInventoryReferences(text string, items map[string]*item) error {
	refs := inventoryReference.FindAllString(text, -1)
	if len(refs) == 0 {
		return fmt.Errorf("no checklist references in %q", text)
	}
	for _, ref := range refs {
		if _, ok := items[ref]; ok {
			continue
		}
		found := false
		if prefix, wildcard := strings.CutSuffix(ref, "*"); wildcard {
			for id := range items {
				if strings.HasPrefix(id, prefix) {
					found = true
					break
				}
			}
		}
		if !found {
			return fmt.Errorf("unknown checklist reference %s", ref)
		}
	}
	return nil
}
