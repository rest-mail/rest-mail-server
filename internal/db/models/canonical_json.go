package models

import "unicode/utf8"

// isCanonicalJSON says data is exactly what decoding it and encoding it again with encoding/json
// answers, so that a seal can read it as it is: decoding and encoding a large header costs several
// times its size. It answers false for anything it isn't sure of, numbers among them, and the seal
// then does both (TestCanonicalJSONMatchesAReencoding).
func isCanonicalJSON(data []byte) bool {
	rest, ok := canonicalValue(data, 0)
	return ok && len(rest) == 0
}

// canonicalDepth is as deep as a value is looked at. Stored JSON of any depth goes the long way.
const canonicalDepth = 16

func canonicalValue(data []byte, depth int) ([]byte, bool) {
	if len(data) == 0 || depth > canonicalDepth {
		return nil, false
	}
	switch data[0] {
	case '"':
		return canonicalString(data)
	case '[':
		data = data[1:]
		if len(data) != 0 && data[0] == ']' {
			return data[1:], true
		}
		for {
			var ok bool
			if data, ok = canonicalValue(data, depth+1); !ok || len(data) == 0 {
				return nil, false
			}
			switch data[0] {
			case ',':
				data = data[1:]
			case ']':
				return data[1:], true
			default:
				return nil, false
			}
		}
	case '{':
		data = data[1:]
		if len(data) != 0 && data[0] == '}' {
			return data[1:], true
		}
		var previous []byte
		for {
			after, ok := canonicalString(data)
			if !ok || len(after) == 0 || after[0] != ':' {
				return nil, false
			}
			key := data[:len(data)-len(after)]
			// Keys sort as decoded. Without a backslash, that's how they're spelled.
			for _, c := range key {
				if c == '\\' {
					return nil, false
				}
			}
			if previous != nil && string(previous) >= string(key[:len(key)-1]) {
				return nil, false
			}
			previous = key[:len(key)-1]
			if data, ok = canonicalValue(after[1:], depth+1); !ok || len(data) == 0 {
				return nil, false
			}
			switch data[0] {
			case ',':
				data = data[1:]
			case '}':
				return data[1:], true
			default:
				return nil, false
			}
		}
	}
	return nil, false
}

// canonicalString answers what follows a string spelled as encoding/json spells it.
func canonicalString(data []byte) ([]byte, bool) {
	if len(data) == 0 || data[0] != '"' {
		return nil, false
	}
	for i := 1; i < len(data); {
		c := data[i]
		switch {
		case c == '"':
			return data[i+1:], true
		case c == '\\':
			if i+1 == len(data) {
				return nil, false
			}
			switch data[i+1] {
			case '"', '\\', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				// Only the HTML characters, because they're the common ones in a header. Others go
				// the long way, which is right whatever they are.
				if i+6 > len(data) {
					return nil, false
				}
				switch string(data[i+2 : i+6]) {
				case "003c", "003e", "0026":
					i += 6
				default:
					return nil, false
				}
			default:
				return nil, false
			}
		case c < ' ' || c == '<' || c == '>' || c == '&':
			return nil, false
		case c < utf8.RuneSelf:
			i++
		default:
			r, size := utf8.DecodeRune(data[i:])
			if (r == utf8.RuneError && size == 1) || r == '\u2028' || r == '\u2029' {
				return nil, false
			}
			i += size
		}
	}
	return nil, false
}
