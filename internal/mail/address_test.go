package mail

import "testing"

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
	}
	for _, a := range accepted {
		if err := ValidateAddress(a); err != nil {
			t.Errorf("ValidateAddress(%q) = %v, want nil", a, err)
		}
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
