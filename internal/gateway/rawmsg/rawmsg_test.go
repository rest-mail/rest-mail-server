package rawmsg

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

func testTime() time.Time {
	return time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC)
}

func testMessage() apiclient.MessageDetail {
	return apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Hello",
			ReceivedAt: testTime(),
		},
		BodyText: "Hello, world!",
	}
}

// parts splits a rendered message into its header block and body.
func split(t *testing.T, raw string) (header, body string) {
	t.Helper()
	header, body, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		t.Fatalf("no header/body separator in:\n%s", raw)
	}
	return header, body
}

// readParts parses a multipart body and returns each part's content.
func readParts(t *testing.T, raw string) []string {
	t.Helper()
	header, body := split(t, raw)

	var ctype string
	for _, line := range strings.Split(header, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "content-type:") {
			ctype = strings.TrimSpace(line[len("content-type:"):])
		}
	}
	_, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		t.Fatalf("Content-Type %q: %v", ctype, err)
	}

	var out []string
	r := multipart.NewReader(strings.NewReader(body), params["boundary"])
	for {
		p, err := r.NextPart()
		if err != nil {
			break
		}
		data, err := io.ReadAll(p)
		if err != nil {
			t.Fatalf("reading part %d: %v", len(out), err)
		}
		out = append(out, string(data))
	}
	return out
}

func TestBuild_TextOnly(t *testing.T) {
	raw := Build(testMessage())

	for _, want := range []string{
		"From: \"Alice\" <alice@example.com>\r\n",
		"Subject: Hello\r\n",
		"Date: Sat, 15 Mar 2025 10:30:00 +0000\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"Hello, world!",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %q in:\n%s", want, raw)
		}
	}
	if strings.Contains(raw, "multipart") {
		t.Errorf("a text-only message was rendered as multipart:\n%s", raw)
	}
}

func TestBuild_HTMLOnly(t *testing.T) {
	msg := testMessage()
	msg.BodyText = ""
	msg.BodyHTML = "<p>Hello</p>"

	raw := Build(msg)

	if !strings.Contains(raw, "Content-Type: text/html; charset=utf-8\r\n") {
		t.Errorf("missing HTML Content-Type in:\n%s", raw)
	}
	if !strings.Contains(raw, "<p>Hello</p>") {
		t.Errorf("missing HTML body in:\n%s", raw)
	}
	if strings.Contains(raw, "multipart") {
		t.Errorf("an HTML-only message was rendered as multipart:\n%s", raw)
	}
}

