package rfctest

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// This mailbox models successful in-memory removal, not storage durability or
// locking. Opaque backend UIDs deliberately differ from wire numbers 1 and 2.
type sessionVectorMailbox struct {
	mu        sync.Mutex
	raw       map[string]string
	retrieved []string
	deletes   map[string]int
}

func (m *sessionVectorMailbox) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if user != "alice" || pass != "secret" {
		return nil, errors.New("invalid fixture credentials")
	}
	return m, nil
}
func (*sessionVectorMailbox) Messages() ([]pop3.Message, error) {
	// Published example sizes, independent of the fixture byte-count assertion.
	return []pop3.Message{{UID: "example-alpha", Size: 120}, {UID: "example-bravo", Size: 200}}, nil
}
func (m *sessionVectorMailbox) Retrieve(uid string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retrieved = append(m.retrieved, uid)
	raw, ok := m.raw[uid]
	if !ok {
		return nil, errors.New("unknown fixture UID")
	}
	return []byte(raw), nil
}
func (*sessionVectorMailbox) MarkSeen(string) error { return nil }
func (m *sessionVectorMailbox) Delete(uid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes[uid]++
	if _, ok := m.raw[uid]; !ok {
		return errors.New("unknown or already removed fixture UID")
	}
	delete(m.raw, uid)
	return nil
}

// TestRFC1939AdaptedTransactionExample follows RFC 1939 section 10's two-message
// 120/200-octet transaction sequence: https://www.rfc-editor.org/rfc/rfc1939.html#section-10
// ADAPTED TRANSACTION example: unsupported APOP is replaced by existing USER/PASS;
// the RFC leaves bodies unspecified, so these canonical CRLF messages are synthetic.
// Extra post-DELE STAT/LIST checks below are controls, not literal section 10 text.
// checklist: POP3-EXAMPLE-01
func TestRFC1939AdaptedTransactionExample(t *testing.T) {
	const headersOne = "Date: Thu, 1 Jan 1970 00:00:00 +0000\r\nFrom: a@example.org\r\nSubject: one\r\n\r\n"
	const headersTwo = "Date: Thu, 1 Jan 1970 00:00:00 +0000\r\nFrom: a@example.org\r\nSubject: two\r\n\r\n"
	first := headersOne + ".\r\n..\r\n.x\r\n" + strings.Repeat("a", 32) + "\r\n"
	second := headersTwo + ".second\r\n\r\n" + strings.Repeat("b", 112) + "\r\n"
	// Do not derive expected sizes from backend metadata or pop3.OctetCount.
	if len(first) != 120 || len(second) != 200 {
		t.Fatalf("synthetic fixture octets = %d/%d, want 120/200", len(first), len(second))
	}
	m := &sessionVectorMailbox{
		raw:     map[string]string{"example-alpha": first, "example-bravo": second},
		deletes: map[string]int{},
	}
	checkDeferred := func() {
		t.Helper()
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.deletes) != 0 || len(m.raw) != 2 || m.raw["example-alpha"] != first || m.raw["example-bravo"] != second {
			t.Fatalf("before QUIT: deletes = %v, remaining = %v", m.deletes, m.raw)
		}
	}
	w := connect(t, m) // Public Session, memory, net.Pipe, nil TLS and no-op limiter.
	w.login("secret")
	expect(t, w.cmd("STAT"), "+OK 2 320")
	expect(t, w.cmd("LIST"), "+OK 2 messages (320 octets)")
	expect(t, w.line(), "1 120")
	expect(t, w.line(), "2 200")
	expect(t, w.line(), ".")

	// Read every transmitted byte, including CRLF, explicit dot stuffing and
	// exactly one terminator. Expected stuffing does not use a library helper.
	readMessage := func(want string) {
		t.Helper()
		_ = w.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, len(want))
		if _, err := io.ReadFull(w.reader, got); err != nil {
			t.Fatalf("read complete RETR: %v (partial %q)", err, got)
		}
		expect(t, string(got), want)
	}
	expect(t, w.cmd("RETR 1"), "+OK 120 octets")
	readMessage(headersOne + "..\r\n...\r\n..x\r\n" + strings.Repeat("a", 32) + "\r\n.\r\n")
	expect(t, w.cmd("DELE 1"), "+OK Message 1 deleted")
	checkDeferred()
	// Added visibility/numbering control: message 2 must not become message 1.
	expect(t, w.cmd("STAT"), "+OK 1 200")
	expect(t, w.cmd("LIST"), "+OK 1 messages (200 octets)")
	expect(t, w.line(), "2 200")
	expect(t, w.line(), ".")

	expect(t, w.cmd("RETR 2"), "+OK 200 octets")
	readMessage(headersTwo + "..second\r\n\r\n" + strings.Repeat("b", 112) + "\r\n.\r\n")
	expect(t, w.cmd("DELE 2"), "+OK Message 2 deleted")
	checkDeferred()
	expect(t, w.cmd("QUIT"), "+OK POP3 server signing off")
	_ = w.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := w.reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("after QUIT: %v, want EOF", err)
	}
	waitStopped(t, w)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !reflect.DeepEqual(m.deletes, map[string]int{"example-alpha": 1, "example-bravo": 1}) || len(m.raw) != 0 {
		t.Fatalf("after QUIT: deletes = %v, remaining = %v", m.deletes, m.raw)
	}
	if !reflect.DeepEqual(m.retrieved, []string{"example-alpha", "example-bravo"}) {
		t.Fatalf("retrieved backend UIDs = %v", m.retrieved)
	}
}
