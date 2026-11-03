package server

import (
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// listedFolders sends LIST "" <pattern> and returns the folder names from the
// untagged "* LIST (attrs) \"/\" \"Name\"" responses.
func listedFolders(t *testing.T, h *imapHarness, tag, pattern string) []string {
	t.Helper()
	untagged, status := h.command(tag, `LIST "" %s`, pattern)
	if !strings.Contains(status, " OK") {
		t.Fatalf("LIST %s status = %q", pattern, status)
	}
	var names []string
	for _, line := range untagged {
		const sep = `"/" "`
		if i := strings.LastIndex(line, sep); i >= 0 && strings.HasSuffix(line, `"`) {
			names = append(names, line[i+len(sep):len(line)-1])
		}
	}
	return names
}

func hasFolder(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestList_CaseSensitiveExceptInbox pins RFC 3501 §5.1 at the protocol level:
// LIST pattern matching is case-sensitive for every mailbox name except INBOX.
// Before the fix matchIMAPPattern lower-cased both sides, so the lowercase
// pattern "work" wrongly matched a folder named "Work"; that negative assertion
// was RED on the old code.
func TestList_CaseSensitiveExceptInbox(t *testing.T) {
	b := newMockBackend()
	b.mbox.folders = append(b.mbox.folders, imap.Folder{Name: "Work"})
	h := newIMAPHarness(t, b)
	h.login("a1")

	// A non-INBOX name matches only in its exact case.
	if got := listedFolders(t, h, "a2", `"Work"`); !hasFolder(got, "Work") {
		t.Errorf(`LIST "" "Work" = %v, want it to include Work`, got)
	}
	if got := listedFolders(t, h, "a3", `"work"`); hasFolder(got, "Work") {
		t.Errorf(`LIST "" "work" = %v, want it to NOT include Work `+
			`(mailbox names are case-sensitive, RFC 3501 §5.1)`, got)
	}

	// INBOX is the one case-insensitive name: a lowercase pattern still lists it.
	if got := listedFolders(t, h, "a4", `"inbox"`); !hasFolder(got, "INBOX") {
		t.Errorf(`LIST "" "inbox" = %v, want it to include INBOX (INBOX is case-insensitive)`, got)
	}

	// The wildcard "*" still lists everything regardless of case.
	if got := listedFolders(t, h, "a5", `"*"`); !hasFolder(got, "Work") || !hasFolder(got, "INBOX") {
		t.Errorf(`LIST "" "*" = %v, want it to include both INBOX and Work`, got)
	}
}
