package rfctest

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// utf8Backend records the utf8 flag of every AuthenticateUTF8 call so the
// tests can see what UTF-8 mode the session reported to the backend.
type utf8Backend struct {
	commandErrorMailbox
	mu    sync.Mutex
	modes []bool
}

func (b *utf8Backend) AuthenticateUTF8(user, pass string, utf8 bool) (pop3.Mailbox, error) {
	b.mu.Lock()
	b.modes = append(b.modes, utf8)
	b.mu.Unlock()
	return b.Authenticate(user, pass)
}

func (b *utf8Backend) reported(t *testing.T, want ...bool) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.modes) != len(want) {
		t.Fatalf("AuthenticateUTF8 modes = %v, want %v", b.modes, want)
	}
	for i := range want {
		if b.modes[i] != want[i] {
			t.Fatalf("AuthenticateUTF8 modes = %v, want %v", b.modes, want)
		}
	}
}

var _ pop3.UTF8Backend = (*utf8Backend)(nil)

// RFC 6856 §2: the UTF8 tag is announced in both states and carries no USER
// argument, because the engine does not apply SASLprep to USER/PASS. A
// Backend without the extension announces nothing.
// checklist: POP3-UTF8-01
func TestUTF8CapabilityBothStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    pop3.Backend
		want bool
	}{
		{"utf8-backend", &utf8Backend{}, true},
		{"plain-backend", &commandErrorMailbox{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := connect(t, tc.b)
			for _, authenticated := range []bool{false, true} {
				if authenticated {
					w.login("secret")
				}
				params, ok := extensionCapabilities(t, w)["UTF8"]
				if ok != tc.want || len(params) != 0 {
					t.Fatalf("authenticated=%t: UTF8 capability = %v, present=%t, want present=%t without arguments",
						authenticated, params, ok, tc.want)
				}
			}
			quit(t, w, "+OK")
		})
	}
}

// RFC 6856 §2.1: UTF8 takes no parameters and is valid only in AUTHORIZATION
// state. A refused UTF8 leaves the session in ASCII mode, and the backend sees
// whether UTF-8 mode was enabled before the login.
// checklist: POP3-UTF8-02 POP3-UTF8-03 POP3-UTF8-11
func TestUTF8CommandStates(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		b := &utf8Backend{}
		w := connect(t, b)
		expect(t, w.cmd("utf8"), "+OK UTF8 enabled")
		w.login("secret")
		w.status(w.cmd("UTF8"), "-ERR")
		w.status(w.cmd("STAT"), "+OK")
		quit(t, w, "+OK")
		b.reported(t, true)
	})
	t.Run("not-issued", func(t *testing.T) {
		b := &utf8Backend{}
		w := connect(t, b)
		w.login("secret")
		quit(t, w, "+OK")
		b.reported(t, false)
	})
	t.Run("argument-refused", func(t *testing.T) {
		b := &utf8Backend{}
		w := connect(t, b)
		w.status(w.cmd("UTF8 USER"), "-ERR")
		w.login("secret")
		quit(t, w, "+OK")
		b.reported(t, false)
	})
	t.Run("mode-survives-failed-login", func(t *testing.T) {
		b := &utf8Backend{}
		w := connect(t, b)
		w.status(w.cmd("UTF8"), "+OK")
		w.status(w.cmd("USER alice"), "+OK")
		w.status(w.cmd("PASS wrong"), "-ERR")
		w.login("secret")
		quit(t, w, "+OK")
		b.reported(t, true, true)
	})
	t.Run("without-extension", func(t *testing.T) {
		m := &commandErrorMailbox{}
		w := connect(t, m)
		w.status(w.cmd("UTF8"), "-ERR")
		w.login("secret")
		w.status(w.cmd("UTF8"), "-ERR")
		quit(t, w, "+OK")
		m.calls(t, 1, 1, 0)
	})
}

