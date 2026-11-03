package rfctest

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

const fetchHeader = "Date: Mon, 07 Feb 1994 21:52:25 -0800\r\nFrom: alice@example.com\r\nSubject: folded\r\n\tvalue\r\nX-From: preserve me\r\n\r\n"
const fetchText = "caf\xc3\xa9\r\n.hello\r\nF1 OK forged\r\n"
const fetchRaw = fetchHeader + fetchText

type fetchMailbox struct {
	*storeMailbox
	raw     map[uint32]string
	failUID uint32
	fetches []uint32
}

func newFetchMailbox() *fetchMailbox {
	return &fetchMailbox{
		storeMailbox: &storeMailbox{
			messages: []imap.Message{{UID: 10, Size: len(fetchRaw), Date: time.Date(1994, 2, 7, 21, 52, 25, 0, time.FixedZone("PST", -8*3600))}},
			updates:  map[uint32][]imap.FlagUpdate{},
		},
		raw: map[uint32]string{10: fetchRaw},
	}
}

func (m *fetchMailbox) Fetch(uid uint32) ([]byte, error) {
	m.fetches = append(m.fetches, uid)
	if uid == m.failUID {
		return nil, errors.New("private backend failure")
	}
	return []byte(m.raw[uid]), nil
}

func fetchClient(t *testing.T, box *fetchMailbox, selection string) *client {
	t.Helper()
	c := selectionClient(t, box)
	_, tagged := c.command("S1", "%s INBOX", selection)
	requireOK(t, tagged, "S1")
	return c
}

type fetchReply struct {
	// framing contains only protocol lines, never message payload bytes.
	framing  string
	literals []string
}

// fetchCommand consumes literals by their declared octet counts, so CRLF,
// dot-leading lines and text resembling tagged responses remain opaque data.
// It also supports multiple literals in one FETCH and multiple FETCH replies.
func fetchCommand(t *testing.T, c *client, command string) ([]fetchReply, string) {
	t.Helper()
	if _, err := fmt.Fprintf(c.conn, "F1 %s\r\n", command); err != nil {
		t.Fatal(err)
	}
	var replies []fetchReply
	for {
		line := c.line()
		if strings.HasPrefix(line, "F1 ") {
			return replies, line
		}
		if !strings.HasPrefix(line, "* ") || !strings.Contains(line, " FETCH (") {
			t.Fatalf("unexpected FETCH response: %q", line)
		}
		reply := fetchReply{}
		for {
			reply.framing += line
			if !strings.HasSuffix(line, "}") {
				if !strings.HasSuffix(line, ")") {
					t.Fatalf("unterminated FETCH: %q", line)
				}
				break
			}
			start := strings.LastIndexByte(line, '{')
			if start < 0 {
				t.Fatalf("missing literal marker: %q", line)
			}
			n, err := strconv.Atoi(line[start+1 : len(line)-1])
			if err != nil || n < 0 || n > 1<<20 {
				t.Fatalf("invalid fixture literal count: %q", line)
			}
			payload := make([]byte, n)
			if _, err := io.ReadFull(c.r, payload); err != nil {
				t.Fatalf("read literal: %v", err)
			}
			reply.literals = append(reply.literals, string(payload))
			line = c.line()
		}
		replies = append(replies, reply)
	}
}

