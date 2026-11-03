package wire

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// ParseCommand parses an IMAP command line into tag, command, and arguments.
// IMAP format: <tag> <command> [<args>]
func ParseCommand(line string) (tag, cmd, args string) {
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 {
		return "", "", ""
	}
	tag = parts[0]
	cmd = parts[1]
	if len(parts) > 2 {
		args = parts[2]
	}
	return
}

// NoArguments are the commands whose grammar ends at the command name (RFC 3501
// §9's command-any, command-nonauth and command-select, RFC 2177 IDLE, RFC 3691
// UNSELECT, RFC 2342 NAMESPACE). STARTTLS starts no negotiation the client may
// not expect.
var NoArguments = map[string]bool{
	"CAPABILITY": true, "LOGOUT": true, "NOOP": true, "STARTTLS": true,
	"CHECK": true, "CLOSE": true, "EXPUNGE": true, "UNSELECT": true, "IDLE": true,
	"NAMESPACE": true,
}

// ValidTag reports whether tag is RFC 3501 §9's tag: one or more ASTRING-CHARs
// other than "+". That leaves out SP, CTL, 8-bit octets, "(", ")", "{", "%",
// "*", DQUOTE and "\", and lets "]" in.
func ValidTag(tag string) bool {
	if tag == "" {
		return false
	}
	for i := 0; i < len(tag); i++ {
		if c := tag[i]; c <= 0x20 || c >= 0x7f || strings.IndexByte(`(){%*"\+`, c) >= 0 {
			return false
		}
	}
	return true
}

// ParseLoginArgs reads LOGIN's arguments, userid SP password (RFC 3501 §9): two
// astrings, one SP between them and nothing after.
func ParseLoginArgs(args string) (user, password string, ok bool) {
	user, rest, ok := ParseAstring(args)
	if !ok || strings.HasPrefix(args, " ") || !ValidAstring(args[:len(args)-len(rest)]) {
		return "", "", false
	}
	if rest, ok = CutSingleSP(rest); !ok {
		return "", "", false
	}
	password, after, ok := ParseAstring(rest)
	if !ok || after != "" || !ValidAstring(rest) {
		return "", "", false
	}
	return user, password, true
}

// ParseArgs splits IMAP arguments respecting quoted strings and parenthesized lists.
func ParseArgs(args string) []string {
	var result []string
	args = strings.TrimSpace(args)
	i := 0

	for i < len(args) {
		// Skip whitespace
		for i < len(args) && args[i] == ' ' {
			i++
		}
		if i >= len(args) {
			break
		}

		switch args[i] {
		case '"':
			// Quoted string — find the closing quote, skipping any
			// backslash-escaped character so an escaped \" does not terminate
			// the string early (RFC 3501 §4.3). The token is kept with its
			// surrounding quotes and escapes intact; Unquote decodes it.
			end := QuotedStringEnd(args[i:])
			if end == -1 {
				// Unterminated quoted string — take the remainder.
				result = append(result, args[i:])
				i = len(args)
			} else {
				result = append(result, args[i:i+end+1])
				i = i + end + 1
			}
		case '(':
			// Parenthesized list — find closing paren
			depth := 1
			j := i + 1
			for j < len(args) && depth > 0 {
				switch args[j] {
				case '(':
					depth++
				case ')':
					depth--
				}
				j++
			}
			result = append(result, args[i:j])
			i = j
		default:
			// Unquoted token — read until space or special char
			j := i
			for j < len(args) && args[j] != ' ' && args[j] != '(' && args[j] != ')' {
				j++
			}
			result = append(result, args[i:j])
			i = j
		}
	}

	return result
}

// ParseStatusItems parses RFC 3501 §9's nonempty, SP-separated status-att
// list. Do not use Fields: tabs, doubled SP and leading/trailing SP are not
// separators in this grammar. Unknown names are rejected by the handler.
func ParseStatusItems(tok string) (items []string, ok bool) {
	if len(tok) < 3 || tok[0] != '(' || tok[len(tok)-1] != ')' {
		return nil, false
	}
	for _, item := range strings.Split(tok[1:len(tok)-1], " ") {
		if item == "" {
			return nil, false
		}
		// ABNF terminals are ASCII: Unicode case conversion must not turn
		// a lookalike such as long s into a valid status item name.
		for i := 0; i < len(item); i++ {
			if item[i] >= 0x80 {
				return nil, false
			}
		}
		items = append(items, strings.ToUpper(item))
	}
	return items, true
}

// DecodeBase64 decodes a base64-encoded string.
func DecodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// CutSingleSP removes the one SP that separates two command arguments (RFC 3501
// §9). It reports false when the separator is missing or doubled.
func CutSingleSP(s string) (string, bool) {
	rest, ok := strings.CutPrefix(s, " ")
	if !ok || strings.HasPrefix(rest, " ") {
		return s, false
	}
	return rest, true
}

// SplitTrailingLiteral inspects the tail of a command line for a literal-length
// marker: a synchronizing literal "{n}" or a LITERAL+ non-synchronizing literal
// "{n+}" (RFC 3501 §4.3, RFC 2088). A literal marker is only ever the final token
// of a command line — the octets follow on the next physical line — so only a
// line ending in '}' can carry one. On a match it returns the text preceding the
// marker, the declared octet count, and sync (true for "{n}", false for "{n+}").
// isLit is false when the tail is not a well-formed marker — a line not ending in
// '}', no '{', an empty or non-numeric count, or a signed/garbage count — in
// which case the line has no trailing literal and is parsed verbatim.
//
// The count is parsed with ParseUint (base 10, no sign), so "{+5}" or "{5x}" are
// rejected rather than misread; the caller separately bounds the value against
// the server's MaxLiteralSize.
func SplitTrailingLiteral(line string) (prefix string, size int, sync, isLit bool) {
	if !strings.HasSuffix(line, "}") {
		return "", 0, false, false
	}
	open := strings.LastIndexByte(line, '{')
	if open < 0 {
		return "", 0, false, false
	}
	inner := line[open+1 : len(line)-1]
	sync = true
	if strings.HasSuffix(inner, "+") { // LITERAL+ non-synchronizing form
		sync = false
		inner = inner[:len(inner)-1]
	}
	n, err := strconv.ParseUint(inner, 10, 32)
	if err != nil {
		return "", 0, false, false
	}
	return line[:open], int(n), sync, true
}
