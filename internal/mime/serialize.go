package mime

import (
	"bytes"
	"encoding/base64"
	"io"
	stdmime "mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
)

const (
	// foldWidth is where a long header is folded. RFC 5322 §2.1.1 allows up to
	// 998 octets on a line, but 78 is the recommended display width and what
	// mainstream MTAs emit.
	foldWidth = 78
	// base64Width is the line length used for base64 part content.
	base64Width = 76
)

// headerEncoder encodes header values containing non-ASCII text as RFC 2047
// encoded-words. Pure-ASCII values pass through unchanged.
var headerEncoder = stdmime.QEncoding

// Serialize converts an EmailJSON back to a raw RFC 5322 message.
//
// Header values are RFC 2047-encoded where they need to be, folded, and
// stripped of CR/LF so a value can never start a header line of its own.
// Multipart bodies are assembled with mime/multipart, whose boundary is random
// rather than derived from the clock.
func Serialize(email *pipeline.EmailJSON) ([]byte, error) {
	var b strings.Builder

	if len(email.Headers.From) > 0 {
		writeHeader(&b, "From", formatAddresses(email.Headers.From))
	}
	if len(email.Headers.To) > 0 {
		writeHeader(&b, "To", formatAddresses(email.Headers.To))
	}
	if len(email.Headers.Cc) > 0 {
		writeHeader(&b, "Cc", formatAddresses(email.Headers.Cc))
	}
	if email.Headers.Subject != "" {
		writeHeader(&b, "Subject", headerEncoder.Encode("utf-8", email.Headers.Subject))
	}
	if email.Headers.Date != "" {
		writeHeader(&b, "Date", email.Headers.Date)
	} else {
		writeHeader(&b, "Date", time.Now().Format(time.RFC1123Z))
	}
	if email.Headers.MessageID != "" {
		writeHeader(&b, "Message-ID", email.Headers.MessageID)
	}
	if email.Headers.InReplyTo != "" {
		writeHeader(&b, "In-Reply-To", email.Headers.InReplyTo)
	}
	if len(email.Headers.References) > 0 {
		writeHeader(&b, "References", strings.Join(email.Headers.References, " "))
	}

	// Extra headers are set by pipeline transforms rather than read back from a
	// parsed message, so they have not been through the parser's sanitising.
	for name, value := range email.Headers.Extra {
		writeHeader(&b, name, value)
	}

	b.WriteString("MIME-Version: 1.0\r\n")

	switch {
	case len(email.Attachments) > 0 || len(email.Inline) > 0:
		if err := writeMixed(&b, email); err != nil {
			return nil, err
		}
	case len(email.Body.Parts) > 0:
		body, boundary, err := renderAlternative(email.Body.Parts)
		if err != nil {
			return nil, err
		}
		writeHeader(&b, "Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
		b.WriteString("\r\n")
		b.Write(body)
	default:
		writeHeader(&b, "Content-Type", contentTypeOr(email.Body.ContentType))
		b.WriteString("\r\n")
		b.WriteString(email.Body.Content)
		if !strings.HasSuffix(email.Body.Content, "\r\n") {
			b.WriteString("\r\n")
		}
	}

	return []byte(b.String()), nil
}

// writeMixed renders a multipart/mixed body: the message body as the first
// part, then inline parts, then attachments.
func writeMixed(b *strings.Builder, email *pipeline.EmailJSON) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	if len(email.Body.Parts) > 0 {
		alt, boundary, err := renderAlternative(email.Body.Parts)
		if err != nil {
			return err
		}
		part, err := w.CreatePart(textproto.MIMEHeader{
			"Content-Type": {`multipart/alternative; boundary="` + boundary + `"`},
		})
		if err != nil {
			return err
		}
		if _, err := part.Write(alt); err != nil {
			return err
		}
	} else {
		part, err := w.CreatePart(textproto.MIMEHeader{
			"Content-Type": {contentTypeOr(email.Body.ContentType)},
		})
		if err != nil {
			return err
		}
		if _, err := io.WriteString(part, email.Body.Content); err != nil {
			return err
		}
	}

	for _, att := range email.Inline {
		if err := writeAttachmentPart(w, att); err != nil {
			return err
		}
	}
	for _, att := range email.Attachments {
		if err := writeAttachmentPart(w, att); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}

	writeHeader(b, "Content-Type", `multipart/mixed; boundary="`+w.Boundary()+`"`)
	b.WriteString("\r\n")
	b.Write(buf.Bytes())
	return nil
}

