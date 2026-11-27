package message

import "testing"

// TestNameSpellerMatchesParameterName: remembering the last name spelled changes no spelling, whatever
// comes between two of the same.
func TestNameSpellerMatchesParameterName(t *testing.T) {
	var names nameSpeller
	for _, key := range []string{"n\xc3\xa4me", "n\xc3\xa4me", "x", "n\xc3\xa4me", "N\xc3\x84ME*0", "N\xc3\x84ME*0", "a b", "", "n\xc3\xa4me*x", "x*"} {
		if got, want := names.spell(key), parameterName(key); got != want {
			t.Errorf("%q: spelled %q, want %q", key, got, want)
		}
	}
}
