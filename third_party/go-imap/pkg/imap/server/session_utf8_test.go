package server

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// utf8Store is a uidPlusStore that also implements [UTF8Mailbox] and records
// each EnableUTF8 call.
type utf8Store struct {
	*uidPlusStore
	enables int
	err     error
}

func (m *utf8Store) EnableUTF8() error {
	m.enables++
	return m.err
}

type utf8Backend struct {
	*uidPlusBackend
	mbox *utf8Store
}

func newUTF8Backend() *utf8Backend {
	b := newUIDPlusBackend()
	return &utf8Backend{uidPlusBackend: b, mbox: &utf8Store{uidPlusStore: b.store}}
}

func (b *utf8Backend) Authenticate(user, pass string) (imap.Mailbox, error) {
	if user != b.user || pass != b.pass {
		return nil, fmt.Errorf("invalid credentials")
	}
	return b.mbox, nil
}

func TestUTF8Accept_CapabilityOnlyWhenSupported(t *testing.T) {
	b := newUTF8Backend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)

	pre, _ := h.command("c1", "CAPABILITY")
	mustNotContain(t, strings.Join(pre, "\n"), "UTF8=ACCEPT", "pre-auth CAPABILITY")

	_, loginStatus := h.command("c2", "LOGIN %s %s", b.user, b.pass)
	mustContain(t, loginStatus, "UTF8=ACCEPT", "LOGIN status")

	post, _ := h.command("c3", "CAPABILITY")
	mustContain(t, strings.Join(post, "\n"), "UTF8=ACCEPT", "post-auth CAPABILITY")

	base := newUIDPlusBackend()
	hb := newIMAPHarnessWith(t, base, base.user, base.pass)
	hb.login("b1")
	caps, _ := hb.command("b2", "CAPABILITY")
	mustNotContain(t, strings.Join(caps, "\n"), "UTF8=ACCEPT", "base backend CAPABILITY")
}

func TestUTF8Accept_EnableReportsEnabledOnce(t *testing.T) {
	b := newUTF8Backend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")

	untagged, status := h.command("a2", "ENABLE CONDSTORE utf8=accept")
	mustContain(t, status, " OK", "ENABLE status")
	if got := strings.Join(untagged, "\n"); got != "* ENABLED UTF8=ACCEPT" {
		t.Errorf("ENABLE untagged = %q, want * ENABLED UTF8=ACCEPT", got)
	}
	if b.mbox.enables != 1 {
		t.Errorf("EnableUTF8 calls = %d, want 1", b.mbox.enables)
	}

	// Already enabled: not listed again, backend not called again.
	untagged, status = h.command("a3", "ENABLE UTF8=ACCEPT")
	mustContain(t, status, " OK", "second ENABLE status")
	if strings.Join(untagged, "\n") != "* ENABLED" {
		t.Errorf("second ENABLE untagged = %q, want empty ENABLED", untagged)
	}
	if b.mbox.enables != 1 {
		t.Errorf("EnableUTF8 calls = %d, want 1", b.mbox.enables)
	}
}

func TestUTF8Accept_IgnoredInSelectedState(t *testing.T) {
	b := newUTF8Backend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")
	if _, st := h.command("a2", "SELECT INBOX"); !strings.Contains(st, " OK") {
		t.Fatalf("SELECT status = %q", st)
	}

	untagged, status := h.command("a3", "ENABLE UTF8=ACCEPT")
	mustContain(t, status, " OK", "ENABLE status")
	if strings.Join(untagged, "\n") != "* ENABLED" || b.mbox.enables != 0 {
		t.Errorf("selected-state ENABLE untagged = %q, enables = %d; want empty ENABLED, 0", untagged, b.mbox.enables)
	}
}

func TestUTF8Accept_BackendErrorFailsEnable(t *testing.T) {
	b := newUTF8Backend()
	b.mbox.err = errors.New("boom")
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")

	untagged, status := h.command("a2", "ENABLE UTF8=ACCEPT")
	mustContain(t, status, " NO", "ENABLE status")
	mustNotContain(t, status, "boom", "ENABLE status")
	if len(untagged) != 0 {
		t.Errorf("failed ENABLE untagged = %q, want none", untagged)
	}

	// The mode stayed off, so a retry calls the backend again.
	b.mbox.err = nil
	untagged, _ = h.command("a3", "ENABLE UTF8=ACCEPT")
	mustContain(t, strings.Join(untagged, "\n"), "* ENABLED UTF8=ACCEPT", "retry ENABLE")
}

func TestUTF8Accept_BaseBackendIgnoresEnable(t *testing.T) {
	b := newUIDPlusBackend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")

	untagged, status := h.command("a2", "ENABLE UTF8=ACCEPT")
	mustContain(t, status, " OK", "ENABLE status")
	if strings.Join(untagged, "\n") != "* ENABLED" {
		t.Errorf("base backend ENABLE untagged = %q, want empty ENABLED", untagged)
	}
}
