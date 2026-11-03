package rfctest

import (
	"strings"
	"testing"
)

// TestEnvelopeGroups preserves RFC 2822 group boundaries, including an empty
// group, in addition to the member addresses that net/mail returns. Quoted
// punctuation and comments must not be mistaken for group delimiters.
//
// checklist: IMAP-ENV-10
func TestEnvelopeGroups(t *testing.T) {
	for _, tc := range []struct {
		name, header, list string
		literals           []string
	}{
		{name: "empty", header: "undisclosed-recipients:;", list: `((NIL NIL "undisclosed-recipients" NIL)(NIL NIL NIL NIL))`},
		{name: "members", header: "Friends: Alice <alice@example.com>, bob@example.net;", list: `((NIL NIL "Friends" NIL)("Alice" NIL "alice" "example.com")(NIL NIL "bob" "example.net")(NIL NIL NIL NIL))`},
		{name: "mixed", header: `before@example.com, First: a@example.com;, Empty:;, after@example.com`, list: `((NIL NIL "before" "example.com")(NIL NIL "First" NIL)(NIL NIL "a" "example.com")(NIL NIL NIL NIL)(NIL NIL "Empty" NIL)(NIL NIL NIL NIL)(NIL NIL "after" "example.com"))`},
		{name: "quoted punctuation", header: `"a:b,;" (ignored : , ;) : "x:y,;" <x@example.com>, "a:b"@example.net; (end , : ;)`, list: `((NIL NIL "a:b,;" NIL)("x:y,;" NIL "x" "example.com")(NIL NIL "a:b" "example.net")(NIL NIL NIL NIL))`},
		{name: "escaped name", header: `"a\"b\\c": x@example.com;`, list: `((NIL NIL "a\"b\\c" NIL)(NIL NIL "x" "example.com")(NIL NIL NIL NIL))`},
		{name: "nested comments", header: `Team (outer (inner : , ;) \) tail): a@example.com; (end)`, list: `((NIL NIL "Team" NIL)(NIL NIL "a" "example.com")(NIL NIL NIL NIL))`},
		{name: "domain literal", header: `Team: x@[127.0.0.1];`, list: `((NIL NIL "Team" NIL)(NIL NIL "x" "[127.0.0.1]")(NIL NIL NIL NIL))`},
		{name: "colon without group", header: `"a:b" <x@[127.0.0.1]>`, list: `(("a:b" NIL "x" "[127.0.0.1]"))`},
		{name: "encoded group name", header: "=?utf-8?Q?caf=C3=A9?=:;", list: `((NIL NIL {5} NIL)(NIL NIL NIL NIL))`, literals: []string{"café"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, field := range []string{"To", "Cc", "Bcc"} {
				t.Run(field, func(t *testing.T) {
					fields := []string{"NIL", "NIL", "NIL", "NIL", "NIL", "NIL", "NIL", "NIL", "NIL", "NIL"}
					index := map[string]int{"To": 5, "Cc": 6, "Bcc": 7}[field]
					fields[index] = tc.list
					requireEnvelope(t, field+": "+tc.header+"\r\n\r\nx", "("+strings.Join(fields, " ")+")", tc.literals...)
				})
			}
			inner := "To: " + tc.header + "\r\n\r\nx"
			messageStructureResponse(t, inner, "(NIL NIL NIL NIL NIL "+tc.list+" NIL NIL NIL NIL)",
				`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1)`,
				`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1 NIL NIL NIL NIL)`, 3, tc.literals...)
		})
	}
}

// TestEnvelopeMalformedGroups rejects the complete malformed list rather than
// emitting unmatched IMAP group sentinels or only the valid prefix addresses.
//
// checklist: IMAP-ENV-10
func TestEnvelopeMalformedGroups(t *testing.T) {
	for _, value := range []string{
		"Friends: a@example.com", "Friends: Nested: a@example.com;;",
		"a@example.com;", "Friends: a@example.com; broken",
		"a@example.com, Friends: not an address;",
	} {
		t.Run(value, func(t *testing.T) {
			requireEnvelope(t, "To: "+value+"\r\n\r\nx", `(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)`)
		})
	}
}
