package rfctest

import (
	"strings"
	"testing"
)

// TestEnvelopeObsoleteAddresses reads the obsolete address forms RFC 5322 §4
// has a reader accept, and RFC 5321 address literals, into ENVELOPE address
// structures instead of losing the whole field (rest-mail/rest-mail-server#1862).
// A mailbox that cannot be read is left out without losing the others in the
// same list, in top-level and encapsulated envelopes.
//
// checklist: IMAP-ENV-14
func TestEnvelopeObsoleteAddresses(t *testing.T) {
	for _, tc := range []struct{ name, header, list string }{
		{name: "quoted word in local part", header: `"a".b@example.com`, list: `((NIL NIL "a.b" "example.com"))`},
		{name: "CFWS around dots", header: `joe . q @ example . com`, list: `((NIL NIL "joe.q" "example.com"))`},
		{name: "IPv6 address literal", header: `a@[IPv6:2001:db8::1]`, list: `((NIL NIL "a" "[IPv6:2001:db8::1]"))`},
		{name: "domain literal", header: `a@[not.an.ip]`, list: `((NIL NIL "a" "[not.an.ip]"))`},
		{name: "angle address with comments", header: `J. Doe (x) <"a" (y) . b @ example . com>`, list: `(("J. Doe" NIL "a.b" "example.com"))`},
		{name: "one refused address", header: `first@example.com, not a valid @@ address, "a".b@example.com`, list: `((NIL NIL "first" "example.com")(NIL NIL "a.b" "example.com"))`},
		{name: "refused group member", header: `Team: a@[not.an.ip], bad@@x;, z@example.com`, list: `((NIL NIL "Team" NIL)(NIL NIL "a" "[not.an.ip]")(NIL NIL NIL NIL)(NIL NIL "z" "example.com"))`},
		{name: "member that is not an address", header: `a@example.com, Friends: not an address;`, list: `((NIL NIL "a" "example.com")(NIL NIL "Friends" NIL)(NIL NIL NIL NIL))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, field := range []string{"From", "To", "Cc"} {
				t.Run(field, func(t *testing.T) {
					fields := []string{"NIL", "NIL", "NIL", "NIL", "NIL", "NIL", "NIL", "NIL", "NIL", "NIL"}
					switch field {
					case "From":
						// Sender and Reply-To default to From.
						fields[2], fields[3], fields[4] = tc.list, tc.list, tc.list
					case "To":
						fields[5] = tc.list
					case "Cc":
						fields[6] = tc.list
					}
					requireEnvelope(t, field+": "+tc.header+"\r\n\r\nx", "("+strings.Join(fields, " ")+")")
				})
			}
			inner := "To: " + tc.header + "\r\n\r\nx"
			messageStructureResponse(t, inner, "(NIL NIL NIL NIL NIL "+tc.list+" NIL NIL NIL NIL)",
				`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1)`,
				`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1 NIL NIL NIL NIL)`, 3)
		})
	}
}
