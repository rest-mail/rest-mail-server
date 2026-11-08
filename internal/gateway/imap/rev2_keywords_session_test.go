package imap

import (
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// rev2Keywords are the keywords RFC 9051 §2.3.2 says a server SHOULD support:
// allowed in SEARCH and kept by APPEND, COPY and MOVE. REST Mail has no message
// field for any of them (no forwarded, MDN-sent, junk or phishing state), so each
// is stored as a plain keyword and does nothing else: $Junk and $Phishing do not
// move a message to Spam.
var rev2Keywords = []string{"$Forwarded", "$MDNSent", "$Junk", "$NotJunk", "$Phishing"}

// TestRev2KeywordsAreStoredAsPlainKeywords drives each keyword through a real
// session: STORE +FLAGS, FLAGS/PERMANENTFLAGS, FETCH FLAGS, SEARCH KEYWORD,
// COPY, STORE -FLAGS, and APPEND.
func TestRev2KeywordsAreStoredAsPlainKeywords(t *testing.T) {
	for _, keyword := range rev2Keywords {
		t.Run(keyword, func(t *testing.T) {
			api := newFakeAPI()
			api.seed(1, "INBOX", "Subject: one\r\n\r\nbody\r\n", "one")
			api.seed(2, "INBOX", "Subject: two\r\n\r\nbody\r\n", "two")
			srv := httptest.NewServer(api.handler())
			defer srv.Close()
			session := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
			session.login("l1")
			lines, status := session.command("s1", "SELECT INBOX")
			if !strings.Contains(status, "OK") {
				t.Fatal(status)
			}
			if !hasLine(lines, "PERMANENTFLAGS", `\*`) {
				t.Fatalf("PERMANENTFLAGS does not allow creating %s: %v", keyword, lines)
			}

			if _, status = session.command("st1", "STORE 1 +FLAGS (%s)", keyword); !strings.Contains(status, "OK") {
				t.Fatal(status)
			}
			// The next mailbox update announces a newly used keyword in FLAGS and
			// PERMANENTFLAGS.
			lines, _ = session.command("n1", "NOOP")
			if !hasLine(lines, "* FLAGS", keyword) || !hasLine(lines, "PERMANENTFLAGS", keyword) {
				t.Fatalf("NOOP after STORE did not announce %s: %v", keyword, lines)
			}
			patches := api.patchesFor(1)
			if len(patches) != 1 {
				t.Fatalf("STORE made %d updates, want 1", len(patches))
			}
			for field := range patches[0] {
				if field != "keywords" {
					t.Fatalf("STORE %s changed %s; it must stay a plain keyword", keyword, field)
				}
			}
			if folderOf(api, 1) != "INBOX" || len(api.imports) != 0 || api.deleted(1) {
				t.Fatalf("STORE %s moved the message", keyword)
			}

			lines, _ = session.command("f1", "FETCH 1:2 (FLAGS)")
			if !hasLine(lines, "* 1 FETCH", keyword) || hasLine(lines, "* 2 FETCH", keyword) {
				t.Fatalf("FETCH FLAGS: %v", lines)
			}
			lines, _ = session.command("se1", "SEARCH KEYWORD %s", strings.ToLower(keyword))
			if !slices.Contains(lines, "* SEARCH 1") {
				t.Fatalf("SEARCH KEYWORD %s: %v", keyword, lines)
			}
			lines, _ = session.command("se2", "SEARCH UNKEYWORD %s", keyword)
			if !slices.Contains(lines, "* SEARCH 2") {
				t.Fatalf("SEARCH UNKEYWORD %s: %v", keyword, lines)
			}

			if _, status := session.command("c1", "COPY 1 Archive"); !strings.Contains(status, "OK") {
				t.Fatal(status)
			}
			api.mu.Lock()
			copied := api.msgs[100]
			api.mu.Unlock()
			if copied == nil || copied.folder != "Archive" || !slices.Equal(copied.keywords, []string{keyword}) || copied.raw != "Subject: one\r\n\r\nbody\r\n" {
				t.Fatalf("COPY did not preserve the plain keyword %s and source: %+v", keyword, copied)
			}

			if _, status := session.command("st2", "STORE 1 -FLAGS (%s)", keyword); !strings.Contains(status, "OK") {
				t.Fatal(status)
			}
			lines, _ = session.command("f2", "FETCH 1 (FLAGS)")
			if hasLine(lines, "* 1 FETCH", keyword) {
				t.Fatalf("STORE -FLAGS left %s: %v", keyword, lines)
			}
			lines, _ = session.command("se3", "SEARCH KEYWORD %s", keyword)
			if !slices.Contains(lines, "* SEARCH") {
				t.Fatalf("SEARCH KEYWORD after removal: %v", lines)
			}

			raw := "Subject: appended\r\n\r\nbody\r\n"
			session.send(`a1 APPEND INBOX (%s) {%d}`, keyword, len(raw))
			if line := session.readLine(); !strings.HasPrefix(line, "+") {
				t.Fatal(line)
			}
			session.writeLine(raw + "\r\n")
			if _, status := session.readResponse("a1"); !strings.Contains(status, "OK") {
				t.Fatal(status)
			}
			if flags := api.lastImport(t).Flags; !slices.Equal(flags, []string{keyword}) {
				t.Fatalf("APPEND sent %v, want [%s]", flags, keyword)
			}
		})
	}
}

func hasLine(lines []string, prefix, flag string) bool {
	for _, line := range lines {
		if strings.Contains(line, prefix) && slices.Contains(strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(line)), flag) {
			return true
		}
	}
	return false
}

// TestRev2KeywordsSearchIsCaseInsensitive: keywords compare without case
// (RFC 9051 §2.3.2), so a client spelling the registered keyword differently
// still finds a message carrying it.
func TestRev2KeywordsSearchIsCaseInsensitive(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "INBOX", "Subject: one\r\n\r\nbody\r\n", "one")
	api.msgs[1].keywords = []string{"$mdnsent"}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()
	session := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
	session.login("l1")
	if _, status := session.command("s1", "SELECT INBOX"); !strings.Contains(status, "OK") {
		t.Fatal(status)
	}
	for index, spelling := range []string{"$MDNSent", "$MDNSENT"} {
		lines, _ := session.command(fmt.Sprintf("se%d", index), "SEARCH KEYWORD %s", spelling)
		if !slices.Contains(lines, "* SEARCH 1") {
			t.Fatalf("SEARCH KEYWORD %s: %v", spelling, lines)
		}
	}
}
