package wire

import (
	"fmt"
	"strings"
)

// RFC 2971 §3.3 bounds on the client's ID data: "Field strings MUST NOT be
// longer than 30 octets. Value strings MUST NOT be longer than 1024 octets.
// Implementations MUST NOT send more than 30 field-value pairs."
const (
	MaxIDPairs       = 30
	MaxIDFieldOctets = 30
	MaxIDValueOctets = 1024
)

// IDPair is one field-value pair of an ID command's id_params_list. Nil marks a
// NIL value.
type IDPair struct {
	Name  string
	Value string
	Nil   bool
}

// ParseIDParams parses the argument of the ID command (RFC 2971 §4):
//
//	id_params_list ::= "(" #(string SPACE nstring) ")" / nil
//
// It returns isNil for ID NIL, otherwise the pairs in the order sent. A string
// is a quoted-string here: the session has already spliced any literal back in
// as one. problem is non-empty when the argument breaks the grammar or the
// RFC 2971 §3.3 limits: more than 30 pairs, a field name over 30 octets, a
// value over 1024 octets, or the same field name twice ("Implementations MUST
// NOT send the same field name more than once"; "Strings are not
// case-sensitive"). The session answers such a command BAD (§3.1: "BAD command
// unknown or arguments invalid").
func ParseIDParams(args string) (pairs []IDPair, isNil bool, problem string) {
	if strings.EqualFold(args, "NIL") {
		return nil, true, ""
	}
	if len(args) < 2 || args[0] != '(' || args[len(args)-1] != ')' {
		return nil, false, "ID requires a parenthesized list of field-value pairs or NIL"
	}
	rest := args[1 : len(args)-1]
	seen := make(map[string]bool)
	for first := true; rest != ""; first = false {
		if !first {
			var ok bool
			if rest, ok = strings.CutPrefix(rest, " "); !ok {
				return nil, false, "ID field-value pairs must be separated by SP"
			}
		}
		name, after, ok := parseIDString(rest)
		if !ok {
			return nil, false, "ID field must be a string"
		}
		if after, ok = strings.CutPrefix(after, " "); !ok {
			return nil, false, "ID field must be followed by SP and a value"
		}
		var p IDPair
		if len(after) >= 3 && strings.EqualFold(after[:3], "NIL") && (len(after) == 3 || after[3] == ' ') {
			p = IDPair{Name: name, Nil: true}
			rest = after[3:]
		} else {
			value, tail, ok := parseIDString(after)
			if !ok {
				return nil, false, "ID value must be a string or NIL"
			}
			p = IDPair{Name: name, Value: value}
			rest = tail
		}
		if len(pairs) == MaxIDPairs {
			return nil, false, fmt.Sprintf("ID takes at most %d field-value pairs", MaxIDPairs)
		}
		if len(p.Name) > MaxIDFieldOctets {
			return nil, false, fmt.Sprintf("ID field names are at most %d octets", MaxIDFieldOctets)
		}
		if len(p.Value) > MaxIDValueOctets {
			return nil, false, fmt.Sprintf("ID values are at most %d octets", MaxIDValueOctets)
		}
		key := strings.ToLower(p.Name)
		if seen[key] {
			return nil, false, "ID field names must not repeat"
		}
		seen[key] = true
		pairs = append(pairs, p)
	}
	return pairs, false, ""
}

// parseIDString reads one quoted-string from the start of s and returns its
// decoded value and the text after its closing quote.
func parseIDString(s string) (val, rest string, ok bool) {
	if s == "" || s[0] != '"' {
		return "", "", false
	}
	end := QuotedStringEnd(s)
	if end < 0 {
		return "", "", false
	}
	return unescapeQuoted(s[1:end]), s[end+1:], true
}

// FormatIDParams renders the server's own pairs as an id_params_list for the
// untagged ID response (RFC 2971 §3.2), or NIL when there are none. The server
// sends only values it has, so no value is NIL.
func FormatIDParams(pairs []IDPair) string {
	if len(pairs) == 0 {
		return "NIL"
	}
	parts := make([]string, 0, 2*len(pairs))
	for _, p := range pairs {
		parts = append(parts, EncodeMailboxName(p.Name), EncodeMailboxName(p.Value))
	}
	return "(" + strings.Join(parts, " ") + ")"
}
