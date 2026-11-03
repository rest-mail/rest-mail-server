package rfctest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

func searchClient(t *testing.T, messages []imap.Message) *client {
	t.Helper()
	c := selectionClient(t, &selectionMailbox{folders: map[string][]imap.Message{"INBOX": messages}})
	_, tagged := c.command("S1", "SELECT INBOX")
	requireOK(t, tagged, "S1")
	return c
}

func requireSearch(t *testing.T, c *client, command, want string) {
	t.Helper()
	lines, tagged := c.command("Q1", "%s", command)
	requireOK(t, tagged, "Q1")
	requireExactly(t, lines, want)
}

// TestSearchExample replays the first two RFC 3501 §6.4.4 examples. Every
// predicate has a near miss, so dropping one changes the returned subset.
// The literal UTF-8 example is outside this slice.
//
// checklist: IMAP-SEARCH-01 IMAP-SEARCH-02 IMAP-SEARCH-03
func TestSearchExample(t *testing.T) {
	messages := make([]imap.Message, 882)
	for i := range messages {
		messages[i] = imap.Message{UID: uint32(1000 + i*2), Date: time.Date(1994, 1, 31, 12, 0, 0, 0, time.UTC)}
	}
	for _, seq := range []int{2, 84, 882} {
		messages[seq-1].Flagged = true
		messages[seq-1].Date = time.Date(1994, 2, 1, 12, 0, 0, 0, time.UTC)
		messages[seq-1].From.Email = "jones@example.com"
	}
	messages[2].Flagged = true          // Too early.
	messages[3].Date = messages[1].Date // Not flagged.
	messages[4] = messages[1]
	messages[4].UID = 1008
	messages[4].From.Email = "Smith@example.com" // Excluded by NOT.
	c := searchClient(t, messages)
	lines, tagged := c.command("A282", `SEARCH FLAGGED SINCE 1-Feb-1994 NOT FROM "Smith"`)
	requireOK(t, tagged, "A282")
	requireExactly(t, lines, "* SEARCH 2 84 882")
	lines, tagged = c.command("A283", `SEARCH TEXT "string not in mailbox"`)
	requireOK(t, tagged, "A283")
	requireExactly(t, lines, "* SEARCH")
}

