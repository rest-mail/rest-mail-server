package wire

import (
	"strconv"
	"strings"
)

// ParseHeaderFields parses the complete RFC 3501 §9 section-msgtext form
// HEADER.FIELDS[.NOT] SP header-list. Literal arguments have already been
// converted to quoted strings by the server's literal reader. Keep decoded
// names separate from their wire tokens so quoted names match and response
// labels stay valid.
func ParseHeaderFields(section string) (fields, wire []string, exclude, ok bool) {
	for i := range len(section) {
		if c := section[i]; c == 0 || c == '\r' || c == '\n' || c >= 0x7f {
			return nil, nil, false, false
		}
	}
	rest := section
	up := strings.ToUpper(rest)
	switch {
	case strings.HasPrefix(up, "HEADER.FIELDS.NOT "):
		rest, exclude = rest[len("HEADER.FIELDS.NOT "):], true
	case strings.HasPrefix(up, "HEADER.FIELDS "):
		rest = rest[len("HEADER.FIELDS "):]
	default:
		return nil, nil, false, false
	}
	if len(rest) < 3 || rest[0] != '(' || rest[len(rest)-1] != ')' {
		return nil, nil, false, false
	}
	rest = rest[1 : len(rest)-1]
	for rest != "" {
		end := strings.IndexByte(rest, ' ')
		if rest[0] == '"' {
			end = QuotedStringEnd(rest)
			if end < 0 {
				return nil, nil, false, false
			}
			end++
		} else if end < 0 {
			end = len(rest)
		}
		token := rest[:end]
		if !ValidAstring(token) {
			return nil, nil, false, false
		}
		fields = append(fields, Unquote(token))
		wire = append(wire, token)
		rest = rest[end:]
		if rest == "" {
			return fields, wire, exclude, true
		}
		var sep bool
		rest, sep = CutSingleSP(rest)
		if !sep || rest == "" {
			return nil, nil, false, false
		}
	}
	return nil, nil, false, false
}

// ParseFetchItems validates the outer RFC 3501 §9 FETCH argument grammar and
// known data-item names before dispatch. Numeric BODY section semantics remain
// separate; bracket/quote framing is checked here so section
// spaces cannot be mistaken for separators between top-level items.
func ParseFetchItems(s string) ([]string, bool) {
	// FETCH names and header-field names are ASCII. Check before case folding:
	// Unicode look-alikes must not turn into a recognized macro or item.
	for i := range len(s) {
		if s[i] == 0 || s[i] == '\r' || s[i] == '\n' || s[i] >= 0x7f {
			return nil, false
		}
	}
	if s == "" {
		return nil, false
	}
	list := s[0] == '('
	if list {
		if len(s) < 3 || s[len(s)-1] != ')' {
			return nil, false
		}
		s = s[1 : len(s)-1]
	} else {
		switch strings.ToUpper(s) {
		case "ALL", "FAST", "FULL":
			return expandFetchMacros([]string{s}), true
		}
	}

	var tokens []string
	start, bracket, parens := 0, false, 0
	sectionSeen := false
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' && !bracket {
			if bracket || parens != 0 || i == start {
				return nil, false
			}
			item := s[start:i]
			switch up := strings.ToUpper(item); up {
			case "FLAGS", "UID", "INTERNALDATE", "ENVELOPE", "BODY", "BODYSTRUCTURE",
				"RFC822", "RFC822.HEADER", "RFC822.TEXT", "RFC822.SIZE":
			default:
				if !strings.HasPrefix(up, "BODY[") && !strings.HasPrefix(up, "BODY.PEEK[") && !BinaryItem(up) {
					return nil, false
				}
			}
			tokens = append(tokens, item)
			start = i + 1
			sectionSeen = false
			continue
		}
		switch c := s[i]; c {
		case '[':
			if parens != 0 {
				continue
			}
			if sectionSeen {
				return nil, false
			}
			bracket, sectionSeen = true, true
		case ']':
			if parens != 0 {
				continue
			}
			if !bracket {
				return nil, false
			}
			bracket = false
		case '(':
			if !bracket || parens != 0 {
				return nil, false
			}
			parens++
		case ')':
			if !bracket || parens != 1 {
				return nil, false
			}
			parens--
		case '"':
			if !bracket || parens != 1 {
				return nil, false
			}
			end := QuotedStringEnd(s[i:])
			if end < 0 {
				return nil, false
			}
			i += end
		default:
			if c < ' ' || c >= 0x7f {
				return nil, false
			}
		}
	}
	if !list && len(tokens) != 1 || !validFetchSections(tokens) {
		return nil, false
	}
	return tokens, true
}

// expandFetchMacros replaces an RFC 3501 §6.4.5 FETCH macro with its component
// data items. A macro (ALL, FAST, FULL) is only meaningful as the sole data item
// — the grammar makes it an alternative to a data item or a parenthesized list,
// not a member of one — so expansion applies only when tokens is exactly one
// macro word; any other token list is returned unchanged.
//
//	ALL  = FLAGS INTERNALDATE RFC822.SIZE ENVELOPE
//	FAST = FLAGS INTERNALDATE RFC822.SIZE
//	FULL = FLAGS INTERNALDATE RFC822.SIZE ENVELOPE BODY
func expandFetchMacros(tokens []string) []string {
	if len(tokens) != 1 {
		return tokens
	}
	switch strings.ToUpper(tokens[0]) {
	case "ALL":
		return []string{"FLAGS", "INTERNALDATE", "RFC822.SIZE", "ENVELOPE"}
	case "FAST":
		return []string{"FLAGS", "INTERNALDATE", "RFC822.SIZE"}
	case "FULL":
		return []string{"FLAGS", "INTERNALDATE", "RFC822.SIZE", "ENVELOPE", "BODY"}
	default:
		return tokens
	}
}

