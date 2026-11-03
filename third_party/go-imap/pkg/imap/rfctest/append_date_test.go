package rfctest

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// RFC 3501 §9 date-time has fixed-width fields, a space-padded or two-digit
// day, no quoted escapes, and no fractional seconds. Refusal must precede the
// continuation and leave synchronizing commands usable; unread LITERAL+ data
// instead requires the existing BYE policy.
//
// checklist: IMAP-APPEND-29
func TestAppendInvalidDateTime(t *testing.T) {
	for _, date := range []string{
		`"7-Feb-1994 21:52:25 -0800"`,
		`"  7-Feb-1994 21:52:25 -0800"`,
		`"07-Feb-1994 21:52:25 -0800 "`,
		`"07-Feb-1994 21:52:25 -0800\t"`,
		"\"07-Feb-1994 21:52:25 -0800\t\"",
		"\"\t7-Feb-1994 21:52:25 -0800\"",
		`"０7-Feb-1994 21:52:25 -0800"`,
		`"07-Feb-1994 21:52:25.123 -0800"`,
		`"07-Feb-1994 21:52:25,123 -0800"`,
		`"07-Feb-1994 1:52:25 -0800"`,
		`"07-Feb-1994 21:2:25 -0800"`,
		`"07-Feb-1994 21:52:5 -0800"`,
		`"07-Feb-1994  21:52:25 -0800"`,
		`"07-Feb-1994 21:52:25  -0800"`,
		"\"07-Feb-1994\t21:52:25 -0800\"",
		"\"07-Feb-1994 21:52:25\t-0800\"",
		`"07-Feb-1994 21:52:25 -08:00"`,
		`"07-Feb-1994 21:52:25 0800"`,
		`"07-Feb-1994 21:52:25 Z"`,
		`"07-Feb-1994 21:52:25 +2400"`,
		`"07-Feb-1994 21:52:25 -0860"`,
		`"07-Feb-1994 24:52:25 -0800"`,
		`"07-Feb-1994 21:60:25 -0800"`,
		`"07-Feb-1994 21:52:60 -0800"`,
		`"00-Feb-1994 21:52:25 -0800"`,
		`"30-Feb-1994 21:52:25 -0800"`,
		`"29-Feb-1900 21:52:25 -0800"`,
		`"31-Apr-1994 21:52:25 -0800"`,
		`"07-Foo-1994 21:52:25 -0800"`,
		`"0x-Feb-1994 21:52:25 -0800"`,
		`"07-Feb-199x 21:52:25 -0800"`,
		`"07-Feb-1994 2x:52:25 -0800"`,
		`"07-Feb-1994 21:52:25 -0x00"`,
		`"07/Feb-1994 21:52:25 -0800"`,
		`"07-Feb/1994 21:52:25 -0800"`,
		`"07-Feb-1994 21-52:25 -0800"`,
		`"07-Feb-1994 21:52:25 ?0800"`,
		`"07-Feb-94 21:52:25 -0800"`,
		`"07-Feb-01994 21:52:25 -0800"`,
		`"\07-Feb-1994 21:52:25 -0800"`,
		`"07-\Feb-1994 21:52:25 -0800"`,
		`"07-Feb-1994\ 21:52:25 -0800"`,
		`"07-Feb-1994 21:52:25 \-0800"`,
		`07-Feb-1994 21:52:25 -0800`,
		`""`,
		`"07-Feb-1994 21:52:25 -0800`,
	} {
		for _, mailbox := range []string{"INBOX", "{5+}\r\nINBOX"} {
			for _, flags := range []string{"", `(\Seen) `} {
				for _, nonSync := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/mailbox=%q/flags=%t/nonSync=%t", date, mailbox, flags != "", nonSync), func(t *testing.T) {
						box := &datedAppendMailbox{&appendMailbox{}}
						c := selectionClient(t, box)
						marker := "{3}"
						if nonSync {
							marker = "{3+}"
						}
						if _, err := fmt.Fprintf(c.conn, "A1 APPEND %s %s%s %s\r\n", mailbox, flags, date, marker); err != nil {
							t.Fatal(err)
						}
						if line := c.line(); !strings.HasPrefix(line, "A1 BAD ") {
							t.Fatalf("invalid date-time: %q, want BAD before continuation", line)
						}
						if len(box.records) != 0 || box.dateCalls != 0 {
							t.Fatalf("invalid date reached backend: records=%+v dated calls=%d", box.records, box.dateCalls)
						}
						if nonSync {
							if line := c.line(); !strings.HasPrefix(line, "* BYE ") {
								t.Fatalf("unread literal: %q", line)
							}
							if _, err := c.r.ReadByte(); err != io.EOF {
								t.Fatalf("after BYE: %v, want EOF", err)
							}
						} else {
							_, tagged := c.command("N1", "NOOP")
							requireOK(t, tagged, "N1")
							_, tagged = appendCommand(t, c, `INBOX "07-Feb-1994 21:52:25 -0800"`, "msg")
							requireOK(t, tagged, "A1")
						}
						want := 1
						if nonSync {
							want = 0
						}
						if len(box.records) != want {
							t.Fatalf("backend records: %+v, want %d successful retries only", box.records, want)
						}
					})
				}
			}
		}
	}
}

