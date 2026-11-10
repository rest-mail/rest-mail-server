package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeclarationOnlyMutationFixture(t *testing.T) {
	const source = `package proof
import "testing"
type helper struct{}
// checklist: IMAP-A-01 IMAP-A-010
// checklist: IMAP-B-02 IMAP-A-01
func TestProof(t *testing.T) {
	const bodyOnly = "do not reparse the proof body for each mutation"
	_ = func() string { return bodyOnly }
}
// checklist: IMAP-C-03
func TestNeighbor(t *testing.T) { panic("body-only sentinel") }
// checklist: IMAP-D-04
func FuzzProof(f *testing.F) { f.Fuzz(func(t *testing.T, value string) {}) }
func ExampleProof() { println("body-only sentinel") }
func (helper) TestNotAProof(t *testing.T) { panic("body-only sentinel") }
func helperFunction() { panic("body-only sentinel") }
`
	compact := declarationOnlySource(t, source)
	if strings.Contains(compact, "bodyOnly") || strings.Contains(compact, "body-only sentinel") {
		t.Fatalf("mutation fixture retained proof bodies: %s", compact)
	}
	var expected map[string][]string
	for _, contents := range []string{source, compact} {
		directory := t.TempDir()
		if err := os.WriteFile(filepath.Join(directory, "proof_test.go"), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		proofs, err := scanTests([]string{directory}, func(message string, arguments ...any) {
			t.Errorf(message, arguments...)
		})
		if err != nil {
			t.Fatal(err)
		}
		actual := map[string][]string{}
		for name, proof := range proofs {
			actual[name] = proof.items
		}
		if expected == nil {
			expected = actual
		} else if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("compaction changed proof mappings: got %v, want %v", actual, expected)
		}
	}
	if len(expected) != 4 {
		t.Fatalf("scanned %d proofs, want TestProof, TestNeighbor, FuzzProof and ExampleProof", len(expected))
	}
}

func TestRealMutationCorpusPreservesChecklistMappings(t *testing.T) {
	root := filepath.Join("..", "..", "pkg", "imap", "rfctest")
	compactDirectory := t.TempDir()
	files, err := filepath.Glob(filepath.Join(root, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(compactDirectory, filepath.Base(path)), []byte(declarationOnlySource(t, string(source))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var expected map[string][]string
	for _, directory := range []string{root, compactDirectory} {
		proofs, err := scanTests([]string{directory}, func(message string, arguments ...any) {
			t.Errorf(message, arguments...)
		})
		if err != nil {
			t.Fatal(err)
		}
		actual := map[string][]string{}
		for name, proof := range proofs {
			actual[name] = proof.items
		}
		if expected == nil {
			expected = actual
		} else if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("real corpus proof mappings changed: got %v, want %v", actual, expected)
		}
	}
	if len(expected) == 0 {
		t.Fatal("real corpus has no proof functions")
	}
}

func TestUnnameMultipleAnnotationLines(t *testing.T) {
	const source = `package proof
import "testing"
// Proof with several checklist lines.
// checklist: IMAP-A-01 IMAP-A-010
// checklist: IMAP-B-02 IMAP-A-01
// checklist: IMAP-C-03
func TestProof(t *testing.T) {}
// checklist: IMAP-A-01 IMAP-B-02 IMAP-C-03
func TestNeighbor(t *testing.T) {}
`
	for _, id := range []string{"IMAP-A-01", "IMAP-A-010", "IMAP-B-02", "IMAP-C-03"} {
		t.Run(id, func(t *testing.T) {
			got := unname(t, source, "TestProof", id)
			proof, neighbor, ok := strings.Cut(got, "func TestProof")
			if !ok || neighbor != strings.SplitN(source, "func TestProof", 2)[1] {
				t.Fatalf("fixture changed another function: %s", got)
			}
			for _, field := range strings.Fields(proof) {
				if field == id {
					t.Fatalf("fixture retained %s: %s", id, proof)
				}
			}
			for _, other := range []string{"IMAP-A-01", "IMAP-A-010", "IMAP-B-02", "IMAP-C-03"} {
				if other != id && !strings.Contains(proof, " "+other+"\n") && !strings.Contains(proof, " "+other+" ") {
					t.Fatalf("removing %s also removed %s: %s", id, other, proof)
				}
			}
		})
	}
}
