package mail

import (
	"strings"
	"testing"
)

func TestValidateAddressDomainBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, address string
		accepted      bool
	}{
		{"ASCII", "user@example.com", true},
		{"Unicode", "用户@例子.广告", true},
		{"display name", "Someone <用户@例子.广告>", true},
		{"quoted local part", `"local@part"@example.com`, true},
		{"IPv4 literal", "user@[127.0.0.1]", true},
		{"maximum label", "user@" + strings.Repeat("a", 63) + ".example", true},
		{"invalid punycode", "user@xn--a.example", false},
		{"overlong label", "user@" + strings.Repeat("a", 64) + ".example", false},
		{"overlong domain", "user@" + strings.Repeat(strings.Repeat("a", 63)+".", 4) + "example", false},
		{"missing domain", "user@", false},
		{"missing local part", "@example.com", false},
		{"invalid local part", "two words@example.com", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateAddress(test.address)
			if (err == nil) != test.accepted {
				t.Fatalf("ValidateAddress(%q) = %v, accepted = %t", test.address, err, test.accepted)
			}
		})
	}
}

func TestValidateAddress_RejectsCRLFInjection(t *testing.T) {
	// The #166 proof-of-concept: a recipient that smuggles a second SMTP command
	// (or a forged header) via CR/LF must be rejected.
	inject := "victim@real.com>\r\nRCPT TO:<attacker@evil.com"
	if err := ValidateAddress(inject); err == nil {
		t.Fatal("ValidateAddress accepted a CRLF-injecting address; SMTP command injection possible")
	}

	rejected := []string{
		inject,
		"user@example.com\r\nBcc: attacker@evil.com",
		"user@example.com\nX-Injected: 1",
		"user@example.com\rTO",
		"user\x00@example.com",
		"",
		"   ",
		"not-an-address",
		"us\xffer@example.com",
		"user@ex\xffample.com",
	}
	for _, a := range rejected {
		if err := ValidateAddress(a); err == nil {
			t.Errorf("ValidateAddress(%q) = nil, want error", a)
		}
	}

	accepted := []string{
		"user@example.com",
		"First Last <user@example.com>",
		"mailer-daemon@mx.example.org",
		"用户@例え.jp",
		"München <münchen@städte.de>",
	}
	for _, a := range accepted {
		if err := ValidateAddress(a); err != nil {
			t.Errorf("ValidateAddress(%q) = %v, want nil", a, err)
		}
	}
}

func TestSanitizeHeaderValue(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clean value unchanged", "bob@example.net", "bob@example.net"},
		{"CRLF collapsed to space", "550 rejected\r\nInjected: header", "550 rejected Injected: header"},
		{"bare LF collapsed", "line1\nline2", "line1 line2"},
		{"DEL and NUL stripped", "a\x7fb\x00c", "a b c"},
		{"leading/trailing whitespace trimmed", "  spaced  ", "spaced"},
		{
			name: "CRLF-injecting recipient neutralized",
			in:   "victim@real.com>\r\nFinal-Recipient: rfc822; forged@evil.com",
			want: "victim@real.com> Final-Recipient: rfc822; forged@evil.com",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SanitizeHeaderValue(c.in)
			if got != c.want {
				t.Errorf("SanitizeHeaderValue(%q) = %q, want %q", c.in, got, c.want)
			}
			if ContainsControlChar(got) {
				t.Errorf("SanitizeHeaderValue(%q) still contains a control char: %q", c.in, got)
			}
		})
	}
}

func TestContainsControlChar(t *testing.T) {
	if !ContainsControlChar("a@b.com\r\nX: y") {
		t.Error("expected CR/LF to be detected")
	}
	if !ContainsControlChar("a@b.com\x7f") {
		t.Error("expected DEL to be detected")
	}
	if ContainsControlChar("a@b.com") {
		t.Error("clean address flagged as containing a control char")
	}
}
