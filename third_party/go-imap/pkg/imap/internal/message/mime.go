package message

import (
	"bufio"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
)

// mimeNode is one entity on a numeric section path: either an RFC 5322
// message (the top-level message or a MESSAGE/RFC822 encapsulation) or a MIME
// body part. header includes its terminating blank line.
type mimeNode struct {
	header, body string
	message      bool
	defaultType  string // used when Content-Type is absent or invalid
	depth        int    // MIME nesting depth, see maxMIMEDepth
}

// maxMIMEDepth bounds MIME nesting for BODYSTRUCTURE, section FETCH and
// SEARCH alike. The top-level message is depth 0; each multipart part and each
// MESSAGE/RFC822 encapsulation adds one. A multipart or MESSAGE/RFC822 entity
// at this depth is an opaque application/octet-stream leaf, so a deeply
// nested message cannot make one FETCH or SEARCH do unbounded work.
const maxMIMEDepth = 64

func messageNode(raw string, depth int) mimeNode {
	return mimeNode{header: HeaderSection(raw), body: TextSection(raw), message: true, defaultType: "text/plain", depth: depth}
}

// entityReaders holds the buffers entityHeader reads with. net/mail keeps a reader
// that is already buffered, and would allocate a full buffer for any other, so a
// message of many small parts shares these instead of allocating one for each.
var entityReaders = sync.Pool{New: func() any { return bufio.NewReader(nil) }}

// entityHeader reads a MIME entity's header block (with or without its
// terminating blank line) with net/mail's RFC 5322 reader. Unlike
// net/textproto, it accepts the control octets RFC 5322 §4.2 keeps as
// obs-utext, so a part whose header holds one is still described by its own
// fields. A header block net/mail cannot read has no trustworthy fields and is
// read as empty. BODYSTRUCTURE, sections and SEARCH all read part headers
// here, so they agree on each part's type.
func entityHeader(header string) textproto.MIMEHeader {
	r := entityReaders.Get().(*bufio.Reader)
	defer func() {
		r.Reset(nil)
		entityReaders.Put(r)
	}()
	r.Reset(strings.NewReader(header))
	m, err := mail.ReadMessage(r)
	if err != nil {
		return textproto.MIMEHeader{}
	}
	return textproto.MIMEHeader(m.Header)
}

// mediaType returns the lower-case Content-Type and its parameters, applying
// the same encoding override, defaults and depth limit as partStructure so
// sections match BODYSTRUCTURE.
func (n mimeNode) mediaType() (string, map[string]string) {
	var contentType, encoding string
	if namesContentField(n.header) {
		h := entityHeader(n.header)
		contentType, encoding = h.Get("Content-Type"), h.Get("Content-Transfer-Encoding")
	}
	if !knownTransferEncoding(encoding) {
		return "application/octet-stream", map[string]string{}
	}
	var mt string
	var params map[string]string
	usable := false
	if contentType != "" {
		var err error
		mt, params, err = parseContentType(contentType)
		usable = usableContentType(mt, params, err)
	}
	if !usable {
		mt, params = n.defaultType, noParameters
	}
	if opaqueEntity(mt, params, encoding, n.depth) {
		return "application/octet-stream", noParameters
	}
	return mt, params
}

// noParameters is what an entity with no usable Content-Type has. Nothing writes to it.
var noParameters = map[string]string{}

// namesContentField reports whether a header block may hold a Content-Type or a
// Content-Transfer-Encoding, so a part with neither doesn't pay to parse its header.
func namesContentField(header string) bool {
	const prefix = "content-t"
	for i := 0; i+len(prefix) <= len(header); i++ {
		if header[i]|0x20 != 'c' {
			continue
		}
		j := 1
		for j < len(prefix) && header[i+j]|0x20 == prefix[j]|0x20 {
			j++
		}
		if j == len(prefix) {
			return true
		}
	}
	return false
}

// usableContentType reports whether a parsed Content-Type field describes the
// entity. RFC 2045 §5.1 requires type/subtype (ParseMediaType also accepts a
// bare token), and RFC 2046 §5.1.1 makes the boundary parameter mandatory for
// multipart, so a multipart without one cannot be split into parts. RFC 2045
// §5.2 gives unusable fields the default type instead of inventing, for example,
// application/plain or a leaf "MULTIPART" body.
func usableContentType(mt string, params map[string]string, err error) bool {
	if err != nil || !strings.Contains(mt, "/") {
		return false
	}
	return !strings.HasPrefix(mt, "multipart/") || params["boundary"] != ""
}

