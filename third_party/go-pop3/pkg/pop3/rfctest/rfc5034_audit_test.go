package rfctest

import (
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// maxPlainBackend accepts one PLAIN identity with 255-octet fields, the
// largest RFC 4616 §2 requires a server to accept, and records each call.
type maxPlainBackend struct {
	backend
	id, pass string
	mu       sync.Mutex
	calls    int
}

func (b *maxPlainBackend) AuthenticatePlain(authzid, authcid, pass string, _ bool) (pop3.Mailbox, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	if authzid != b.id || authcid != b.id || pass != b.pass {
		return nil, errors.New("invalid credentials")
	}
	return mailbox{}, nil
}

// RFC 5034 §4 lifts the line limit for responses after the "+ " challenge:
// the server MUST handle the largest response its mechanisms generate. With
// 255-octet authzid, authcid and password, PLAIN encodes to 1024 base64
// characters, a 1026-octet line with CRLF, which is accepted and reaches the
// Backend intact. One octet more is refused and closes, with no Backend call.
// checklist: POP3-SASL-08 POP3-LENGTH-03 POP3-STLS-18 POP3-PLAIN-01
func TestRFC5034LargestPlainResponse(t *testing.T) {
	id := strings.Repeat("a", 255)
	pass := strings.Repeat("p", 255)
	response := plain(id, id, pass)
	if len(response) != 1024 {
		t.Fatalf("fixture response is %d octets, want 1024", len(response))
	}
	t.Run("largest", func(t *testing.T) {
		b := &maxPlainBackend{backend: backend{password: "secret"}, id: id, pass: pass}
		w := connect(t, b)
		expect(t, w.cmd("AUTH PLAIN"), "+ ")
		expect(t, w.cmd(response), "+OK Authentication successful")
		expect(t, w.cmd("STAT"), "+OK 1 42")
		if b.calls != 1 {
			t.Fatalf("AuthenticatePlain calls = %d, want 1", b.calls)
		}
	})
	t.Run("one-over", func(t *testing.T) {
		b := &maxPlainBackend{backend: backend{password: "secret"}, id: id, pass: pass}
		w := connect(t, b)
		expect(t, w.cmd("AUTH PLAIN"), "+ ")
		go func() { _, _ = io.WriteString(w.conn, response+"A\r\n") }()
		expect(t, w.line(), "-ERR Command line too long")
		w.closed()
		if b.calls != 0 {
			t.Fatalf("AuthenticatePlain calls = %d, want 0", b.calls)
		}
	})
}

// RFC 5034 §4: the server MUST reject, not ignore, any character outside the
// base64 alphabet, and any pad character before the end. encoding/base64
// silently skips CR and LF, so a bare CR inside a response must still be
// refused. None of these reach the Backend, and a fresh AUTH then succeeds.
// checklist: POP3-SASL-09
func TestRFC5034Base64Strictness(t *testing.T) {
	good := plain("", "alice", "secret")
	for _, tc := range []struct {
		name, initial, response string
	}{
		{"pad-first", "", "=AAA"},
		{"pad-middle", "", "AAA=BBBB"},
		{"unpadded", "", strings.TrimRight(base64.StdEncoding.EncodeToString([]byte("\x00alice\x00secret")), "=")},
		{"space", "", good[:4] + " " + good[4:]},
		{"bare-cr", "", good[:4] + "\r" + good[4:]},
		{"url-alphabet", "", "-_" + good[2:]},
		{"initial-bare-cr", good[:4] + "\r" + good[4:], ""},
		{"initial-pad-middle", "AAA=BBBB", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &saslBackend{backend: backend{password: "secret"}}
			w := connect(t, b)
			if tc.initial != "" {
				expect(t, w.cmd("AUTH PLAIN "+tc.initial), "-ERR Invalid base64 response")
			} else {
				expect(t, w.cmd("AUTH PLAIN"), "+ ")
				expect(t, w.cmd(tc.response), "-ERR Invalid base64 response")
			}
			b.expectCalls(t)
			expect(t, w.cmd("STAT"), "-ERR Not authenticated")
			expect(t, w.cmd("AUTH PLAIN "+good), "+OK Authentication successful")
		})
	}
}
