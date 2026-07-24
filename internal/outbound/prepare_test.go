package outbound

import (
	"strings"
	"testing"
)

func TestStripBcc_RemovesHeaderAndFoldedContinuation(t *testing.T) {
	raw := "From: alice@example.com\r\n" +
		"To: bob@example.net\r\n" +
		"Bcc: hidden1@example.org,\r\n" +
		"\thidden2@example.org\r\n" +
		"Subject: hi\r\n" +
		"\r\n" +
		"body line\r\n"

	got := StripBcc(raw)

	if strings.Contains(strings.ToLower(got), "bcc:") {
		t.Fatalf("Bcc header not removed:\n%s", got)
	}
	if strings.Contains(got, "hidden2@example.org") {
		t.Fatalf("folded Bcc continuation line not removed:\n%s", got)
	}
	for _, keep := range []string{"From: alice@example.com", "To: bob@example.net", "Subject: hi", "body line"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("StripBcc dropped %q:\n%s", keep, got)
		}
	}
}

func TestStripBcc_NoBccReturnsInputUnchanged(t *testing.T) {
	raw := "From: alice@example.com\r\nTo: bob@example.net\r\nSubject: hi\r\n\r\nbody\r\n"
	if got := StripBcc(raw); got != raw {
		t.Fatalf("StripBcc altered a message with no Bcc:\n got: %q\nwant: %q", got, raw)
	}
}

func TestStripBcc_CaseInsensitive(t *testing.T) {
	raw := "From: a@b.com\r\nBCC: x@y.com\r\n\r\nbody\r\n"
	if strings.Contains(strings.ToLower(StripBcc(raw)), "bcc:") {
		t.Fatal("lowercased match missed a BCC header")
	}
}

func TestHasDKIMSignature(t *testing.T) {
	signed := "DKIM-Signature: v=1; a=rsa-sha256; d=x; b=zz\r\nFrom: a@b.com\r\n\r\nbody\r\n"
	if !HasDKIMSignature(signed) {
		t.Fatal("expected HasDKIMSignature=true for a signed message")
	}
	unsigned := "From: a@b.com\r\nTo: c@d.com\r\n\r\nbody\r\n"
	if HasDKIMSignature(unsigned) {
		t.Fatal("expected HasDKIMSignature=false for an unsigned message")
	}
	// A "DKIM-Signature" token in the body must not be mistaken for the header.
	bodyMention := "From: a@b.com\r\n\r\nplease add a DKIM-Signature: here\r\n"
	if HasDKIMSignature(bodyMention) {
		t.Fatal("body text mistaken for a DKIM-Signature header")
	}
}

// Prepare with a nil db (no key source) must still strip Bcc and, since the
// message is unsigned and there is no key, leave it unsigned — proving the
// privacy fix does not depend on DKIM being configured.
func TestPrepare_NilDBStripsBccLeavesUnsigned(t *testing.T) {
	raw := "From: a@b.com\r\nBcc: hidden@x.com\r\n\r\nbody\r\n"
	got, err := Prepare(nil, "", "b.com", raw)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if strings.Contains(strings.ToLower(got), "bcc:") {
		t.Fatalf("Prepare did not strip Bcc:\n%s", got)
	}
	if HasDKIMSignature(got) {
		t.Fatal("Prepare signed with no key available")
	}
}

// Prepare must be idempotent for already-signed mail: no second signature, and
// the bytes are returned unchanged (nothing to strip, nothing to re-sign).
func TestPrepare_AlreadySignedPassthrough(t *testing.T) {
	raw := "DKIM-Signature: v=1; d=x; b=zz\r\nFrom: a@b.com\r\nTo: c@d.com\r\n\r\nbody\r\n"
	got, err := Prepare(nil, "", "b.com", raw)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got != raw {
		t.Fatalf("Prepare disturbed already-signed mail:\n got: %q\nwant: %q", got, raw)
	}
}
