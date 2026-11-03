package wire

import "strings"

// ValidFlagKeyword reports whether s is an RFC 3501 §9 flag-keyword: one atom
// of ATOM-CHARs, excluding atom-specials and the resp-special "]".
func ValidFlagKeyword(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= 0x20 || c >= 0x7f || strings.IndexByte(`(){%*"\]`, c) >= 0 {
			return false
		}
	}
	return true
}

// ParseFlags extracts IMAP flags from a parenthesized list like "(\Seen \Flagged)".
func ParseFlags(s string) []string {
	s = strings.TrimSpace(s)
	// Remove surrounding parentheses
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")

	var flags []string
	for _, f := range strings.Fields(s) {
		if f != "" {
			flags = append(flags, f)
		}
	}
	return flags
}

// ValidStoreFlags reports whether s is the flag operand of an RFC 3501 §9
// store-att-flags: a parenthesised flag-list, which may be empty, or one or
// more bare flags. Flags are separated by a single SP, and each is an atom with
// an optional leading "\" (a system flag, flag-keyword or flag-extension).
func ValidStoreFlags(s string) bool {
	if len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		s = s[1 : len(s)-1]
		if s == "" {
			return true
		}
	}
	for _, f := range strings.Split(s, " ") {
		f = strings.TrimPrefix(f, `\`)
		if f == "" {
			return false
		}
		for i := 0; i < len(f); i++ {
			if c := f[i]; c <= 0x20 || c >= 0x7f || strings.IndexByte(`(){%*"\]`, c) >= 0 {
				return false
			}
		}
	}
	return true
}

// ValidFlagList reports whether s is an RFC 3501 §9 flag-list: a parenthesised,
// possibly empty, list of flags separated by a single SP, as APPEND requires.
func ValidFlagList(s string) bool {
	return len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' && ValidStoreFlags(s)
}
