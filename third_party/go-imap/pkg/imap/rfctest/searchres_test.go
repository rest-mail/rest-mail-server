package rfctest

import (
	"fmt"
	"strings"
	"testing"
)

// runExactly sends command and requires OK with exactly want untagged.
func runExactly(t *testing.T, c *client, command string, want ...string) {
	t.Helper()
	lines, tagged := c.command("R1", "%s", command)
	requireOK(t, tagged, "R1")
	requireExactly(t, lines, want...)
}

// TestSearchRes: with SEARCHRES advertised (and ESEARCH, which it requires),
// SEARCH RETURN (SAVE) keeps its result and answers nothing untagged; "$"
// then names those messages in FETCH, UID FETCH, SEARCH and UID SEARCH UID,
// alone or inside OR (RFC 5182 §2.2, examples 1 to 4). Pipelined, the two
// commands run in order (§2.3).
//
// checklist: IMAP-SEARCHRES-01 IMAP-SEARCHRES-02
// checklist: IMAP2-CMD-148
func TestSearchRes(t *testing.T) {
	c := sharedClient(t, esearchMailbox())
	requireCaps(t, c.capabilities("C1"), []string{"SEARCHRES", "ESEARCH"}, nil)
	flagged := []string{`* 2 FETCH (UID 8 FLAGS (\Seen \Flagged))`, `* 10 FETCH (UID 70 FLAGS (\Flagged))`, `* 11 FETCH (UID 3800 FLAGS (\Flagged))`}

	runExactly(t, c, "SEARCH RETURN (SAVE) FLAGGED")
	runExactly(t, c, "FETCH $ (UID)", flagged...)
	runExactly(t, c, "UID FETCH $ (UID)", flagged...)

	if _, err := fmt.Fprint(c.conn, "P1 UID SEARCH RETURN (SAVE) 1:4\r\nP2 UID SEARCH UID $ FLAGGED\r\n"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"P1 OK", "* SEARCH 8", "P2 OK"} {
		if got := c.line(); !strings.HasPrefix(got, want) {
			t.Fatalf("pipelined: %q, want %q", got, want)
		}
	}
	runExactly(t, c, "SEARCH $ UNSEEN", "* SEARCH 4")
	runExactly(t, c, "SEARCH (OR $ 11) UNSEEN", "* SEARCH 4 11")
}

// TestSearchResSaveOptions: SAVE with MIN and/or MAX and no ALL or COUNT
// keeps only the messages those name; with ALL or COUNT, every one found; it
// changes nothing about the ESEARCH answer itself (RFC 5182 §2.4, example 10).
//
// checklist: IMAP-SEARCHRES-03
func TestSearchResSaveOptions(t *testing.T) {
	c := sharedClient(t, esearchMailbox())
	for _, tc := range []struct {
		search, esearch string
		saved           []string
	}{
		{"SEARCH RETURN (SAVE MIN) FLAGGED", `* ESEARCH (TAG "R1") MIN 2`, []string{"* 2 FETCH (UID 8 FLAGS (\\Seen \\Flagged))"}},
		{"SEARCH RETURN (MAX SAVE MIN) FLAGGED", `* ESEARCH (TAG "R1") MIN 2 MAX 11`, []string{"* 2 FETCH (UID 8 FLAGS (\\Seen \\Flagged))", "* 11 FETCH (UID 3800 FLAGS (\\Flagged))"}},
		{"UID SEARCH RETURN (SAVE MAX) 1:4", `* ESEARCH (TAG "R1") UID MAX 10`, []string{"* 4 FETCH (UID 10 FLAGS ())"}},
		{"SEARCH RETURN (MAX SAVE MIN COUNT) FLAGGED", `* ESEARCH (TAG "R1") MIN 2 MAX 11 COUNT 3`, []string{"* 2 FETCH (UID 8 FLAGS (\\Seen \\Flagged))", "* 10 FETCH (UID 70 FLAGS (\\Flagged))", "* 11 FETCH (UID 3800 FLAGS (\\Flagged))"}},
		{"SEARCH RETURN (ALL SAVE) 3:4", `* ESEARCH (TAG "R1") ALL 3:4`, []string{"* 3 FETCH (UID 9 FLAGS (\\Seen))", "* 4 FETCH (UID 10 FLAGS ())"}},
	} {
		runExactly(t, c, tc.search, tc.esearch)
		runExactly(t, c, "FETCH $ (UID)", tc.saved...)
	}
}

