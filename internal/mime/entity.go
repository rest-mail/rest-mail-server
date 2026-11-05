package mime

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	stdmime "mime"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
	"unsafe"

	gomessage "github.com/emersion/go-message"
)

// This file reads a message as github.com/emersion/go-message's Read and New do, and answers what
// they answer (TestEntityReadsAsGoMessageDoes), without their cost on what a sender chooses. That
// library grows a buffer a quarter at a time for each header field, keeps each field twice, and
// parses a part's Content-Type once to open it and again each time it's asked: a long field cost
// many times its size. Here a field is read once into a buffer every field shares, a part keeps
// only the few fields that are looked at, and each of those is parsed once.

// A header block has no limit of its own, only the message's. go-message refuses one past 1 MiB, but
// a message that isn't parsed is one no filter reads: a long field would hide the attachments, the
// threading fields and everything else a filter looks at from all of them (#1915).

// field is a header field, unfolded, under its canonical name.
type field struct{ key, value string }

// entity is a message or a part, opened: its decoded body, and what's read of its header.
type entity struct {
	body        io.Reader
	mediaType   string
	params      map[string]string
	typeFailed  bool
	disposition string
	dispParams  map[string]string
	contentID   string
}

// unknownCoding says an entity's transfer encoding or charset wasn't known. It can still be read,
// undecoded.
type unknownCoding struct{ err error }

func (e unknownCoding) Error() string { return "unknown coding: " + e.err.Error() }

// fieldReader reads header fields. Its one buffer is used again for every field of a part, and a
// message's own fields stay in it, so that together they cost one buffer of about their size.
type fieldReader struct {
	line []byte
	// chunk is how large the buffer was when it was last made.
	chunk int
	// sourceSize is the size of the whole message, when it is known.
	sourceSize int
	// wanted says which fields are kept, when not all of them are: a field nothing reads costs
	// nothing to read past, however long it is. Reading one field, decided says whether it's been
	// asked yet and skipping what it said.
	wanted            func(key []byte) bool
	decided, skipping bool
	// typeOnly says a part's fields are read only for whether it holds parts of its own. A
	// Content-Type that can't say so isn't parsed.
	typeOnly bool
}

// fieldBufferDoubles is as far as the buffer grows by doubling. A field longer than that is hostile
// or very nearly so, and the message's own size holds it with no more steps.
const fieldBufferDoubles = 32 << 10

func (r *fieldReader) room(need int) {
	if cap(r.line)-len(r.line) >= need {
		return
	}
	size := max(2*r.chunk, 2*len(r.line)+need, 1<<10)
	if size > fieldBufferDoubles {
		// With room left to read what follows the header into, which takeBuffer gives away.
		size = max(size, r.sourceSize+2*bytes.MinRead)
	}
	grown := make([]byte, len(r.line), size)
	copy(grown, r.line)
	r.line, r.chunk = grown, size
}

// takeBuffer hands over what's left of the buffer when it has at least size free, because a long
// field left it large and nothing is using it between fields. Nothing is written over what a field
// kept: that's before it. The reader makes itself a new one if it needs one.
func (r *fieldReader) takeBuffer(size int) []byte {
	if r == nil || cap(r.line) < size {
		return nil
	}
	taken := r.line[:0]
	r.line, r.chunk = nil, 0
	return taken
}