// RFC 6856 §2.1: clients must not issue STLS after UTF8, and servers MAY
// refuse it. This library accepts it but, as with CAPA (RFC 2595 §4), does not
// carry the mode across TLS: the client must issue UTF8 again.
// checklist: POP3-UTF8-04
func TestUTF8ModeResetByTLS(t *testing.T) {
	serverConfig, clientConfig := stlsConfigs(t)
	for _, again := range []bool{false, true} {
		b := &utf8Backend{}
		w := extensionConnect(t, b, serverConfig)
		w.status(w.cmd("UTF8"), "+OK")
		stlsUpgrade(t, w, clientConfig)
		if _, ok := extensionCapabilities(t, w)["UTF8"]; !ok {
			t.Fatal("UTF8 not announced after TLS")
		}
		if again {
			w.status(w.cmd("UTF8"), "+OK")
		}
		w.login("secret")
		quit(t, w, "+OK")
		b.reported(t, again)
	}
}

// utf8RefusalMailbox serves message 2 and refuses message 1 with err, as a
// Mailbox with no downgraded form of an internationalized message would.
type utf8RefusalMailbox struct {
	err  error
	seen atomic.Int32
}

func (m *utf8RefusalMailbox) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if user != "alice" || pass != "secret" {
		return nil, errors.New("invalid credentials")
	}
	return m, nil
}
func (m *utf8RefusalMailbox) Messages() ([]pop3.Message, error) {
	return []pop3.Message{{UID: "eai", Size: 40}, {UID: "ascii", Size: 21}}, nil
}
func (m *utf8RefusalMailbox) Retrieve(uid string) ([]byte, error) {
	if uid == "eai" {
		return nil, m.err
	}
	return []byte("Subject: hi\r\n\r\nbody\r\n"), nil
}
func (m *utf8RefusalMailbox) MarkSeen(string) error { m.seen.Add(1); return nil }
func (m *utf8RefusalMailbox) Delete(string) error   { return nil }

type utf8RefusalBackend struct{ *utf8RefusalMailbox }

func (b utf8RefusalBackend) AuthenticateUTF8(user, pass string, _ bool) (pop3.Mailbox, error) {
	return b.Authenticate(user, pass)
}

// RFC 6856 §5: a TOP or RETR refused because the message needs UTF-8 mode
// carries [UTF8], telling the client to issue UTF8 and try again. LIST is never
// refused: it reports the Message.Size the Mailbox supplies (POP3-UTF8-06).
// The code is sent only when the client could act on it: the Backend
// announces UTF8 and the session is not already in UTF-8 mode. The refused
// message is not marked seen, and the session goes on serving other messages.
// checklist: POP3-UTF8-10
func TestUTF8ResponseCode(t *testing.T) {
	const coded, plain = "-ERR [UTF8] Message requires UTF-8 mode", "-ERR Failed to retrieve message"
	for _, tc := range []struct {
		name      string
		err       error
		extension bool
		utf8      bool
		want      string
	}{
		{"direct", pop3.ErrUTF8Required, true, false, coded},
		{"wrapped", fmt.Errorf("downgrade: %w", pop3.ErrUTF8Required), true, false, coded},
		{"other-error", errors.New("disk failure"), true, false, plain},
		{"without-extension", pop3.ErrUTF8Required, false, false, plain},
		{"already-utf8", pop3.ErrUTF8Required, true, true, plain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &utf8RefusalMailbox{err: tc.err}
			var b pop3.Backend = m
			if tc.extension {
				b = utf8RefusalBackend{m}
			}
			w := connect(t, b)
			if tc.utf8 {
				w.status(w.cmd("UTF8"), "+OK")
			}
			w.login("secret")
			expect(t, w.cmd("RETR 1"), tc.want)
			expect(t, w.cmd("TOP 1 0"), tc.want)
			expect(t, w.cmd("LIST 1"), "+OK 1 40")
			expect(t, w.cmd("RETR 2"), "+OK 21 octets")
			for _, want := range []string{"Subject: hi", "", "body", "."} {
				expect(t, w.line(), want)
			}
			expect(t, w.cmd("STAT"), "+OK 2 61")
			quit(t, w, "+OK")
			if got := m.seen.Load(); got != 1 {
				t.Fatalf("MarkSeen calls = %d, want 1 (message 2 only)", got)
			}
		})
	}
}