// TestFetchSections proves exact section octets and the RFC822 aliases, not
// decoded/rendered text. A UTF-8 body and forged response line exercise framing.
//
// checklist: IMAP-FETCH-01 IMAP-FETCH-02 IMAP-FETCH-03
// checklist: IMAP2-CMD-151
// checklist: IMAP2-RESP-207
func TestFetchSections(t *testing.T) {
	for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
		for _, tc := range []struct {
			item, label, payload string
			seen                 bool
		}{
			{"BODY[]", "BODY[]", fetchRaw, true},
			{"BODY.PEEK[]", "BODY[]", fetchRaw, false},
			{"BODY[HEADER]", "BODY[HEADER]", fetchHeader, true},
			{"BODY.PEEK[HEADER]", "BODY[HEADER]", fetchHeader, false},
			{"BODY[TEXT]", "BODY[TEXT]", fetchText, true},
			{"RFC822", "RFC822", fetchRaw, true},
			{"RFC822.HEADER", "RFC822.HEADER", fetchHeader, false},
			{"RFC822.TEXT", "RFC822.TEXT", fetchText, true},
		} {
			t.Run(prefix+"/"+tc.item, func(t *testing.T) {
				box := newFetchMailbox()
				c := fetchClient(t, box, "SELECT")
				replies, tagged := fetchCommand(t, c, prefix+" "+tc.item)
				requireOK(t, tagged, "F1")
				if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{tc.payload}) {
					t.Fatalf("literal replies: %#v", replies)
				}
				wantFlags := "FLAGS ()"
				if tc.seen {
					wantFlags = `FLAGS (\Seen)`
				}
				for _, want := range []string{"* 1 FETCH (", "UID 10", wantFlags, fmt.Sprintf("%s {%d}", tc.label, len(tc.payload))} {
					if !strings.Contains(replies[0].framing, want) {
						t.Fatalf("framing %q lacks %q", replies[0].framing, want)
					}
				}
				if strings.Contains(replies[0].framing, "PEEK") {
					t.Fatal("PEEK leaked into response name")
				}
				wantWrites := 0
				if tc.seen {
					wantWrites = 1
				}
				updates := box.updates[10]
				if len(updates) != wantWrites || (tc.seen && (updates[0].Seen == nil || !*updates[0].Seen)) {
					t.Fatalf("Seen writes: %#v", box.updates)
				}
				lines, tagged := c.command("N1", "FETCH 1 FLAGS")
				requireOK(t, tagged, "N1")
				if len(lines) != 1 || !strings.Contains(lines[0], wantFlags) {
					t.Fatalf("session flags: %q", lines)
				}
			})
		}
	}
}

// TestFetchPartialOctets checks zero-based byte slicing, EOF truncation and
// origin labels, including ranges that split a UTF-8 character.
//
// checklist: IMAP-FETCH-04
// checklist: IMAP2-RESP-207
func TestFetchPartialOctets(t *testing.T) {
	for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
		for _, tc := range []struct{ suffix, origin, payload string }{
			{"<0.999>", "<0>", fetchText},
			{"<3.1>", "<3>", "\xc3"},
			{"<4.3>", "<4>", "\xa9\r\n"},
			{"<999.2>", "<999>", ""},
			{fmt.Sprintf("<%d.2>", len(fetchText)), fmt.Sprintf("<%d>", len(fetchText)), ""},
			{"", "", fetchText},
		} {
			t.Run(prefix+"/"+tc.suffix, func(t *testing.T) {
				box := newFetchMailbox()
				c := fetchClient(t, box, "SELECT")
				replies, tagged := fetchCommand(t, c, prefix+" BODY.PEEK[TEXT]"+tc.suffix)
				requireOK(t, tagged, "F1")
				if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{tc.payload}) {
					t.Fatalf("replies: %#v", replies)
				}
				want := fmt.Sprintf(" BODY[TEXT]%s {%d}", tc.origin, len(tc.payload))
				if !strings.Contains(replies[0].framing, want) {
					t.Fatalf("framing %q lacks %q", replies[0].framing, want)
				}
				if len(box.updates) != 0 {
					t.Fatal("PEEK changed flags")
				}
			})
		}
	}
}

