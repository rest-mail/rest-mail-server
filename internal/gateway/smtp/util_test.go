package smtp

import (
	"strings"
	"testing"
)

// ---------- extractIP ----------

func TestExtractIP(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"IPv4 with port", "192.168.1.1:25", "192.168.1.1"},
		{"IPv4 no port", "192.168.1.1", "192.168.1.1"},
		{"IPv6 with port", "[::1]:25", "::1"},
		{"IPv6 no port", "[::1]", "::1"},
		{"IPv6 full with port", "[2001:db8::1]:587", "2001:db8::1"},
		{"empty string", "", ""},
		{"localhost with port", "127.0.0.1:2525", "127.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractIP(tt.addr)
			if got != tt.want {
				t.Errorf("extractIP(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

// ---------- splitHostPort ----------

func TestSplitHostPort(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		wantHost string
		wantPort string
	}{
		{"IPv4 with port", "192.168.1.1:25", "192.168.1.1", "25"},
		{"IPv4 no port", "192.168.1.1", "192.168.1.1", ""},
		{"IPv6 with port", "[::1]:25", "::1", "25"},
		{"IPv6 no port brackets", "[::1]", "::1", ""},
		{"IPv6 full with port", "[2001:db8::1]:587", "2001:db8::1", "587"},
		{"empty string", "", "", ""},
		{"hostname with port", "mail.example.com:25", "mail.example.com", "25"},
		{"hostname no port", "mail.example.com", "mail.example.com", ""},
		{"IPv6 missing closing bracket", "[::1", "[::1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, _ := splitHostPort(tt.addr)
			if host != tt.wantHost {
				t.Errorf("splitHostPort(%q) host = %q, want %q", tt.addr, host, tt.wantHost)
			}
			if port != tt.wantPort {
				t.Errorf("splitHostPort(%q) port = %q, want %q", tt.addr, port, tt.wantPort)
			}
		})
	}
}

// The quoted-printable, hex and address-scanning helpers these tests covered no
// longer exist: parsing is delegated to internal/mime (go-message), which does
// all of it and handles the cases the hand-rolled versions did not. The
// behaviours are still asserted, now through parseRawMessage itself — see the
// encoded-word, charset and display-name tests at the end of this file (#298).

// ---------- parseRawMessage ----------

func TestParseRawMessage_SimpleText(t *testing.T) {
	raw := "Subject: Hello\r\nFrom: Alice <alice@example.com>\r\nTo: bob@example.com\r\nMessage-ID: <msg1@example.com>\r\n\r\nThis is the body."
	subject, bodyText, bodyHTML, messageID, senderName, _, _, toList, _, fromAddr := parseRawMessage([]byte(raw))
	if subject != "Hello" {
		t.Errorf("subject = %q, want %q", subject, "Hello")
	}
	if bodyText != "This is the body." {
		t.Errorf("bodyText = %q, want %q", bodyText, "This is the body.")
	}
	if bodyHTML != "" {
		t.Errorf("bodyHTML = %q, want empty", bodyHTML)
	}
	if messageID != "msg1@example.com" {
		t.Errorf("messageID = %q, want %q", messageID, "msg1@example.com")
	}
	if senderName != "Alice" {
		t.Errorf("senderName = %q, want %q", senderName, "Alice")
	}
	if fromAddr != "alice@example.com" {
		t.Errorf("fromAddr = %q, want %q", fromAddr, "alice@example.com")
	}
	if len(toList) != 1 || toList[0] != "bob@example.com" {
		t.Errorf("toList = %v, want [bob@example.com]", toList)
	}
}

func TestParseRawMessage_HTMLContentType(t *testing.T) {
	raw := "Subject: HTML Test\r\nContent-Type: text/html\r\n\r\n<h1>Hello</h1>"
	_, bodyText, bodyHTML, _, _, _, _, _, _, _ := parseRawMessage([]byte(raw))
	if bodyHTML != "<h1>Hello</h1>" {
		t.Errorf("bodyHTML = %q, want %q", bodyHTML, "<h1>Hello</h1>")
	}
	if bodyText != "" {
		t.Errorf("bodyText = %q, want empty", bodyText)
	}
}

func TestParseRawMessage_LFOnly(t *testing.T) {
	raw := "Subject: LF Test\nFrom: test@example.com\n\nBody here."
	subject, bodyText, _, _, _, _, _, _, _, _ := parseRawMessage([]byte(raw))
	if subject != "LF Test" {
		t.Errorf("subject = %q, want %q", subject, "LF Test")
	}
	if bodyText != "Body here." {
		t.Errorf("bodyText = %q, want %q", bodyText, "Body here.")
	}
}

func TestParseRawMessage_FoldedHeaders(t *testing.T) {
	raw := "Subject: This is a very\r\n long subject line\r\nTo: alice@example.com\r\n\r\nBody."
	subject, _, _, _, _, _, _, toList, _, _ := parseRawMessage([]byte(raw))
	expected := "This is a very long subject line"
	if subject != expected {
		t.Errorf("subject = %q, want %q", subject, expected)
	}
	if len(toList) != 1 || toList[0] != "alice@example.com" {
		t.Errorf("toList = %v, want [alice@example.com]", toList)
	}
}

func TestParseRawMessage_MultipleToCc(t *testing.T) {
	raw := "To: Alice <alice@a.com>, Bob <bob@b.com>\r\nCc: Carol <carol@c.com>, dave@d.com\r\n\r\nBody."
	_, _, _, _, _, _, _, toList, ccList, _ := parseRawMessage([]byte(raw))
	if len(toList) != 2 {
		t.Fatalf("toList len = %d, want 2", len(toList))
	}
	if toList[0] != "alice@a.com" || toList[1] != "bob@b.com" {
		t.Errorf("toList = %v, want [alice@a.com, bob@b.com]", toList)
	}
	if len(ccList) != 2 {
		t.Fatalf("ccList len = %d, want 2", len(ccList))
	}
	if ccList[0] != "carol@c.com" || ccList[1] != "dave@d.com" {
		t.Errorf("ccList = %v, want [carol@c.com, dave@d.com]", ccList)
	}
}

func TestParseRawMessage_InReplyToAndReferences(t *testing.T) {
	raw := "In-Reply-To: <orig@example.com>\r\nReferences: <orig@example.com> <reply@example.com>\r\n\r\nBody."
	_, _, _, _, _, inReplyTo, references, _, _, _ := parseRawMessage([]byte(raw))
	if inReplyTo != "orig@example.com" {
		t.Errorf("inReplyTo = %q, want %q", inReplyTo, "orig@example.com")
	}
	if references != "<orig@example.com> <reply@example.com>" {
		t.Errorf("references = %q, want %q", references, "<orig@example.com> <reply@example.com>")
	}
}

func TestParseRawMessage_NoBody(t *testing.T) {
	raw := "Subject: Headers Only"
	subject, bodyText, bodyHTML, _, _, _, _, _, _, _ := parseRawMessage([]byte(raw))
	if subject != "Headers Only" {
		t.Errorf("subject = %q, want %q", subject, "Headers Only")
	}
	if bodyText != "" {
		t.Errorf("bodyText = %q, want empty", bodyText)
	}
	if bodyHTML != "" {
		t.Errorf("bodyHTML = %q, want empty", bodyHTML)
	}
}

func TestParseRawMessage_QuotedSenderName(t *testing.T) {
	raw := "From: \"Smith, Alice\" <alice@example.com>\r\n\r\nBody."
	_, _, _, _, senderName, _, _, _, _, fromAddr := parseRawMessage([]byte(raw))
	if senderName != "Smith, Alice" {
		t.Errorf("senderName = %q, want %q", senderName, "Smith, Alice")
	}
	if fromAddr != "alice@example.com" {
		t.Errorf("fromAddr = %q, want %q", fromAddr, "alice@example.com")
	}
}

func TestParseRawMessage_EmptyInput(t *testing.T) {
	subject, bodyText, bodyHTML, messageID, senderName, inReplyTo, references, toList, ccList, fromAddr := parseRawMessage([]byte(""))
	if subject != "" || bodyText != "" || bodyHTML != "" || messageID != "" || senderName != "" || inReplyTo != "" || references != "" || fromAddr != "" {
		t.Errorf("expected all empty strings for empty input")
	}
	if len(toList) != 0 || len(ccList) != 0 {
		t.Errorf("expected empty lists for empty input")
	}
}

func TestParseRawMessage_TabFolding(t *testing.T) {
	raw := "Subject: Folded\r\n\twith tab\r\n\r\nBody."
	subject, _, _, _, _, _, _, _, _, _ := parseRawMessage([]byte(raw))
	expected := "Folded with tab"
	if subject != expected {
		t.Errorf("subject = %q, want %q", subject, expected)
	}
}

// The parseMultipartBody tests that stood here drove a helper that no longer
// exists; multipart walking now happens inside internal/mime. The two cases that
// were about behaviour rather than that helper — alternative parts, and a
// text/plain nested inside a multipart/alternative inside a multipart/mixed —
// are asserted through parseRawMessage below and in TestParseRawMessage_NestedMultipart.

func TestParseRawMessage_MultipartMessage(t *testing.T) {
	boundary := "msgboundary"
	raw := "Subject: Multipart\r\n" +
		"Content-Type: multipart/alternative; boundary=" + boundary + "\r\n" +
		"\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"Plain body\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Type: text/html\r\n\r\n" +
		"<p>HTML body</p>\r\n" +
		"--" + boundary + "--\r\n"

	subject, bodyText, bodyHTML, _, _, _, _, _, _, _ := parseRawMessage([]byte(raw))
	if subject != "Multipart" {
		t.Errorf("subject = %q, want %q", subject, "Multipart")
	}
	if bodyText != "Plain body" {
		t.Errorf("bodyText = %q, want %q", bodyText, "Plain body")
	}
	if bodyHTML != "<p>HTML body</p>" {
		t.Errorf("bodyHTML = %q, want %q", bodyHTML, "<p>HTML body</p>")
	}
}

// ---------- real-world messages the hand-rolled parser mishandles (#298) ----------

// A non-ASCII Subject arrives as an RFC 2047 encoded-word. Matching headers by
// prefix and slicing the value stored the encoding itself, so the subject shown
// in a mailbox was "=?UTF-8?Q?...?=" rather than the words the sender typed.
// The same mail arriving over the REST path is decoded, because that path parses
// with internal/mime.
func TestParseRawMessage_DecodesEncodedWordSubject(t *testing.T) {
	raw := "Subject: =?UTF-8?Q?=C3=9Cberweisung_f=C3=BCr_Kaffee?=\r\n" +
		"From: alice@example.com\r\n\r\nBody."

	subject, _, _, _, _, _, _, _, _, _ := parseRawMessage([]byte(raw))

	if want := "Überweisung für Kaffee"; subject != want {
		t.Errorf("subject = %q, want %q", subject, want)
	}
}

// A display name may contain a comma. Splitting To: on every comma survives
// this shape only by luck: the `"Doe` fragment carries no address, so it is
// skipped rather than turned into a bogus recipient. It passes today and must
// keep passing once the splitting is gone.
func TestParseRawMessage_CommaInsideQuotedDisplayName(t *testing.T) {
	raw := "To: \"Doe, John\" <john@example.org>, jane@example.org\r\n\r\nBody."

	_, _, _, _, _, _, _, toList, _, _ := parseRawMessage([]byte(raw))

	want := []string{"john@example.org", "jane@example.org"}
	if len(toList) != len(want) {
		t.Fatalf("toList = %v, want %v", toList, want)
	}
	for i := range want {
		if toList[i] != want[i] {
			t.Errorf("toList[%d] = %q, want %q", i, toList[i], want[i])
		}
	}
}

// A text part in a non-UTF-8 charset has to be converted, not stored as the raw
// bytes of the original encoding.
func TestParseRawMessage_ConvertsNonUTF8Charset(t *testing.T) {
	raw := "Subject: Charset\r\n" +
		"Content-Type: text/plain; charset=ISO-8859-1\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"Caf=E9\r\n"

	_, bodyText, _, _, _, _, _, _, _, _ := parseRawMessage([]byte(raw))

	if !strings.Contains(bodyText, "Café") {
		t.Errorf("bodyText = %q, want it to contain %q", bodyText, "Café")
	}
}

// The From display name is an encoded-word as often as the Subject is, and it
// is stored as the sender's name, so the raw =?UTF-8?...?= text reaches the
// mailbox listing where a person reads it.
func TestParseRawMessage_DecodesEncodedWordSenderName(t *testing.T) {
	raw := "From: =?UTF-8?Q?Ren=C3=A9e_M=C3=BCller?= <renee@example.org>\r\n" +
		"Subject: Hallo\r\n\r\nBody."

	_, _, _, _, senderName, _, _, _, _, fromAddr := parseRawMessage([]byte(raw))

	if want := "Renée Müller"; senderName != want {
		t.Errorf("senderName = %q, want %q", senderName, want)
	}
	if want := "renee@example.org"; fromAddr != want {
		t.Errorf("fromAddr = %q, want %q", fromAddr, want)
	}
}

// A text part nested inside a multipart/alternative inside a multipart/mixed is
// still found — the shape a message carrying an attachment alongside both body
// types takes. This is the nesting case the removed parseMultipartBody tests
// asserted, re-stated against the message rather than the helper.
func TestParseRawMessage_NestedMultipart(t *testing.T) {
	inner, outer := "inner_boundary", "outer_boundary"
	raw := "Subject: Nested\r\n" +
		"Content-Type: multipart/mixed; boundary=" + outer + "\r\n\r\n" +
		"--" + outer + "\r\n" +
		"Content-Type: multipart/alternative; boundary=" + inner + "\r\n\r\n" +
		"--" + inner + "\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"Nested plain\r\n" +
		"--" + inner + "\r\n" +
		"Content-Type: text/html\r\n\r\n" +
		"<b>Nested HTML</b>\r\n" +
		"--" + inner + "--\r\n" +
		"--" + outer + "--\r\n"

	_, bodyText, bodyHTML, _, _, _, _, _, _, _ := parseRawMessage([]byte(raw))

	if want := "Nested plain"; bodyText != want {
		t.Errorf("bodyText = %q, want %q", bodyText, want)
	}
	if want := "<b>Nested HTML</b>"; bodyHTML != want {
		t.Errorf("bodyHTML = %q, want %q", bodyHTML, want)
	}
}
