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

type sessionExtensionIndex struct {
	Sources []sessionExtensionSource `json:"sources"`
}
type sessionExtensionSource struct {
	RFC      int                       `json:"rfc"`
	URL      string                    `json:"url"`
	SHA256   string                    `json:"sha256"`
	Sections []sessionExtensionSection `json:"sections"`
}
type sessionExtensionSection struct {
	Section      string   `json:"section"`
	Title        string   `json:"title"`
	SHA256       string   `json:"source_section_sha256"`
	Requirements []string `json:"requirements"`
	Note         string   `json:"note"`
}

// Independent source/section pins prevent deleting a source section from both
// the rendered document and its editable crosswalk without a deliberate audit
// update. They do not establish completeness of individual sentence mappings.
var sessionExtensionPins = map[int]struct{ sha, sections string }{
	2595: {"5d644cd1053214b5f2a75584efedb16acac07472b099496562fbfe574c2d775b", "1 1.1 2 2.1 2.2 2.3 2.4 2.5 3 3.1 3.2 4 5 5.1 6 7 8 9 10 11 A"},
	4616: {"33aa677ab7b8efe52e86147c63b38dd8dd5de96438c63e10ba8a04504bbdbca4", "1 2 3 4 5 6 7 8 9 A"},
	4959: {"22a39f041118aec0cb7b5f879ccba9cc146081c5c138768b79bbd4e798ac44b8", "1 2 3 4 5 6 7 8 9 9.1 9.2"},
	5161: {"a8a34b3442e20ac21336e32a418828c21e24c253ca5d1706d9a0019e8f1301e3", "1 2 3 3.1 3.2 3.3 4 5 6 7 8 9"},
	3691: {"2d3527e0d7cb70a3877f77685d487eed5044671197fd5f437be4f7a540dd958b", "1 2 3 4 5 6 7 8 9"},
	2177: {"600c02aeddb4dc47618700797b2293115d077404ce1781990fdad1a88597d8d2", "1 2 3 4 5 6 7"},
	7888: {"62e676eeece48d072ab1e2049613357ef47e7a9f3fb52163f27eece0d7d95b15", "1 2 3 4 5 6 7 8 9 10 11 11.1 11.2"},
}

func validateSessionExtensionIndex(index sessionExtensionIndex, items map[string]*item, doc string) error {
	if len(index.Sources) != len(sessionExtensionPins) {
		return fmt.Errorf("source count: got%d want%d", len(index.Sources), len(sessionExtensionPins))
	}
	seenRFC := map[int]bool{}
	hex := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, source := range index.Sources {
		pin, ok := sessionExtensionPins[source.RFC]
		if !ok || seenRFC[source.RFC] {
			return fmt.Errorf("unknown or duplicate RFC%d", source.RFC)
		}
		seenRFC[source.RFC] = true
		if source.SHA256 != pin.sha || source.URL != fmt.Sprintf("https://www.rfc-editor.org/rfc/rfc%d.txt", source.RFC) {
			return fmt.Errorf("source pin mismatch RFC%d", source.RFC)
		}
		want := strings.Fields(pin.sections)
		if len(source.Sections) != len(want) {
			return fmt.Errorf("section count RFC%d", source.RFC)
		}
		for i, section := range source.Sections {
			if section.Section != want[i] {
				return fmt.Errorf("missing, reordered or duplicate section RFC%d/%s", source.RFC, want[i])
			}
			if section.Title == "" || strings.TrimSpace(section.Note) == "" || !hex.MatchString(section.SHA256) {
				return fmt.Errorf("incomplete disposition RFC%d/%s", source.RFC, section.Section)
			}
			refs := make([]string, 0, len(section.Requirements))
			seenID := map[string]bool{}
			for _, id := range section.Requirements {
				if items[id] == nil || seenID[id] {
					return fmt.Errorf("unknown or duplicate requirement %s", id)
				}
				seenID[id] = true
				refs = append(refs, "`"+id+"`")
			}
			rendered := strings.Join(refs, ", ")
			if len(refs) == 0 {
				rendered = "—"
			}
			row := fmt.Sprintf("| RFC%d §%s | %s | %s |", source.RFC, section.Section, rendered, section.Note)
			if strings.Count(doc, row) != 1 {
				return fmt.Errorf("crosswalk drift RFC%d/%s", source.RFC, section.Section)
			}
		}
	}
	return nil
}

func loadSessionExtensionAudit(t *testing.T) (sessionExtensionIndex, map[string]*item, string) {
	t.Helper()
	root := filepath.Join("..", "..")
	data, err := os.ReadFile(filepath.Join("testdata", "session-extensions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index sessionExtensionIndex
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	items, err := parseChecklist(filepath.Join(root, "CHECKLIST.md"), func(format string, args ...any) { t.Errorf(format, args...) })
	if err != nil {
		t.Fatal(err)
	}
	return index, items, readFile(t, root, "RFC-SESSION-EXTENSIONS.md")
}

func TestSessionExtensionInventory(t *testing.T) {
	index, items, doc := loadSessionExtensionAudit(t)
	if err := validateSessionExtensionIndex(index, items, doc); err != nil {
		t.Fatal(err)
	}
}

// A lost section, duplicated source, stale source pin, invented requirement or
// drift between the machine index and review document must fail closed.
func TestSessionExtensionInventoryMutations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*sessionExtensionIndex, *string)
		want   string
	}{
		{"lost RFC", func(x *sessionExtensionIndex, _ *string) { x.Sources = x.Sources[1:] }, "source count"},
		{"duplicate RFC", func(x *sessionExtensionIndex, _ *string) { x.Sources[1] = x.Sources[0] }, "duplicate RFC"},
		{"lost section", func(x *sessionExtensionIndex, _ *string) { x.Sources[0].Sections = x.Sources[0].Sections[1:] }, "section count"},
		{"duplicate section", func(x *sessionExtensionIndex, _ *string) { x.Sources[0].Sections[1] = x.Sources[0].Sections[0] }, "duplicate section"},
		{"source drift", func(x *sessionExtensionIndex, _ *string) { x.Sources[0].SHA256 = strings.Repeat("0", 64) }, "source pin mismatch"},
		{"unknown requirement", func(x *sessionExtensionIndex, _ *string) {
			x.Sources[0].Sections[0].Requirements = []string{"IMAP-INVENTED-999"}
		}, "unknown or duplicate requirement"},
		{"empty disposition", func(x *sessionExtensionIndex, _ *string) { x.Sources[0].Sections[0].Note = "" }, "incomplete disposition"},
		{"document drift", func(_ *sessionExtensionIndex, doc *string) {
			*doc = strings.Replace(*doc, "| RFC2595 §1 |", "| RFC2595 §999 |", 1)
		}, "crosswalk drift"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index, items, doc := loadSessionExtensionAudit(t)
			tc.mutate(&index, &doc)
			err := validateSessionExtensionIndex(index, items, doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}