// TestFetchHeaderExample replays §6.4.5 A654 with concrete headers, and checks
// exact case-insensitive field names, folded continuations and post-filter ranges.
//
// checklist: IMAP-FETCH-05 IMAP-FETCH-06
func TestFetchHeaderExample(t *testing.T) {
	box := newFetchMailbox()
	box.messages = append(box.messages, imap.Message{UID: 40}, imap.Message{UID: 90}, imap.Message{UID: 120})
	for _, uid := range []uint32{40, 90, 120} {
		box.raw[uid] = fetchRaw
	}
	c := fetchClient(t, box, "SELECT")
	replies, tagged := fetchCommand(t, c, "FETCH 2:4 (FLAGS BODY[HEADER.FIELDS (DATE FROM)])")
	requireOK(t, tagged, "F1")
	want := "Date: Mon, 07 Feb 1994 21:52:25 -0800\r\nFrom: alice@example.com\r\n\r\n"
	if len(replies) != 3 {
		t.Fatalf("replies: %#v", replies)
	}
	for i, reply := range replies {
		if !strings.HasPrefix(reply.framing, fmt.Sprintf("* %d FETCH (", i+2)) || !slices.Equal(reply.literals, []string{want}) {
			t.Fatalf("reply: %#v", reply)
		}
	}
	for _, tc := range []struct{ item, want string }{
		{"BODY.PEEK[HEADER.FIELDS (sUbJeCt)]", "Subject: folded\r\n\tvalue\r\n\r\n"},
		{"BODY.PEEK[HEADER.FIELDS.NOT (DATE FROM SUBJECT)]", "X-From: preserve me\r\n\r\n"},
		{"BODY.PEEK[HEADER.FIELDS (missing)]", "\r\n"},
		{"BODY.PEEK[HEADER.FIELDS (FROM)]<0.5>", "From:"},
	} {
		replies, tagged = fetchCommand(t, c, "FETCH 1 "+tc.item)
		requireOK(t, tagged, "F1")
		if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{tc.want}) {
			t.Fatalf("%s: %#v", tc.item, replies)
		}
	}
}

// TestFetchMetadataAndMacros checks stored metadata, macro expansion and that
// metadata/peek fetches do not write Seen. Multiple body items share one raw load.
//
// checklist: IMAP-FETCH-07 IMAP-FETCH-08
func TestFetchMetadataAndMacros(t *testing.T) {
	for _, item := range []string{"FAST", "ALL", "FULL", "(UID FLAGS INTERNALDATE RFC822.SIZE)", "(BODY.PEEK[HEADER] BODY.PEEK[TEXT])"} {
		t.Run(item, func(t *testing.T) {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			replies, tagged := fetchCommand(t, c, "FETCH 1 "+item)
			requireOK(t, tagged, "F1")
			if len(replies) != 1 {
				t.Fatalf("replies: %#v", replies)
			}
			framing := replies[0].framing
			if !strings.Contains(framing, "FLAGS ()") {
				t.Fatalf("flags: %q", framing)
			}
			if strings.Contains(item, "PEEK") {
				if !slices.Equal(replies[0].literals, []string{fetchHeader, fetchText}) {
					t.Fatalf("literals: %#v", replies[0])
				}
			} else {
				for _, want := range []string{fmt.Sprintf("RFC822.SIZE %d", len(fetchRaw)), `INTERNALDATE "07-Feb-1994 21:52:25 -0800"`} {
					if !strings.Contains(framing, want) {
						t.Fatalf("%q lacks %q", framing, want)
					}
				}
				if strings.Contains(framing, "ENVELOPE (") != (item == "ALL" || item == "FULL") || strings.Contains(framing, " BODY (") != (item == "FULL") {
					t.Fatalf("macro expansion: %q", framing)
				}
			}
			wantFetches := 0
			if item == "ALL" || item == "FULL" || strings.Contains(item, "PEEK") {
				wantFetches = 1
			}
			if len(box.fetches) != wantFetches || len(box.updates) != 0 {
				t.Fatalf("fetches=%v, updates=%v", box.fetches, box.updates)
			}
		})
	}
}