func TestBuild_Multipart(t *testing.T) {
	msg := testMessage()
	msg.BodyHTML = "<p>Rich text</p>"

	raw := Build(msg)

	if !strings.Contains(raw, "Content-Type: multipart/alternative; boundary=") {
		t.Errorf("not multipart/alternative:\n%s", raw)
	}
	got := readParts(t, raw)
	want := []string{"Hello, world!", "<p>Rich text</p>"}
	if len(got) != len(want) {
		t.Fatalf("read %d parts, want %d:\n%s", len(got), len(want), raw)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The parts have to carry their own media types, since the alternative is only
// useful to a client that can tell them apart.
func TestBuild_MultipartPartsAreTyped(t *testing.T) {
	msg := testMessage()
	msg.BodyHTML = "<p>Rich text</p>"

	raw := Build(msg)

	for _, want := range []string{
		"Content-Type: text/plain; charset=utf-8\r\n",
		"Content-Type: text/html; charset=utf-8\r\n",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing part header %q in:\n%s", want, raw)
		}
	}
}

// A non-ASCII Subject must leave as an RFC 2047 encoded-word, and must decode
// back to exactly what the sender wrote.
func TestBuild_NonASCIISubjectIsEncoded(t *testing.T) {
	msg := testMessage()
	msg.Subject = "Überweisung für Kaffee"

	raw := Build(msg)

	if strings.Contains(raw, "Subject: Überweisung") {
		t.Error("the subject was written as raw UTF-8 rather than an encoded-word")
	}
	header, _ := split(t, raw)
	var subject string
	for _, line := range strings.Split(header, "\r\n") {
		if strings.HasPrefix(line, "Subject: ") {
			subject = strings.TrimPrefix(line, "Subject: ")
		}
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(subject)
	if err != nil {
		t.Fatalf("decoding %q: %v", subject, err)
	}
	if decoded != msg.Subject {
		t.Errorf("subject round-tripped to %q, want %q", decoded, msg.Subject)
	}
}

// The same holds for a non-ASCII display name, which also has to stay parseable
// as an address.
func TestBuild_NonASCIISenderNameIsEncoded(t *testing.T) {
	msg := testMessage()
	msg.SenderName = "Renée Müller"

	raw := Build(msg)

	header, _ := split(t, raw)
	var from string
	for _, line := range strings.Split(header, "\r\n") {
		if strings.HasPrefix(line, "From: ") {
			from = strings.TrimPrefix(line, "From: ")
		}
	}
	if strings.Contains(from, "Renée") {
		t.Errorf("the display name was written as raw UTF-8: %q", from)
	}
	addr, err := mail.ParseAddress(from)
	if err != nil {
		t.Fatalf("parsing %q: %v", from, err)
	}
	if addr.Name != msg.SenderName || addr.Address != msg.Sender {
		t.Errorf("From round-tripped to %q <%s>, want %q <%s>",
			addr.Name, addr.Address, msg.SenderName, msg.Sender)
	}
}

// A message with no sender name renders a bare address rather than leaving an
// empty display name before the angle bracket.
func TestBuild_EmptySenderName(t *testing.T) {
	msg := testMessage()
	msg.SenderName = ""

	raw := Build(msg)

	if strings.Contains(raw, "From:  <") {
		t.Errorf("empty sender name left a double space in:\n%s", raw)
	}
	if !strings.Contains(raw, "From: <alice@example.com>\r\n") {
		t.Errorf("missing bare-address From header in:\n%s", raw)
	}
}

// The boundary must not be one a body can contain. It used to be derived from
// the receipt timestamp, which the message itself carries in its Date header, so
// a body could close the multipart early and hide the part that followed.
func TestBuild_BodyCannotForgeTheBoundary(t *testing.T) {
	ts := testTime()
	msg := testMessage()
	msg.ReceivedAt = ts
	msg.BodyText = fmt.Sprintf("before\r\n--%s%d--\r\nafter", boundaryPrefix, ts.UnixNano())
	msg.BodyHTML = "<p>Rich text</p>"

	raw := Build(msg)

	got := readParts(t, raw)
	if len(got) != 2 {
		t.Fatalf("read %d parts, want 2:\n%s", len(got), raw)
	}
	if !strings.Contains(got[0], "after") {
		t.Errorf("the text part was cut short at the forged boundary: %q", got[0])
	}
	if got[1] != msg.BodyHTML {
		t.Errorf("HTML part = %q, want %q", got[1], msg.BodyHTML)
	}
}

// Two renderings of the same message must not collide on a boundary, and each
// must be absent from the bodies it delimits.
func TestBuild_BoundaryIsFreshEachTime(t *testing.T) {
	msg := testMessage()
	msg.BodyHTML = "<p>Rich text</p>"

	boundaryOf := func(raw string) string {
		header, _ := split(t, raw)
		for _, line := range strings.Split(header, "\r\n") {
			if !strings.HasPrefix(strings.ToLower(line), "content-type:") {
				continue
			}
			_, params, err := mime.ParseMediaType(strings.TrimSpace(line[len("content-type:"):]))
			if err != nil {
				t.Fatalf("Content-Type %q: %v", line, err)
			}
			return params["boundary"]
		}
		t.Fatalf("no Content-Type in:\n%s", raw)
		return ""
	}

	first, second := boundaryOf(Build(msg)), boundaryOf(Build(msg))
	if first == second {
		t.Errorf("two renderings reused the boundary %q", first)
	}
	if !strings.HasPrefix(first, boundaryPrefix) {
		t.Errorf("boundary %q does not carry the %q prefix", first, boundaryPrefix)
	}
}

func TestBuild_OptionalHeaders(t *testing.T) {
	t.Run("present when set", func(t *testing.T) {
		msg := testMessage()
		msg.MessageID = "abc123@example.com"
		msg.InReplyTo = "original@example.com"

		raw := Build(msg)

		if !strings.Contains(raw, "Message-ID: <abc123@example.com>\r\n") {
			t.Errorf("missing Message-ID in:\n%s", raw)
		}
		if !strings.Contains(raw, "In-Reply-To: <original@example.com>\r\n") {
			t.Errorf("missing In-Reply-To in:\n%s", raw)
		}
	})

	t.Run("absent when unset", func(t *testing.T) {
		raw := Build(testMessage())

		if strings.Contains(raw, "Message-ID:") {
			t.Errorf("Message-ID written for a message that has none:\n%s", raw)
		}
		if strings.Contains(raw, "In-Reply-To:") {
			t.Errorf("In-Reply-To written for a message that has none:\n%s", raw)
		}
	})
}
