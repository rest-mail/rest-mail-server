package outbound

import (
	"context"
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
	got, err := Prepare(context.Background(), nil, "", "b.com", raw)
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
	got, err := Prepare(context.Background(), nil, "", "b.com", raw)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got != raw {
		t.Fatalf("Prepare disturbed already-signed mail:\n got: %q\nwant: %q", got, raw)
	}
}

// A message asks for the recipient's TLS policy to be ignored with exactly one
// "TLS-Required: No" field in its header, in any case, folded or not; a second
// field, another value, or the words in the body do not (RFC 8689 §3).
func TestTLSOptional(t *testing.T) {
	for raw, want := range map[string]bool{
		"Subject: x\r\nTLS-Required: No\r\n\r\nbody\r\n":                     true,
		"tls-required:no\r\nSubject: x\r\n\r\nbody\r\n":                      true,
		"Subject: x\r\nTLS-Required:\r\n\tNO \r\n\r\nbody\r\n":               true,
		"Subject: x\nTLS-Required: No\n\nbody\n":                             true,
		"Subject: x\r\nTLS-Required: No\r\nTLS-Required: No\r\n\r\nbody\r\n": false,
		"Subject: x\r\nTLS-Required: Yes\r\n\r\nbody\r\n":                    false,
		"Subject: x\r\nTLS-Required: No thanks\r\n\r\nbody\r\n":              false,
		"Subject: x\r\nX-TLS-Required: No\r\n\r\nbody\r\n":                   false,
		"Subject: x\r\n\r\nTLS-Required: No\r\n":                             false,
		"Subject: TLS-Required: No\r\n\r\nbody\r\n":                          false,
		"Subject: x\r\nTLS-Required: No\r\n":                                 true,
	} {
		if got := TLSOptional(raw); got != want {
			t.Errorf("%q: %v, want %v", raw, got, want)
		}
	}
}

// headMixes puts a field into a message with each mix of line ends #1970 found a split missing it.
var headMixes = map[string]func(field string) string{
	"all CRLF":                  func(f string) string { return "From: a@x.test\r\n" + f + "\r\nSubject: s\r\n\r\nbody\r\n" },
	"all LF":                    func(f string) string { return "From: a@x.test\n" + f + "\nSubject: s\n\nbody\n" },
	"LF head, CRLF blank later": func(f string) string { return "From: a@x.test\n" + f + "\nSubject: s\n\nbody\n\r\n\r\nmore\r\n" },
	"one LF, after the field":   func(f string) string { return "From: a@x.test\r\n" + f + "\nSubject: s\r\n\r\nbody\r\n" },
	"one LF, before the field":  func(f string) string { return "From: a@x.test\n" + f + "\r\nSubject: s\r\n\r\nbody\r\n" },
	"bare CR, before the field": func(f string) string { return "From: a@x.test\r" + f + "\r\nSubject: s\r\n\r\nbody\r\n" },
	"bare CR, after the field":  func(f string) string { return "From: a@x.test\r\n" + f + "\rSubject: s\r\n\r\nbody\r\n" },
	"first field":               func(f string) string { return f + "\nFrom: a@x.test\r\nSubject: s\r\n\r\nbody\r\n" },
	"last field":                func(f string) string { return "From: a@x.test\nSubject: s\r\n" + f + "\n\r\nbody\r\n" },
}

func TestStripBccAnyLineEnds(t *testing.T) {
	for name, mix := range headMixes {
		t.Run(name, func(t *testing.T) {
			raw := mix("Bcc: secret@c.test")
			got := StripBcc(raw)
			if strings.Contains(got, "secret") {
				t.Fatalf("the Bcc stayed: %q", got)
			}
			for _, kept := range []string{"From: a@x.test", "Subject: s", "body"} {
				if !strings.Contains(got, kept) {
					t.Fatalf("%q went too: %q from %q", kept, got, raw)
				}
			}
			if len(raw)-len(got) < len("Bcc: secret@c.test") || len(raw)-len(got) > len("Bcc: secret@c.test\r\n") {
				t.Fatalf("more than the field went: %q from %q", got, raw)
			}
		})
	}
}

// A signature the old split hid is seen, and so is not signed over a second time by Prepare.
func TestHasDKIMSignatureAnyLineEnds(t *testing.T) {
	for name, mix := range headMixes {
		t.Run(name, func(t *testing.T) {
			raw := mix("DKIM-Signature: v=1; d=x.test; s=sel; b=abc")
			if !HasDKIMSignature(raw) {
				t.Fatalf("no signature seen in %q", raw)
			}
			if HasDKIMSignature(mix("X-Other: DKIM-Signature: v=1")) {
				t.Fatal("found a signature in another field")
			}
		})
	}
	for _, raw := range []string{
		"From: a@x.test\n\nDKIM-Signature: v=1\n",
		"From: a@x.test\r\n\r\nDKIM-Signature: v=1\r\n",
		"From: a@x.test\n\nDKIM-Signature: v=1\n\r\n\r\nmore",
	} {
		if HasDKIMSignature(raw) {
			t.Errorf("found a signature in the body of %q", raw)
		}
	}
}

// One field asks for the policy to be ignored, in any mix. A second field keeps the policy wherever
// it hides, and so does the word in the body.
func TestTLSOptionalAnyLineEnds(t *testing.T) {
	for name, mix := range headMixes {
		t.Run(name, func(t *testing.T) {
			if raw := mix("TLS-Required: No"); !TLSOptional(raw) {
				t.Fatalf("field not seen in %q", raw)
			}
			if raw := mix("TLS-Required:\n No"); !TLSOptional(raw) {
				t.Fatalf("folded field not seen in %q", raw)
			}
			if raw := mix("TLS-Required: No\nTLS-Required: No"); TLSOptional(raw) {
				t.Fatalf("two fields ask for nothing, in %q", raw)
			}
			if raw := mix("TLS-Required: No\rtls-required: no"); TLSOptional(raw) {
				t.Fatalf("two fields ask for nothing, in %q", raw)
			}
			if raw := mix("TLS-Required: Yes"); TLSOptional(raw) {
				t.Fatalf("another value asks for nothing, in %q", raw)
			}
		})
	}
	if raw := "From: a@x.test\n\nTLS-Required: No\n\r\n\r\nmore"; TLSOptional(raw) {
		t.Errorf("the words in the body asked for it, in %q", raw)
	}
}
