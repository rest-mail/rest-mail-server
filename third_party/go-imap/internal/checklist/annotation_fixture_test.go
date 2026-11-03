package main

import (
	"strings"
	"testing"
)

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
