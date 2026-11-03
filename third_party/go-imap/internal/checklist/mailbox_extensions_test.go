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

type mailboxExtensionIndex struct {
	Sources []mailboxExtensionSource `json:"sources"`
}
type mailboxExtensionSource struct {
	RFC      int                       `json:"rfc"`
	URL      string                    `json:"url"`
	SHA256   string                    `json:"sha256"`
	Sections []mailboxExtensionSection `json:"sections"`
}
type mailboxExtensionSection struct {
	Section      string   `json:"section"`
	Title        string   `json:"title"`
	SHA256       string   `json:"source_section_sha256"`
	Requirements []string `json:"requirements"`
	Note         string   `json:"note"`
}

// Independent source/section pins prevent deleting a source section from both
// the rendered document and its editable crosswalk without a deliberate audit
// update. They do not establish completeness of individual sentence mappings.
var mailboxExtensionPins = map[int]struct{ sha, sections string }{
	2342: {"6023278a88e94a0b1a530b1e7a14a37e89699b07d7ba65e3bfe45dd276d56aab", "1 2 3 4 5 6 7 8 9 11 12"},
	8438: {"36432e42934250b81f19e2c7f529decc66508dcbfad19a3225654c4a375673f1", "1 2 3 4 5 6 7"},
	5819: {"7cecc3c6f63e70e872a2cc62888ecfdb5f701f11b339bb4c81e8ad40759de63a", "1 1.1 2 3 4 5 6 7 8"},
	6154: {"c5908ee88071617d19fc847b947b149a58fb2d65366f6c800683c5b8bc3cfc0f", "1 1.1 2 3 4 5 5.1 5.2 5.3 5.4 6 7 8 8.1 8.2 8.3 8.4 8.5 8.6 9 9.1 9.2"},
	5258: {"9ec84900ac85244a0ec0d530fc704349737570ffaaada7b20e78725714bccb02", "1 2 3 3.1 3.2 3.3 3.4 3.5 4 5 6 7 8 9 9.1 9.2 9.3 9.4 9.5 9.6 10 11 11.1 11.2"},
}

func validateMailboxExtensionIndex(index mailboxExtensionIndex, items map[string]*item, doc string) error {
	if len(index.Sources) != len(mailboxExtensionPins) {
		return fmt.Errorf("source count: got%d want%d", len(index.Sources), len(mailboxExtensionPins))
	}
	seenRFC := map[int]bool{}
	hex := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, source := range index.Sources {
		pin, ok := mailboxExtensionPins[source.RFC]
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

func loadMailboxExtensionAudit(t *testing.T) (mailboxExtensionIndex, map[string]*item, string) {
	t.Helper()
	root := filepath.Join("..", "..")
	data, err := os.ReadFile(filepath.Join("testdata", "mailbox-extensions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index mailboxExtensionIndex
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	items, err := parseChecklist(filepath.Join(root, "CHECKLIST.md"), func(format string, args ...any) { t.Errorf(format, args...) })
	if err != nil {
		t.Fatal(err)
	}
	return index, items, readFile(t, root, "RFC-MAILBOX-EXTENSIONS.md")
}

func TestMailboxExtensionInventory(t *testing.T) {
	index, items, doc := loadMailboxExtensionAudit(t)
	if err := validateMailboxExtensionIndex(index, items, doc); err != nil {
		t.Fatal(err)
	}
}

// A lost section, duplicated source, stale source pin, invented requirement or
// drift between the machine index and review document must fail closed.
func TestMailboxExtensionInventoryMutations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*mailboxExtensionIndex, *string)
		want   string
	}{
		{"lost RFC", func(x *mailboxExtensionIndex, _ *string) { x.Sources = x.Sources[1:] }, "source count"},
		{"duplicate RFC", func(x *mailboxExtensionIndex, _ *string) { x.Sources[1] = x.Sources[0] }, "duplicate RFC"},
		{"lost section", func(x *mailboxExtensionIndex, _ *string) { x.Sources[0].Sections = x.Sources[0].Sections[1:] }, "section count"},
		{"duplicate section", func(x *mailboxExtensionIndex, _ *string) { x.Sources[0].Sections[1] = x.Sources[0].Sections[0] }, "duplicate section"},
		{"source drift", func(x *mailboxExtensionIndex, _ *string) { x.Sources[0].SHA256 = strings.Repeat("0", 64) }, "source pin mismatch"},
		{"unknown requirement", func(x *mailboxExtensionIndex, _ *string) {
			x.Sources[0].Sections[0].Requirements = []string{"IMAP-INVENTED-999"}
		}, "unknown or duplicate requirement"},
		{"empty disposition", func(x *mailboxExtensionIndex, _ *string) { x.Sources[0].Sections[0].Note = "" }, "incomplete disposition"},
		{"document drift", func(_ *mailboxExtensionIndex, doc *string) {
			*doc = strings.Replace(*doc, "| RFC2342 §1 |", "| RFC2342 §999 |", 1)
		}, "crosswalk drift"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index, items, doc := loadMailboxExtensionAudit(t)
			tc.mutate(&index, &doc)
			err := validateMailboxExtensionIndex(index, items, doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}
