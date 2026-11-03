package rfctest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// TestUntaggedBadForUndeterminedCommand sends lines from which no tag and
// command can be read. Each gets an untagged BAD, and the session still runs
// the next command, rather than dropping the line and leaving the client
// waiting for a completion that never comes.
//
// checklist: IMAP-RESP-03 IMAP-RESP-01
func TestUntaggedBadForUndeterminedCommand(t *testing.T) {
	c := dial(t)
	c.line()
	for i, bad := range []string{"garbage", "A1"} {
		if _, err := fmt.Fprintf(c.conn, "%s\r\n", bad); err != nil {
			t.Fatal(err)
		}
		if l := c.line(); !strings.HasPrefix(l, "* BAD ") {
			t.Fatalf("%q: got %q, want an untagged BAD", bad, l)
		}
		tag := fmt.Sprintf("N%d", i)
		if _, tagged := c.command(tag, "NOOP"); !strings.HasPrefix(tagged, tag+" OK") {
			t.Fatalf("after %q: %q", bad, tagged)
		}
	}
}

// TestNoExpungeDuringFetchStoreSearch removes a message behind the session's
// back, as another session would, and checks FETCH, STORE and SEARCH never
// answer with an untagged EXPUNGE: sequence numbers stay those the client
// already knows.
//
// checklist: IMAP-RESP-12
// checklist: IMAP2-RESP-199
func TestNoExpungeDuringFetchStoreSearch(t *testing.T) {
	box := &selectionMailbox{folders: map[string][]imap.Message{"INBOX": {
		{UID: 1, Size: 24}, {UID: 2, Size: 24}, {UID: 3, Size: 24},
	}}}
	c := selectionClient(t, box)
	if _, tagged := c.command("S1", "SELECT INBOX"); !strings.HasPrefix(tagged, "S1 OK") {
		t.Fatalf("SELECT: %q", tagged)
	}
	box.folders["INBOX"] = []imap.Message{{UID: 1, Size: 24}, {UID: 3, Size: 24}}
	for i, cmd := range []string{"FETCH 1:* (UID)", "STORE 3 +FLAGS (\\Flagged)", "SEARCH ALL", "UID FETCH 1:* (UID)", "UID STORE 3 +FLAGS (\\Flagged)", "UID SEARCH ALL"} {
		tag := fmt.Sprintf("C%d", i)
		lines, tagged := c.command(tag, "%s", cmd)
		if !strings.HasPrefix(tagged, tag+" OK") {
			t.Fatalf("%s: %q", cmd, tagged)
		}
		for _, l := range lines {
			if strings.HasSuffix(l, " EXPUNGE") {
				t.Fatalf("%s sent %q", cmd, l)
			}
		}
	}
}

// TestGreetingCapabilityCode checks the greeting's optional CAPABILITY response
// code lists IMAP4rev1 and matches what CAPABILITY itself then reports, so a
// client may skip the CAPABILITY round trip.
//
// checklist: IMAP-RESP-02
func TestGreetingCapabilityCode(t *testing.T) {
	c := dial(t)
	g := c.line()
	const prefix = "* OK [CAPABILITY "
	end := strings.Index(g, "] ")
	if !strings.HasPrefix(g, prefix) || end < 0 {
		t.Fatalf("greeting %q has no CAPABILITY response code", g)
	}
	code := g[len(prefix):end]
	if !strings.HasPrefix(code, "IMAP4rev1") {
		t.Fatalf("greeting capabilities %q do not list IMAP4rev1", code)
	}
	untagged, tagged := c.command("C1", "CAPABILITY")
	if !strings.HasPrefix(tagged, "C1 OK") || len(untagged) != 1 {
		t.Fatalf("CAPABILITY: %q %q", untagged, tagged)
	}
	if untagged[0] != "* CAPABILITY "+code {
		t.Fatalf("greeting code %q, CAPABILITY response %q", code, untagged[0])
	}
}

// TestExperimentalCommand checks an X-prefixed command this engine does not
// define gets a tagged BAD, in every state, and the session goes on.
//
// checklist: IMAP-XCMD-01
func TestExperimentalCommand(t *testing.T) {
	c := dial(t)
	c.line()
	for i, authenticate := range []bool{false, true} {
		if authenticate {
			c.login()
		}
		tag := fmt.Sprintf("X%d", i)
		if _, tagged := c.command(tag, "XPERIMENT 1 2"); !strings.HasPrefix(tagged, tag+" BAD") {
			t.Fatalf("authenticated=%v: %q, want BAD", authenticate, tagged)
		}
	}
	if _, tagged := c.command("N1", "NOOP"); !strings.HasPrefix(tagged, "N1 OK") {
		t.Fatalf("NOOP after XPERIMENT: %q", tagged)
	}
}
