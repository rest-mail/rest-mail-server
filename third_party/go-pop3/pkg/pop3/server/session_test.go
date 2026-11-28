package server

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Non-contiguous message UIDs prove the engine keys UIDL on the real message
// UID (not the sequence number) and that seq<->UID mapping is consistent.
const (
	rawMsg5 = "From: Alice <alice@example.com>\r\n" +
		"Subject: First\r\n" +
		"\r\n" +
		"Body line 1\r\n" +
		"Body line 2\r\n"

	// Body contains a line beginning with "." to exercise POP3 dot-stuffing.
	rawMsg9 = "From: Bob <bob@example.com>\r\n" +
		"Subject: Second\r\n" +
		"\r\n" +
		"Hello\r\n" +
		".hidden\r\n" +
		"World\r\n"

	rawMsg20 = "From: Carol <carol@example.com>\r\n" +
		"Subject: Third\r\n" +
		"\r\n" +
		"Third body\r\n"
)

func seedThree(m *mockBackend) {
	m.seed("5", 100, rawMsg5)
	m.seed("9", 200, rawMsg9)
	m.seed("20", 300, rawMsg20)
}

func TestPOP3_StatListUidl(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3Harness(t, m)
	h.login()

	if got := h.cmd("STAT"); got != "+OK 3 600" {
		t.Errorf("STAT = %q, want %q", got, "+OK 3 600")
	}

	// LIST (all messages).
	if got := h.cmd("LIST"); got != "+OK 3 messages (600 octets)" {
		t.Errorf("LIST header = %q", got)
	}
	if body := h.readDotBody(); !reflect.DeepEqual(body, []string{"1 100", "2 200", "3 300"}) {
		t.Errorf("LIST body = %v", body)
	}

	// UIDL must report the true message UIDs (5, 9, 20), not sequence numbers.
	if got := h.cmd("UIDL"); !strings.HasPrefix(got, "+OK") {
		t.Errorf("UIDL header = %q", got)
	}
	if body := h.readDotBody(); !reflect.DeepEqual(body, []string{"1 5", "2 9", "3 20"}) {
		t.Errorf("UIDL body = %v, want seq->UID mapping 1 5 / 2 9 / 3 20", body)
	}

	// LIST for a single message.
	if got := h.cmd("LIST 2"); got != "+OK 2 200" {
		t.Errorf("LIST 2 = %q", got)
	}
	// UIDL for a single message.
	if got := h.cmd("UIDL 3"); got != "+OK 3 20" {
		t.Errorf("UIDL 3 = %q", got)
	}
}

func TestPOP3_RetrServesRawWithDotStuffing(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3Harness(t, m)
	h.login()

	// RETR 2 -> message UID 9, served verbatim from stored raw.
	header := h.cmd("RETR 2")
	// The advertised octet count is the original raw length.
	if want := "+OK " + strconv.Itoa(len(rawMsg9)) + " octets"; header != want {
		t.Errorf("RETR header = %q, want %q", header, want)
	}

	body := h.readDotBody()
	joined := strings.Join(body, "\n")
	if !strings.Contains(joined, "Subject: Second") {
		t.Errorf("RETR body missing subject: %v", body)
	}
	// Dot-stuffing: a body line beginning with "." must be sent doubled.
	found := false
	for _, l := range body {
		if l == "..hidden" {
			found = true
		}
		if l == ".hidden" {
			t.Errorf("RETR body line %q was NOT dot-stuffed", l)
		}
	}
	if !found {
		t.Errorf("RETR body did not contain dot-stuffed line '..hidden': %v", body)
	}

	// Synchronize: a subsequent command reply guarantees the RETR handler fully
	// returned, including its post-body "mark seen" call.
	if got := h.cmd("NOOP"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("NOOP = %q", got)
	}
	// RETR marks the message seen via the mailbox.
	if !m.mbox.wasMarkedSeen("9") {
		t.Errorf("RETR did not mark message 9 as seen")
	}
}

