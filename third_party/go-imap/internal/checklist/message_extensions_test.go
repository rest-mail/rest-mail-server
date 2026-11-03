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

type messageExtensionIndex struct {
	Sources []messageExtensionSource `json:"sources"`
}
type messageExtensionSource struct {
	RFC      int                       `json:"rfc"`
	URL      string                    `json:"url"`
	SHA256   string                    `json:"sha256"`
	Sections []messageExtensionSection `json:"sections"`
}
type messageExtensionSection struct {
	Section      string   `json:"section"`
	Title        string   `json:"title"`
	SHA256       string   `json:"source_section_sha256"`
	Requirements []string `json:"requirements"`
	Note         string   `json:"note"`
}

// Independent source/section pins prevent deleting a source section from both
// the rendered document and its editable crosswalk without a deliberate audit
// update. They do not establish completeness of individual sentence mappings.
var messageExtensionPins = map[int]struct{ sha, sections string }{
	3516: {"53b70a42caba0863cdd4e054a097acd6a073261eef5c2f0585670e318c35bc00", "1 2 3 4 4.1 4.2 4.3 4.4 5 6 7 8 9 10 11 12"},
	4315: {"4ce974eeaa9110752c7f803ee16bb1c39bd84a2f836a68e413cecbb4c61fa805", "1 1.1 2 2.1 3 4 5 6 7 8 9"},
	4731: {"e5fad166cf955ab1420a57db02cef70eefe15e6db55f80f8fb99067bfb55facd", "1 2 3 3.1 3.2 4 5 6 7 8"},
	5182: {"449a1a747d75a37cbcf01e099e32357ed24d13bdbeb91f1df7ca667717afff6a", "1 1.1 2 2.1 2.2 2.3 2.4 2.5 3 4 5 6 7 7.1 7.2"},
	6851: {"c4d5c0a7297ae1d23982ceb8422ddae8275483c4fef7e5db56edc46e55608290", "1 2 3 3.1 3.2 3.3 4 4.1 4.2 4.3 4.4 4.5 5 6 7 8 9 9.1 9.2"},
	6855: {"d9b7eba828f5ec10b3d1d89a4ced437fbc4dd7813aacde08b8addbf8d351a1ed", "1 2 3 4 5 6 7 8 9 10 11 11.1 11.2"},
}

func validateMessageExtensionIndex(index messageExtensionIndex, items map[string]*item, doc string) error {
	if len(index.Sources) != len(messageExtensionPins) {
		return fmt.Errorf("source count: got%d want%d", len(index.Sources), len(messageExtensionPins))
	}
	seenRFC := map[int]bool{}
	hex := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, source := range index.Sources {
		pin, ok := messageExtensionPins[source.RFC]
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

func loadMessageExtensionAudit(t *testing.T) (messageExtensionIndex, map[string]*item, string) {
	t.Helper()
	root := filepath.Join("..", "..")
	data, err := os.ReadFile(filepath.Join("testdata", "message-extensions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index messageExtensionIndex
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	items, err := parseChecklist(filepath.Join(root, "CHECKLIST.md"), func(format string, args ...any) { t.Errorf(format, args...) })
	if err != nil {
		t.Fatal(err)
	}
	return index, items, readFile(t, root, "RFC-MESSAGE-EXTENSIONS.md")
}

func TestMessageExtensionInventory(t *testing.T) {
	index, items, doc := loadMessageExtensionAudit(t)
	if err := validateMessageExtensionIndex(index, items, doc); err != nil {
		t.Fatal(err)
	}
}

// A lost section, duplicated source, stale source pin, invented requirement or
// drift between the machine index and review document must fail closed.
func TestMessageExtensionInventoryMutations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*messageExtensionIndex, *string)
		want   string
	}{
		{"lost RFC", func(x *messageExtensionIndex, _ *string) { x.Sources = x.Sources[1:] }, "source count"},
		{"duplicate RFC", func(x *messageExtensionIndex, _ *string) { x.Sources[1] = x.Sources[0] }, "duplicate RFC"},
		{"lost section", func(x *messageExtensionIndex, _ *string) { x.Sources[0].Sections = x.Sources[0].Sections[1:] }, "section count"},
		{"duplicate section", func(x *messageExtensionIndex, _ *string) { x.Sources[0].Sections[1] = x.Sources[0].Sections[0] }, "duplicate section"},
		{"source drift", func(x *messageExtensionIndex, _ *string) { x.Sources[0].SHA256 = strings.Repeat("0", 64) }, "source pin mismatch"},
		{"unknown requirement", func(x *messageExtensionIndex, _ *string) {
			x.Sources[0].Sections[0].Requirements = []string{"IMAP-INVENTED-999"}
		}, "unknown or duplicate requirement"},
		{"empty disposition", func(x *messageExtensionIndex, _ *string) { x.Sources[0].Sections[0].Note = "" }, "incomplete disposition"},
		{"document drift", func(_ *messageExtensionIndex, doc *string) {
			*doc = strings.Replace(*doc, "| RFC3516 §1 |", "| RFC3516 §999 |", 1)
		}, "crosswalk drift"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index, items, doc := loadMessageExtensionAudit(t)
			tc.mutate(&index, &doc)
			err := validateMessageExtensionIndex(index, items, doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}
