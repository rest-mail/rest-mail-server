package mime

import "testing"

func TestRecoverBody(t *testing.T) {
	for raw, want := range map[string]string{
		"bad line\r\n\r\nbody\r\n":     "body\r\n",
		"bad line\n\nbody\n":           "body\n",
		"bad\n\nfirst\r\n\r\nsecond":   "second",
		"bad line\r\n\r\n":             "",
		"only headers\r\nand no break": "",
		"":                             "",
	} {
		if got := RecoverBody(raw); got != want {
			t.Errorf("RecoverBody(%q) = %q, want %q", raw, got, want)
		}
	}
}
