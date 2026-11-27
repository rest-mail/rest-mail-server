package rfctest

import (
	"testing"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// loginDelayCapaBackend announces LOGIN-DELAY. Authenticate returns a Mailbox
// that reports its own delay only when userSeconds is set.
type loginDelayCapaBackend struct {
	commandErrorMailbox
	seconds     int
	perUser     bool
	userSeconds *int
}

func (b *loginDelayCapaBackend) LoginDelay() (int, bool) { return b.seconds, b.perUser }

func (b *loginDelayCapaBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if _, err := b.commandErrorMailbox.Authenticate(user, pass); err != nil {
		return nil, err
	}
	if b.userSeconds != nil {
		return &loginDelayCapaMailbox{commandErrorMailbox: &b.commandErrorMailbox, seconds: *b.userSeconds}, nil
	}
	return &b.commandErrorMailbox, nil
}

type loginDelayCapaMailbox struct {
	*commandErrorMailbox
	seconds int
}

func (m *loginDelayCapaMailbox) LoginDelay() int { return m.seconds }

var (
	_ pop3.LoginDelayBackend = (*loginDelayCapaBackend)(nil)
	_ pop3.LoginDelayMailbox = (*loginDelayCapaMailbox)(nil)
)

// loginDelayCapa reads a whole CAPA response and returns the LOGIN-DELAY line,
// or "" when it is absent. The rest of the list is pinned so the tag adds a
// line rather than replacing one.
func loginDelayCapa(t *testing.T, w *wire) string {
	t.Helper()
	expect(t, w.cmd("CAPA"), "+OK Capability list follows")
	for _, line := range []string{"USER", "TOP", "UIDL", "RESP-CODES", "AUTH-RESP-CODE", "PIPELINING"} {
		expect(t, w.line(), line)
	}
	tag := ""
	line := w.line()
	if line != "." {
		tag = line
		expect(t, w.line(), ".")
	}
	return tag
}

// RFC 2449 §6.5: LOGIN-DELAY announces the minimum seconds between logins in
// both states, optionally followed by USER in AUTHORIZATION state when the
// value varies per user; after login CAPA carries the user's own value.
// checklist: POP3-CAPA-05 POP3-LDELAY-03
func TestCapaLoginDelay(t *testing.T) {
	own := func(n int) *int { return &n }
	for _, tc := range []struct {
		name          string
		seconds       int
		perUser       bool
		userSeconds   *int
		authorization string
		transaction   string
	}{
		{"fixed", 900, false, nil, "LOGIN-DELAY 900", "LOGIN-DELAY 900"},
		{"zero", 0, false, nil, "LOGIN-DELAY 0", "LOGIN-DELAY 0"},
		{"per-user", 300, true, own(1800), "LOGIN-DELAY 300 USER", "LOGIN-DELAY 1800"},
		{"per-user-no-mailbox-value", 300, true, nil, "LOGIN-DELAY 300 USER", "LOGIN-DELAY 300"},
		{"omitted-before-login", -1, true, own(600), "", "LOGIN-DELAY 600"},
		{"omitted-after-login", 60, true, own(-1), "LOGIN-DELAY 60 USER", ""},
		{"omitted", -1, false, nil, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &loginDelayCapaBackend{seconds: tc.seconds, perUser: tc.perUser, userSeconds: tc.userSeconds}
			w := connect(t, b)
			expect(t, loginDelayCapa(t, w), tc.authorization)
			// A failed login stays in AUTHORIZATION state.
			expect(t, w.cmd("USER alice"), "+OK")
			expect(t, w.cmd("PASS wrong"), "-ERR [AUTH] Authentication failed")
			expect(t, loginDelayCapa(t, w), tc.authorization)
			w.login("secret")
			expect(t, loginDelayCapa(t, w), tc.transaction)
			expect(t, w.cmd("STAT"), "+OK 2 59")
			quit(t, w, "+OK")
			b.calls(t, 2, 1, 0)
		})
	}
}

// A Backend without LoginDelayBackend never announces the tag, even when its
// Mailbox reports a delay.
// checklist: POP3-CAPA-05
func TestCapaLoginDelayRequiresBackend(t *testing.T) {
	m := &commandErrorMailbox{}
	w := connect(t, loginDelayMailboxOnly{m})
	expect(t, loginDelayCapa(t, w), "")
	w.login("secret")
	expect(t, loginDelayCapa(t, w), "")
	quit(t, w, "+OK")
}

type loginDelayMailboxOnly struct{ m *commandErrorMailbox }

func (b loginDelayMailboxOnly) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if _, err := b.m.Authenticate(user, pass); err != nil {
		return nil, err
	}
	return &loginDelayCapaMailbox{commandErrorMailbox: b.m, seconds: 900}, nil
}
