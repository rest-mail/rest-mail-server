package wire

import "testing"

func TestValidCommandUTF8TokenBoundaries(t *testing.T) {
	for _, text := range []string{
		`SUBJECT ascii`, `SUBJECT ""`, `SUBJECT "Grüße"`,
		`OR SUBJECT "a\"Grüße" SUBJECT "b\\Grüße"`,
		`SUBJECT "Grüße" SEEN`, `"Grüße" "é"`,
	} {
		if !ValidCommandUTF8(text) {
			t.Errorf("legal quoted text rejected: %q", text)
		}
	}
	for _, text := range []string{
		`SUBJECT Grüße`, `ſEEN`, `"Grüße"ſEEN`,
		`SUBJECT "a\\" Grüße`, `SUBJECT "a\"" Grüße`,
		"SUBJECT \"\xff\"", "SUBJECT \"\xc3\"", "SUBJECT \"\xed\xa0\x80\"",
		"SUBJECT \"\xc0\xaf\"", "SUBJECT \xff", `"é" ſEEN "é"`,
	} {
		if ValidCommandUTF8(text) {
			t.Errorf("non-ASCII atom or malformed UTF-8 accepted: %q", text)
		}
	}
}
