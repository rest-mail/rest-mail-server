package mail

import (
	"bufio"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"strings"
)

// maxTextLine is the longest line SMTP must carry, line end included (RFC 5321 section 4.5.3.1.6).
const maxTextLine = 1000

// maxEntityDepth bounds how deep ForData follows nested parts: a message chooses its own depth.
const maxEntityDepth = 32

// base64LineLength is the longest line of base64 RFC 2045 section 6.8 allows.
const base64LineLength = 76

// ErrDataCannotCarry says a message needs a change SMTP DATA can't make without breaking it.
var ErrDataCannotCarry = errors.New("message holds line ends SMTP DATA cannot carry, in a part that cannot be converted")

// HasBareLineEnds reports whether raw holds a CR or an LF that isn't part of a CRLF.
func HasBareLineEnds(raw string) bool {
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '\r':
			if i+1 == len(raw) || raw[i+1] != '\n' {
				return true
			}
			i++
		case '\n':
			return true
		}
	}
	return false
}

// ForData writes raw as SMTP DATA may carry it, where CR and LF appear only together as CRLF
// (RFC 5321 section 2.3.8). A line ended by a bare LF, a bare CR, or CR CR LF ends in one CRLF,
// so nothing a receiver could read as an end of data stays hidden from dot-stuffing, which comes
// after this. A part that isn't text keeps its octets: when it holds anything DATA would change,
// it goes as base64 (RFC 2045 section 6.8). ForData writes as it reads and holds no copy of its own.
//
// A part that can't be converted, and a line this would take over the length limit where nothing
// can be converted, answer ErrDataCannotCarry: what was written is then of no use. The caller only
// reaches here for a message HasBareLineEnds says needs it.
func ForData(w io.Writer, raw string) error {
	n := dataNormaliser{w: bufio.NewWriter(w)}
	if err := n.entity(raw, entityContext{mimeVersionNeeded: true}); err != nil {
		return err
	}
	return n.w.Flush()
}

type dataNormaliser struct {
	w *bufio.Writer
}

// entityContext is what an entity's surroundings say about it.
type entityContext struct {
	depth int
	// defaultRFC822 is true in a multipart/digest, whose parts are messages by default.
	defaultRFC822 bool
	// sealed is true under multipart/signed or multipart/encrypted, where converting a part breaks
	// what protects it (RFC 1847 section 2.1).
	sealed bool
	// mimeVersionNeeded is true for a message, as opposed to a part of one: base64 means nothing
	// to a reader without MIME-Version (RFC 2045 section 4).
	mimeVersionNeeded bool
	// lastLineGrows says the line end after this entity becomes CRLF and takes its last line over
	// the limit. That line end belongs to the delimiter that follows, so the entity can't see it.
	lastLineGrows bool
}

// writeLines writes s with every line end as CRLF. It refuses a line the rewrite would take over
// the limit that it was within. One already over goes as it is, and the next hop decides.
func (n *dataNormaliser) writeLines(s string) error {
	if anyLineOutgrows(s) {
		return ErrDataCannotCarry
	}
	return writeWithCRLF(n.w, s)
}

// writeWithCRLF writes s with every line end as CRLF.
func writeWithCRLF(w *bufio.Writer, s string) error {
	for pos := 0; pos < len(s); {
		end, next, eol := lineAt(s, pos)
		if _, err := w.WriteString(s[pos:end]); err != nil {
			return err
		}
		if eol {
			if _, err := w.WriteString("\r\n"); err != nil {
				return err
			}
		}
		pos = next
	}
	return nil
}

// lineOutgrows says CRLF takes this line over the limit when what ended it didn't.
func lineOutgrows(pos, end, next int, eol bool) bool {
	return eol && next-pos <= maxTextLine && end-pos+2 > maxTextLine
}

func anyLineOutgrows(s string) bool {
	for pos := 0; pos < len(s); {
		end, next, eol := lineAt(s, pos)
		if lineOutgrows(pos, end, next, eol) {
			return true
		}
		pos = next
	}
	return false
}

// entityFields is what the header section of an entity says about its body.
type entityFields struct {
	contentType, encoding string
	hasType, hasVersion   bool
	headerEnd             int // the header section, without the blank line
	bodyStart             int
	hasBlank              bool
}

func readEntityFields(s string) entityFields {
	var f entityFields
	pos, field, fieldName := 0, "", ""
	finish := func() {
		switch fieldName {
		case "content-type":
			if !f.hasType {
				f.contentType, f.hasType = strings.TrimSpace(field), true
			}
		case "content-transfer-encoding":
			if f.encoding == "" {
				f.encoding = encodingToken(field)
			}
		case "mime-version":
			f.hasVersion = true
		}
	}
	for pos < len(s) {
		end, next, _ := lineAt(s, pos)
		if end == pos {
			finish()
			f.headerEnd, f.bodyStart, f.hasBlank = pos, next, true
			return f
		}
		line := s[pos:end]
		if line[0] == ' ' || line[0] == '\t' {
			field += " " + strings.TrimSpace(line)
		} else {
			finish()
			name, value, _ := strings.Cut(line, ":")
			fieldName, field = strings.ToLower(strings.TrimSpace(name)), value
		}
		pos = next
	}
	finish()
	f.headerEnd, f.bodyStart = len(s), len(s)
	return f
}

