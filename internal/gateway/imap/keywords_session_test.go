package imap

import (
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	goimap "github.com/rest-mail/go-imap/pkg/imap"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// The gateway mailbox keeps keywords through the API, so the engine offers \*.
var _ goimap.KeywordMailbox = (*mailbox)(nil)

// TestKeywordsPersistThroughTheAPI: a keyword one session STOREs is written to
// the message's keywords in the API, and a second session (another device)
// reads it back in FETCH FLAGS and finds it with SEARCH KEYWORD; its -FLAGS
// reaches the first session on the next NOOP. A STORE past imap.MaxKeywords on one message is
// NO [LIMIT] and writes nothing.
func TestKeywordsPersistThroughTheAPI(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "INBOX", "Subject: one\r\n\r\nbody\r\n", "one")
	api.seed(2, "INBOX", "Subject: two\r\n\r\nbody\r\n", "two")
	srv := httptest.NewServer(api.handler())
	defer srv.Close()
	backend := NewBackend(apiclient.New(srv.URL), nil)

	first := newTranscript(t, backend)
	first.login("l1")
	if _, status := first.command("s1", "SELECT INBOX"); !strings.Contains(status, "OK") {
		t.Fatal(status)
	}
	if _, status := first.command("st1", "STORE 1 +FLAGS.SILENT (Work $Forwarded)"); !strings.Contains(status, "OK") {
		t.Fatal(status)
	}
	if patches := api.patchesFor(1); len(patches) != 1 || fmt.Sprint(patches[0]["keywords"]) != "[Work $Forwarded]" {
		t.Fatalf("STORE patches %v, want keywords [Work $Forwarded]", patches)
	}

	second := newTranscript(t, backend)
	second.login("l2")
	lines, status := second.command("s2", "SELECT INBOX")
	if !strings.Contains(status, "OK") || !hasKeywordLine(lines, "PERMANENTFLAGS", `\*`) || !hasKeywordLine(lines, "PERMANENTFLAGS", "Work") {
		t.Fatalf("second session SELECT: %v %s", lines, status)
	}
	lines, _ = second.command("f1", "FETCH 1:2 (FLAGS)")
	if !hasKeywordLine(lines, "* 1 FETCH", "Work") || !hasKeywordLine(lines, "* 1 FETCH", "$Forwarded") || hasKeywordLine(lines, "* 2 FETCH", "Work") {
		t.Fatalf("second session FETCH FLAGS: %v", lines)
	}
	if lines, _ = second.command("se1", "SEARCH KEYWORD work"); !slices.Contains(lines, "* SEARCH 1") {
		t.Fatalf("SEARCH KEYWORD: %v", lines)
	}
	if lines, _ = second.command("se2", "SEARCH UNKEYWORD $forwarded"); !slices.Contains(lines, "* SEARCH 2") {
		t.Fatalf("SEARCH UNKEYWORD: %v", lines)
	}
	if _, status := second.command("st2", "STORE 1 -FLAGS.SILENT (work)"); !strings.Contains(status, "OK") {
		t.Fatal(status)
	}
	// NOOP reports the other session's change as an untagged FETCH.
	if lines, _ = first.command("n1", "NOOP"); !slices.Contains(lines, `* 1 FETCH (UID 1 FLAGS ($Forwarded \Recent))`) {
		t.Fatalf("first session after the other removed Work: %v", lines)
	}

	before := len(api.patchesFor(2))
	keywords := make([]string, goimap.MaxKeywords+1)
	for i := range keywords {
		keywords[i] = fmt.Sprintf("k%d", i)
	}
	if _, status := first.command("st3", "STORE 2 +FLAGS (%s)", strings.Join(keywords, " ")); !strings.HasPrefix(status, "st3 NO [LIMIT] ") {
		t.Fatalf("STORE over the cap: %s", status)
	}
	if got := len(api.patchesFor(2)); got != before {
		t.Fatalf("STORE over the cap wrote %d updates", got-before)
	}
}

func hasKeywordLine(lines []string, prefix, flag string) bool {
	for _, line := range lines {
		if strings.Contains(line, prefix) && slices.Contains(strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(line)), flag) {
			return true
		}
	}
	return false
}
