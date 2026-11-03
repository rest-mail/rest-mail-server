package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// rfc9051Sections lists every numbered section and appendix entry of the RFC
// Editor's RFC 9051 text, whose hash is pinned in CHECKLIST.md.
const rfc9051Sections = `1 1.1 1.2 1.3 2 2.1 2.2 2.2.1 2.2.2 2.3 2.3.1 2.3.1.1 2.3.1.2 2.3.2 2.3.3 2.3.4 2.3.5 2.3.6 2.4 3 3.1 3.2 3.3 3.4 4 4.1 4.1.1 4.2 4.3 4.3.1 4.4 4.5 5 5.1 5.1.1 5.1.2 5.1.2.1 5.1.2.2 5.2 5.3 5.4 5.5 6 6.1 6.1.1 6.1.2 6.1.3 6.2 6.2.1 6.2.2 6.2.3 6.3 6.3.1 6.3.1.1 6.3.2 6.3.3 6.3.4 6.3.5 6.3.6 6.3.7 6.3.8 6.3.9 6.3.9.1 6.3.9.2 6.3.9.3 6.3.9.4 6.3.9.5 6.3.9.6 6.3.9.7 6.3.9.8 6.3.10 6.3.11 6.3.12 6.3.13 6.4 6.4.1 6.4.2 6.4.3 6.4.4 6.4.4.1 6.4.4.2 6.4.4.3 6.4.4.4 6.4.5 6.4.5.1 6.4.6 6.4.7 6.4.8 6.4.9 6.5 7 7.1 7.1.1 7.1.2 7.1.3 7.1.4 7.1.5 7.2 7.2.1 7.2.2 7.3 7.3.1 7.3.2 7.3.3 7.3.4 7.3.5 7.4 7.4.1 7.5 7.5.1 7.5.2 7.6 8 9 10 11 11.1 11.2 11.3 11.4 11.5 11.6 11.7 12 12.1 12.2 12.3 12.4 13 13.1 13.2 13.2.1 13.2.2 A A.1 B C D E F`

// rfc9051Exclusions are the sections that may be marked n/a: headings,
// examples, client-only duties, IANA and bibliography. Any other section must
// cite checklist rows.
const rfc9051Exclusions = " 1 1.1 1.3 2 2.3 5 6.3.1.1 6.3.9.4 6.3.9.8 6.4.4.4 8 10 11.3 12 12.1 12.2 12.3 12.4 13 13.1 13.2 13.2.1 13.2.2 F "

const rfc9051SourceHash = "acf52a50eb748ea21b5ffdcb31c761d3fbba6242d53d4f53c2555de039c5c3cf"

const (
	rfc9051IndexStart = "## RFC 9051: complete section index"
	rfc9051IndexEnd   = "## RFC 9051: core inventory (sections 2–5; first pass)"
)

func TestRFC9051Inventory(t *testing.T) {
	document, items := rfc9051Fixture(t)
	if err := auditRFC9051Inventory(document, items); err != nil {
		t.Fatal(err)
	}
}

func TestRFC9051InventoryDetectsOmissions(t *testing.T) {
	document, items := rfc9051Fixture(t)
	index, _ := inventoryBetween(document, rfc9051IndexStart, rfc9051IndexEnd)
	var sectionRow string
	for _, line := range strings.Split(index, "\n") {
		if strings.HasPrefix(line, "| 6.3.11 |") {
			sectionRow = line
		}
	}
	if sectionRow == "" {
		t.Fatal("missing real inventory row")
	}
	for _, fixture := range []struct{ name, before, after, want string }{
		{"missing section", sectionRow + "\n", "", "missing section 6.3.11"},
		{"duplicate section", sectionRow, sectionRow + "\n" + sectionRow, "duplicate section 6.3.11"},
		{"invented section", sectionRow, strings.Replace(sectionRow, "| 6.3.11 |", "| 6.3.99 |", 1), "unknown section 6.3.99"},
		{"unknown row", sectionRow, strings.Replace(sectionRow, "IMAP2-DELTA-03", "IMAP2-NONEXISTENT-01", 1), "unknown checklist reference"},
		{"excluded command", sectionRow, "| 6.3.11 | STATUS Command | n/a: ignore the STATUS requirements. |", "cannot exclude requirement section"},
		{"exclusion without reason", "n/a: Author acknowledgement and history; no server behavior.", "n/a: none", "section 10 has no exclusion reason"},
		{"source mismatch", rfc9051SourceHash, strings.Repeat("0", 64), "source hash"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			mutated := strings.Replace(document, fixture.before, fixture.after, 1)
			if mutated == document {
				t.Fatal("mutation did not change the real inventory")
			}
			if err := auditRFC9051Inventory(mutated, items); err == nil || !strings.Contains(err.Error(), fixture.want) {
				t.Fatalf("got %v, want %q", err, fixture.want)
			}
		})
	}
}

func rfc9051Fixture(t *testing.T) (string, map[string]*item) {
	t.Helper()
	root := filepath.Join("..", "..")
	items, err := parseChecklist(filepath.Join(root, "CHECKLIST.md"), func(format string, args ...any) { t.Errorf(format, args...) })
	if err != nil {
		t.Fatal(err)
	}
	return readFile(t, root, "CHECKLIST.md"), items
}

func auditRFC9051Inventory(document string, items map[string]*item) error {
	sectionText, found := inventoryBetween(document, rfc9051IndexStart, rfc9051IndexEnd)
	if !found {
		return fmt.Errorf("missing RFC9051 section index")
	}
	if !strings.Contains(sectionText, "`"+rfc9051SourceHash+"`") {
		return fmt.Errorf("missing RFC9051 source hash")
	}
	knownSections := map[string]bool{}
	for _, section := range strings.Fields(rfc9051Sections) {
		knownSections[section] = true
	}
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
			if !strings.Contains(rfc9051Exclusions, " "+section+" ") {
				return fmt.Errorf("cannot exclude requirement section %s", section)
			}
			if len(strings.TrimSpace(strings.TrimPrefix(cells[2], "n/a:"))) < 15 {
				return fmt.Errorf("section %s has no exclusion reason", section)
			}
		} else if err := auditInventoryReferences(cells[2], items); err != nil {
			return fmt.Errorf("section %s: %w", section, err)
		}
	}
	for _, section := range strings.Fields(rfc9051Sections) {
		if !seenSections[section] {
			return fmt.Errorf("missing section %s", section)
		}
	}
	return nil
}
