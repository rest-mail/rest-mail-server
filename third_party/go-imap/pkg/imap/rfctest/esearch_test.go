package rfctest

import (
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// esearchMailbox holds eleven messages: UIDs 7 to 3800, messages 2, 10 and 11
// flagged, 1 to 3 seen.
func esearchMailbox() *sharedMailbox {
	uids := []uint32{7, 8, 9, 10, 20, 30, 40, 50, 60, 70, 3800}
	box := &sharedMailbox{}
	for i, uid := range uids {
		seq := i + 1
		box.messages = append(box.messages, imap.Message{UID: uid, Flagged: seq == 2 || seq >= 10, Seen: seq <= 3})
	}
	return box
}

// TestESearch: with ESEARCH advertised (RFC 4731), SEARCH and UID SEARCH take
// RETURN result options before the criteria and then answer one untagged
// ESEARCH, correlated by the command's tag, instead of SEARCH: MIN and MAX the
// lowest and highest match, ALL every match as a sequence-set, COUNT their
// number; an empty list means ALL; UID SEARCH marks its answer UID and gives
// UIDs. The RFC's A282-A286 examples, against this mailbox.
//
// checklist: IMAP-ESEARCH-01 IMAP-ESEARCH-02 IMAP-ESEARCH-03
// checklist: IMAP2-CMD-135 IMAP2-CMD-141 IMAP2-CMD-142 IMAP2-CMD-165
// checklist: IMAP2-RESP-194 IMAP2-RESP-196
func TestESearch(t *testing.T) {
	c := sharedClient(t, esearchMailbox())
	requireCaps(t, c.capabilities("C1"), []string{"ESEARCH"}, nil)
	for _, tc := range []struct{ command, want string }{
		{"SEARCH RETURN (MIN COUNT) FLAGGED", `* ESEARCH (TAG "A1") MIN 2 COUNT 3`},
		{"SEARCH RETURN () FLAGGED", `* ESEARCH (TAG "A1") ALL 2,10:11`},
		{"SEARCH RETURN (MIN) UNSEEN", `* ESEARCH (TAG "A1") MIN 4`},
		{"UID SEARCH RETURN (MIN MAX) 1:5000", `* ESEARCH (TAG "A1") UID MIN 7 MAX 3800`},
		{"SEARCH RETURN (COUNT) FLAGGED", `* ESEARCH (TAG "A1") COUNT 3`},
		{"UID SEARCH RETURN (ALL) FLAGGED", `* ESEARCH (TAG "A1") UID ALL 8,70,3800`},
		{"search return (count max) charset utf-8 all", `* ESEARCH (TAG "A1") MAX 11 COUNT 11`},
		{"SEARCH RETURN (MAX MIN ALL) 1:4", `* ESEARCH (TAG "A1") MIN 1 MAX 4 ALL 1:4`},
	} {
		lines, tagged := c.command("A1", "%s", tc.command)
		requireOK(t, tagged, "A1")
		requireExactly(t, lines, tc.want)
	}
	// Without RETURN, SEARCH still answers SEARCH.
	lines, tagged := c.command("A2", "SEARCH FLAGGED")
	requireOK(t, tagged, "A2")
	requireExactly(t, lines, "* SEARCH 2 10 11")
}

// TestESearchNoMatches: when nothing matches, the ESEARCH response is still
// sent and still carries COUNT (0), and MIN, MAX and ALL are left out (RFC 4731
// §3.1).
//
// checklist: IMAP-ESEARCH-04
// checklist: IMAP2-CMD-137 IMAP2-CMD-138 IMAP2-CMD-140 IMAP2-CMD-141 IMAP2-CMD-142
// checklist: IMAP2-RESP-192 IMAP2-RESP-193 IMAP2-RESP-195 IMAP2-RESP-196
func TestESearchNoMatches(t *testing.T) {
	c := sharedClient(t, esearchMailbox())
	for _, tc := range []struct{ command, want string }{
		{"SEARCH RETURN (MIN MAX ALL COUNT) KEYWORD nothing", `* ESEARCH (TAG "A1") COUNT 0`},
		{"UID SEARCH RETURN (MIN MAX ALL) KEYWORD nothing", `* ESEARCH (TAG "A1") UID`},
		{"SEARCH RETURN () KEYWORD nothing", `* ESEARCH (TAG "A1")`},
	} {
		lines, tagged := c.command("A1", "%s", tc.command)
		requireOK(t, tagged, "A1")
		requireExactly(t, lines, tc.want)
	}
}

// TestESearchRefusals: a RETURN list that is not "(" options ")" followed by
// one SP and the criteria, or that names an option this server does not offer
// (PARTIAL, RFC 9394), is BAD with no response.
//
// checklist: IMAP-ESEARCH-05
// checklist: IMAP2-CMD-135
func TestESearchRefusals(t *testing.T) {
	c := sharedClient(t, esearchMailbox())
	for _, command := range []string{
		"SEARCH RETURN (FOO) ALL",
		"SEARCH RETURN (MIN",
		"SEARCH RETURN (MIN)",
		"SEARCH RETURN (MIN) ",
		"SEARCH RETURN (MIN)  ALL",
		"SEARCH RETURN (MIN  MAX) ALL",
		"SEARCH RETURN MIN ALL",
		"UID SEARCH RETURN (PARTIAL 1:10) ALL",
	} {
		lines, tagged := c.command("B1", "%s", command)
		if !strings.HasPrefix(tagged, "B1 BAD ") || len(lines) != 0 {
			t.Fatalf("%q: %q %q, want BAD", command, lines, tagged)
		}
	}
}
