package mail

import (
	"errors"
	"unicode/utf8"
)

// MaxMsgIDLength is the longest msg-id accepted, in octets. A msg-id cannot be folded, so it has
// to fit on one header line, and RFC 5322 §2.1.1 caps a line at 998 octets.
const MaxMsgIDLength = 998

// The reasons a value is not RFC 5322 §3.6.4 msg-id syntax, worded for a field refusal.
var (
	ErrMsgIDControl  = errors.New("must not contain line breaks or other control characters")
	ErrMsgIDSyntax   = errors.New("must be an RFC 5322 msg-id: <id-left@id-right>")
	ErrMsgIDListForm = errors.New("must be one or more RFC 5322 msg-ids (<id-left@id-right>), separated by spaces")
	ErrMsgIDTooLong  = errors.New("a msg-id must not be longer than 998 octets (RFC 5322 §2.1.1)")
)

// ValidateMsgID reports whether s is one RFC 5322 §3.6.4 msg-id, as a Message-ID field holds:
// "<" id-left "@" id-right ">", with optional spaces or tabs around it. id-left is a dot-atom-text
// or a quoted-string (obs-id-left), id-right a dot-atom-text or a domain literal (no-fold-literal,
// obs-id-right); atext, qtext and dtext take UTF-8 beyond ASCII as RFC 6532 §3.2 allows. No CR,
// LF or other control character is accepted anywhere, so the value can never become more than
// one header line.
func ValidateMsgID(s string) error {
	n, err := validateMsgIDs(s)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrMsgIDSyntax
	}
	return nil
}

// ValidateMsgIDList reports whether s is one or more RFC 5322 §3.6.4 msg-ids, optionally
// separated by spaces or tabs, as an In-Reply-To or References field holds (1*msg-id). Each is held to
// ValidateMsgID's rules.
func ValidateMsgIDList(s string) error {
	n, err := validateMsgIDs(s)
	if err != nil {
		if err == ErrMsgIDSyntax {
			return ErrMsgIDListForm
		}
		return err
	}
	if n == 0 {
		return ErrMsgIDListForm
	}
	return nil
}

// validateMsgIDs reads s as *msg-id with optional spaces or tabs between and around them, and
// answers how many it holds.
func validateMsgIDs(s string) (int, error) {
	if !utf8.ValidString(s) {
		return 0, ErrMsgIDSyntax
	}
	for _, r := range s {
		if (r < 0x20 && r != '\t') || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return 0, ErrMsgIDControl
		}
	}
	count := 0
	i := skipWSP(s, 0)
	for i < len(s) {
		end, err := msgIDEnd(s, i)
		if err != nil {
			return 0, err
		}
		if end-i > MaxMsgIDLength {
			return 0, ErrMsgIDTooLong
		}
		count++
		// The space between msg-ids is optional ([CFWS] on each side), so "<a@b><c@d>" is a list too.
		i = skipWSP(s, end)
	}
	return count, nil
}

// msgIDEnd reads one msg-id starting at s[i], which must be "<", and answers the index just
// past its ">".
func msgIDEnd(s string, i int) (int, error) {
	if i >= len(s) || s[i] != '<' {
		return 0, ErrMsgIDSyntax
	}
	i++
	var ok bool
	if i < len(s) && s[i] == '"' {
		i, ok = quotedStringEnd(s, i)
	} else {
		i, ok = dotAtomTextEnd(s, i)
	}
	if !ok || i >= len(s) || s[i] != '@' {
		return 0, ErrMsgIDSyntax
	}
	i++
	if i < len(s) && s[i] == '[' {
		i, ok = domainLiteralEnd(s, i)
	} else {
		i, ok = dotAtomTextEnd(s, i)
	}
	if !ok || i >= len(s) || s[i] != '>' {
		return 0, ErrMsgIDSyntax
	}
	return i + 1, nil
}

func skipWSP(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// isAtext is RFC 5322 §3.2.3 atext, with RFC 6532's utf8-non-ascii.
func isAtext(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c >= 0x80:
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '/', '=', '?', '^', '_', '`', '{', '|', '}', '~':
		return true
	}
	return false
}

// dotAtomTextEnd reads 1*atext *("." 1*atext) from s[i] and answers the index past it.
func dotAtomTextEnd(s string, i int) (int, bool) {
	for {
		start := i
		for i < len(s) && isAtext(s[i]) {
			i++
		}
		if i == start {
			return i, false
		}
		if i < len(s) && s[i] == '.' {
			i++
			continue
		}
		return i, true
	}
}

// quotedStringEnd reads a quoted-string (RFC 5322 §3.2.4) from s[i], which is its opening quote:
// qtext, quoted-pairs and spaces or tabs up to the closing quote.
func quotedStringEnd(s string, i int) (int, bool) {
	for i++; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			return i + 1, true
		case c == '\\':
			i++
			if i >= len(s) {
				return i, false
			}
		case c == ' ', c == '\t', c == 33, c >= 35 && c <= 91, c >= 93 && c <= 126, c >= 0x80:
		default:
			return i, false
		}
	}
	return i, false
}

// domainLiteralEnd reads a no-fold-literal, "[" *dtext "]" (RFC 5322 §3.6.4), from s[i].
func domainLiteralEnd(s string, i int) (int, bool) {
	for i++; i < len(s); i++ {
		switch c := s[i]; {
		case c == ']':
			return i + 1, true
		case c >= 33 && c <= 90, c >= 94 && c <= 126, c >= 0x80:
		default:
			return i, false
		}
	}
	return i, false
}