// TestSearchResVariable: what changes the saved result and what does not (RFC
// 5182 §2.1). A SEARCH with SAVE that fails with NO empties it (example 5); a
// BAD one, and a SEARCH without SAVE, leave it; an expunged message leaves it;
// SELECT, EXAMINE, CLOSE and UNSELECT reset it. An empty "$" is a valid set
// that matches nothing: FETCH, COPY and STORE complete OK with no response
// (example 6).
//
// checklist: IMAP-SEARCHRES-04 IMAP-SEARCHRES-05
// checklist: IMAP2-CMD-146 IMAP2-CMD-147
func TestSearchResVariable(t *testing.T) {
	box := esearchMailbox()
	c := sharedClient(t, box)
	runExactly(t, c, "FETCH $ (UID)")
	runExactly(t, c, `COPY $ "Other Messages"`)
	runExactly(t, c, `STORE $ +FLAGS (\Deleted)`)

	save := func() {
		t.Helper()
		runExactly(t, c, "SEARCH RETURN (SAVE) 2:3")
	}
	saved := []string{`* 2 FETCH (UID 8 FLAGS (\Seen \Flagged))`, `* 3 FETCH (UID 9 FLAGS (\Seen))`}
	save()
	if _, tagged := c.command("B1", "SEARCH RETURN (SAVE) BOGUS"); !strings.HasPrefix(tagged, "B1 BAD") {
		t.Fatalf("bad key: %q", tagged)
	}
	runExactly(t, c, "SEARCH FLAGGED", "* SEARCH 2 10 11")
	runExactly(t, c, "FETCH $ (UID)", saved...)
	if _, tagged := c.command("N1", "SEARCH RETURN (SAVE) CHARSET KOI8-R ALL"); !strings.HasPrefix(tagged, "N1 NO [BADCHARSET") {
		t.Fatalf("unsupported charset: %q", tagged)
	}
	runExactly(t, c, "FETCH $ (UID)")

	save()
	box.remove(8)
	runExactly(t, c, "NOOP", "* 2 EXPUNGE")
	runExactly(t, c, "FETCH $ (UID)", `* 2 FETCH (UID 9 FLAGS (\Seen))`)

	for _, reset := range []string{"SELECT INBOX", "EXAMINE INBOX", "CLOSE", "UNSELECT"} {
		save()
		if _, tagged := c.command("X1", "%s", reset); !strings.HasPrefix(tagged, "X1 OK") {
			t.Fatalf("%s: %q", reset, tagged)
		}
		if reset == "CLOSE" || reset == "UNSELECT" {
			if _, tagged := c.command("X2", "SELECT INBOX"); !strings.HasPrefix(tagged, "X2 OK") {
				t.Fatalf("reselect: %q", tagged)
			}
		}
		runExactly(t, c, "FETCH $ (UID)")
	}
}

// TestSearchResGrammar: "$" is a whole sequence-set (seq-last-command, RFC
// 5182 §3), not a number inside one: "1,$", "$:5" and "$,2" are BAD.
//
// checklist: IMAP-SEARCHRES-06
func TestSearchResGrammar(t *testing.T) {
	c := sharedClient(t, esearchMailbox())
	runExactly(t, c, "SEARCH RETURN (SAVE) ALL")
	for _, command := range []string{"FETCH 1,$ (UID)", "FETCH $:5 (UID)", "UID FETCH $,2 (UID)", "SEARCH 1:$"} {
		if lines, tagged := c.command("G1", "%s", command); !strings.HasPrefix(tagged, "G1 BAD") || len(lines) != 0 {
			t.Fatalf("%s: %q %q, want BAD", command, lines, tagged)
		}
	}
}
