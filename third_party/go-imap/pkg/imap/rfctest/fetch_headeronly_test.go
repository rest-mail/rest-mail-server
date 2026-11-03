package rfctest

import (
	"slices"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// TestFetchHeaderOnlyMessage: "the blank line is included in all header
// fetches, except in the case of a message which has no body and no blank
// line" (RFC 3501 §6.4.5). A message that ends after its header fields answers
// HEADER, HEADER.FIELDS, HEADER.FIELDS.NOT and RFC822.HEADER without a blank
// line it does not have, and TEXT with nothing. A message with the blank line
// and an empty body keeps the blank line in each header fetch, and so does a
// message with no header fields, whose header is that blank line alone and
// whose text is everything after it.
//
// checklist: IMAP-FETCH-24
func TestFetchHeaderOnlyMessage(t *testing.T) {
	const from, subject = "From: anne@example.com\r\n", "Subject: header only\r\n"
	for _, tc := range []struct {
		name, raw, header, from, notFrom, missing, text string
	}{
		{"no blank line", from + subject, from + subject, from, subject, "", ""},
		{"blank line, empty body", from + subject + "\r\n", from + subject + "\r\n", from + "\r\n", subject + "\r\n", "\r\n", ""},
		{"no header fields", "\r\nFrom: body text\r\n\r\nmore", "\r\n", "\r\n", "\r\n", "\r\n", "From: body text\r\n\r\nmore"},
	} {
		for _, item := range []struct{ fetch, payload string }{
			{"BODY.PEEK[]", tc.raw},
			{"BODY.PEEK[HEADER]", tc.header},
			{"RFC822.HEADER", tc.header},
			{"BODY.PEEK[HEADER.FIELDS (FROM)]", tc.from},
			{"BODY.PEEK[HEADER.FIELDS.NOT (FROM)]", tc.notFrom},
			{"BODY.PEEK[HEADER.FIELDS (X-MISSING)]", tc.missing},
			{"BODY.PEEK[TEXT]", tc.text},
		} {
			for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
				t.Run(tc.name+"/"+prefix+"/"+item.fetch, func(t *testing.T) {
					box := newFetchMailbox()
					box.messages = []imap.Message{{UID: 10, Size: len(tc.raw)}}
					box.raw = map[uint32]string{10: tc.raw}
					c := fetchClient(t, box, "SELECT")
					replies, tagged := fetchCommand(t, c, prefix+" "+item.fetch)
					requireOK(t, tagged, "F1")
					if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{item.payload}) {
						t.Fatalf("got %#v, want %q", replies, item.payload)
					}
					if len(box.updates) != 0 {
						t.Fatalf("a peek changed flags: %v", box.updates)
					}
				})
			}
		}
	}
}