func encodingToken(value string) string {
	value, _, _ = strings.Cut(value, "(")
	return strings.ToLower(strings.TrimSpace(value))
}

// mediaOf reads a Content-Type. A value too broken to parse still names its type when it can.
func mediaOf(f entityFields, ctx entityContext) (mediaType, boundary string) {
	if !f.hasType {
		if ctx.defaultRFC822 {
			return "message/rfc822", ""
		}
		return "text/plain", ""
	}
	mediaType, params, err := mime.ParseMediaType(f.contentType)
	if mediaType == "" || (err != nil && !errors.Is(err, mime.ErrInvalidMediaParameter)) {
		token, _, _ := strings.Cut(f.contentType, ";")
		return strings.ToLower(strings.TrimSpace(token)), ""
	}
	return mediaType, params["boundary"]
}

func (n *dataNormaliser) entity(s string, ctx entityContext) error {
	f := readEntityFields(s)
	mediaType, boundary := mediaOf(f, ctx)
	body := s[f.bodyStart:]
	identity := f.encoding == "" || f.encoding == "7bit" || f.encoding == "8bit" || f.encoding == "binary"
	major, _, _ := strings.Cut(mediaType, "/")
	grows := ctx.lastLineGrows
	if grows && (!f.hasBlank || ctx.depth >= maxEntityDepth || major == "multipart" || !identity) {
		// A header line or a line of a part that can't be encoded: as for any other such line.
		return ErrDataCannotCarry
	}

	switch {
	case ctx.depth >= maxEntityDepth:
		return n.unconvertible(s, f, body)
	case major == "multipart" && identity && boundary != "":
		if err := n.headerSection(s, f); err != nil {
			return err
		}
		return n.multipart(body, mediaType, boundary, ctx)
	case (mediaType == "message/rfc822" || mediaType == "message/global") && identity:
		if err := n.headerSection(s, f); err != nil {
			return err
		}
		return n.entity(body, entityContext{depth: ctx.depth + 1, sealed: ctx.sealed, mimeVersionNeeded: true, lastLineGrows: grows})
	case major == "multipart" || (!identity && f.encoding != "base64" && f.encoding != "quoted-printable"):
		// No way to find its parts, or an encoding this server can't read: its octets can't be
		// told apart and can't be encoded again (RFC 2045 section 6.4).
		return n.unconvertible(s, f, body)
	case !identity:
		// What an encoding carries is safe from its line ends: base64 ignores them, and in
		// quoted-printable they're line breaks (RFC 2045 section 6.7).
		if err := n.headerSection(s, f); err != nil {
			return err
		}
		return n.writeLines(body)
	}

	text := (major == "text" || major == "message") && f.encoding != "binary" && !strings.Contains(body, "\x00")
	switch {
	case text && !grows && !anyLineOutgrows(body):
		if err := n.headerSection(s, f); err != nil {
			return err
		}
		return n.writeLines(body)
	case text:
		// Its line ends are line ends, and one of them can't become CRLF within the limit.
		return n.asBase64(s, f, ctx, func(w *bufio.Writer) error { return writeWithCRLF(w, body) })
	case !grows && !HasBareLineEnds(body):
		if err := n.headerSection(s, f); err != nil {
			return err
		}
		_, err := n.w.WriteString(body)
		return err
	}
	return n.asBase64(s, f, ctx, func(w *bufio.Writer) error { _, err := w.WriteString(body); return err })
}

// unconvertible passes on what DATA carries as it stands, and refuses what it doesn't.
func (n *dataNormaliser) unconvertible(s string, f entityFields, body string) error {
	if HasBareLineEnds(body) {
		return ErrDataCannotCarry
	}
	if err := n.headerSection(s, f); err != nil {
		return err
	}
	_, err := n.w.WriteString(body)
	return err
}

func (n *dataNormaliser) headerSection(s string, f entityFields) error {
	if err := n.writeLines(s[:f.headerEnd]); err != nil {
		return err
	}
	if !f.hasBlank {
		return nil
	}
	_, err := n.w.WriteString("\r\n")
	return err
}

// asBase64 writes the entity with its body encoded from what content writes, and its header
// section saying so.
func (n *dataNormaliser) asBase64(s string, f entityFields, ctx entityContext, content func(*bufio.Writer) error) error {
	if ctx.sealed {
		return ErrDataCannotCarry
	}
	if err := n.writeLinesWithoutEncoding(s[:f.headerEnd]); err != nil {
		return err
	}
	if ctx.mimeVersionNeeded && !f.hasVersion {
		if _, err := n.w.WriteString("MIME-Version: 1.0\r\n"); err != nil {
			return err
		}
	}
	if _, err := n.w.WriteString("Content-Transfer-Encoding: base64\r\n\r\n"); err != nil {
		return err
	}
	lines := &base64Lines{w: n.w}
	encoder := base64.NewEncoder(base64.StdEncoding, lines)
	in := bufio.NewWriter(encoder)
	if err := content(in); err != nil {
		return err
	}
	if err := in.Flush(); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return lines.end()
}

