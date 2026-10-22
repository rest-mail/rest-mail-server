package mail

import (
	"strings"
	"testing"
)

func TestDomainASCII(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"example.com", "example.com"},
		{"BÜCHER.Example", "xn--bcher-kva.example"},
		{"bu\u0308cher.example", "xn--bcher-kva.example"},
		{"例え.jp", "xn--r8jz45g.jp"},
		{"faß.de", "xn--fa-hia.de"},
		{"xn--bcher-kva.example", "xn--bcher-kva.example"},
		{"bücher.example.", "xn--bcher-kva.example."},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := DomainASCII(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("DomainASCII(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
			}
		})
	}
	for _, input := range []string{"", ".", "a..example", "-bad.example", "bad_.example", "bad example", "a\r\n.example", "\xff.example", "xn--.example", strings.Repeat("a", 64) + ".example", strings.Repeat("a.", 128) + "com", "a\u200db.example"} {
		if got, err := DomainASCII(input); err == nil || got != "" {
			t.Errorf("DomainASCII(%q) = %q, %v; want empty result and error", input, got, err)
		}
	}
}

func TestDomainUnicode(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"example.com", "example.com"},
		{"xn--bcher-kva.example", "bücher.example"},
		{"XN--BCHER-KVA.Example", "bücher.example"},
		{"bu\u0308cher.example", "bücher.example"},
		{"xn--fa-hia.de", "faß.de"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := DomainUnicode(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("DomainUnicode(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
			}
		})
	}
	for _, input := range []string{"", "bad_.example", "xn--.example"} {
		if got, err := DomainUnicode(input); err == nil || got != "" {
			t.Errorf("DomainUnicode(%q) = %q, %v; want empty result and error", input, got, err)
		}
	}
}

func TestSMTPAddressASCII(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""},
		{"user@例え.jp", "user@xn--r8jz45g.jp"},
		{"用户@例え.jp", "用户@xn--r8jz45g.jp"},
		{"mu\u0308nchen@städte.de", "mu\u0308nchen@xn--stdte-hra.de"},
		{"\"quoted@local\"@bücher.example", "\"quoted@local\"@xn--bcher-kva.example"},
		{"user@[127.0.0.1]", "user@[127.0.0.1]"},
		{"user@[IPv6:2001:db8::1]", "user@[IPv6:2001:db8::1]"},
	} {
		got, err := SMTPAddressASCII(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("SMTPAddressASCII(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	for _, input := range []string{"user", "@example.com", "user@", "user@a..com", "user\r\n@example.com", "\xff@example.com", "user@[bad]"} {
		if got, err := SMTPAddressASCII(input); err == nil || got != "" {
			t.Errorf("SMTPAddressASCII(%q) = %q, %v; want empty result and error", input, got, err)
		}
	}
}
