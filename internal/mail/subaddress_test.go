package mail

import "testing"

// TestStripSubaddress covers RFC 5233 subaddressing: a "+detail" tag is split
// off the local part, everything after the FIRST "+" is the detail, and inputs
// that carry no usable tag (no "+", empty base local part, or no "@") are
// reported unchanged with ok=false.
func TestStripSubaddress(t *testing.T) {
	cases := []struct {
		in         string
		wantBase   string
		wantDetail string
		wantOK     bool
	}{
		// Plain address, no tag.
		{"user@example.com", "user@example.com", "", false},
		// Simple subaddress.
		{"user+amazon@example.com", "user@example.com", "amazon", true},
		// Detail is everything after the first "+".
		{"user+a+b@example.com", "user@example.com", "a+b", true},
		// Empty detail after the "+" is still a stripped tag.
		{"user+@example.com", "user@example.com", "", true},
		// "+" at the start leaves no base local part: not valid subaddressing.
		{"+tag@example.com", "+tag@example.com", "", false},
		// No "@": returned unchanged.
		{"not-an-address", "not-an-address", "", false},
		// A "+" only in the domain is not a local-part tag.
		{"user@ex+ample.com", "user@ex+ample.com", "", false},
	}

	for _, tc := range cases {
		base, detail, ok := StripSubaddress(tc.in)
		if base != tc.wantBase || detail != tc.wantDetail || ok != tc.wantOK {
			t.Errorf("StripSubaddress(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, base, detail, ok, tc.wantBase, tc.wantDetail, tc.wantOK)
		}
	}
}
