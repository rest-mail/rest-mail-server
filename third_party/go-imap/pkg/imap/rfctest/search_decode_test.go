package rfctest

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// decodeSearchMailbox stores text only in encoded forms, so a match proves
// SEARCH decoded it:
//
//	UID10  base64 UTF-8 body; RFC 2047 B-encoded Subject (also in metadata)
//	UID20  multipart: quoted-printable ISO-8859-1 text, base64 UTF-8 HTML;
//	       Q-encoded ISO-8859-1 X-Tag
//	UID30  MESSAGE/RFC822 part with a Q-encoded Subject and a base64 body
//	UID40  7BIT body that merely looks like base64
//	UID50  malformed base64 body, searched as stored
func decodeSearchMailbox() *fetchMailbox {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	subject10 := "=?UTF-8?B?" + b64("Rechnung März") + "?="
	raws := map[uint32]string{
		10: "Subject: " + subject10 + "\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
			b64("Viele Grüße aus Köln") + "\r\n",
		20: "Subject: two\r\nX-Tag: =?ISO-8859-1?Q?d=E9j=E0_vu?=\r\nContent-Type: multipart/alternative; boundary=b\r\n\r\n" +
			"--b\r\nContent-Type: text/plain; charset=ISO-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ncaf=E9 cr=E8me=\r\n br=FBl=E9e\r\n" +
			"--b\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: BASE64\r\n\r\n" + b64("<p>Zürich</p>") + "\r\n--b--\r\n",
		30: "Subject: three\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
			"--m\r\nContent-Type: message/rfc822\r\n\r\nSubject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64("hidden treasure") + "\r\n--m--\r\n",
		40: "Subject: four\r\n\r\nSGVsbG8= plain words\r\n",
		50: "Subject: five\r\nContent-Transfer-Encoding: base64\r\n\r\n!!not*base64\r\n",
	}
	box := newFetchMailbox()
	box.messages = nil
	subjects := map[uint32]string{10: subject10, 20: "two", 30: "three", 40: "four", 50: "five"}
	for _, uid := range []uint32{10, 20, 30, 40, 50} {
		box.messages = append(box.messages, imap.Message{UID: uid, Subject: subjects[uid], Size: len(raws[uid]), Date: time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)})
	}
	box.raw = raws
	return box
}

// TestSearchDecodesMIME checks RFC 3501 §6.4.4's requirement that SEARCH
// match decoded text: BODY and TEXT remove base64 and quoted-printable
// transfer encodings in every MIME leaf, including inside MESSAGE/RFC822, and
// convert ISO-8859-1 to UTF-8; TEXT, HEADER and SUBJECT decode RFC 2047
// encoded words. Encoded octets no longer match, a part boundary cannot be
// spanned, and malformed or absent encodings are searched as stored.
//
// checklist: IMAP-SEARCH-10
// checklist: IMAP2-CMD-143 IMAP2-CMD-145
func TestSearchDecodesMIME(t *testing.T) {
	encoded10 := base64.StdEncoding.EncodeToString([]byte("Viele Grüße aus Köln"))
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		t.Run(selection, func(t *testing.T) {
			c := fetchClient(t, decodeSearchMailbox(), selection)
			for _, tc := range []struct{ query, seqs, uids string }{
				{`BODY "aus köln"`, "1", "10"},
				{`BODY "grüße"`, "1 3", "10 30"},
				{`BODY "` + encoded10[:12] + `"`, "", ""},
				{`BODY "café crème brûlée"`, "2", "20"},
				{`BODY "caf=E9"`, "", ""},
				{`BODY "zürich"`, "2", "20"},
				{`BODY "brûlée zürich"`, "", ""},
				{`BODY "hidden treasure"`, "3", "30"},
				{`BODY "SGVsbG8="`, "4", "40"},
				{`BODY "!!not*base64"`, "5", "50"},
				{`TEXT "rechnung märz"`, "1", "10"},
				{`TEXT "=?UTF-8?B?"`, "", ""},
				{`TEXT "déjà vu"`, "2", "20"},
				{`TEXT "hidden treasure"`, "3", "30"},
				{`SUBJECT "MÄRZ"`, "1", "10"},
				{`SUBJECT "=?utf-8?"`, "", ""},
				{`HEADER X-Tag "déjà vu"`, "2", "20"},
				{`HEADER X-Tag "E9"`, "", ""},
				{`OR BODY "zürich" BODY "köln"`, "1 2", "10 20"},
				{`NOT BODY "grüße"`, "2 4 5", "20 40 50"},
			} {
				t.Run(tc.query, func(t *testing.T) {
					requireLiteralSearch(t, c, "SEARCH CHARSET UTF-8 "+tc.query, strings.TrimSpace("* SEARCH "+tc.seqs))
					requireLiteralSearch(t, c, "UID SEARCH CHARSET UTF-8 "+tc.query, strings.TrimSpace("* SEARCH "+tc.uids))
				})
			}
			lines, tagged := c.command("F1", "FETCH 1:* FLAGS")
			requireOK(t, tagged, "F1")
			for i, l := range lines {
				if strings.Contains(l, `\Seen`) {
					t.Fatalf("SEARCH set Seen: line %d %q", i, l)
				}
			}
		})
	}
}
