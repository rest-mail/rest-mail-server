package server

import (
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// RFC 3501 §5.1, §7.2.2, §7.2.3, §9: a mailbox name is an astring. Every mailbox
// name emitted in a LIST/STATUS/QUOTAROOT response MUST be encoded as an astring —
// a quoted-string with '"' and '\' escaped, or (for CR/LF/8-bit octets a
// quoted-string cannot hold) a literal. Interpolating a raw name breaks client
// parsing when the name holds a quote/backslash, and lets a CR/LF in a
// backend-supplied folder name inject additional response lines (issue #24).

// hasLine reports whether want appears verbatim among the untagged lines.
func hasLine(untagged []string, want string) bool {
	for _, l := range untagged {
		if l == want {
			return true
		}
	}
	return false
}

// TestListEscapesQuoteInMailboxName: a backend folder whose name contains a
// double quote must be emitted as a quoted-string with the quote backslash-
// escaped. On the pre-fix code LIST interpolated the raw name inside hardcoded
// quotes, yielding the malformed `"Weird"Name"` — RED there.
func TestListEscapesQuoteInMailboxName(t *testing.T) {
	b := newMockBackend()
	b.mbox.folders = append(b.mbox.folders, imap.Folder{Name: `Weird"Name`})
	h := newIMAPHarness(t, b)
	h.login("a1")

	untagged, status := h.command("a2", `LIST "" "*"`)
	if !hasStatus(status, "OK") {
		t.Fatalf("LIST status = %q, want OK", status)
	}
	want := `* LIST () "/" "Weird\"Name"`
	if !hasLine(untagged, want) {
		t.Errorf("LIST lines = %v,\n want one to be %q (quote must be escaped)", untagged, want)
	}
	// The malformed unescaped form must not appear.
	if hasLine(untagged, `* LIST () "/" "Weird"Name"`) {
		t.Errorf("LIST emitted the malformed unescaped name; lines = %v", untagged)
	}
}

// TestListEscapesBackslashInMailboxName: a backslash in a folder name must be
// doubled so it is not read as an escape introducer. RED on the pre-fix raw
// interpolation.
func TestListEscapesBackslashInMailboxName(t *testing.T) {
	b := newMockBackend()
	b.mbox.folders = append(b.mbox.folders, imap.Folder{Name: `Back\slash`})
	h := newIMAPHarness(t, b)
	h.login("a1")

	untagged, status := h.command("a2", `LIST "" "*"`)
	if !hasStatus(status, "OK") {
		t.Fatalf("LIST status = %q, want OK", status)
	}
	want := `* LIST () "/" "Back\\slash"`
	if !hasLine(untagged, want) {
		t.Errorf("LIST lines = %v,\n want one to be %q (backslash must be doubled)", untagged, want)
	}
}

// TestListLiteralEncodesControlChars: a folder name containing CR/LF must never
// inject extra response lines (the security bug in issue #24). Before the client
// enables UTF-8 the name is modified UTF-7 (RFC 3501 §5.1.3), whose base64 carries
// the CR and LF, so it is one quoted token on one line. Once UTF-8 is enabled
// the name travels as it is, and a quoted-string cannot carry CR/LF, so it MUST
// be emitted as a literal.
func TestListLiteralEncodesControlChars(t *testing.T) {
	const evil = "Bad\r\nInject"
	b := newMockBackend()
	b.mbox.folders = append(b.mbox.folders, imap.Folder{Name: evil})
	h := newIMAPHarness(t, b)
	h.login("a1")

	untagged, status := h.command("a2", `LIST "" "*"`)
	if !hasStatus(status, "OK") {
		t.Fatalf("LIST status = %q, want OK", status)
	}
	if want := `* LIST () "/" "Bad&AA0ACg-Inject"`; !hasLine(untagged, want) {
		t.Errorf("LIST lines = %v, want one to be %q", untagged, want)
	}

	h = newIMAPHarnessWith(t, utf8MockBackend{b}, b.user, b.pass)
	h.login("u1")
	if _, status := h.command("u2", "ENABLE UTF8=ACCEPT"); !hasStatus(status, "OK") {
		t.Fatalf("ENABLE status = %q, want OK", status)
	}
	untagged, status = h.command("u3", `LIST "" "*"`)
	if !hasStatus(status, "OK") {
		t.Fatalf("LIST status = %q, want OK", status)
	}
	// The name must arrive verbatim inside a literal, not as injected line(s).
	if h.lastLiteral != evil {
		t.Errorf("literal payload = %q, want %q (CR/LF name must be literal-encoded, not injected); lines = %v",
			h.lastLiteral, evil, untagged)
	}
	sawLiteralMarker := false
	for _, l := range untagged {
		if strings.HasPrefix(l, "* LIST") && strings.Contains(l, "{11}") {
			sawLiteralMarker = true
		}
	}
	if !sawLiteralMarker {
		t.Errorf("no LIST line carried the {11} literal marker; lines = %v", untagged)
	}
}

// utf8MockBackend serves its mockBackend's mailbox as a [UTF8Mailbox].
type utf8MockBackend struct{ *mockBackend }

func (b utf8MockBackend) Authenticate(user, pass string) (imap.Mailbox, error) {
	m, err := b.mockBackend.Authenticate(user, pass)
	if err != nil {
		return nil, err
	}
	return utf8MockMailbox{m.(*mockMailbox)}, nil
}

type utf8MockMailbox struct{ *mockMailbox }

func (utf8MockMailbox) EnableUTF8() error { return nil }

// TestListRoundTripAtomSafeName: a plain, atom-safe name round-trips — it is
// emitted quoted per the encoder's rule and decodes back to the original.
func TestListRoundTripAtomSafeName(t *testing.T) {
	b := newMockBackend()
	b.mbox.folders = append(b.mbox.folders, imap.Folder{Name: "Work"})
	h := newIMAPHarness(t, b)
	h.login("a1")

	untagged, status := h.command("a2", `LIST "" "*"`)
	if !hasStatus(status, "OK") {
		t.Fatalf("LIST status = %q, want OK", status)
	}
	const want = `* LIST () "/" "Work"`
	if !hasLine(untagged, want) {
		t.Fatalf("LIST lines = %v,\n want one to be %q", untagged, want)
	}
	// Decode the emitted astring back to the name it encoded.
	const sep = `"/" `
	var encoded string
	for _, l := range untagged {
		if i := strings.Index(l, sep); i >= 0 && strings.Contains(l, "Work") {
			encoded = l[i+len(sep):]
		}
	}
	if got := wire.Unquote(encoded); got != "Work" {
		t.Errorf("round-trip: wire.Unquote(%q) = %q, want %q", encoded, got, "Work")
	}
}

// TestStatusEscapesQuoteInMailboxName: STATUS echoes the requested mailbox name;
// a name with an embedded quote must be escaped. RED on the pre-fix raw form
// `* STATUS "Weird"Name" (...)`.
func TestStatusEscapesQuoteInMailboxName(t *testing.T) {
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")

	// Client sends the name as a quoted-string with the quote backslash-escaped.
	untagged, status := h.command("a2", `STATUS "Weird\"Name" (MESSAGES)`)
	if !hasStatus(status, "OK") {
		t.Fatalf("STATUS status = %q, want OK", status)
	}
	want := `* STATUS "Weird\"Name" (MESSAGES 0)`
	if line := statusLine(untagged); line != want {
		t.Errorf("STATUS line = %q, want %q (quote must be escaped)", line, want)
	}
}

// TestQuotaRootQuotesMailboxName: the QUOTAROOT response emitted the mailbox as a
// bare atom, so a name containing a space was split into two tokens — a malformed
// line. It must be encoded as an astring (quoted). RED on the pre-fix bare form.
func TestQuotaRootQuotesMailboxName(t *testing.T) {
	b := newMockBackend()
	b.mbox.folders = append(b.mbox.folders, imap.Folder{Name: "Foo Bar"})
	h := newIMAPHarness(t, b)
	h.login("a1")

	untagged, status := h.command("a2", `GETQUOTAROOT "Foo Bar"`)
	if !hasStatus(status, "OK") {
		t.Fatalf("GETQUOTAROOT status = %q, want OK", status)
	}
	want := `* QUOTAROOT "Foo Bar" ""`
	if !hasLine(untagged, want) {
		t.Errorf("QUOTAROOT lines = %v,\n want one to be %q (name must be astring-quoted)", untagged, want)
	}
	// The malformed bare form must not appear.
	if hasLine(untagged, `* QUOTAROOT Foo Bar ""`) {
		t.Errorf("QUOTAROOT emitted the bare unquoted name; lines = %v", untagged)
	}
}
