package rfctest

import (
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// charsetSearchMailbox stores encoded words beside text SEARCH can't decode:
//
//	UID10  a word in another charset, and a UTF-8 word in another field
//	UID20  the same UTF-8 word and nothing to stop it
//	UID30  words in three charsets in one field, around one it can't decode
//	UID40  a field holding "=?" that starts no word, before a UTF-8 word
//	UID50  an opening with no close on its line, before a UTF-8 word
//	UID60  an encapsulated message with both kinds of word
//	UID70  adjacent words go-imap can decode, folded
//	UID80  an opening with no close before a UTF-8 word on the same line
func charsetSearchMailbox() *fetchMailbox {
	raws := map[uint32]string{
		10: "Subject: =?windows-1252?Q?J=F6rg?=\r\nX-Tag: =?UTF-8?Q?M=C3=BCller?=\r\n\r\nbody\r\n",
		20: "Subject: plain\r\nX-Tag: =?UTF-8?Q?M=C3=BCller?=\r\n\r\nbody\r\n",
		30: "Subject: =?UTF-8?Q?Gr=C3=BC=C3=9Fe?= =?ISO-2022-JP?B?GyRCJF8bKEI=?= =?ISO-8859-1?Q?caf=E9?=\r\n" +
			"To: =?windows-1252?Q?J=F6?= =?UTF-8?Q?Z=C3=BCrich?=\r\n\r\nbody\r\n",
		40: "Subject: what =? now\r\nX-Tag: =?UTF-8?Q?Zur=C3=BCck?=\r\n\r\nbody\r\n",
		50: "Subject: =?x-no-such-charset?Q?open\r\nX-Tag: =?UTF-8?Q?Stra=C3=9Fe?=\r\n\r\nbody\r\n",
		60: "Subject: six\r\nContent-Type: message/rfc822\r\n\r\n" +
			"Subject: =?windows-1252?Q?J=F6rg?=\r\nX-Inner: =?UTF-8?Q?K=C3=B6ln?=\r\n\r\ninner\r\n",
		70: "Subject: =?UTF-8?Q?Sch=C3=B6n?=\r\n =?UTF-8?Q?brunn?=\r\n\r\nbody\r\n",
		80: "Subject: =?x-no-such-charset?Q?open =?UTF-8?Q?Z=C3=BCge?=\r\n\r\nbody\r\n",
	}
	box := newFetchMailbox()
	box.messages = nil
	for _, uid := range []uint32{10, 20, 30, 40, 50, 60, 70, 80} {
		subject, _, _ := strings.Cut(strings.TrimPrefix(raws[uid], "Subject: "), "\r\nX-")
		subject, _, _ = strings.Cut(subject, "\r\nTo:")
		subject, _, _ = strings.Cut(subject, "\r\nContent-Type:")
		m := imap.Message{UID: uid, Subject: subject, Size: len(raws[uid])}
		if uid == 30 {
			m.To = "=?windows-1252?Q?J=F6?= =?UTF-8?Q?Z=C3=BCrich?="
			m.From = imap.Address{Name: "=?windows-1252?Q?J=F6?= =?UTF-8?Q?M=C3=BC?=", Email: "m@example.com"}
		}
		box.messages = append(box.messages, m)
	}
	box.raw = raws
	return box
}

// TestSearchDecodesEveryWordItCan: IMAP-SEARCH-10 compares a word in a charset
// go-imap can't convert as stored. That is true of the word alone, so the words
// around it still decode, in TEXT, HEADER, SUBJECT, TO, FROM and an encapsulated
// message, and the message still matches on them.
//
// checklist: IMAP-SEARCH-10
func TestSearchDecodesEveryWordItCan(t *testing.T) {
	c := fetchClient(t, charsetSearchMailbox(), "EXAMINE")
	for _, tc := range []struct{ query, seqs string }{
		// The case the issue reported: another field's word in another charset.
		{`TEXT "Müller"`, "1 2"},
		{`HEADER X-Tag "Müller"`, "1 2"},
		{`TEXT "=?UTF-8?Q?M=C3=BCller?="`, ""},
		// A word it can't decode is compared as stored.
		{`TEXT "=?windows-1252?Q?J=F6rg?="`, "1 6"},
		{`SUBJECT "=?windows-1252?Q?J=F6rg?="`, "1"},
		// Several charsets in one field.
		{`SUBJECT "Grüße"`, "3"},
		{`SUBJECT "café"`, "3"},
		{`TEXT "Grüße"`, "3"},
		{`TEXT "café"`, "3"},
		{`SUBJECT "=?ISO-2022-JP?B?GyRCJF8bKEI=?="`, "3"},
		{`TEXT "Grüße =?ISO-2022-JP?B?GyRCJF8bKEI=?= café"`, "3"},
		{`TO "Zürich"`, "3"},
		{`TO "=?windows-1252?Q?J=F6?="`, "3"},
		{`FROM "Mü"`, "3"},
		{`FROM "=?windows-1252?Q?J=F6?="`, "3"},
		// Text that starts no word stops nothing after it.
		{`TEXT "Zurück"`, "4"},
		{`TEXT "what =? now"`, "4"},
		{`TEXT "Straße"`, "5"},
		{`TEXT "=?x-no-such-charset?Q?open"`, "5 8"},
		{`TEXT "Züge"`, "8"},
		{`SUBJECT "Züge"`, "8"},
		// An encapsulated message.
		{`TEXT "Köln"`, "6"},
		{`BODY "Köln"`, "6"},
		{`BODY "=?windows-1252?Q?J=F6rg?="`, "6"},
		// Space between adjacent decoded words still isn't text (RFC 2047 §6.2).
		{`TEXT "Schönbrunn"`, "7"},
		{`SUBJECT "Schönbrunn"`, "7"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			requireLiteralSearch(t, c, "SEARCH CHARSET UTF-8 "+tc.query, strings.TrimSpace("* SEARCH "+tc.seqs))
		})
	}
}
