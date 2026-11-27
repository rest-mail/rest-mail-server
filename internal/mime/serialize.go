package mime

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	stdmime "mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
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

// AttachmentSource opens the raw (unencoded) bytes of a MIME attachment.
// SerializeTo closes the returned reader, including when rendering fails.
type AttachmentSource interface {
	Open() (io.ReadCloser, error)
}

// SerializeOptions supplies optional sources for the parts in EmailJSON.
type SerializeOptions struct {
	// A nonempty slice matches the corresponding EmailJSON slice by index.
	// Nil entries use the attachment's existing base64 Content.
	AttachmentSources []AttachmentSource
	InlineSources     []AttachmentSource

	// Boundary supplies a fresh boundary per multipart, starting at the root.
	// Nil uses mime/multipart's random boundaries.
	Boundary func() string
}

// Serialize converts an EmailJSON back to a raw RFC 5322 message.
func Serialize(email *pipeline.EmailJSON) ([]byte, error) {
	var b bytes.Buffer
	if err := SerializeTo(&b, email, SerializeOptions{}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// SerializeTo writes an RFC 5322 message, streaming attachment sources through
// a base64 encoder. It may leave partial output if a read or write fails.
func SerializeTo(dst io.Writer, email *pipeline.EmailJSON, opts SerializeOptions) error {
	if len(opts.AttachmentSources) != 0 && len(opts.AttachmentSources) != len(email.Attachments) {
		return fmt.Errorf("mime: attachment source count must match attachment count")
	}
	if len(opts.InlineSources) != 0 && len(opts.InlineSources) != len(email.Inline) {
		return fmt.Errorf("mime: inline source count must match inline part count")
	}
	var b strings.Builder

	if email.Headers.From.Len() > 0 {
		writeHeader(&b, "From", formatAddresses(email.Headers.From.All()))
	}
	if email.Headers.To.Len() > 0 {
		writeHeader(&b, "To", formatAddresses(email.Headers.To.All()))
	}
	if email.Headers.Cc.Len() > 0 {
		writeHeader(&b, "Cc", formatAddresses(email.Headers.Cc.All()))
	}
	if email.Headers.Subject != "" {
		// Sanitized before it is encoded: encoded, a line break would no longer
		// look like one to writeHeader, and would come back when it is decoded.
		writeHeader(&b, "Subject", headerEncoder.Encode("utf-8", sanitizeHeaderValue(email.Headers.Subject)))
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
		return opts.writeMixed(dst, &b, email)
	case len(email.Body.Parts) > 0:
		w, err := opts.multipartWriter(dst)
		if err != nil {
			return err
		}
		writeHeader(&b, "Content-Type", multipartContentType(multipartType(email.Body.ContentType), w))
		if err := writeHead(dst, &b); err != nil {
			return err
		}
		if err := opts.writeBodyParts(w, email.Body.Parts); err != nil {
			return err
		}
		return w.Close()
	default:
		writeHeader(&b, "Content-Type", contentTypeOr(email.Body.ContentType))
		if email.Body.TransferEncoding != "" {
			writeHeader(&b, "Content-Transfer-Encoding", email.Body.TransferEncoding)
		}
		if err := writeHead(dst, &b); err != nil {
			return err
		}
		if _, err := io.WriteString(dst, email.Body.Content); err != nil {
			return err
		}
		if !strings.HasSuffix(email.Body.Content, "\r\n") {
			_, err := io.WriteString(dst, "\r\n")
			return err
		}
		return nil
	}
}

func writeHead(dst io.Writer, b *strings.Builder) error {
	b.WriteString("\r\n")
	_, err := io.WriteString(dst, b.String())
	return err
}

func (opts SerializeOptions) multipartWriter(dst io.Writer) (*multipart.Writer, error) {
	w := multipart.NewWriter(dst)
	if opts.Boundary != nil {
		if err := w.SetBoundary(opts.Boundary()); err != nil {
			return nil, err
		}
	}
	return w, nil
}

func multipartContentType(mediaType string, w *multipart.Writer) string {
	return mediaType + `; boundary="` + w.Boundary() + `"`
}

// writeMixed renders a multipart/mixed body: the message body, then inline parts, then attachments.
// A multipart/mixed body's own parts are parts of it, as they were in the message; any other body is
// its first part.
func (opts SerializeOptions) writeMixed(dst io.Writer, b *strings.Builder, email *pipeline.EmailJSON) error {
	w, err := opts.multipartWriter(dst)
	if err != nil {
		return err
	}
	writeHeader(b, "Content-Type", multipartContentType("multipart/mixed", w))
	if err := writeHead(dst, b); err != nil {
		return err
	}

	parts := []pipeline.Body{email.Body}
	if len(email.Body.Parts) > 0 && MediaType(email.Body.ContentType) == "multipart/mixed" {
		parts = email.Body.Parts
	}
	if err := opts.writeBodyParts(w, parts); err != nil {
		return err
	}
	if err := writeAttachments(w, email.Inline, opts.InlineSources); err != nil {
		return err
	}
	if err := writeAttachments(w, email.Attachments, opts.AttachmentSources); err != nil {
		return err
	}
	return w.Close()
}

func (opts SerializeOptions) writeBodyParts(w *multipart.Writer, parts []pipeline.Body) error {
	for _, part := range parts {
		if err := opts.writeBodyPart(w, part); err != nil {
			return err
		}
	}
	return nil
}

// A nested multipart needs a boundary before its containing part can be
// created. Create the multipart writer first, then bind its destination.
type multipartOutput struct{ io.Writer }

// writeBodyPart appends one part of the body tree. A part that has parts of its own is written as
// the multipart it is, with its own boundary, so a body tree of any depth reaches a scanner whole:
// the texts under multipart/mixed → multipart/alternative are what a mail client reads (#1968).
func (opts SerializeOptions) writeBodyPart(w *multipart.Writer, part pipeline.Body) error {
	header := textproto.MIMEHeader{"Content-Type": {contentTypeOr(part.ContentType)}}
	var nested *multipart.Writer
	var dst multipartOutput
	if len(part.Parts) > 0 {
		var err error
		nested, err = opts.multipartWriter(&dst)
		if err != nil {
			return err
		}
		header.Set("Content-Type", multipartContentType(multipartType(part.ContentType), nested))
	}
	if part.TransferEncoding != "" {
		header.Set("Content-Transfer-Encoding", part.TransferEncoding)
	}
	pw, err := w.CreatePart(header)
	if err != nil {
		return err
	}
	if nested != nil {
		dst.Writer = pw
		if err := opts.writeBodyParts(nested, part.Parts); err != nil {
			return err
		}
		return nested.Close()
	}
	_, err = io.WriteString(pw, part.Content)
	return err
}

func writeAttachments(w *multipart.Writer, attachments []pipeline.Attachment, sources []AttachmentSource) error {
	for i, att := range attachments {
		var source AttachmentSource
		if len(sources) != 0 {
			source = sources[i]
		}
		if err := writeAttachmentPart(w, att, source); err != nil {
			return err
		}
	}
	return nil
}

func writeAttachmentPart(w *multipart.Writer, att pipeline.Attachment, source AttachmentSource) error {
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
	if source != nil {
		return writeAttachmentSource(part, source)
	}
	if len(att.Content) <= base64Width {
		_, err := io.WriteString(part, att.Content)
		return err
	}
	// Preserve the pipeline's already-encoded content verbatim, including
	// noncanonical base64, with the same line breaks as Serialize used before.
	lines := base64LineWriter{out: bufio.NewWriter(part)}
	var buf [4096]byte
	if _, err := io.CopyBuffer(&lines, struct{ io.Reader }{strings.NewReader(att.Content)}, buf[:]); err != nil {
		return err
	}
	return lines.out.Flush()
}

func writeAttachmentSource(dst io.Writer, source AttachmentSource) (err error) {
	r, err := source.Open()
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := r.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	lines := base64LineWriter{out: bufio.NewWriter(dst)}
	encoder := base64.NewEncoder(base64.StdEncoding, &lines)
	// Read in chunks even when the source offers WriterTo, which may try to
	// convert its entire payload to a byte slice before writing it.
	if _, err := io.Copy(encoder, struct{ io.Reader }{r}); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return lines.out.Flush()
}

// multipartType is the multipart media type a body with parts is written as: its own, when it names
// one, or multipart/alternative, which is what a body built here with parts is.
func multipartType(ct string) string {
	if mediaType := MediaType(ct); strings.HasPrefix(mediaType, "multipart/") && !strings.ContainsAny(mediaType, "\"\r\n") {
		return mediaType
	}
	return "multipart/alternative"
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

// base64LineWriter inserts CRLF between base64 lines. The multipart writer
// supplies the line break after the last line, even if it is exactly full.
type base64LineWriter struct {
	out    *bufio.Writer
	column int
}

func (w *base64LineWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		if w.column == base64Width {
			if _, err := w.out.WriteString("\r\n"); err != nil {
				return written, err
			}
			w.column = 0
		}
		n := min(base64Width-w.column, len(p))
		count, err := w.out.Write(p[:n])
		written += count
		w.column += count
		if err != nil {
			return written, err
		}
		p = p[n:]
	}
	return written, nil
}

// EnvelopeFromEmail extracts envelope information from the email headers.
func EnvelopeFromEmail(email *pipeline.EmailJSON) pipeline.Envelope {
	env := email.Envelope
	if env.MailFrom == "" && email.Headers.From.Len() > 0 {
		env.MailFrom = email.Headers.From.FirstAddress()
	}
	if len(env.RcptTo) == 0 {
		for _, to := range email.Headers.To.All() {
			env.RcptTo = append(env.RcptTo, to.Address)
		}
		for _, cc := range email.Headers.Cc.All() {
			env.RcptTo = append(env.RcptTo, cc.Address)
		}
	}
	return env
}

// EstimateSize returns a rough estimate of the message size in bytes.
func EstimateSize(email *pipeline.EmailJSON) int64 {
	var size int64
	// Headers
	for _, from := range email.Headers.From.All() {
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