// validFetchSections checks BODY section-specs (RFC 3501 §9) and partial
// suffixes before any message is read or changed. In particular an invalid
// later item cannot follow an earlier BODY[] read/Seen update, and an empty UID
// result cannot hide invalid syntax.
func validFetchSections(tokens []string) bool {
	for _, item := range tokens {
		up := strings.ToUpper(item)
		if BinaryItem(up) {
			if !validBinaryItem(item) {
				return false
			}
			continue
		}
		if !strings.HasPrefix(up, "BODY[") && !strings.HasPrefix(up, "BODY.PEEK[") {
			continue
		}
		rb := strings.LastIndexByte(item, ']')
		if rb < 0 {
			return false
		}
		lb := strings.IndexByte(item, '[')
		if !validSectionSpec(item[lb+1 : rb]) {
			return false
		}
		if _, ok := ParseBodyPartial(item[rb+1:]); !ok {
			return false
		}
	}
	return true
}

// BodyPartial is the RFC 3501 §9 request suffix "<number.nz-number>".
// Both operands are unsigned 32-bit values, and count must be nonzero.
type BodyPartial struct {
	Start uint32
	Count uint32
}

// ParseBodyPartial parses the text following a body section's closing ']'. An
// empty rem means no partial was requested. The response-only "<start>" form
// is not a valid request; nz-number also excludes leading zeroes in the count.
func ParseBodyPartial(rem string) (*BodyPartial, bool) {
	if rem == "" {
		return nil, true
	}
	if len(rem) < 2 || rem[0] != '<' || rem[len(rem)-1] != '>' {
		return nil, false
	}
	startStr, countStr, hasCount := strings.Cut(rem[1:len(rem)-1], ".")
	if !hasCount || countStr == "" || countStr[0] < '1' || countStr[0] > '9' {
		return nil, false
	}
	// ParseUint's base-10 grammar rejects signs, whitespace and non-digits.
	start, err := strconv.ParseUint(startStr, 10, 32)
	if err != nil {
		return nil, false
	}
	count, err := strconv.ParseUint(countStr, 10, 32)
	if err != nil {
		return nil, false
	}
	return &BodyPartial{Start: uint32(start), Count: uint32(count)}, true
}

// BinaryItem reports whether up, an upper-case FETCH item, is BINARY,
// BINARY.PEEK or BINARY.SIZE.
func BinaryItem(up string) bool {
	return strings.HasPrefix(up, "BINARY[") || strings.HasPrefix(up, "BINARY.PEEK[") || strings.HasPrefix(up, "BINARY.SIZE[")
}

// validBinaryItem checks a BINARY item's section-binary, "[" [section-part]
// "]" (RFC 3516 §6): part numbers only, no HEADER, TEXT or MIME. BINARY and
// BINARY.PEEK may carry a partial; BINARY.SIZE may not.
func validBinaryItem(item string) bool {
	lb, rb := strings.IndexByte(item, '['), strings.IndexByte(item, ']')
	if lb < 0 || rb < lb {
		return false
	}
	if section := item[lb+1 : rb]; section != "" {
		if path, text, ok := SplitSectionPart(section); !ok || len(path) == 0 || text != "" {
			return false
		}
	}
	if strings.HasPrefix(strings.ToUpper(item), "BINARY.SIZE[") {
		return item[rb+1:] == ""
	}
	_, ok := ParseBodyPartial(item[rb+1:])
	return ok
}

// SplitSectionPart splits an RFC 3501 §9 section-spec into its numeric
// section-part (nz-number *("." nz-number)) and the remaining section-text.
// ok is false for a malformed part path, such as a zero, a leading zero, an
// out-of-range number or a trailing dot.
func SplitSectionPart(section string) (path []uint32, text string, ok bool) {
	for section != "" && section[0] >= '0' && section[0] <= '9' {
		num, rest, more := strings.Cut(section, ".")
		if num[0] == '0' {
			return nil, "", false
		}
		n, err := strconv.ParseUint(num, 10, 32)
		if err != nil {
			return nil, "", false
		}
		path = append(path, uint32(n))
		if !more {
			return path, "", true
		}
		if rest == "" {
			return nil, "", false
		}
		section = rest
	}
	return path, section, true
}

// validSectionSpec reports whether section is a complete RFC 3501 §9
// section-spec: empty, a section-msgtext, or a section-part optionally followed
// by a section-msgtext or MIME (MIME needs a part number).
func validSectionSpec(section string) bool {
	path, text, ok := SplitSectionPart(section)
	if !ok {
		return false
	}
	switch strings.ToUpper(text) {
	case "", "HEADER", "TEXT":
		return true
	case "MIME":
		return len(path) > 0
	}
	_, _, _, ok = ParseHeaderFields(text)
	return ok
}