// TestUIDFetchExample proves §6.4.8's sparse UID example, sequence-number
// response prefixes and implicit UID items, plus nonexistent UID no-ops.
//
// checklist: IMAP-FETCH-09
// checklist: IMAP2-CMD-151 IMAP2-CMD-166
func TestUIDFetchExample(t *testing.T) {
	box := newFetchMailbox()
	box.messages = make([]imap.Message, 25)
	for i := range box.messages {
		box.messages[i] = imap.Message{UID: uint32(i + 1), Seen: true}
	}
	for i, uid := range []uint32{4827313, 4827943, 4828442} {
		box.messages[22+i].UID = uid
	}
	c := fetchClient(t, box, "SELECT")
	lines, tagged := c.command("A999", "UID FETCH 4827313:4828442 FLAGS")
	requireOK(t, tagged, "A999")
	requireExactly(t, lines, `* 23 FETCH (UID 4827313 FLAGS (\Seen))`, `* 24 FETCH (UID 4827943 FLAGS (\Seen))`, `* 25 FETCH (UID 4828442 FLAGS (\Seen))`)
	lines, tagged = c.command("N1", "UID FETCH 4827314 FLAGS")
	requireOK(t, tagged, "N1")
	requireExactly(t, lines)
	if len(box.fetches)+len(box.updates) != 0 {
		t.Fatal("metadata UID fetch touched body/flags")
	}
}

// TestFetchBackendFailure requires NO rather than a success lacking requested
// data. No failed-message literal or Seen write is emitted; later commands recover.
//
// checklist: IMAP-FETCH-10 IMAP-RESP-10
func TestFetchBackendFailure(t *testing.T) {
	for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
		for _, item := range []string{"BODY[]", "BODY.PEEK[TEXT]", "RFC822", "RFC822.HEADER", "RFC822.TEXT", "BODY", "BODYSTRUCTURE", "ENVELOPE", "FULL", "(FLAGS RFC822.SIZE BODY[])"} {
			t.Run(prefix+"/"+item, func(t *testing.T) {
				box := newFetchMailbox()
				box.failUID = 10
				c := fetchClient(t, box, "SELECT")
				replies, tagged := fetchCommand(t, c, prefix+" "+item)
				if len(replies) != 0 || !strings.HasPrefix(tagged, "F1 NO ") || strings.Contains(tagged, "private") {
					t.Fatalf("failure: %#v %q", replies, tagged)
				}
				if len(box.fetches) != 1 || len(box.updates) != 0 {
					t.Fatalf("fetches=%v updates=%v", box.fetches, box.updates)
				}
				lines, tagged := c.command("N1", "FETCH 1 FLAGS")
				requireOK(t, tagged, "N1")
				requireExactly(t, lines, "* 1 FETCH (UID 10 FLAGS ())")
				box.failUID = 0
				replies, tagged = fetchCommand(t, c, prefix+" BODY.PEEK[]")
				requireOK(t, tagged, "F1")
				if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{fetchRaw}) {
					t.Fatalf("recovery: %#v", replies)
				}
			})
		}
	}
}

// TestFetchSeenFailure proves rejected implicit flag writes do not advertise
// or cache phantom Seen, for both command forms, and that retry can succeed.
//
// checklist: IMAP-FETCH-11
func TestFetchSeenFailure(t *testing.T) {
	for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
		t.Run(prefix, func(t *testing.T) {
			box := newFetchMailbox()
			box.fail = true
			c := fetchClient(t, box, "SELECT")
			replies, tagged := fetchCommand(t, c, prefix+" BODY[]")
			if len(replies) != 0 || !strings.HasPrefix(tagged, "F1 NO ") {
				t.Fatalf("refused Seen write: %#v %q", replies, tagged)
			}
			lines, tagged := c.command("N1", "FETCH 1 FLAGS")
			requireOK(t, tagged, "N1")
			requireExactly(t, lines, "* 1 FETCH (UID 10 FLAGS ())")
			box.fail = false
			replies, tagged = fetchCommand(t, c, prefix+" BODY[]")
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || !strings.Contains(replies[0].framing, `FLAGS (\Seen)`) || !slices.Equal(replies[0].literals, []string{fetchRaw}) {
				t.Fatalf("retry: %#v", replies)
			}
			if updates := box.updates[10]; len(updates) != 1 || updates[0].Seen == nil || !*updates[0].Seen {
				t.Fatalf("updates: %#v", updates)
			}
			replies, tagged = fetchCommand(t, c, prefix+" BODY[]")
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || !strings.Contains(replies[0].framing, `FLAGS (\Seen)`) {
				t.Fatalf("already Seen response: %#v", replies)
			}
			if len(box.updates[10]) != 1 {
				t.Fatal("already Seen fetch repeated the write")
			}
		})
	}
}