// Valid fixed-width dates preserve both the calendar components and the numeric
// UTC offset at the DateAppender boundary, with and without flags or LITERAL+.
//
// checklist: IMAP-APPEND-29
func TestAppendValidDateTime(t *testing.T) {
	for _, tc := range []struct {
		date string
		want time.Time
	}{
		{" 7-Feb-1994 21:52:25 -0800", time.Date(1994, 2, 7, 21, 52, 25, 0, time.FixedZone("", -8*3600))},
		{"07-fEb-1994 21:52:25 +0530", time.Date(1994, 2, 7, 21, 52, 25, 0, time.FixedZone("", 19800))},
		{"29-Feb-2000 00:00:00 +0000", time.Date(2000, 2, 29, 0, 0, 0, 0, time.UTC)},
		{"31-Dec-9999 23:59:59 -2359", time.Date(9999, 12, 31, 23, 59, 59, 0, time.FixedZone("", -86340))},
		{"01-Jan-0000 00:00:00 +2359", time.Date(0, 1, 1, 0, 0, 0, 0, time.FixedZone("", 86340))},
		{"01-Jan-0001 00:00:00 +0000", time.Time{}},
	} {
		for _, mailbox := range []string{"INBOX", "{5+}\r\nINBOX"} {
			for _, flags := range []string{"", `(\Seen) `} {
				for _, nonSync := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/mailbox=%q/flags=%t/nonSync=%t", tc.date, mailbox, flags != "", nonSync), func(t *testing.T) {
						box := &datedAppendMailbox{&appendMailbox{}}
						c := selectionClient(t, box)
						args := mailbox + ` ` + flags + `"` + tc.date + `"`
						if nonSync {
							if _, err := fmt.Fprintf(c.conn, "A1 APPEND %s {3+}\r\nmsg\r\nN1 NOOP\r\n", args); err != nil {
								t.Fatal(err)
							}
							_, tagged := appendCompletion(t, c)
							requireOK(t, tagged, "A1")
							requireOK(t, c.line(), "N1")
						} else {
							_, tagged := appendCommand(t, c, args, "msg")
							requireOK(t, tagged, "A1")
						}
						if len(box.records) != 1 || box.dateCalls != 1 {
							t.Fatalf("backend records: %+v, dated calls: %d, want one", box.records, box.dateCalls)
						}
						got := box.records[0]
						_, gotOffset := got.date.Zone()
						_, wantOffset := tc.want.Zone()
						if got.folder != "INBOX" || got.raw != "msg" || !got.date.Equal(tc.want) || gotOffset != wantOffset || got.date.Nanosecond() != 0 {
							t.Fatalf("record: %+v, want date %v", got, tc.want)
						}
						if flags != "" && (got.flags.Seen == nil || !*got.flags.Seen) {
							t.Fatalf("Seen not preserved: %+v", got.flags)
						}
					})
				}
			}
		}
	}
}