func TestPOP3_TopHeadersOnly(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3Harness(t, m)
	h.login()

	// TOP 1 0 -> headers of message UID 5 plus zero body lines.
	if got := h.cmd("TOP 1 0"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("TOP header = %q", got)
	}
	body := h.readDotBody()
	joined := strings.Join(body, "\n")
	if !strings.Contains(joined, "From: Alice <alice@example.com>") {
		t.Errorf("TOP missing From header: %v", body)
	}
	if !strings.Contains(joined, "Subject: First") {
		t.Errorf("TOP missing Subject header: %v", body)
	}
	// Zero body lines requested: the actual body must not appear.
	if strings.Contains(joined, "Body line 1") {
		t.Errorf("TOP 1 0 leaked body content: %v", body)
	}
	// TOP must not mark the message seen.
	if m.mbox.wasMarkedSeen("5") {
		t.Errorf("TOP unexpectedly marked message 5 as seen")
	}
}

func TestPOP3_DeleCommittedOnQuit(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3Harness(t, m)
	h.login()

	if got := h.cmd("DELE 2"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("DELE 2 = %q", got)
	}
	// A deleted message drops out of STAT immediately, but is not yet committed.
	if got := h.cmd("STAT"); got != "+OK 2 400" {
		t.Errorf("STAT after DELE = %q, want +OK 2 400", got)
	}
	if len(m.mbox.deletedUIDs()) != 0 {
		t.Errorf("Delete called before QUIT: %v", m.mbox.deletedUIDs())
	}

	if got := h.cmd("QUIT"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("QUIT = %q", got)
	}

	// The commit happens on QUIT, and targets the real message UID (9), not seq 2.
	if got := m.mbox.deletedUIDs(); !reflect.DeepEqual(got, []string{"9"}) {
		t.Errorf("committed deletes = %v, want [9]", got)
	}
}

// TestPOP3_QuitReportsDeleteFailure asserts that when the UPDATE-phase expunge
// fails, QUIT answers -ERR rather than +OK. A +OK would tell the client its
// DELEtes committed when the messages are still present, causing the client to
// forget mail it never actually removed (RFC 1939 §6: the server MAY reply -ERR
// when some deleted messages could not be removed).
func TestPOP3_QuitReportsDeleteFailure(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3Harness(t, m)
	h.login()

	if got := h.cmd("DELE 2"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("DELE 2 = %q", got)
	}

	// Arm the backend so the commit on QUIT fails.
	m.mbox.failDelete(errors.New("store offline"))

	got := h.cmd("QUIT")
	if !strings.HasPrefix(got, "-ERR") {
		t.Fatalf("QUIT after failed delete = %q, want -ERR...", got)
	}

	// The failure must not be silent: Delete was still attempted on the UID.
	if d := m.mbox.deletedUIDs(); !reflect.DeepEqual(d, []string{"9"}) {
		t.Errorf("attempted deletes = %v, want [9]", d)
	}
}

// rawBareLF exercises the bare-LF hazard (RFC 1939 §3): a stored message whose
// lines end in a bare LF rather than CRLF, including a line that is exactly "."
// and a line beginning with ".". Both must be dot-stuffed and re-framed with
// CRLF so a bare LF cannot forge the "." terminator or desync the session.
const rawBareLF = "From: Eve <eve@example.com>\n" +
	"Subject: Bare LF\n" +
	"\n" +
	"line1\n" +
	".\n" + // a line that is exactly "." — must be stuffed to ".."
	".hidden\n" + // a line beginning with "." after bare LF — must be stuffed
	"line4\n"