// TestFetchExamineEcho proves the flags in the literal-bearing response itself
// stay unchanged on EXAMINE, even when the host would refuse writes.
//
// checklist: IMAP-FETCH-12
func TestFetchExamineEcho(t *testing.T) {
	for _, command := range []string{"FETCH 1 BODY[]", "UID FETCH 10 BODY[]"} {
		t.Run(command, func(t *testing.T) {
			box := newFetchMailbox()
			box.fail = true
			c := fetchClient(t, box, "EXAMINE")
			replies, tagged := fetchCommand(t, c, command)
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || !strings.Contains(replies[0].framing, "FLAGS ()") || !slices.Equal(replies[0].literals, []string{fetchRaw}) {
				t.Fatalf("EXAMINE response: %#v", replies)
			}
			if len(box.updates) != 0 {
				t.Fatal("EXAMINE wrote flags")
			}
		})
	}
}

// TestFetchPartialFailure retains prior successful responses and writes, but
// stops on the first unavailable message and sends NO instead of a final OK.
//
// checklist: IMAP-FETCH-10 IMAP-FETCH-11
func TestFetchPartialFailure(t *testing.T) {
	for _, command := range []string{"FETCH 1:3 BODY[]", "UID FETCH 10:90 BODY[]"} {
		t.Run(command, func(t *testing.T) {
			box := newFetchMailbox()
			box.messages = append(box.messages, imap.Message{UID: 40}, imap.Message{UID: 90})
			box.raw[40], box.raw[90] = fetchRaw, fetchRaw
			box.failUID = 40
			c := fetchClient(t, box, "SELECT")
			replies, tagged := fetchCommand(t, c, command)
			if !strings.HasPrefix(tagged, "F1 NO ") || len(replies) != 1 || !strings.HasPrefix(replies[0].framing, "* 1 FETCH (") || !slices.Equal(replies[0].literals, []string{fetchRaw}) {
				t.Fatalf("partial failure: %#v %q", replies, tagged)
			}
			if !slices.Equal(box.fetches, []uint32{10, 40}) || len(box.updates) != 1 || len(box.updates[10]) != 1 {
				t.Fatalf("fetches=%v updates=%v", box.fetches, box.updates)
			}
			lines, tagged := c.command("N1", "FETCH 1:3 FLAGS")
			requireOK(t, tagged, "N1")
			requireExactly(t, lines, `* 1 FETCH (UID 10 FLAGS (\Seen))`, "* 2 FETCH (UID 40 FLAGS ())", "* 3 FETCH (UID 90 FLAGS ())")
		})
	}
}

// TestFetchRequiresSelection proves neither form can read authenticated account
// data before selection or after UNSELECT, with a successful selected control.
//
// checklist: IMAP-FETCH-13
// checklist: IMAP2-CORE-22
func TestFetchRequiresSelection(t *testing.T) {
	box := newFetchMailbox()
	c := selectionClient(t, box)
	for _, phase := range []string{"authenticated", "unselected"} {
		if phase == "unselected" {
			_, tagged := c.command("S1", "SELECT INBOX")
			requireOK(t, tagged, "S1")
			_, tagged = fetchCommand(t, c, "FETCH 1 BODY.PEEK[]")
			requireOK(t, tagged, "F1")
			_, tagged = c.command("U1", "UNSELECT")
			requireOK(t, tagged, "U1")
		}
		for _, command := range []string{"FETCH 1 BODY[]", "UID FETCH 10 BODY[]"} {
			replies, tagged := fetchCommand(t, c, command)
			if len(replies) != 0 || strings.HasPrefix(tagged, "F1 OK ") {
				t.Fatalf("%s %s: %#v %q", phase, command, replies, tagged)
			}
		}
	}
	if !slices.Equal(box.fetches, []uint32{10}) || len(box.updates) != 0 {
		t.Fatalf("fetches=%v updates=%v", box.fetches, box.updates)
	}
}
