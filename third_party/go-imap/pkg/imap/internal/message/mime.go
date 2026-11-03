package message

import (
	"bufio"
	"net/textproto"
	"strings"
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

// mediaType returns the lower-case Content-Type and its parameters, applying
// the same encoding override, defaults and depth limit as partStructure so
// sections match BODYSTRUCTURE.
func (n mimeNode) mediaType() (string, map[string]string) {
	h, _ := textproto.NewReader(bufio.NewReader(strings.NewReader(n.header))).ReadMIMEHeader()
	if !knownTransferEncoding(h.Get("Content-Transfer-Encoding")) {
		return "application/octet-stream", map[string]string{}
	}
	mt, params, err := parseContentType(h.Get("Content-Type"))
	if !usableContentType(mt, params, err) {
		mt, params = n.defaultType, map[string]string{}
	}
	if opaqueEntity(mt, params, h.Get("Content-Transfer-Encoding"), n.depth) {
		return "application/octet-stream", map[string]string{}
	}
	return mt, params
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
	delim := "--" + boundary
	// next finds the next delimiter line at or after from. It returns the
	// offset of the delimiter, the offset just after its line and whether it
	// is the close delimiter.
	next := func(from int) (at, after int, closing, found bool) {
		for from <= len(body) {
			i := strings.Index(body[from:], delim)
			if i < 0 {
				return 0, 0, false, false
			}
			at = from + i
			from = at + 1
			if at != 0 && (at < 2 || body[at-2:at] != "\r\n") {
				continue
			}
			rest := body[at+len(delim):]
			closing = strings.HasPrefix(rest, "--")
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
			after = len(body) - len(rest) + len(line)
			if eol >= 0 {
				after += 2
			}
			return at, after, closing, true
		}
		return 0, 0, false, false
	}

	var parts []string
	_, start, closing, found := next(0)
	for found && !closing {
		at, after, c, ok := next(start)
		if !ok {
			parts = append(parts, body[start:])
			break
		}
		end := at
		if end >= 2 {
			end -= 2 // the CRLF before the delimiter is part of it
		}
		if end < start {
			end = start
		}
		parts = append(parts, body[start:end])
		start, closing = after, c
	}
	return parts
}