// containsLine reports whether lines holds an element equal to want.
func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestPOP3_RetrBareLFDotStuffing(t *testing.T) {
	m := newMockBackend()
	m.seed("7", 100, rawBareLF)
	h := newPOP3Harness(t, m)
	h.login()

	if got := h.cmd("RETR 1"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("RETR header = %q", got)
	}
	body := h.readDotBody()

	// A bare-LF "." line must not prematurely terminate RETR: content after it
	// (line4) must still arrive.
	if !containsLine(body, "line4") {
		t.Errorf("RETR terminated early on a bare-LF '.' line; body = %v", body)
	}
	// The exactly-"." line must be dot-stuffed to "..".
	if !containsLine(body, "..") {
		t.Errorf("bare-LF '.' line was not dot-stuffed to '..'; body = %v", body)
	}
	// A bare-LF line beginning with "." must be dot-stuffed to "..hidden".
	if !containsLine(body, "..hidden") {
		t.Errorf("bare-LF '.hidden' line was not dot-stuffed; body = %v", body)
	}
	if containsLine(body, ".hidden") {
		t.Errorf("bare-LF '.hidden' line reached the wire unstuffed; body = %v", body)
	}

	// The session must not be desynchronized: the next command gets its own
	// reply, not leftover message bytes.
	if got := h.cmd("NOOP"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("session desynced after bare-LF RETR: NOOP = %q", got)
	}
}

func TestPOP3_TopBareLFDotStuffing(t *testing.T) {
	m := newMockBackend()
	m.seed("7", 100, rawBareLF)
	h := newPOP3Harness(t, m)
	h.login()

	// Request more body lines than exist, so the whole (bare-LF) body is served.
	if got := h.cmd("TOP 1 5"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("TOP header = %q", got)
	}
	body := h.readDotBody()

	// Header/body split must survive bare-LF separators, and body content after
	// the bare-LF "." line must not be truncated.
	if !containsLine(body, "Subject: Bare LF") {
		t.Errorf("TOP dropped headers on bare-LF message; body = %v", body)
	}
	if !containsLine(body, "line4") {
		t.Errorf("TOP terminated early on a bare-LF '.' line; body = %v", body)
	}
	if !containsLine(body, "..") {
		t.Errorf("bare-LF '.' line was not dot-stuffed to '..'; body = %v", body)
	}
	if !containsLine(body, "..hidden") {
		t.Errorf("bare-LF '.hidden' line was not dot-stuffed; body = %v", body)
	}

	if got := h.cmd("NOOP"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("session desynced after bare-LF TOP: NOOP = %q", got)
	}
	// TOP must not mark the message seen.
	if m.mbox.wasMarkedSeen("7") {
		t.Errorf("TOP unexpectedly marked message 7 as seen")
	}
}

func TestPOP3_AuthFailureRejected(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3Harness(t, m)

	if got := h.cmd("USER %s", m.user); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("USER = %q", got)
	}
	if got := h.cmd("PASS wrong-password"); !strings.HasPrefix(got, "-ERR") {
		t.Errorf("PASS with wrong password = %q, want -ERR", got)
	}
	// A command that requires auth must be refused.
	if got := h.cmd("STAT"); !strings.HasPrefix(got, "-ERR") {
		t.Errorf("STAT while unauthenticated = %q, want -ERR", got)
	}
}

// A keyword with a Unicode look-alike letter is not a POP3 command (RFC 1939
// §3 keywords are ASCII): "paſs" must not log in and "quıt" must not end the
// session.
func TestPOP3_UnicodeLookalikeKeywordsRejected(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3Harness(t, m)

	if got := h.cmd("USER %s", m.user); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("USER = %q", got)
	}
	if got := h.cmd("paſs %s", m.pass); !strings.HasPrefix(got, "-ERR") {
		t.Fatalf("paſs with the right password = %q, want -ERR", got)
	}
	if got := h.cmd("STAT"); !strings.HasPrefix(got, "-ERR") {
		t.Fatalf("STAT after paſs = %q, want -ERR (not logged in)", got)
	}
	// The look-alike and STAT invalidate pending USER. Start a fresh exchange.
	if got := h.cmd("USER %s", m.user); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("fresh USER = %q", got)
	}
	if got := h.cmd("PASS %s", m.pass); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("PASS = %q", got)
	}
	for _, line := range []string{"lıst", "quıt", "\xffSTAT"} {
		if got := h.cmd("%s", line); !strings.HasPrefix(got, "-ERR") {
			t.Errorf("%q = %q, want -ERR", line, got)
		}
	}
	if got := h.cmd("LIST 1"); !strings.HasPrefix(got, "+OK") {
		t.Errorf("LIST 1 after the look-alikes = %q, want +OK", got)
	}
}
