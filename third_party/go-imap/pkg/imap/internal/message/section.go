package message

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// filterHeaders extracts only the requested headers from a raw RFC 2822 message
// (the BODY[HEADER.FIELDS (...)] section), terminated by a blank line.
func filterHeaders(raw string, fields []string) string {
	return selectHeaders(raw, fields, false)
}

// selectHeaders returns header lines from raw's RFC 5322 header block, terminated
// by the blank line that ends it when raw has one: a message with no body and no
// blank line answers without it (RFC 3501 §6.4.5). Field-name matching is
// case-insensitive. With exclude=false only headers whose name appears in fields
// are kept (HEADER.FIELDS); with exclude=true every header EXCEPT those in fields
// is kept (HEADER.FIELDS.NOT, RFC 3501 §6.4.5). A folded header — one whose value
// continues on following lines beginning with SP or HT (RFC 5322 §2.2.3) — is kept
// or dropped as a whole together with its parent header, so folded values are
// returned in full rather than truncated to their first line.
func selectHeaders(raw string, fields []string, exclude bool) string {
	end, blank := headerEnd(raw)

	// Build a set of field names to match (case-insensitive).
	set := make(map[string]bool, len(fields))
	for _, f := range fields {
		set[strings.ToLower(f)] = true
	}

	var result strings.Builder
	keep := false // whether the header currently being read is selected
	for _, line := range strings.Split(raw[:end], "\r\n") {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			// Continuation of the previous (folded) header value: keep or drop
			// it with the header it belongs to.
			if keep {
				result.WriteString(line + "\r\n")
			}
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			keep = false
			continue
		}
		name := strings.ToLower(strings.TrimSpace(line[:colonIdx]))
		// exclude=false: keep when listed; exclude=true: keep when NOT listed.
		keep = set[name] != exclude
		if keep {
			result.WriteString(line + "\r\n")
		}
	}
	if blank {
		result.WriteString("\r\n")
	}
	return result.String()
}

// HeaderFieldValues returns the unfolded text after the colon of every header
// field in raw's header block whose name matches name case-insensitively, in
// order. Unfolding removes only the CRLF before a continuation line, keeping its
// leading whitespace (RFC 5322 §2.2.3).
func HeaderFieldValues(raw, name string) []string {
	var values []string
	for _, line := range strings.Split(selectHeaders(raw, []string{name}, false), "\r\n") {
		switch {
		case line == "":
		case line[0] == ' ' || line[0] == '\t':
			if len(values) > 0 {
				values[len(values)-1] += line
			}
		default:
			values = append(values, line[strings.Index(line, ":")+1:])
		}
	}
	return values
}

// headerEnd is the length of raw's header block, including the blank line that
// ends it, and whether raw has that line. Raw starting with a blank line has no
// header fields; raw with no blank line is header fields only, with no body.
func headerEnd(raw string) (end int, blank bool) {
	if strings.HasPrefix(raw, "\r\n") {
		return 2, true
	}
	if i := strings.Index(raw, "\r\n\r\n"); i >= 0 {
		return i + 4, true
	}
	return len(raw), false
}

// HeaderSection returns the RFC 2822 header block of raw, including the blank
// line that terminates it when there is one.
func HeaderSection(raw string) string {
	end, _ := headerEnd(raw)
	return raw[:end]
}

// TextSection returns the body of raw (everything after the header-terminating
// blank line), or "" if there is none.
func TextSection(raw string) string {
	end, _ := headerEnd(raw)
	return raw[end:]
}

