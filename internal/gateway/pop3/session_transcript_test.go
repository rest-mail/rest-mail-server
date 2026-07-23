package pop3

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Non-contiguous message IDs prove the gateway keys UIDL on the real message ID
// (not the sequence number) and that seq<->id mapping is consistent.
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
	m.seed("INBOX", 5, 100, rawMsg5)
	m.seed("INBOX", 9, 200, rawMsg9)
	m.seed("INBOX", 20, 300, rawMsg20)
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

	// UIDL must report the true message IDs (5, 9, 20), not sequence numbers.
	if got := h.cmd("UIDL"); !strings.HasPrefix(got, "+OK") {
		t.Errorf("UIDL header = %q", got)
	}
	if body := h.readDotBody(); !reflect.DeepEqual(body, []string{"1 5", "2 9", "3 20"}) {
		t.Errorf("UIDL body = %v, want seq->id mapping 1 5 / 2 9 / 3 20", body)
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

	// RETR 2 -> message ID 9, served verbatim from stored raw.
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
	// returned, including its post-body "mark read" call.
	if got := h.cmd("NOOP"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("NOOP = %q", got)
	}
	// RETR marks the message read via the backend.
	if !m.wasMarkedRead(9) {
		t.Errorf("RETR did not mark message 9 as read")
	}
}

func TestPOP3_TopHeadersOnly(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3Harness(t, m)
	h.login()

	// TOP 1 0 -> headers of message ID 5 plus zero body lines.
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
	// TOP must not mark the message read.
	if m.wasMarkedRead(5) {
		t.Errorf("TOP unexpectedly marked message 5 as read")
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
	if len(m.deletedIDs()) != 0 {
		t.Errorf("DeleteMessage called before QUIT: %v", m.deletedIDs())
	}

	if got := h.cmd("QUIT"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("QUIT = %q", got)
	}

	// The commit happens on QUIT, and targets the real message ID (9), not seq 2.
	if got := m.deletedIDs(); !reflect.DeepEqual(got, []uint{9}) {
		t.Errorf("committed deletes = %v, want [9]", got)
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