// writeLinesWithoutEncoding writes a header section, leaving out Content-Transfer-Encoding.
func (n *dataNormaliser) writeLinesWithoutEncoding(s string) error {
	skipping := false
	for pos := 0; pos < len(s); {
		end, next, eol := lineAt(s, pos)
		line := s[pos:end]
		switch {
		case line != "" && (line[0] == ' ' || line[0] == '\t'):
		default:
			name, _, _ := strings.Cut(line, ":")
			skipping = strings.EqualFold(strings.TrimSpace(name), "Content-Transfer-Encoding")
		}
		if !skipping {
			if lineOutgrows(pos, end, next, eol) {
				return ErrDataCannotCarry
			}
			if _, err := n.w.WriteString(line); err != nil {
				return err
			}
			// The fields added after this need the last one ended.
			if _, err := n.w.WriteString("\r\n"); err != nil {
				return err
			}
		}
		pos = next
	}
	return nil
}

// base64Lines cuts encoded text into lines.
type base64Lines struct {
	w      *bufio.Writer
	column int
}

func (l *base64Lines) Write(p []byte) (int, error) {
	written := len(p)
	for len(p) > 0 {
		take := min(base64LineLength-l.column, len(p))
		if _, err := l.w.Write(p[:take]); err != nil {
			return 0, err
		}
		p = p[take:]
		l.column += take
		if l.column == base64LineLength {
			if _, err := l.w.WriteString("\r\n"); err != nil {
				return 0, err
			}
			l.column = 0
		}
	}
	return written, nil
}

// end finishes the last line. Text after the entity starts with its own line end, and the line
// before it may as well have one: a blank line at the end of base64 decodes to nothing.
func (l *base64Lines) end() error {
	if l.column == 0 {
		return nil
	}
	l.column = 0
	_, err := l.w.WriteString("\r\n")
	return err
}

// multipart writes a multipart body. The line end before a delimiter belongs to the delimiter
// (RFC 2046 section 5.1.1), so a part ends where the line before it ends. A line is judged as the
// receiver will see it once written, whichever line end it arrived with. A delimiter of a multipart
// around this one never gets here: that one's scan has already ended this body before it.
func (n *dataNormaliser) multipart(body, mediaType, boundary string, ctx entityContext) error {
	inner := entityContext{
		depth:         ctx.depth + 1,
		defaultRFC822: mediaType == "multipart/digest",
		sealed:        ctx.sealed || mediaType == "multipart/signed" || mediaType == "multipart/encrypted",
	}
	boundary = strings.TrimRight(boundary, " \t")
	pos, partStart, inPart, lastStart, lastEnd := 0, 0, false, 0, 0
	for pos < len(body) {
		end, next, _ := lineAt(body, pos)
		kind := delimiterKind(body[pos:end], boundary)
		if kind == notDelimiter {
			lastStart, lastEnd, pos = pos, end, next
			continue
		}
		inner.lastLineGrows = lineOutgrows(lastStart, lastEnd, pos, true)
		if err := n.beforeDelimiter(body, partStart, pos, lastEnd, inPart, inner); err != nil {
			return err
		}
		if err := n.writeLines(body[pos:next]); err != nil {
			return err
		}
		if kind == closingDelimiter {
			// The epilogue is no one's part.
			return n.writeLines(body[next:])
		}
		pos, partStart, inPart = next, next, true
	}
	// No closing delimiter: what is left runs to the end of the body.
	if !inPart {
		return n.writeLines(body)
	}
	inner.lastLineGrows = false
	return n.entity(body[partStart:], inner)
}

// beforeDelimiter writes what comes before a delimiter at lineStart: a part or the preamble, each
// ending at lastEnd, and the line end that belongs to the delimiter. Nothing comes before it when
// it opens the body or directly follows another delimiter.
func (n *dataNormaliser) beforeDelimiter(body string, partStart, lineStart, lastEnd int, inPart bool, inner entityContext) error {
	if lineStart == partStart {
		return nil
	}
	var err error
	switch {
	case inPart:
		err = n.entity(body[partStart:lastEnd], inner)
	case inner.lastLineGrows:
		err = ErrDataCannotCarry
	default:
		err = n.writeLines(body[:lastEnd])
	}
	if err != nil {
		return err
	}
	_, err = n.w.WriteString("\r\n")
	return err
}

const (
	notDelimiter = iota
	openingDelimiter
	closingDelimiter
)

// delimiterKind says whether line is a delimiter of boundary. Only white space may follow it
// (RFC 2046 section 5.1.1).
func delimiterKind(line, boundary string) int {
	rest, ok := strings.CutPrefix(line, "--")
	if !ok {
		return notDelimiter
	}
	rest = strings.TrimRight(rest, " \t")
	switch rest {
	case boundary:
		return openingDelimiter
	case boundary + "--":
		return closingDelimiter
	}
	return notDelimiter
}