// TestSearchBooleanKeys proves intersection, grouping, binary OR and unary NOT
// with sparse UIDs, so UID SEARCH must change only the result identifiers.
//
// checklist: IMAP-SEARCH-02 IMAP-SEARCH-03 IMAP-SEARCH-04
func TestSearchBooleanKeys(t *testing.T) {
	c := searchClient(t, []imap.Message{
		{UID: 10, Seen: true, Subject: "Quarterly Report", From: imap.Address{Email: "anne@example.com"}},
		{UID: 40, Flagged: true, Subject: "Lunch plans", From: imap.Address{Email: "bob@example.com"}},
		{UID: 90, Subject: "Vacation photos", From: imap.Address{Email: "anne@example.com"}},
	})
	for _, tc := range []struct{ query, seqs, uids string }{
		{"ALL", "1 2 3", "10 40 90"},
		{`FROM "ANNE" UNSEEN`, "3", "90"},
		{`(FROM "ANNE" UNSEEN)`, "3", "90"},
		{`OR (FROM "bob") (SUBJECT "REPORT")`, "1 2", "10 40"},
		{`NOT OR SEEN FLAGGED`, "3", "90"},
		{`NOT (FROM "anne" UNSEEN)`, "1 2", "10 40"},
		{`SUBJECT "absent"`, "", ""},
	} {
		t.Run(tc.query, func(t *testing.T) {
			requireSearch(t, c, "SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.seqs))
			requireSearch(t, c, "UID SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.uids))
		})
	}
}

// TestSearchSequenceAndUIDKeys proves bare sequence keys remain sequence
// numbers even under UID SEARCH, and UID keys always address UIDs.
//
// checklist: IMAP-SEARCH-04 IMAP-SEARCH-05
func TestSearchSequenceAndUIDKeys(t *testing.T) {
	c := searchClient(t, []imap.Message{{UID: 10}, {UID: 40}, {UID: 90}})
	for _, tc := range []struct{ query, seqs, uids string }{
		{"2", "2", "40"}, {"3:1", "1 2 3", "10 40 90"},
		{"2:*", "2 3", "40 90"}, {"UID 40:90", "2 3", "40 90"},
		{"UID 90:40", "2 3", "40 90"}, {"UID *", "3", "90"},
		{"UID 11:39", "", ""}, {"2:* UID 10:40", "2", "40"},
		{"OR 1 UID 90", "1 3", "10 90"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			requireSearch(t, c, "SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.seqs))
			requireSearch(t, c, "UID SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.uids))
		})
	}
}

// TestSearchCalendarDates distinguishes a message's calendar date from its UTC
// instant, covering both offset directions and times within the boundary day.
//
// checklist: IMAP-SEARCH-06
func TestSearchCalendarDates(t *testing.T) {
	var messages []imap.Message
	for i, value := range []string{
		"2025-06-14T23:30:00-12:00", // UTC is the next day; local date is before.
		"2025-06-15T00:30:00+14:00", // UTC is the previous day; local date is on.
		"2025-06-15T23:30:00-12:00", // UTC is the next day; local date is on.
		"2025-06-16T00:30:00+14:00", // UTC is the boundary day; local date is after.
		"2025-06-15T00:00:00Z",
		"2025-06-15T23:59:59Z",
	} {
		date, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, imap.Message{UID: uint32(10 + i*10), Date: date})
	}
	c := searchClient(t, messages)
	for _, tc := range []struct{ query, seqs, uids string }{
		{"BEFORE 15-Jun-2025", "1", "10"},
		{"ON 15-Jun-2025", "2 3 5 6", "20 30 50 60"},
		{"SINCE 15-Jun-2025", "2 3 4 5 6", "20 30 40 50 60"},
		{"SINCE 15-Jun-2025 BEFORE 16-Jun-2025", "2 3 5 6", "20 30 50 60"},
		{"NOT ON 15-Jun-2025", "1 4", "10 40"},
		{`ON "15-Jun-2025"`, "2 3 5 6", "20 30 50 60"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			requireSearch(t, c, "SEARCH "+tc.query, "* SEARCH "+tc.seqs)
			requireSearch(t, c, "UID SEARCH "+tc.query, "* SEARCH "+tc.uids)
		})
	}
}

// TestSearchCharsets proves mandatory US-ASCII acceptance, supported UTF-8
// metadata matching and the unsupported-charset response, followed by recovery.
// Its 8-bit UTF-8 operand uses a literal; MIME decoding is proved separately.
//
// checklist: IMAP-SEARCH-07 IMAP-RESP-06
// checklist: IMAP2-CMD-143 IMAP2-CMD-144
func TestSearchCharsets(t *testing.T) {
	c := searchClient(t, []imap.Message{{UID: 10, Subject: "hello"}, {UID: 40, Subject: "café"}})
	for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
		for _, charset := range []string{"US-ASCII", `"us-ascii"`, "UTF-8"} {
			want := "* SEARCH 1"
			if prefix == "UID SEARCH" {
				want = "* SEARCH 10"
			}
			requireSearch(t, c, prefix+" CHARSET "+charset+` SUBJECT "HELLO"`, want)
		}
		want := "* SEARCH 2"
		if prefix == "UID SEARCH" {
			want = "* SEARCH 40"
		}
		requireLiteralSearch(t, c, prefix+` CHARSET UTF-8 SUBJECT "café"`, want)
		lines, tagged := c.command("Q1", "%s CHARSET ISO-8859-1 ALL", prefix)
		requireExactly(t, lines)
		if !strings.HasPrefix(tagged, "Q1 NO [BADCHARSET (US-ASCII UTF-8)] ") {
			t.Fatal(tagged)
		}
		want = "* SEARCH 1 2"
		if prefix == "UID SEARCH" {
			want = "* SEARCH 10 40"
		}
		requireSearch(t, c, prefix+" ALL", want)
	}
}

// TestSearchInvalidCriteria proves parse failures produce no partial results
// and do not prevent a later valid search. Quoted-string grammar edge cases
// remain outside this partial inventory.
//
// checklist: IMAP-SEARCH-08
func TestSearchInvalidCriteria(t *testing.T) {
	c := searchClient(t, []imap.Message{{UID: 10}})
	for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
		for i, query := range []string{"", "CHARSET US-ASCII", "()", "OR ALL ()", "NOT ()", "UNKNOWN", "FROM", "SINCE 32-Jan-2025", "OR ALL", "NOT", "(ALL", "ALL)", "CHARSET"} {
			t.Run(fmt.Sprintf("%s/%d", prefix, i), func(t *testing.T) {
				lines, tagged := c.command("Q1", "%s %s", prefix, query)
				requireExactly(t, lines)
				if !strings.HasPrefix(tagged, "Q1 BAD ") {
					t.Fatalf("%s %s: %q", prefix, query, tagged)
				}
			})
		}
		want := "* SEARCH 1"
		if prefix == "UID SEARCH" {
			want = "* SEARCH 10"
		}
		requireSearch(t, c, prefix+" ALL", want)
	}
}

// TestSearchRequiresSelection proves neither form searches before selecting a
// mailbox, and an empty selected mailbox still yields an empty SEARCH response.
//
// checklist: IMAP-SEARCH-01 IMAP-SEARCH-09
// checklist: IMAP2-CORE-22
func TestSearchRequiresSelection(t *testing.T) {
	c := selectionClient(t, &selectionMailbox{folders: map[string][]imap.Message{"INBOX": nil}})
	for _, command := range []string{"SEARCH ALL", "UID SEARCH ALL"} {
		lines, tagged := c.command("Q1", "%s", command)
		requireExactly(t, lines)
		if strings.HasPrefix(tagged, "Q1 OK ") {
			t.Fatal(tagged)
		}
	}
	_, tagged := c.command("S1", "SELECT INBOX")
	requireOK(t, tagged, "S1")
	requireSearch(t, c, "SEARCH ALL", "* SEARCH")
	requireSearch(t, c, "UID SEARCH ALL", "* SEARCH")
}
