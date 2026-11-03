package rfctest

import (
	"fmt"
	"strings"
	"testing"
)

// TestReusedCommandTags proves the server does not require client tags to be
// unique, including a reused tag across authentication and selection changes.
//
// checklist: IMAP2-CORE-02
func TestReusedCommandTags(t *testing.T) {
	c := dial(t)
	c.line()
	const tag = "Same.Tag"
	for _, command := range []string{
		"CAPABILITY", "NOOP", "NOOP",
		fmt.Sprintf("LOGIN %s %s", testUser, testPass),
		"NOOP", "SELECT INBOX", "NOOP", "CAPABILITY",
	} {
		_, completion := c.command(tag, "%s", command)
		if !strings.HasPrefix(completion, tag+" OK") {
			t.Fatalf("%s with reused tag: %q", command, completion)
		}
	}
	_, completion := c.command(tag, "NOT-A-COMMAND")
	if !strings.HasPrefix(completion, tag+" BAD") {
		t.Fatalf("invalid command with reused tag: %q", completion)
	}
	_, completion = c.command(tag, "NOOP")
	requireOK(t, completion, tag)
}

// TestFetchSizeMatchesBodyOctets checks the shared size requirement against
// bytes actually sent, rather than a size illustration unrelated to the body.
// The engine reports the host's size; consistency of the host's representation
// is a backend obligation, not inferred from this controlled fixture.
//
// checklist: IMAP2-CORE-20
// checklist: IMAP2-CORE-26
func TestFetchSizeMatchesBodyOctets(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
			t.Run(selection+"/"+prefix, func(t *testing.T) {
				box := newFetchMailbox()
				c := fetchClient(t, box, selection)
				replies, completion := fetchCommand(t, c, prefix+" (RFC822.SIZE BODY.PEEK[])")
				requireOK(t, completion, "F1")
				if len(replies) != 1 || len(replies[0].literals) != 1 {
					t.Fatalf("expected one message literal, got %+v", replies)
				}
				body := replies[0].literals[0]
				if body != fetchRaw || len(body) != box.messages[0].Size {
					t.Fatal("reported size and fetched message representation differ")
				}
				want := fmt.Sprintf("RFC822.SIZE %d ", len(body))
				if !strings.Contains(replies[0].framing, want) {
					t.Fatalf("size missing or wrong in framing %q", replies[0].framing)
				}
				if len(box.updates) != 0 || len(box.fetches) != 1 {
					t.Fatalf("peek changed flags or fetched twice: updates=%v fetches=%v", box.updates, box.fetches)
				}
			})
		}
	}
}
