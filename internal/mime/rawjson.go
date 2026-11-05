package mime

import (
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"slices"
	"unicode/utf8"
)

// RawHeadersJSON is json.Marshal(raw), in one buffer of about its size. A header can be as long as a
// message, and json.Marshal grows a buffer for it and then copies it (TestRawJSONMatchesEncodingJSON).
func RawHeadersJSON(raw map[string][]string) []byte {
	if raw == nil {
		return []byte("null")
	}
	size := 2
	keys := make([]string, 0, len(raw))
	for key, values := range raw {
		keys = append(keys, key)
		size += jsonStringSize(key) + 6
		for _, value := range values {
			size += jsonStringSize(value) + 1
		}
	}
	slices.Sort(keys)
	out := make([]byte, 0, size)
	out = append(out, '{')
	for i, key := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		out = appendJSONString(out, key)
		out = append(out, ':')
		out = appendJSONStrings(out, raw[key])
	}
	return append(out, '}')
}

// StringsJSON is json.Marshal(list), in one buffer of about its size.
func StringsJSON(list []string) []byte {
	size := 2
	for _, value := range list {
		size += jsonStringSize(value) + 1
	}
	return appendJSONStrings(make([]byte, 0, size), list)
}

// AddressesJSON is StringsJSON of the addresses of a list, never null, built without holding them.
func AddressesJSON(list pipeline.AddressList) []byte {
	out := make([]byte, 0, list.FieldSize()+list.FieldSize()/2+2)
	out = append(out, '[')
	first := true
	ok := list.Visit(func(address pipeline.Address) {
		if !first {
			out = append(out, ',')
		}
		first = false
		out = appendJSONString(out, address.Address)
	})
	if !ok {
		return []byte("[]")
	}
	return append(out, ']')
}

// jsonStringSize is as much as appendJSONString writes for s, or a little more.
func jsonStringSize(s string) int {
	size := len(s) + 2
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			size++
		case c < ' ' || c == '<' || c == '>' || c == '&' || c >= utf8.RuneSelf:
			size += 5
		}
	}
	return size
}

func appendJSONStrings(out []byte, list []string) []byte {
	if list == nil {
		return append(out, "null"...)
	}
	out = append(out, '[')
	for i, value := range list {
		if i > 0 {
			out = append(out, ',')
		}
		out = appendJSONString(out, value)
	}
	return append(out, ']')
}

// appendJSONString writes a string as encoding/json does, HTML escaping included.
func appendJSONString(out []byte, s string) []byte {
	const hex = "0123456789abcdef"
	out = append(out, '"')
	start := 0
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if c >= ' ' && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
				i++
				continue
			}
			out = append(out, s[start:i]...)
			switch c {
			case '"', '\\':
				out = append(out, '\\', c)
			case '\b':
				out = append(out, '\\', 'b')
			case '\f':
				out = append(out, '\\', 'f')
			case '\n':
				out = append(out, '\\', 'n')
			case '\r':
				out = append(out, '\\', 'r')
			case '\t':
				out = append(out, '\\', 't')
			default:
				out = append(out, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			out = append(out, s[start:i]...)
			out = append(out, `\ufffd`...)
			i += size
			start = i
		case r == '\u2028' || r == '\u2029':
			out = append(out, s[start:i]...)
			out = append(out, '\\', 'u', '2', '0', '2', hex[r&0xf])
			i += size
			start = i
		default:
			i += size
		}
	}
	out = append(out, s[start:]...)
	return append(out, '"')
}
