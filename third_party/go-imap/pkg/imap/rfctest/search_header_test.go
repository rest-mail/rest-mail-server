package rfctest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// headerSearchMailbox holds four messages whose header fields, not their
// metadata, decide HEADER/CC/BCC and SENT* matches. Every internal date is in
// 2030, so a SENT* match can only come from the Date: header.
//
//	UID10  Date 10-Jan-2025 23:30 -0500 (11-Jan in UTC); Cc carol; X-Tag Red
//	UID20  Date 15-Jun-2025; Bcc dave; empty X-Tag; Cc folded over two lines
//	UID30  no Date:; X-Other red; header-like text only in the body
//	UID40  unparseable Date:
func headerSearchMailbox() *fetchMailbox {
	raws := map[uint32]string{
		10: "Date: Fri, 10 Jan 2025 23:30:00 -0500\r\nSubject: one\r\nCc: Carol <carol@example.com>\r\nX-Tag: Red\r\n\r\nCc: nobody@example.com\r\n",
		20: "Date: 15 Jun 2025 09:00:00 +0000\r\nSubject: two\r\nBcc: dave@example.com\r\nx-tag:\r\nCC: erin@example.org,\r\n carol-two@example.net\r\n\r\nhello\r\n",
		30: "Subject: three\r\nX-Other: red\r\n\r\nX-Tag: red\r\nDate: 15 Jun 2025 09:00:00 +0000\r\n",
		40: "Date: not a date\r\nSubject: four\r\n\r\nhello\r\n",
	}
	box := newFetchMailbox()
	box.messages = nil
	for _, uid := range []uint32{10, 20, 30, 40} {
		box.messages = append(box.messages, imap.Message{UID: uid, Size: len(raws[uid]), Date: time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)})
	}
	box.raw = raws
	return box
}