// renderAlternative renders the body parts as multipart/alternative, returning
// the rendered parts and the boundary that delimits them.
func renderAlternative(parts []pipeline.Body) (body []byte, boundary string, err error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for _, part := range parts {
		pw, err := w.CreatePart(textproto.MIMEHeader{
			"Content-Type": {contentTypeOr(part.ContentType)},
		})
		if err != nil {
			return nil, "", err
		}
		if _, err := io.WriteString(pw, part.Content); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.Boundary(), nil
}

// writeAttachmentPart appends one attachment or inline part. Content is assumed
// to be base64 already, as it is carried through the pipeline in that form.
func writeAttachmentPart(w *multipart.Writer, att pipeline.Attachment) error {
	disposition := att.Disposition
	if disposition == "" {
		disposition = "attachment"
	}

	header := textproto.MIMEHeader{
		"Content-Type":              {contentTypeOr(att.ContentType)},
		"Content-Transfer-Encoding": {"base64"},
	}
	if att.Filename != "" {
		// FormatMediaType quotes what needs quoting and falls back to RFC 2231
		// for a non-ASCII filename, which a hand-built header does not.
		header.Set("Content-Disposition", stdmime.FormatMediaType(disposition,
			map[string]string{"filename": sanitizeHeaderValue(att.Filename)}))
	} else {
		header.Set("Content-Disposition", disposition)
	}
	if att.ContentID != "" {
		header.Set("Content-ID", sanitizeHeaderValue(att.ContentID))
	}

	part, err := w.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = io.WriteString(part, wrapBase64(att.Content))
	return err
}

// contentTypeOr returns ct, or a sensible default when it is empty.
func contentTypeOr(ct string) string {
	if ct == "" {
		return "text/plain; charset=utf-8"
	}
	return ct
}

// writeHeader appends one header, sanitized, encoded and folded. A header whose
// name is unusable is dropped rather than written as a broken line.
func writeHeader(b *strings.Builder, name, value string) {
	name = strings.TrimSpace(sanitizeHeaderValue(name))
	if name == "" || strings.ContainsAny(name, ": \t") {
		return
	}
	b.WriteString(foldHeader(name, sanitizeHeaderValue(value)))
}

// foldHeader renders "Name: value" folded at whitespace so no line runs past
// foldWidth. A single token longer than the limit is left intact: there is
// nowhere legal to break it.
func foldHeader(name, value string) string {
	line := name + ": " + value
	if len(line) <= foldWidth {
		return line + "\r\n"
	}

	words := strings.Fields(value)
	if len(words) == 0 {
		return line + "\r\n"
	}

	var out strings.Builder
	current := name + ":"
	for _, word := range words {
		// +1 for the space that precedes the word.
		if current != name+":" && len(current)+1+len(word) > foldWidth {
			out.WriteString(current)
			out.WriteString("\r\n")
			current = ""
		}
		current += " " + word
	}
	out.WriteString(current)
	out.WriteString("\r\n")
	return out.String()
}

// formatAddresses renders an address list. net/mail quotes a display name that
// needs it and encodes a non-ASCII one as an RFC 2047 encoded-word.
func formatAddresses(addrs []pipeline.Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		addr := mail.Address{
			Name:    sanitizeHeaderValue(a.Name),
			Address: sanitizeHeaderValue(a.Address),
		}
		parts = append(parts, addr.String())
	}
	return strings.Join(parts, ", ")
}

// wrapBase64 breaks base64 content into lines of at most base64Width octets.
func wrapBase64(content string) string {
	if len(content) <= base64Width {
		return content
	}
	var out strings.Builder
	for len(content) > base64Width {
		out.WriteString(content[:base64Width])
		out.WriteString("\r\n")
		content = content[base64Width:]
	}
	out.WriteString(content)
	return out.String()
}

// EnvelopeFromEmail extracts envelope information from the email headers.
func EnvelopeFromEmail(email *pipeline.EmailJSON) pipeline.Envelope {
	env := email.Envelope
	if env.MailFrom == "" && len(email.Headers.From) > 0 {
		env.MailFrom = email.Headers.From[0].Address
	}
	if len(env.RcptTo) == 0 {
		for _, to := range email.Headers.To {
			env.RcptTo = append(env.RcptTo, to.Address)
		}
		for _, cc := range email.Headers.Cc {
			env.RcptTo = append(env.RcptTo, cc.Address)
		}
	}
	return env
}

// EstimateSize returns a rough estimate of the message size in bytes.
func EstimateSize(email *pipeline.EmailJSON) int64 {
	var size int64
	// Headers
	for _, from := range email.Headers.From {
		size += int64(len(from.Address) + len(from.Name) + 20)
	}
	size += int64(len(email.Headers.Subject) + 20)
	// Body — recurse the full part tree, not just the top level, so a nested
	// multipart (multipart/mixed → multipart/alternative → text) is counted in
	// full rather than undercounted (issue #201).
	size += bodyTreeSize(email.Body)
	// Attachments
	for _, att := range email.Attachments {
		if att.Content != "" {
			decoded, _ := base64.StdEncoding.DecodeString(att.Content)
			size += int64(len(decoded))
		} else {
			size += att.Size
		}
	}
	for _, att := range email.Inline {
		if att.Content != "" {
			decoded, _ := base64.StdEncoding.DecodeString(att.Content)
			size += int64(len(decoded))
		} else {
			size += att.Size
		}
	}
	return size
}

// bodyTreeSize returns the total content length of a body and every nested part,
// recursively. A multipart body carries its payload in Parts (which may
// themselves be multipart), so summing only the top level undercounts.
func bodyTreeSize(b pipeline.Body) int64 {
	size := int64(len(b.Content))
	for _, part := range b.Parts {
		size += bodyTreeSize(part)
	}
	return size
}
