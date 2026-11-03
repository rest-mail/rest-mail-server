package imap

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	goimap "github.com/rest-mail/go-imap/pkg/imap"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// These tests are for issue #1553: COPY and APPEND each add a message, even when
// the destination already holds identical octets. fakeAPI skips such a message as
// the real import does unless the request says always_store. The tests in
// internal/api/handlers/imap_new_message_db_test.go repeat the sequences against
// the real import on PostgreSQL.

const newMessageRaw = "Message-ID: <same@example.test>\r\nSubject: Same\r\n\r\nsame\r\n"

func newMessageTranscript(t *testing.T, api *fakeAPI) *transcript {
	t.Helper()
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
	h.login("l1")
	return h
}

func TestServer_CopyInPlaceThenExpungeKeepsTheCopy(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "INBOX", newMessageRaw, "Same")
	h := newMessageTranscript(t, api)
	if _, st := h.command("a1", "SELECT INBOX"); !strings.Contains(st, "OK") {
		t.Fatalf("SELECT: %q", st)
	}

	unt, st := h.command("a2", "COPY 1 INBOX")
	if !strings.Contains(st, "[COPYUID 1 1 100]") {
		t.Errorf("COPY = %q, want [COPYUID 1 1 100]", st)
	}
	if !slices.Contains(unt, "* 2 EXISTS") {
		t.Errorf("COPY untagged = %q, want * 2 EXISTS", unt)
	}

	h.command("a3", `UID STORE 1 +FLAGS (\Deleted)`)
	if _, st := h.command("a4", "EXPUNGE"); !strings.Contains(st, "OK") {
		t.Fatalf("EXPUNGE: %q", st)
	}
	if api.exists(1) {
		t.Errorf("the original is still there after EXPUNGE")
	}
	if !api.exists(100) {
		t.Fatalf("the copy did not survive the expunge of the original")
	}
}

func TestServer_MoveInPlaceKeepsTheMessage(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "INBOX", newMessageRaw, "Same")
	h := newMessageTranscript(t, api)
	h.command("a1", "SELECT INBOX")

	unt, st := h.command("a2", "MOVE 1 INBOX")
	if !strings.Contains(st, "OK") {
		t.Fatalf("MOVE: %q", st)
	}
	if joined := strings.Join(unt, "\n"); !strings.Contains(joined, "[COPYUID 1 1 100]") {
		t.Errorf("MOVE untagged = %q, want [COPYUID 1 1 100]", joined)
	}
	if !api.exists(100) {
		t.Fatalf("MOVE into the same folder left no message")
	}
}

func TestServer_RepeatedIdenticalAppendAddsEachMessage(t *testing.T) {
	api := newFakeAPI()
	h := newMessageTranscript(t, api)
	h.command("a1", "SELECT INBOX")

	for _, tc := range []struct{ tag, uid, exists string }{
		{"b1", "[APPENDUID 1 100]", "* 1 EXISTS"},
		{"b2", "[APPENDUID 1 101]", "* 2 EXISTS"},
	} {
		unt, st := h.appendMsg(tc.tag, "INBOX", newMessageRaw)
		if !strings.Contains(st, tc.uid) {
			t.Errorf("APPEND %s = %q, want %s", tc.tag, st, tc.uid)
		}
		if !slices.Contains(unt, tc.exists) {
			t.Errorf("APPEND %s untagged = %q, want %s", tc.tag, unt, tc.exists)
		}
	}
}

// Identical octets appended with two date-times are two messages. Each has a new
// UID and the date-time its own APPEND carried (#1553, #1556). A COPY of the first
// is a third message, which keeps the date of its source.
func TestServer_RepeatedAppendWithDateTimeAddsEachMessageAtItsDate(t *testing.T) {
	api := newFakeAPI()
	h := newMessageTranscript(t, api)
	h.command("a1", "SELECT INBOX")

	const earlier, later = "07-Feb-1994 22:43:04 -0800", "08-Mar-1995 01:02:03 +0100"
	for _, tc := range []struct{ tag, date, uid, exists string }{
		{"b1", earlier, "[APPENDUID 1 100]", "* 1 EXISTS"},
		{"b2", later, "[APPENDUID 1 101]", "* 2 EXISTS"},
	} {
		h.send(`%s APPEND INBOX "%s" {%d}`, tc.tag, tc.date, len(newMessageRaw))
		if cont := h.readLine(); !strings.HasPrefix(cont, "+") {
			t.Fatalf("APPEND %s continuation = %q, want +...", tc.tag, cont)
		}
		h.writeLine(newMessageRaw + "\r\n")
		unt, st := h.readResponse(tc.tag)
		if !strings.Contains(st, tc.uid) {
			t.Errorf("APPEND %s = %q, want %s", tc.tag, st, tc.uid)
		}
		if !slices.Contains(unt, tc.exists) {
			t.Errorf("APPEND %s untagged = %q, want %s", tc.tag, unt, tc.exists)
		}
	}
	if _, st := h.command("c1", "COPY 1 INBOX"); !strings.Contains(st, "[COPYUID 1 100 102]") {
		t.Errorf("COPY = %q, want [COPYUID 1 100 102]", st)
	}

	for _, tc := range []struct{ seq, uid, date string }{
		{"1", "UID 100", earlier},
		{"2", "UID 101", later},
		{"3", "UID 102", earlier},
	} {
		unt, st := h.command("f"+tc.seq, "FETCH %s (UID INTERNALDATE)", tc.seq)
		joined := strings.Join(unt, "\n")
		if !strings.Contains(st, "OK") || !strings.Contains(joined, tc.uid) || !strings.Contains(joined, `INTERNALDATE "`+tc.date+`"`) {
			t.Errorf("FETCH %s = %q / %q, want %s at %s", tc.seq, unt, st, tc.uid, tc.date)
		}
	}
}

func TestCopyUID_FlagsApplyToTheNewMessage(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "Archive", newMessageRaw, "Same") // unread, already in the destination
	api.seedFlagged(2, "INBOX", newMessageRaw, "Same", true, true, false, false, fakeReceivedAt)
	m := newUnitMailbox(t, api)

	uid, err := m.CopyUID(2, "Archive")
	if err != nil {
		t.Fatalf("CopyUID: %v", err)
	}
	if uid != 100 {
		t.Fatalf("CopyUID = %d, want the new message 100", uid)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if made := api.msgs[100]; made == nil || !made.isRead || !made.isFlagged {
		t.Errorf("the new message does not carry the source's flags: %+v", made)
	}
	if existing := api.msgs[1]; existing.isRead || existing.isFlagged {
		t.Errorf("the message already there changed: %+v", existing)
	}
}

func TestAppendUID_FlagsApplyToTheNewMessage(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "Archive", newMessageRaw, "Same")
	m := newUnitMailbox(t, api)

	uid, err := m.AppendUID("Archive", goimap.FlagUpdate{Seen: bptr(true)}, []byte(newMessageRaw))
	if err != nil {
		t.Fatalf("AppendUID: %v", err)
	}
	if uid != 100 {
		t.Fatalf("AppendUID = %d, want the new message 100", uid)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if made := api.msgs[100]; made == nil || !made.isRead {
		t.Errorf("the new message does not carry the requested flags: %+v", made)
	}
	if api.msgs[1].isRead {
		t.Errorf("the message already there changed: %+v", api.msgs[1])
	}
}