// TestSearchHeaderKeys checks that HEADER, CC and BCC search the text after
// the colon of the named header fields only: field names and strings compare
// case-insensitively, folded values are unfolded, the body is never searched,
// and an empty string matches any message that has the field.
//
// checklist: IMAP-SEARCH-19
// checklist: IMAP2-CMD-145
func TestSearchHeaderKeys(t *testing.T) {
	box := headerSearchMailbox()
	c := fetchClient(t, box, "EXAMINE")
	for _, tc := range []struct{ query, seqs, uids string }{
		{`HEADER X-Tag red`, "1", "10"},
		{`HEADER x-TAG "RED"`, "1", "10"},
		{`HEADER X-Tag ""`, "1 2", "10 20"},
		{`HEADER X-Missing ""`, "", ""},
		{`HEADER Subject two`, "2", "20"},
		{`HEADER "X-Other" red`, "3", "30"},
		{`HEADER X-Tag "X-Tag"`, "", ""},
		{`header date "15 jun"`, "2", "20"},
		{`CC carol`, "1 2", "10 20"},
		{`cc "EXAMPLE.NET"`, "2", "20"},
		{`CC "org, carol-two"`, "2", "20"},
		{`CC nobody`, "", ""},
		{`CC ""`, "1 2", "10 20"},
		{`BCC dave`, "2", "20"},
		{`BCC carol`, "", ""},
		{`NOT CC carol`, "3 4", "30 40"},
		{`OR HEADER X-Other red BCC dave`, "2 3", "20 30"},
		{`(CC carol UID 20)`, "2", "20"},
		{`CC carol 1`, "1", "10"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			requireSearch(t, c, "SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.seqs))
			requireSearch(t, c, "UID SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.uids))
		})
	}
	if len(box.updates) != 0 {
		t.Fatalf("header search wrote flags: %v", box.updates)
	}
}

// TestSearchSentDates checks that SENTBEFORE, SENTON and SENTSINCE compare the
// Date: header's calendar date as written, disregarding its time and timezone,
// never the internal date; a message without a parseable Date: header has no
// sent date and matches none of them.
//
// checklist: IMAP-SEARCH-20
func TestSearchSentDates(t *testing.T) {
	box := headerSearchMailbox()
	c := fetchClient(t, box, "SELECT")
	for _, tc := range []struct{ query, seqs, uids string }{
		{"SENTON 10-Jan-2025", "1", "10"},
		{"SENTON 11-Jan-2025", "", ""},
		{"SENTON 15-Jun-2025", "2", "20"},
		{"SENTBEFORE 15-Jun-2025", "1", "10"},
		{"SENTBEFORE 16-Jun-2025", "1 2", "10 20"},
		{"SENTBEFORE 10-Jan-2025", "", ""},
		{"SENTSINCE 15-Jun-2025", "2", "20"},
		{"SENTSINCE 11-Jan-2025", "2", "20"},
		{"SENTSINCE 10-Jan-2025", "1 2", "10 20"},
		{"sentsince 1-Jan-2000", "1 2", "10 20"},
		{"NOT SENTSINCE 1-Jan-2000", "3 4", "30 40"},
		{"SENTSINCE 1-Jan-2026", "", ""},
		{"SINCE 1-Jan-2030", "1 2 3 4", "10 20 30 40"},
		{"OR SENTON 10-Jan-2025 SENTON 15-Jun-2025", "1 2", "10 20"},
		{"(SENTSINCE 1-Jan-2000 SENTBEFORE 1-Feb-2025)", "1", "10"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			requireSearch(t, c, "SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.seqs))
			requireSearch(t, c, "UID SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.uids))
		})
	}
	if len(box.updates) != 0 {
		t.Fatalf("sent-date search wrote flags: %v", box.updates)
	}
}

// TestSearchHeaderUnavailable checks that a message whose content the backend
// cannot supply matches no header or sent-date key, while other messages still
// match.
//
// checklist: IMAP-SEARCH-19
// checklist: IMAP-SEARCH-20
func TestSearchHeaderUnavailable(t *testing.T) {
	box := headerSearchMailbox()
	box.failUID = 10
	c := fetchClient(t, box, "SELECT")
	requireSearch(t, c, "UID SEARCH CC carol", "* SEARCH 20")
	requireSearch(t, c, `UID SEARCH HEADER X-Tag ""`, "* SEARCH 20")
	requireSearch(t, c, "UID SEARCH SENTSINCE 1-Jan-2000", "* SEARCH 20")
}

// TestSearchInvalidHeaderKeys rejects missing or malformed operands with BAD
// before any message content is read, even inside Boolean/groups or in an
// empty mailbox, and keeps the session usable.
//
// checklist: IMAP-SEARCH-21
func TestSearchInvalidHeaderKeys(t *testing.T) {
	for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
		for _, empty := range []bool{false, true} {
			box := headerSearchMailbox()
			if empty {
				box.messages = nil
			}
			c := fetchClient(t, box, "SELECT")
			for _, key := range []string{"HEADER", "HEADER X-Tag", `HEADER "" red`, "HEADER ( red", "CC", "BCC", "SENTON", "SENTBEFORE", "SENTSINCE", "SENTON 32-Jan-2025", "SENTBEFORE notadate", `SENTSINCE "1-Foo-2025"`} {
				for _, form := range []string{"%s", "NOT %s", "OR ALL %s", "(ALL %s)"} {
					query := fmt.Sprintf(form, key)
					t.Run(fmt.Sprintf("%s/empty=%t/%s", prefix, empty, query), func(t *testing.T) {
						lines, tagged := c.command("Q1", "%s %s", prefix, query)
						if !strings.HasPrefix(tagged, "Q1 BAD ") || len(lines) != 0 {
							t.Errorf("invalid key: lines=%q tagged=%q, want only BAD", lines, tagged)
						}
						if len(box.fetches) != 0 || len(box.updates) != 0 {
							t.Fatalf("invalid search accessed backend: fetches=%v updates=%v", box.fetches, box.updates)
						}
						lines, tagged = c.command("N1", "NOOP")
						requireOK(t, tagged, "N1")
						requireExactly(t, lines)
					})
				}
			}
			want := "* SEARCH"
			if !empty {
				want += " 2"
				if prefix == "UID SEARCH" {
					want = "* SEARCH 20"
				}
			}
			requireSearch(t, c, prefix+" BCC dave", want)
		}
	}
}