// BodySection answers a BODY[...] / BODY.PEEK[...] fetch item. It returns the
// response item name (always the non-PEEK BODY[...] form, per RFC 3501 §7.4.2),
// the octet payload, and whether the request was a peek (a peek must not set
// \Seen). ok is false when the body could not be loaded or the item is
// malformed. loadRaw fetches the raw message lazily, so callers pay for the body
// only when a section actually needs it.
func BodySection(item string, loadRaw func() (string, bool)) (name, payload string, peek, ok bool) {
	up := strings.ToUpper(item)
	peek = strings.HasPrefix(up, "BODY.PEEK[")
	lb := strings.IndexByte(item, '[')
	rb := strings.LastIndexByte(item, ']')
	if lb < 0 || rb < 0 || rb < lb {
		return "", "", peek, false
	}
	section := strings.ToUpper(item[lb+1 : rb])

	// A body item may carry a "<start.count>" partial specifier after the section
	// bracket (RFC 3501 §6.4.5). Parse it before loading the body so a malformed
	// specifier is rejected without paying for a fetch.
	partial, pok := wire.ParseBodyPartial(item[rb+1:])
	if !pok {
		return "", "", peek, false
	}

	raw, rok := loadRaw()
	if !rok {
		return "", "", peek, false
	}

	switch {
	case section == "":
		name, payload = "BODY[]", raw
	case section == "HEADER":
		name, payload = "BODY[HEADER]", HeaderSection(raw)
	case section == "TEXT":
		name, payload = "BODY[TEXT]", TextSection(raw)
	case strings.HasPrefix(section, "HEADER.FIELDS"):
		fields, wire, exclude, valid := wire.ParseHeaderFields(item[lb+1 : rb])
		if !valid {
			return "", "", peek, false
		}
		label := "HEADER.FIELDS"
		if exclude {
			label += ".NOT"
		}
		name = "BODY[" + label + " (" + strings.Join(wire, " ") + ")]"
		payload = selectHeaders(raw, fields, exclude)
	default:
		path, text, valid := wire.SplitSectionPart(item[lb+1 : rb])
		if !valid || len(path) == 0 {
			return "", "", peek, false
		}
		label, part, valid := numericSection(raw, path, text)
		if !valid {
			return "", "", peek, false
		}
		name, payload = "BODY["+label+"]", part
	}

	// A partial fetch returns only the requested octets and labels the response
	// item with the origin octet — "BODY[section]<start>", the start ONLY, never
	// the count (RFC 3501 §7.4.2).
	if partial != nil {
		payload = partialOctets(payload, partial.Start, partial.Count)
		name = fmt.Sprintf("%s<%d>", name, partial.Start)
	}

	return name, payload, peek, true
}

// partialOctets returns at most count octets of s beginning at zero-based octet
// start (RFC 3501 §6.4.5): fewer if the section is shorter, empty if start is at
// or past the end. Compare before converting to int, including on 32-bit hosts.
func partialOctets(s string, start, count uint32) string {
	if uint64(start) >= uint64(len(s)) {
		return ""
	}
	s = s[int(start):]
	if uint64(count) < uint64(len(s)) {
		s = s[:int(count)]
	}
	return s
}

// numericSection answers a section-spec with a non-empty section-part. text
// is the original-case section-text after the part path. It returns the
// response section label and the payload; a part that does not exist, and a
// HEADER/TEXT/HEADER.FIELDS on a part that is not MESSAGE/RFC822, yield an
// empty payload. ok is false only for an invalid section-text.
func numericSection(raw string, path []uint32, text string) (label, payload string, ok bool) {
	parts := make([]string, len(path))
	for i, k := range path {
		parts[i] = strconv.FormatUint(uint64(k), 10)
	}
	label = strings.Join(parts, ".")

	node, found := messageNode(raw, 0), true
	for _, k := range path {
		if node, found = node.child(k); !found {
			break
		}
	}
	encapsulated := func() (mimeNode, bool) {
		if !found {
			return mimeNode{}, false
		}
		mt, _ := node.mediaType()
		return messageNode(node.body, node.depth+1), mt == "message/rfc822"
	}

	up := strings.ToUpper(text)
	switch up {
	case "":
		if found {
			payload = node.body
		}
		return label, payload, true
	case "MIME":
		if found {
			payload = node.header
		}
		return label + ".MIME", payload, true
	case "HEADER", "TEXT":
		if m, isMsg := encapsulated(); isMsg {
			payload = m.header
			if up == "TEXT" {
				payload = m.body
			}
		}
		return label + "." + up, payload, true
	}
	fields, wire, exclude, valid := wire.ParseHeaderFields(text)
	if !valid {
		return "", "", false
	}
	name := "HEADER.FIELDS"
	if exclude {
		name += ".NOT"
	}
	if _, isMsg := encapsulated(); isMsg {
		payload = selectHeaders(node.body, fields, exclude)
	}
	return label + "." + name + " (" + strings.Join(wire, " ") + ")", payload, true
}

// HeaderHas8bit reports whether the header section of msg, everything before
// the first empty line, carries an octet with the high bit set. RFC 6855 §4
// requires a UTF8=ACCEPT server to answer NO to an APPEND of such a message
// until the client has issued ENABLE UTF8=ACCEPT.
func HeaderHas8bit(msg []byte) bool {
	for len(msg) > 0 {
		line := msg
		next := len(msg)
		if i := bytes.IndexByte(msg, '\n'); i >= 0 {
			line, next = msg[:i], i+1
		}
		if len(bytes.TrimSuffix(line, []byte("\r"))) == 0 {
			return false // the empty line that ends the header section
		}
		for _, c := range line {
			if c >= 0x80 {
				return true
			}
		}
		msg = msg[next:]
	}
	return false
}
