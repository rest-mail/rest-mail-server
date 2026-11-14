package imap

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// RFC 3501 §6.3.11 / RFC 9051 §6.3.12: "If a date-time is specified, the
// internal date SHOULD be set in the resulting message; otherwise, the internal
// date of the resulting message is set to the current date and time by default."

// TestServer_AppendWithDateTime_KeepsInternalDate proves an APPEND carrying a
// date-time reaches the import operation with that exact instant as its
// internal date, and still answers with APPENDUID.
func TestServer_AppendWithDateTime_KeepsInternalDate(t *testing.T) {
	api := newFakeAPI()
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
	h.login("a1")

	msg := "Subject: Old mail\r\n\r\nfrom another server\r\n"
	h.send(`a2 APPEND INBOX (\Seen) "07-Feb-1994 22:43:04 -0800" {%d}`, len(msg))
	if cont := h.readLine(); !strings.HasPrefix(cont, "+") {
		t.Fatalf("APPEND continuation = %q, want +...", cont)
	}
	h.writeLine(msg + "\r\n")
	_, st := h.readResponse("a2")
	if !strings.Contains(st, "[APPENDUID 1 100]") {
		t.Fatalf("APPEND status = %q, want [APPENDUID 1 100]", st)
	}

	want := time.Date(1994, time.February, 7, 22, 43, 4, 0, time.FixedZone("", -8*3600))
	req := api.lastImport(t)
	if !req.InternalDate.Equal(want) {
		t.Errorf("import internal date = %v, want the client's %v", req.InternalDate, want)
	}
	if req.Folder != "INBOX" {
		t.Errorf("import folder = %q, want INBOX", req.Folder)
	}
	if len(req.Flags) != 1 || req.Flags[0] != `\Seen` {
		t.Errorf("import flags = %v, want [\\Seen]", req.Flags)
	}
	// The stored message answers FETCH INTERNALDATE with the client's instant, in
	// UTC, and with no account zone SEARCH ON / BEFORE / SINCE use that UTC day
	// (#1557).
	if _, st := h.command("a3", "SELECT INBOX"); !strings.Contains(st, "OK") {
		t.Fatalf("SELECT status = %q", st)
	}
	unt, st := h.command("a4", "FETCH 1 INTERNALDATE")
	if !strings.Contains(st, "OK") || !strings.Contains(strings.Join(unt, "\n"), `INTERNALDATE "08-Feb-1994 06:43:04 +0000"`) {
		t.Errorf("FETCH INTERNALDATE = %q / %q, want the client's date-time in UTC", unt, st)
	}
	for _, c := range []struct{ criteria, want string }{
		{"ON 8-Feb-1994", "* SEARCH 1"},
		{"BEFORE 9-Feb-1994", "* SEARCH 1"},
		{"SINCE 9-Feb-1994", "* SEARCH"},
		{"BEFORE 8-Feb-1994", "* SEARCH"},
	} {
		unt, st := h.command("a5", "SEARCH %s", c.criteria)
		if !strings.Contains(st, "OK") || len(unt) != 1 || strings.TrimSpace(unt[0]) != c.want {
			t.Errorf("SEARCH %s = %q / %q, want %q", c.criteria, unt, st, c.want)
		}
	}
}

// TestServer_AppendWithoutDateTime_UsesCurrentTime proves an APPEND with no
// date-time still stores the current date and time as the internal date.
func TestServer_AppendWithoutDateTime_UsesCurrentTime(t *testing.T) {
	api := newFakeAPI()
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
	h.login("a1")

	before := time.Now().Add(-time.Second)
	_, st := h.appendMsg("a2", "INBOX", "Subject: New\r\n\r\nhi\r\n")
	after := time.Now().Add(time.Second)
	if !strings.Contains(st, "[APPENDUID 1 100]") {
		t.Fatalf("APPEND status = %q, want [APPENDUID 1 100]", st)
	}
	got := api.lastImport(t).InternalDate
	if got.Before(before) || got.After(after) {
		t.Errorf("import internal date = %v, want the current time (between %v and %v)", got, before, after)
	}
}
