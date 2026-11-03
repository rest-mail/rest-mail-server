package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type grammarSource struct {
	RFC         int      `json:"rfc"`
	Productions []string `json:"productions"`
	Incremental []string `json:"incremental_alternatives"`
}

type grammarIndex struct {
	Sources []grammarSource `json:"sources"`
}

// TestCoreGrammarAudit compares the human crosswalk with an independently
// pinned index extracted from the two RFC Editor section-9 source texts. It
// checks inventory completeness, not conformance of the protocol engine.
func TestCoreGrammarAudit(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join("testdata", "core-grammar-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	const indexHash = "c5732b902f288f588be32aa448e2b76a5a3bca2f9be070246f754bd5f28c196f"
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != indexHash {
		t.Fatalf("canonical RFC production index changed: %s; re-audit the original source before updating this pin", got)
	}
	var index grammarIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Sources) != 2 || len(index.Sources[0].Productions) != 148 || len(index.Sources[1].Productions) != 232 {
		t.Fatal("unexpected canonical production counts")
	}
	if len(index.Sources[1].Incremental) != 1 || index.Sources[1].Incremental[0] != "sequence-set" {
		t.Fatal("RFC9051 sequence-set incremental alternative is missing")
	}
	items, err := parseChecklist(filepath.Join(root, "CHECKLIST.md"), func(f string, a ...any) { t.Errorf(f, a...) })
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.id)
	}
	document := readFile(t, root, "RFC-GRAMMAR.md")
	if err := auditGrammar(index, document, ids); err != nil {
		t.Fatal(err)
	}
}

// TestCoreGrammarAuditDetectsOmissions proves the guard rejects the actual
// crosswalk losing a family, gaining duplicates/unknown rules, or pointing at
// a nonexistent requirement family. This is independent of runtime behavior.
func TestCoreGrammarAuditDetectsOmissions(t *testing.T) {
	root := filepath.Join("..", "..")
	var index grammarIndex
	raw, err := os.ReadFile(filepath.Join("testdata", "core-grammar-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	items, err := parseChecklist(filepath.Join(root, "CHECKLIST.md"), func(f string, a ...any) { t.Errorf(f, a...) })
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range items {
		ids = append(ids, it.id)
	}
	document := readFile(t, root, "RFC-GRAMMAR.md")
	var first string
	for _, line := range strings.Split(document, "\n") {
		if strings.HasPrefix(line, "| 3501 | ") {
			first = line
			break
		}
	}
	if first == "" {
		t.Fatal("no real crosswalk row")
	}
	for _, tc := range []struct{ name, doc, want string }{
		{"missing family", strings.Replace(document, first, "", 1), "missing production"},
		{"duplicate family", document + "\n" + first, "duplicate production"},
		{"unknown production", strings.Replace(document, "`digit-nz`", "`invented-production`", 1), "unknown production"},
		{"unknown requirement", strings.Replace(document, "IMAP-SYNTAX-*", "IMAP-NONEXISTENT-*", 1), "unknown checklist reference"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := auditGrammar(index, tc.doc, ids); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

var grammarName = regexp.MustCompile("`([A-Za-z][A-Za-z0-9-]*)`")

func auditGrammar(index grammarIndex, document string, ids []string) error {
	known := map[string]bool{}
	for _, source := range index.Sources {
		for _, name := range source.Productions {
			known[strconv.Itoa(source.RFC)+"/"+strings.ToLower(name)] = true
		}
	}
	seen := map[string]bool{}
	for number, line := range strings.Split(document, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) != 7 {
			continue
		}
		rfc := strings.TrimSpace(cells[1])
		if rfc != "3501" && rfc != "9051" {
			continue
		}
		if strings.TrimSpace(cells[2]) == "" || strings.TrimSpace(cells[5]) == "" {
			return fmt.Errorf("line %d: empty audit family or boundary", number+1)
		}
		names := grammarName.FindAllStringSubmatch(cells[3], -1)
		if len(names) == 0 {
			return fmt.Errorf("line %d: no productions", number+1)
		}
		for _, name := range names {
			key := rfc + "/" + strings.ToLower(name[1])
			if !known[key] {
				return fmt.Errorf("line %d: unknown production %s", number+1, key)
			}
			if seen[key] {
				return fmt.Errorf("line %d: duplicate production %s", number+1, key)
			}
			seen[key] = true
		}
		for _, ref := range strings.Split(cells[4], ";") {
			ref = strings.TrimSpace(ref)
			found := false
			for _, id := range ids {
				if id == ref || (strings.HasSuffix(ref, "-*") && strings.HasPrefix(id, strings.TrimSuffix(ref, "*"))) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("line %d: unknown checklist reference %s", number+1, ref)
			}
		}
	}
	for key := range known {
		if !seen[key] {
			return fmt.Errorf("missing production %s", key)
		}
	}
	return nil
}