// opaqueEntity reports whether an entity with lower-case media type mt is
// described as one opaque application/octet-stream leaf instead of being
// parsed: a multipart or MESSAGE/RFC822 entity at maxMIMEDepth, or a
// MESSAGE/RFC822 entity whose Content-Transfer-Encoding is not 7bit, 8bit or
// binary. RFC 2046 §5.2.1 permits no other encoding there, and an encoded body
// is not a message that can be parsed as stored. BODYSTRUCTURE, sections and
// SEARCH all use this, so they agree on where the structure stops.
func opaqueEntity(mt string, params map[string]string, cte string, depth int) bool {
	if mt == "message/rfc822" {
		switch transferEncoding(cte) {
		case "7BIT", "8BIT", "BINARY":
		default:
			return true
		}
	}
	nests := (strings.HasPrefix(mt, "multipart/") && params["boundary"] != "") || mt == "message/rfc822"
	return nests && depth >= maxMIMEDepth
}

// child returns part k (1-based) of n (RFC 3501 §6.4.5): a multipart's parts
// are numbered from 1; a non-multipart message has only part 1, its body; the
// part numbers of a MESSAGE/RFC822 part refer to the encapsulated message.
func (n mimeNode) child(k uint32) (mimeNode, bool) {
	mt, params := n.mediaType()
	if !n.message && mt == "message/rfc822" {
		n = messageNode(n.body, n.depth+1)
		mt, params = n.mediaType()
	}
	if strings.HasPrefix(mt, "multipart/") && params["boundary"] != "" {
		parts := multipartParts(n.body, params["boundary"])
		if uint64(k) > uint64(len(parts)) {
			return mimeNode{}, false
		}
		def := "text/plain"
		if mt == "multipart/digest" {
			def = "message/rfc822"
		}
		h, b := splitEntity(parts[k-1])
		return mimeNode{header: h, body: b, defaultType: def, depth: n.depth + 1}, true
	}
	if n.message && k == 1 {
		return mimeNode{header: n.header, body: n.body, defaultType: n.defaultType, depth: n.depth}, true
	}
	return mimeNode{}, false
}

// splitEntity splits a MIME entity into its header (with the terminating blank
// line) and body. An entity starting with a blank line has no header fields.
func splitEntity(raw string) (header, body string) {
	return HeaderSection(raw), TextSection(raw)
}

// multipartParts returns the raw body parts (header and body) of a multipart
// body (RFC 2046 §5.1.1). A delimiter is "--boundary" at the start of a line,
// followed only by optional linear whitespace; the CRLF before it belongs to
// the delimiter. Preamble and epilogue are skipped; an unterminated last part
// runs to the end of the body.
func multipartParts(body, boundary string) []string {
	spans := multipartSpans(body, boundary)
	parts := make([]string, len(spans))
	for i, span := range spans {
		parts[i] = body[span[0]:span[1]]
	}
	return parts
}

// multipartSpans returns where each part of a multipart body starts and ends.
func multipartSpans(body, boundary string) [][2]int {
	return partSpans(body, multipartDelimiters(body, boundary))
}

// multipartDelimiter is one delimiter line: where "--boundary" starts, where its line
// ends, and whether it is the close delimiter.
type multipartDelimiter struct {
	at, after int
	closing   bool
}

// multipartDelimiters finds each delimiter line of a multipart body, up to and
// including the close delimiter.
func multipartDelimiters(body, boundary string) []multipartDelimiter {
	delim := "--" + boundary
	// next finds the next delimiter line at or after from.
	next := func(from int) (multipartDelimiter, bool) {
		for from <= len(body) {
			i := strings.Index(body[from:], delim)
			if i < 0 {
				return multipartDelimiter{}, false
			}
			at := from + i
			from = at + 1
			if at != 0 && (at < 2 || body[at-2:at] != "\r\n") {
				continue
			}
			rest := body[at+len(delim):]
			closing := strings.HasPrefix(rest, "--")
			if closing {
				rest = rest[2:]
			}
			eol := strings.Index(rest, "\r\n")
			line := rest
			if eol >= 0 {
				line = rest[:eol]
			}
			if strings.Trim(line, " \t") != "" {
				continue
			}
			after := len(body) - len(rest) + len(line)
			if eol >= 0 {
				after += 2
			}
			return multipartDelimiter{at, after, closing}, true
		}
		return multipartDelimiter{}, false
	}

	delimiters := make([]multipartDelimiter, 0, strings.Count(body, delim))
	for from := 0; ; {
		d, found := next(from)
		if !found {
			return delimiters
		}
		delimiters = append(delimiters, d)
		if d.closing {
			return delimiters
		}
		from = d.after
	}
}

// partSpans returns where the part after each delimiter that isn't the close one
// starts and ends. The CRLF before a delimiter is part of it, and an unterminated last
// part runs to the end of the body.
func partSpans(body string, delimiters []multipartDelimiter) [][2]int {
	spans := make([][2]int, 0, len(delimiters))
	for i, d := range delimiters {
		if d.closing {
			break
		}
		end := len(body)
		if i+1 < len(delimiters) {
			end = delimiters[i+1].at
			if end >= 2 {
				end -= 2
			}
			if end < d.after {
				end = d.after
			}
		}
		spans = append(spans, [2]int{d.after, end})
	}
	return spans
}