// readLine adds one line to the buffer, without its line break, unless the field is being skipped.
func (r *fieldReader) readLine(br *bufio.Reader) error {
	for {
		l, more, err := br.ReadLine()
		if !r.skipping {
			r.room(len(l))
			r.line = append(r.line, l...)
			if r.wanted != nil && !r.decided {
				if colon := bytes.IndexByte(r.line, ':'); colon >= 0 {
					r.decided = true
					if !r.wanted(bytes.Trim(r.line[:colon], " \t")) {
						r.skipping = true
						r.line = r.line[:colon+1]
					}
				}
			}
		}
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
}

// readField reads a field and its continuation lines into the buffer, each line ending in CRLF.
// Empty means the end of the header, for which an error isn't always one. A field wanted says no to
// is read past and comes back as its name and its colon alone.
func (r *fieldReader) readField(br *bufio.Reader) ([]byte, error) {
	r.line = r.line[:0]
	r.decided, r.skipping = false, false
	if err := r.readLine(br); err == io.EOF && len(r.line) == 0 {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if len(r.line) == 0 {
		return r.line, nil
	}
	r.addBreak()
	for continues(br) {
		if err := r.readLine(br); err != nil {
			break
		}
		r.addBreak()
	}
	return r.line, nil
}

func (r *fieldReader) addBreak() {
	if !r.skipping {
		r.room(2)
		r.line = append(r.line, '\r', '\n')
	}
}

func continues(br *bufio.Reader) bool {
	c, err := br.ReadByte()
	if err != nil {
		return false
	}
	_ = br.UnreadByte()
	return c == ' ' || c == '\t'
}

// each calls visit with each field's name and its value still folded, which visit may overwrite. It
// stops at the first blank line and at the first it can't read, which is the error.
func (r *fieldReader) each(br *bufio.Reader, visit func(key, value []byte)) error {
	if first, err := br.Peek(1); err == nil && (first[0] == ' ' || first[0] == '\t') {
		return errors.New("message: malformed MIME header initial line")
	}
	for {
		kv, err := r.readField(br)
		if len(kv) == 0 {
			return err
		}
		colon := bytes.IndexByte(kv, ':')
		if colon < 0 {
			return errors.New("message: malformed MIME header line")
		}
		key := bytes.Trim(kv[:colon], " \t")
		for _, c := range key {
			if c < 33 || c > 126 || c == ':' {
				return errors.New("message: malformed MIME header key")
			}
		}
		if len(key) == 0 {
			continue
		}
		visit(key, kv[colon+1:])
	}
}

// unfold is a value with its line breaks and the spaces around them gone, written over itself.
func unfold(value []byte) []byte {
	out := value[:0]
	for rest := value; ; {
		line := rest
		next := bytes.IndexByte(rest, '\n')
		if next >= 0 {
			line, rest = rest[:next], rest[next+1:]
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		line = bytes.Trim(line, " \t")
		if len(line) != 0 {
			if len(out) != 0 {
				out = append(out, ' ')
			}
			out = append(out, line...)
		}
		if next < 0 {
			return out
		}
	}
}

// readTopFields reads a message's header block. What's read past the block stays in the reader it
// answers, and headerSize says how long the block was.
func readTopFields(source io.Reader, sourceSize int) (_ []field, _ *bufio.Reader, _ *fieldReader, headerSize int64, _ error) {
	counted := &sourceCounter{source: source}
	br := bufio.NewReader(counted)
	reader := &fieldReader{sourceSize: sourceSize}
	var fields []field
	err := reader.each(br, func(key, value []byte) {
		view := unfold(value)
		fields = append(fields, field{textproto.CanonicalMIMEHeaderKey(string(key)), unsafe.String(unsafe.SliceData(view), len(view))})
		// What's read so far stays where it is, as the values' own octets, and the next field
		// starts after it. Nothing writes there again.
		reader.line = reader.line[len(reader.line):]
	})
	return fields, br, reader, counted.read - int64(br.Buffered()), err
}

// firstField is the value of the first field under a canonical name, or empty.
func firstField(fields []field, key string) string {
	for i := range fields {
		if fields[i].key == key {
			return fields[i].value
		}
	}
	return ""
}

// readTopForOpening reads only what opening a message needs from its header block, for a caller
// that has the rest of it already.
func readTopForOpening(source io.Reader, sourceSize int) (_ *bufio.Reader, _ *fieldReader, _ partFields, headerSize int64, _ error) {
	counted := &sourceCounter{source: source}
	br := bufio.NewReader(counted)
	reader := &fieldReader{sourceSize: sourceSize, wanted: isPartField}
	kept, err := reader.readPartFields(br)
	return br, reader, kept, counted.read - int64(br.Buffered()), err
}

// sourceCounter says how much of its source was read.
type sourceCounter struct {
	source io.Reader
	read   int64
}

func (c *sourceCounter) Read(p []byte) (int, error) {
	n, err := c.source.Read(p)
	c.read += int64(n)
	return n, err
}

// isPartField says whether readPartFields reads a field.
func isPartField(key []byte) bool {
	return bytes.EqualFold(key, []byte("Content-Type")) || bytes.EqualFold(key, []byte("Content-Disposition")) ||
		bytes.EqualFold(key, []byte("Content-Transfer-Encoding")) || bytes.EqualFold(key, []byte("Content-Id"))
}

func isContentType(key []byte) bool { return bytes.EqualFold(key, []byte("Content-Type")) }

// namesMultipart says whether parseMedia could answer a multipart type for a value: true of every
// value it does, as it lowers and trims the type before the first semicolon, or answers the value.
func namesMultipart(value []byte) bool {
	base, _, _ := bytes.Cut(value, []byte(";"))
	return bytes.HasPrefix(bytes.ToLower(bytes.TrimSpace(base)), []byte("multipart/"))
}

// partFields is what's read of a header when only opening the entity matters: each field's first
// occurrence, with its parameters already parsed.
type partFields struct {
	hasType     bool
	mediaType   string
	params      map[string]string
	typeFailed  bool
	hasDisp     bool
	disposition string
	dispParams  map[string]string
	hasEncoding bool
	encoding    string
	hasID       bool
	contentID   string
}

// readPartFields reads a header block and keeps only partFields of it. A value can be as long as
// its sender likes, and it's read in the shared buffer, so nothing that's kept may be a view of it.
func (r *fieldReader) readPartFields(br *bufio.Reader) (partFields, error) {
	var kept partFields
	err := r.each(br, func(key, value []byte) {
		switch {
		case bytes.EqualFold(key, []byte("Content-Type")):
			if !kept.hasType {
				kept.hasType = true
				view := unfold(value)
				if !r.typeOnly || namesMultipart(view) {
					kept.mediaType, kept.params, kept.typeFailed = parseContentType(unsafe.String(unsafe.SliceData(view), len(view)))
				}
			}
		case bytes.EqualFold(key, []byte("Content-Disposition")):
			if !kept.hasDisp {
				kept.hasDisp = true
				view := unfold(value)
				kept.disposition, kept.dispParams, _ = parseMedia(unsafe.String(unsafe.SliceData(view), len(view)))
			}
		case bytes.EqualFold(key, []byte("Content-Transfer-Encoding")):
			if !kept.hasEncoding {
				kept.hasEncoding, kept.encoding = true, string(unfold(value))
			}
		case bytes.EqualFold(key, []byte("Content-Id")):
			if !kept.hasID {
				kept.hasID, kept.contentID = true, string(unfold(value))
			}
		}
	})
	return kept, err
}

// open makes an entity of a body and its header, decoding its transfer encoding and, for text, its
// charset. It answers an error wrapping unknownCoding when it can't, with the entity readable.
func (kept partFields) open(body io.Reader) (*entity, error) {
	e := &entity{mediaType: "text/plain", disposition: kept.disposition, dispParams: kept.dispParams, contentID: kept.contentID}
	if kept.hasType {
		e.mediaType, e.params, e.typeFailed = kept.mediaType, kept.params, kept.typeFailed
	}
	var err error
	if !strings.HasPrefix(e.mediaType, "multipart/") {
		decoded, encodingErr := transferDecoder(kept.encoding, body)
		if encodingErr != nil {
			err = unknownCoding{encodingErr}
		} else {
			body = decoded
		}
	}
	if strings.HasPrefix(e.mediaType, "text/") {
		if charset, ok := e.params["charset"]; ok {
			converted, charsetErr := charsetDecoder(charset, body)
			if charsetErr != nil {
				err = unknownCoding{charsetErr}
			} else {
				body = converted
			}
		}
	}
	e.body = body
	return e, err
}

const maxCharsetName = 256

var errCharsetName = errors.New("message: unknown charset")

// transferDecoder is go-message's encodingReader.
func transferDecoder(encoding string, r io.Reader) (io.Reader, error) {
	switch strings.ToLower(encoding) {
	case "quoted-printable":
		return quotedprintable.NewReader(r), nil
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &spacesAsBreaks{r}), nil
	case "7bit", "8bit", "binary", "":
		return r, nil
	}
	return nil, fmt.Errorf("unhandled encoding %q", encoding)
}

// spacesAsBreaks lets a base64 body with its lines indented decode.
type spacesAsBreaks struct{ source io.Reader }

func (r *spacesAsBreaks) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	for i := 0; i < n; i++ {
		if p[i] == ' ' || p[i] == '\t' {
			p[i] = '\n'
		}
	}
	return n, err
}

// charsetDecoder is go-message's charsetReader.
func charsetDecoder(charset string, input io.Reader) (io.Reader, error) {
	charset = strings.ToLower(charset)
	if charset == "utf-8" || charset == "us-ascii" {
		return input, nil
	}
	// No charset has a name near this long, and refusing one quotes all of it in an error.
	if len(charset) > maxCharsetName {
		return nil, errCharsetName
	}
	if gomessage.CharsetReader != nil {
		return gomessage.CharsetReader(charset, input)
	}
	return input, fmt.Errorf("message: unhandled charset %q", charset)
}

// decodeWords is a header value with its RFC 2047 encoded-words decoded, or the value when it can't
// be, as go-message reads one.
func decodeWords(value string) string {
	decoder := stdmime.WordDecoder{CharsetReader: gomessage.CharsetReader}
	decoded, err := decoder.DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

// multipartReader reads the parts of a multipart. It's go-message's textproto.MultipartReader, in
// turn Go's mime/multipart, reading each part's header with fieldReader:
//
// Copyright 2010 The Go Authors. All rights reserved. Use of this source code is governed by a
// BSD-style license. Copyright (c) 2016 emersion, MIT.
type multipartReader struct {
	bufReader        *bufio.Reader
	fields           *fieldReader
	current          *multipartPart
	partsRead        int
	nl               []byte
	nlDashBoundary   []byte
	dashBoundaryDash []byte
	dashBoundary     []byte
}

// multipartPeek must be at least 76: \r\n--separator_of_len_70- would otherwise fill the buffer.
const multipartPeek = 4096

func newMultipartReader(r io.Reader, boundary string, fields *fieldReader) *multipartReader {
	if !fields.typeOnly {
		fields.wanted = isPartField
	}
	b := []byte("\r\n--" + boundary + "--")
	return &multipartReader{
		bufReader:        bufio.NewReaderSize(&stickyErrorReader{r: r}, multipartPeek),
		fields:           fields,
		nl:               b[:2],
		nlDashBoundary:   b[:len(b)-2],
		dashBoundaryDash: b[2:],
		dashBoundary:     b[2 : len(b)-2],
	}
}

// stickyErrorReader never reads again after an error.
type stickyErrorReader struct {
	r   io.Reader
	err error
}

func (r *stickyErrorReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	var n int
	n, r.err = r.r.Read(p)
	return n, r.err
}

// multipartPart reads the raw body of a part, up to the next boundary.
type multipartPart struct {
	mr      *multipartReader
	n       int
	total   int64
	err     error
	readErr error
}

func (p *multipartPart) Read(d []byte) (int, error) {
	br := p.mr.bufReader
	for p.n == 0 && p.err == nil {
		peek, _ := br.Peek(br.Buffered())
		p.n, p.err = scanUntilBoundary(peek, p.mr.dashBoundary, p.mr.nlDashBoundary, p.total, p.readErr)
		if p.n == 0 && p.err == nil {
			_, p.readErr = br.Peek(len(peek) + 1)
			if p.readErr == io.EOF {
				p.readErr = io.ErrUnexpectedEOF
			}
		}
	}
	if p.n == 0 {
		return 0, p.err
	}
	n, _ := br.Read(d[:min(len(d), p.n)])
	p.total += int64(n)
	p.n -= n
	if p.n == 0 {
		return n, p.err
	}
	return n, nil
}

func scanUntilBoundary(buf, dashBoundary, nlDashBoundary []byte, total int64, readErr error) (int, error) {
	if total == 0 {
		if bytes.HasPrefix(buf, dashBoundary) {
			switch matchAfterPrefix(buf, dashBoundary, readErr) {
			case -1:
				return len(dashBoundary), nil
			case 0:
				return 0, nil
			case +1:
				return 0, io.EOF
			}
		}
		if bytes.HasPrefix(dashBoundary, buf) {
			return 0, readErr
		}
	}
	if i := bytes.Index(buf, nlDashBoundary); i >= 0 {
		switch matchAfterPrefix(buf[i:], nlDashBoundary, readErr) {
		case -1:
			return i + len(nlDashBoundary), nil
		case 0:
			return i, nil
		case +1:
			return i, io.EOF
		}
	}
	if bytes.HasPrefix(nlDashBoundary, buf) {
		return 0, readErr
	}
	i := bytes.LastIndexByte(buf, nlDashBoundary[0])
	if i >= 0 && bytes.HasPrefix(nlDashBoundary, buf[i:]) {
		return i, nil
	}
	return len(buf), readErr
}

func matchAfterPrefix(buf, prefix []byte, readErr error) int {
	if len(buf) == len(prefix) {
		if readErr != nil {
			return +1
		}
		return 0
	}
	c := buf[len(prefix)]
	if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '-' {
		return +1
	}
	return -1
}

// nextPart opens the next part. The error is io.EOF when there are no more, and wraps unknownCoding
// when the part is readable but can't be decoded.
func (r *multipartReader) nextPart() (*entity, error) {
	if r.current != nil {
		_, _ = io.Copy(io.Discard, r.current)
	}
	if string(r.dashBoundary) == "--" {
		return nil, errors.New("multipart: boundary is empty")
	}
	expectNewPart := false
	for {
		line, err := r.bufReader.ReadSlice('\n')
		if err == io.EOF && r.isFinalBoundary(line) {
			return nil, io.EOF
		}
		if err != nil {
			return nil, fmt.Errorf("multipart: NextPart: %w", err)
		}
		if r.isBoundaryDelimiterLine(line) {
			r.partsRead++
			kept, err := r.fields.readPartFields(r.bufReader)
			if err != nil {
				return nil, err
			}
			r.current = &multipartPart{mr: r}
			return kept.open(r.current)
		}
		if r.isFinalBoundary(line) {
			return nil, io.EOF
		}
		if expectNewPart {
			return nil, errors.New("multipart: expecting a new Part")
		}
		if r.partsRead == 0 {
			continue
		}
		if bytes.Equal(line, r.nl) {
			expectNewPart = true
			continue
		}
		return nil, errors.New("multipart: unexpected line in Next()")
	}
}

func (r *multipartReader) isFinalBoundary(line []byte) bool {
	if !bytes.HasPrefix(line, r.dashBoundaryDash) {
		return false
	}
	rest := bytes.TrimLeft(line[len(r.dashBoundaryDash):], " \t")
	return len(rest) == 0 || bytes.Equal(rest, r.nl)
}

func (r *multipartReader) isBoundaryDelimiterLine(line []byte) bool {
	if !bytes.HasPrefix(line, r.dashBoundary) {
		return false
	}
	rest := bytes.TrimLeft(line[len(r.dashBoundary):], " \t")
	if r.partsRead == 0 && len(rest) == 1 && rest[0] == '\n' {
		r.nl = r.nl[1:]
		r.nlDashBoundary = r.nlDashBoundary[1:]
	}
	return bytes.Equal(rest, r.nl)
}

// multipart answers a reader of the entity's parts, or nil when it has none.
func (e *entity) multipart(fields *fieldReader) *multipartReader {
	if !strings.HasPrefix(e.mediaType, "multipart/") {
		return nil
	}
	return newMultipartReader(e.body, e.params["boundary"], fields)
}

// filename is the part's filename: the Content-Disposition "filename" parameter or else the
// Content-Type "name" parameter, with no line break left in it.
func (e *entity) filename() string {
	if name := e.dispParams["filename"]; name != "" {
		return sanitizeHeaderValue(name)
	}
	if !e.typeFailed {
		if name := e.params["name"]; name != "" {
			return sanitizeHeaderValue(name)
		}
	}
	return ""
}
