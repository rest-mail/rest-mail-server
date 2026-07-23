package smtp

import "testing"

// TestMaskEmail covers the OSI-5 auth-log PII redaction: the local-part is
// reduced to its first rune, the domain is preserved for triage.
func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"alice@example.com": "a***@example.com",
		"a@b.com":           "a***@b.com",
		"":                  "",
		"  bob@corp.io  ":   "b***@corp.io",
		"noatsign":          "n***",
	}
	for in, want := range cases {
		if got := maskEmail(in); got != want {
			t.Errorf("maskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
