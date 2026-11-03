package imap

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

func TestImportedFlagsAPPENDCOPYFETCHAndExpunge(t *testing.T) {
	api := newFakeAPI()
	srv := httptest.NewServer(api.handler())
	defer srv.Close()
	backend := NewBackend(apiclient.New(srv.URL), nil)
	session := newTranscript(t, backend)
	session.login("a1")
	raw := "Message-ID: <flags@example.test>\r\nSubject: flags\r\n\r\nbody\r\n"
	session.send(`a2 APPEND INBOX (\Seen \Deleted $Forwarded $Junk) {%d}`, len(raw))
	if line := session.readLine(); !strings.HasPrefix(line, "+") {
		t.Fatal(line)
	}
	session.writeLine(raw + "\r\n")
	if _, status := session.readResponse("a2"); !strings.Contains(status, "OK") {
		t.Fatal(status)
	}
	for _, flag := range []string{`\Seen`, `\Deleted`, "$Forwarded", "$Junk"} {
		if !strings.Contains(fmt.Sprint(api.lastImport(t).Flags), flag) {
			t.Fatalf("APPEND lost %s", flag)
		}
	}
	lines, status := session.command("a3", "SELECT INBOX")
	if !strings.Contains(status, "OK") {
		t.Fatal(status)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "* FLAGS") || strings.Contains(line, "PERMANENTFLAGS") {
			if !strings.Contains(line, "$Forwarded") || !strings.Contains(line, "$Junk") {
				t.Fatal(line)
			}
			if strings.Contains(line, "PERMANENTFLAGS") && !strings.Contains(line, `\*`) {
				t.Fatal("imapsync needs keyword creation advertised")
			}
		}
	}
	lines, _ = session.command("a4", "FETCH 1 (FLAGS)")
	for _, flag := range []string{`\Seen`, `\Deleted`, "$Forwarded", "$Junk"} {
		if !strings.Contains(strings.Join(lines, " "), flag) {
			t.Fatalf("FETCH lost %s: %v", flag, lines)
		}
	}
	if _, status := session.command("a5", "COPY 1 Archive"); !strings.Contains(status, "OK") {
		t.Fatal(status)
	}
	if got := fmt.Sprint(api.lastImport(t).Flags); !strings.Contains(got, `\Deleted`) || !strings.Contains(got, "$Forwarded") {
		t.Fatal(got)
	}
	if _, status := session.command("store1", `STORE 1 +FLAGS ($Custom)`); !strings.Contains(status, "OK") {
		t.Fatal(status)
	}
	if _, status := session.command("reselect", "SELECT INBOX"); !strings.Contains(status, "OK") {
		t.Fatal(status)
	}
	lines, _ = session.command("keywords", "FETCH 1 FLAGS")
	if !strings.Contains(strings.Join(lines, " "), "$Custom") {
		t.Fatal("keyword STORE was not persistent", lines)
	}
	lines, _ = session.command("search", "SEARCH KEYWORD $forwarded")
	if !strings.Contains(strings.Join(lines, " "), "* SEARCH 1") {
		t.Fatal("keyword search lost imported metadata", lines)
	}
	if _, status := session.command("a6", "EXPUNGE"); !strings.Contains(status, "OK") {
		t.Fatal(status)
	}
	if !api.deleted(100) || !api.exists(101) {
		t.Fatal("EXPUNGE did not use the existing deletion path")
	}
}

func TestAPPENDRefusesInvalidKeywordsBeforeLiteral(t *testing.T) {
	api := newFakeAPI()
	srv := httptest.NewServer(api.handler())
	defer srv.Close()
	session := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
	session.login("a1")
	for index, flags := range []string{"bad]keyword", strings.Repeat("x", 256), `\Unknown`} {
		tag := fmt.Sprintf("bad%d", index)
		_, status := session.command(tag, "APPEND INBOX (%s) {3}", flags)
		if !strings.Contains(status, "NO") && !strings.Contains(status, "BAD") {
			t.Fatal(status)
		}
	}
	if len(api.imports) != 0 {
		t.Fatal("invalid flags reached import")
	}
}
