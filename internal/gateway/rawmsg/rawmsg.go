// Package rawmsg renders a stored message as RFC 5322 bytes for the retrieval
// gateways.
//
// IMAP FETCH and POP3 RETR hand a client the same stored message, so the two
// gateways have to agree byte for byte on how it is rendered. They each used to
// carry their own copy of the construction rules, which meant a fix to one
// silently left the other behind.
package rawmsg

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strings"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// dateLayout is the RFC 5322 date-time form used in the Date header.
const dateLayout = "Mon, 02 Jan 2006 15:04:05 -0700"

// boundaryPrefix marks a generated multipart boundary as ours when a message is
// read by hand. The random suffix, not the prefix, is what makes it unguessable.
const boundaryPrefix = "=_restmail_"

// headerEncoder encodes header values that contain non-ASCII text as RFC 2047
// encoded-words. Pure-ASCII values pass through unchanged.
var headerEncoder = mime.QEncoding

// Build renders msg as an RFC 5322 message with CRLF line endings.
//
// A message carrying both a text and an HTML body becomes multipart/alternative;
// one carrying a single body is emitted with that body's media type directly.
func Build(msg apiclient.MessageDetail) string {
	var b strings.Builder

	// mail.Address quotes or encodes the display name as needed, and renders a
	// bare <addr> when there is no name rather than leaving an empty gap.
	from := (&mail.Address{Name: msg.SenderName, Address: msg.Sender}).String()
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "Subject: %s\r\n", headerEncoder.Encode("utf-8", msg.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", msg.ReceivedAt.Format(dateLayout))
	if msg.MessageID != "" {
		fmt.Fprintf(&b, "Message-ID: <%s>\r\n", msg.MessageID)
	}
	if msg.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\n", msg.InReplyTo)
	}
	b.WriteString("MIME-Version: 1.0\r\n")

	switch {
	case msg.BodyText != "" && msg.BodyHTML != "":
		body, boundary := alternative(msg.BodyText, msg.BodyHTML)
		fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
		b.WriteString(body)
	case msg.BodyHTML != "":
		b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
		b.WriteString(msg.BodyHTML)
	default:
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(msg.BodyText)
	}

	return b.String()
}

// alternative renders the two bodies as multipart/alternative parts, returning
// the rendered parts and the boundary that delimits them.
func alternative(text, html string) (body, boundary string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// SetBoundary only fails on a malformed boundary, and newBoundary returns a
	// well-formed one, so the error cannot occur here.
	_ = w.SetBoundary(newBoundary(text, html))

	writePart(w, "text/plain; charset=utf-8", text)
	writePart(w, "text/html; charset=utf-8", html)
	// Writes go to a bytes.Buffer, which cannot fail.
	_ = w.Close()

	return buf.String(), w.Boundary()
}

// writePart appends one part with the given media type.
func writePart(w *multipart.Writer, mediaType, content string) {
	part, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {mediaType}})
	if err != nil {
		// CreatePart fails only on a closed writer, which this package never
		// does before its parts are written.
		return
	}
	_, _ = io.WriteString(part, content)
}

// newBoundary returns a delimiter that appears in neither body.
//
// The boundary used to be derived from the message's receipt timestamp, which
// the same message carries in its Date header: anyone who could guess it could
// put the delimiter in a body and close the multipart early, hiding everything
// after it from a client. The 24 random bytes make the value unguessable, and
// the containment check makes its absence from the bodies certain rather than
// merely overwhelmingly likely.
func newBoundary(bodies ...string) string {
	for {
		var raw [24]byte
		// crypto/rand.Read fills the buffer entirely or crashes the program; it
		// never returns a short read.
		_, _ = rand.Read(raw[:])
		candidate := boundaryPrefix + hex.EncodeToString(raw[:])

		clash := false
		for _, body := range bodies {
			if strings.Contains(body, candidate) {
				clash = true
				break
			}
		}
		if !clash {
			return candidate
		}
	}
}
