package mail

import "testing"

func TestOrgDomain(t *testing.T) {
	cases := []struct{ domain, want string }{
		{"example.com", "example.com"},
		{"mail.example.com", "example.com"},
		{"a.example.co.uk", "example.co.uk"},
		{"b.other.co.uk", "other.co.uk"},
		{"co.uk", ""}, // a public suffix has no Organizational Domain
		{"com", ""},
	}
	for _, c := range cases {
		if got := OrgDomain(c.domain); got != c.want {
			t.Errorf("OrgDomain(%q) = %q, want %q", c.domain, got, c.want)
		}
	}
}
