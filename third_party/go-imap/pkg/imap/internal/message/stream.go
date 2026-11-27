package message

import (
	"bytes"
	"io"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// SpanKind is which part of the whole message a [SpanItem] sends.
type SpanKind int

const (
	SpanWhole  SpanKind = iota // the message: BODY[], RFC822, BINARY[]
	SpanHeader                 // its header block: BODY[HEADER], RFC822.HEADER
	SpanText                   // what follows it: BODY[TEXT], RFC822.TEXT
)

// SpanItem is a FETCH item answered with one run of the message's octets, so the
// server can copy it from a reader instead of holding the message (#2107). Only
// items that need no MIME parse are spans; ParseSpanItem says which.
type SpanItem struct {
	name    string // the response item's name, partial not yet applied
	kind    SpanKind
	partial *wire.BodyPartial
	// Peek is set for an item that leaves \Seen alone.
	Peek bool
	// Binary is set for BINARY, BINARY.PEEK and BINARY.SIZE, which send the stored
	// octets (RFC 3516); the others send the octets FETCH sends (WireForm), which
	// are the stored ones only when [Scan.EightBit] is false.
	Binary bool
	// Size is set for BINARY.SIZE, answered with the count alone.
	Size bool
}

// ParseSpanItem is item as a span, and whether it is one. It takes exactly the
// items fetchResponse answers with BodySection or BinarySection on the whole
// message, its header or its text, and RFC822, RFC822.HEADER and RFC822.TEXT;
// naming, peek and partial are those functions' own, so a span sends what they
// send. Any other item, or one with a partial BodySection refuses, is not a span.
func ParseSpanItem(item string) (SpanItem, bool) {
	up := strings.ToUpper(item)
	switch up {
	case "RFC822":
		return SpanItem{name: "RFC822", kind: SpanWhole}, true
	case "RFC822.HEADER":
		return SpanItem{name: "RFC822.HEADER", kind: SpanHeader, Peek: true}, true
	case "RFC822.TEXT":
		return SpanItem{name: "RFC822.TEXT", kind: SpanText}, true
	}
	if wire.BinaryItem(up) {
		// As BinarySection reads it.
		lb, rb := strings.IndexByte(item, '['), strings.IndexByte(item, ']')
		if lb < 0 || rb < lb || item[lb+1:rb] != "" {
			return SpanItem{}, false
		}
		partial, _ := wire.ParseBodyPartial(item[rb+1:])
		size := strings.HasPrefix(up, "BINARY.SIZE[")
		name := "BINARY[]"
		if size {
			name = "BINARY.SIZE[]"
		}
		return SpanItem{name: name, kind: SpanWhole, partial: partial,
			Peek: size || strings.HasPrefix(up, "BINARY.PEEK["), Binary: true, Size: size}, true
	}
	if !strings.HasPrefix(up, "BODY[") && !strings.HasPrefix(up, "BODY.PEEK[") {
		return SpanItem{}, false
	}
	// As BodySection reads it.
	lb, rb := strings.IndexByte(item, '['), strings.LastIndexByte(item, ']')
	if lb < 0 || rb < 0 || rb < lb {
		return SpanItem{}, false
	}
	partial, ok := wire.ParseBodyPartial(item[rb+1:])
	if !ok {
		return SpanItem{}, false
	}
	it := SpanItem{partial: partial, Peek: strings.HasPrefix(up, "BODY.PEEK[")}
	switch strings.ToUpper(item[lb+1 : rb]) {
	case "":
		it.name, it.kind = "BODY[]", SpanWhole
	case "HEADER":
		it.name, it.kind = "BODY[HEADER]", SpanHeader
	case "TEXT":
		it.name, it.kind = "BODY[TEXT]", SpanText
	default:
		return SpanItem{}, false
	}
	return it, true
}

// Range is the response item's name and the run of the message it sends, as
// start and count octets: the section's, partial applied as partialOctets
// applies it. size is the message's length and headerEnd its [Scan.HeaderEnd].
func (it SpanItem) Range(size, headerEnd int64) (name string, start, count int64) {
	switch it.kind {
	case SpanHeader:
		start, count = 0, headerEnd
	case SpanText:
		start, count = headerEnd, size-headerEnd
	default:
		start, count = 0, size
	}
	name = it.name
	if it.partial != nil {
		from, n := partialRange(count, it.partial.Start, it.partial.Count)
		start, count = start+from, n
		name = name + "<" + uitoa(it.partial.Start) + ">"
	}
	return name, start, count
}

// partialRange is partialOctets of a section length octets long, as the offset
// into it and the count it keeps.
func partialRange(length int64, start, count uint32) (from, n int64) {
	if uint64(start) >= uint64(length) {
		return 0, 0
	}
	from = int64(start)
	n = length - from
	if int64(count) < n {
		n = int64(count)
	}
	return from, n
}

func uitoa(v uint32) string {
	var b [10]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
		if v == 0 {
			return string(b[i:])
		}
	}
}

// Scan reads a message's octets in order, in pieces of any size, and finds what
// sending spans of it needs, holding nothing that grows with the message.
type Scan struct {
	n int64
	// eightBit is whether an octet is 8-bit or NUL: needsWireForm of the message.
	eightBit bool
	nul      bool
	// headerEnd is headerEnd of the message once found, -1 until then; match is how
	// much of "\r\n\r\n" the octets read so far end with.
	headerEnd int64
	match     int
}

// NewScan is a Scan of a message not yet read.
func NewScan() *Scan { return &Scan{headerEnd: -1} }

// Write takes the message's next octets. It never fails.
func (s *Scan) Write(p []byte) (int, error) {
	i := 0
	for ; i < len(p) && s.headerEnd < 0; i++ {
		c := p[i]
		s.octet(c)
		switch {
		case c == '\r' && s.match == 2:
			s.match = 3
		case c == '\r':
			s.match = 1
		case c == '\n' && s.match == 1:
			s.match = 2
			if s.n+int64(i) == 1 { // the message starts with a blank line
				s.headerEnd = 2
			}
		case c == '\n' && s.match == 3:
			s.headerEnd = s.n + int64(i) + 1
		default:
			s.match = 0
		}
	}
	if !s.eightBit || !s.nul {
		for _, c := range p[i:] {
			s.octet(c)
		}
	}
	s.n += int64(len(p))
	return len(p), nil
}

func (s *Scan) octet(c byte) {
	if c >= 0x80 || c == 0 {
		s.eightBit = true
		s.nul = s.nul || c == 0
	}
}

// Size is how many octets were read.
func (s *Scan) Size() int64 { return s.n }

// EightBit is whether the message holds an 8-bit octet or a NUL. Without one,
// WireForm sends it as stored, so every BODY and RFC822 span is a span of the
// stored octets.
func (s *Scan) EightBit() bool { return s.eightBit }

// NUL is whether the message holds a NUL.
func (s *Scan) NUL() bool { return s.nul }

// HeaderEnd is headerEnd of the octets read: the length of the header block,
// blank line included.
func (s *Scan) HeaderEnd() int64 {
	if s.headerEnd < 0 {
		return s.n
	}
	return s.headerEnd
}

// HasNUL is whether the count octets of r at start hold a NUL, read through buf.
func HasNUL(r io.ReaderAt, start, count int64, buf []byte) (bool, error) {
	sr := io.NewSectionReader(r, start, count)
	for {
		n, err := sr.Read(buf)
		if bytes.IndexByte(buf[:n], 0) >= 0 {
			return true, nil
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}
