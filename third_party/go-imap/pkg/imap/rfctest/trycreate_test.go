package rfctest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// trycreateMailbox knows only INBOX and Archive. Any other destination is
// refused with a wrapped [imap.ErrNoSuchMailbox], as a store that does not
// create folders implicitly would report it; nothing is stored or moved.
type trycreateMailbox struct {
	*copyMailbox
	appended int
}

func newTrycreateMailbox() *trycreateMailbox {
	return &trycreateMailbox{copyMailbox: newCopyMailbox()}
}

func (m *trycreateMailbox) exists(dest string) error {
	if dest == "INBOX" || dest == "Archive" {
		return nil
	}
	return fmt.Errorf("store: folder %q: %w", dest, imap.ErrNoSuchMailbox)
}
func (m *trycreateMailbox) Copy(uid uint32, dest string) error {
	if err := m.exists(dest); err != nil {
		return err
	}
	return m.copyMailbox.Copy(uid, dest)
}
func (m *trycreateMailbox) Move(uid uint32, dest string) error { return m.Copy(uid, dest) }
func (m *trycreateMailbox) Append(dest string, _ imap.FlagUpdate, _ []byte) error {
	if err := m.exists(dest); err != nil {
		return err
	}
	m.appended++
	return nil
}

// trycreateUIDMailbox reaches the same store through the UIDPLUS and atomic
// Mover forms, so every backend entry point APPEND, COPY and MOVE use is shown.
type trycreateUIDMailbox struct{ *trycreateMailbox }

func (*trycreateUIDMailbox) UIDValidity(string) (uint32, error) { return 38505, nil }
func (m *trycreateUIDMailbox) AppendUID(dest string, f imap.FlagUpdate, raw []byte) (uint32, error) {
	if err := m.Append(dest, f, raw); err != nil {
		return 0, err
	}
	return 3955, nil
}
func (m *trycreateUIDMailbox) CopyUID(uid uint32, dest string) (uint32, error) {
	if err := m.Copy(uid, dest); err != nil {
		return 0, err
	}
	return 4000 + uid, nil
}
func (m *trycreateUIDMailbox) MoveUID(uid uint32, dest string) (uint32, error) {
	return m.CopyUID(uid, dest)
}

// TestTryCreateMissingDestination proves that a destination the store reports
// as missing makes APPEND, COPY and MOVE fail with NO [TRYCREATE], that the
// engine creates nothing and moves nothing, and that any other backend error
// keeps a plain NO, since TRYCREATE would send the client to CREATE in vain.
// checklist: IMAP-APPEND-18 IMAP-COPY-13 IMAP-MOVE-11 IMAP-RESP-09
// checklist: IMAP2-CMD-157 IMAP2-CMD-163
func TestTryCreateMissingDestination(t *testing.T) {
	for _, uidPlus := range []bool{false, true} {
		box := func() (*trycreateMailbox, imap.Mailbox) {
			m := newTrycreateMailbox()
			if uidPlus {
				return m, &trycreateUIDMailbox{m}
			}
			return m, m
		}
		for _, tc := range []struct{ name, set string }{{"COPY", "2:3"}, {"UID COPY", "304:319"}, {"MOVE", "2:3"}, {"UID MOVE", "304:319"}} {
			t.Run(fmt.Sprintf("%v/%s", uidPlus, tc.name), func(t *testing.T) {
				m, b := box()
				c := expungeClient(t, b)
				lines, tagged := c.command("C1", "%s %s Missing", tc.name, tc.set)
				requireExactly(t, lines)
				if want := "C1 NO [TRYCREATE] " + tc.name + " failed"; tagged != want {
					t.Fatalf("missing destination: %q, want %q", tagged, want)
				}
				if len(m.calls) != 0 {
					t.Fatalf("store received %v", m.calls)
				}
				requireRemaining(t, c, 300, 304, 319, 320)

				m.fail = 304
				_, tagged = c.command("C2", "%s %s Archive", tc.name, tc.set)
				if want := "C2 NO " + tc.name + " failed"; tagged != want {
					t.Fatalf("other failure: %q, want %q", tagged, want)
				}
			})
		}
		t.Run(fmt.Sprintf("%v/APPEND", uidPlus), func(t *testing.T) {
			m, b := box()
			c := selectionClient(t, b)
			lines, st := appendCommand(t, c, "Missing", "message")
			requireExactly(t, lines)
			if st != "A1 NO [TRYCREATE] APPEND failed" || m.appended != 0 {
				t.Fatalf("missing destination: %q appended=%d", st, m.appended)
			}
			_, st = appendCommand(t, c, "Archive", "message")
			if m.appended != 1 || !strings.HasPrefix(st, "A1 OK") {
				t.Fatalf("existing destination: %q appended=%d", st, m.appended)
			}
		})
	}
}

// TestTryCreatePlainAppendFailure keeps an unrelated store error a plain NO.
// checklist: IMAP-APPEND-18
func TestTryCreatePlainAppendFailure(t *testing.T) {
	c := selectionClient(t, &appendMailbox{fail: true})
	_, st := appendCommand(t, c, "Sent", "message")
	if st != "A1 NO APPEND failed" {
		t.Fatalf("plain failure: %q", st)
	}
}
