package rfctest

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// moveMailbox records Move dispatch through the base Mailbox fallback. The
// backend's own atomicity is not modelled: these fixtures prove the session.
type moveMailbox struct {
	*copyMailbox
}

func newMoveMailbox() *moveMailbox { return &moveMailbox{copyMailbox: newCopyMailbox()} }
func (m *moveMailbox) Move(uid uint32, dest string) error {
	m.calls = append(m.calls, copyCall{uid, dest})
	if uid == m.fail {
		return errors.New("move refused")
	}
	return nil
}

// uidMoveMailbox adds UIDPLUS and the atomic [imap.Mover].
type uidMoveMailbox struct {
	*moveMailbox
	validity    uint32
	validityErr error
	assigned    []uint32 // destination UIDs to report in call order; nil counts from 1202
}

func (m *uidMoveMailbox) UIDValidity(string) (uint32, error) { return m.validity, m.validityErr }
func (*uidMoveMailbox) AppendUID(string, imap.FlagUpdate, []byte) (uint32, error) {
	return 0, errors.New("unused")
}
func (*uidMoveMailbox) CopyUID(uint32, string) (uint32, error) { return 0, errors.New("unused") }
func (m *uidMoveMailbox) MoveUID(uid uint32, dest string) (uint32, error) {
	if err := m.Move(uid, dest); err != nil {
		return 0, err
	}
	if m.assigned != nil {
		return m.assigned[len(m.calls)-1], nil
	}
	return 1201 + uint32(len(m.calls)), nil
}

// requireRemaining fetches the session view and checks which UIDs are left.
func requireRemaining(t *testing.T, c *client, uids ...uint32) {
	t.Helper()
	lines, tagged := c.command("F1", "FETCH 1:* (UID)")
	requireOK(t, tagged, "F1")
	if len(lines) != len(uids) {
		t.Fatalf("remaining %q, want UIDs %v", lines, uids)
	}
	for i, uid := range uids {
		if !strings.HasPrefix(lines[i], fmt.Sprintf("* %d FETCH (UID %d ", i+1, uid)) {
			t.Fatalf("remaining %q, want UIDs %v", lines, uids)
		}
	}
}

// TestMoveExample moves 2:4 through the base Mailbox.Move fallback.
// checklist: IMAP-MOVE-01
// checklist: IMAP2-CMD-161
func TestMoveExample(t *testing.T) {
	m := newMoveMailbox()
	c := expungeClient(t, m)
	lines, tagged := c.command("A1", "MOVE 2:4 foo")
	requireOK(t, tagged, "A1")
	requireExactly(t, lines, "* 4 EXPUNGE", "* 3 EXPUNGE", "* 2 EXPUNGE")
	requireCopies(t, m.copyMailbox, "foo", 304, 319, 320)
	requireRemaining(t, c, 300)
}

// TestMoveUIDExample follows the shape of RFC 6851's UID MOVE example:
// COPYUID in an untagged OK, then the EXPUNGEs, then the tagged OK.
// checklist: IMAP-MOVE-02 IMAP-MOVE-03
// checklist: IMAP2-CMD-161
func TestMoveUIDExample(t *testing.T) {
	m := &uidMoveMailbox{moveMailbox: newMoveMailbox(), validity: 432432}
	c := expungeClient(t, m)
	lines, tagged := c.command("a", "UID MOVE 304:319 foo")
	requireOK(t, tagged, "a")
	requireExactly(t, lines, "* OK [COPYUID 432432 304,319 1202,1203] Moved", "* 3 EXPUNGE", "* 2 EXPUNGE")
	requireCopies(t, m.copyMailbox, "foo", 304, 319)
	requireRemaining(t, c, 300, 320)

	m.calls = nil
	lines, tagged = c.command("b", "UID MOVE 305:310 foo")
	requireOK(t, tagged, "b")
	requireExactly(t, lines)
	requireCopies(t, m.copyMailbox, "foo")
}

// checklist: IMAP-MOVE-03 IMAP-MOVE-04
func TestMoveSets(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []uint32
		left    []uint32
	}{
		{"MOVE 4:2 Archive", []uint32{304, 319, 320}, []uint32{300}},
		{"MOVE 2,2:3,3 Archive", []uint32{304, 319}, []uint32{300, 320}},
		{"MOVE 3,3 Archive", []uint32{319}, []uint32{300, 304, 320}},
		{"UID MOVE 320:304 Archive", []uint32{304, 319, 320}, []uint32{300}},
		{"UID MOVE 304,304:319,319 Archive", []uint32{304, 319}, []uint32{300, 320}},
		{"UID MOVE 999:* Archive", []uint32{320}, []uint32{300, 304, 319}},
		{"UID MOVE 1,304,305,320 Archive", []uint32{304, 320}, []uint32{300, 319}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			m := newMoveMailbox()
			c := expungeClient(t, m)
			lines, tagged := c.command("M1", "%s", tc.command)
			requireOK(t, tagged, "M1")
			if len(lines) != len(tc.want) {
				t.Fatalf("EXPUNGEs %q, want one per moved message %v", lines, tc.want)
			}
			requireCopies(t, m.copyMailbox, "Archive", tc.want...)
			requireRemaining(t, c, tc.left...)
		})
	}
}

// checklist: IMAP-MOVE-02 IMAP-MOVE-04
func TestMoveUIDMapping(t *testing.T) {
	for _, tc := range []struct{ set, sources, destinations string }{
		{"304,304:319,319:320", "304,319,320", "1202,1203,1204"},
		{"320,304", "320,304", "1202,1203"},
	} {
		t.Run(tc.set, func(t *testing.T) {
			m := &uidMoveMailbox{moveMailbox: newMoveMailbox(), validity: 38505}
			c := expungeClient(t, m)
			lines, tagged := c.command("M1", "UID MOVE %s Archive", tc.set)
			requireOK(t, tagged, "M1")
			if len(lines) == 0 || lines[0] != "* OK [COPYUID 38505 "+tc.sources+" "+tc.destinations+"] Moved" {
				t.Fatalf("COPYUID %q, want sources %s destinations %s first", lines, tc.sources, tc.destinations)
			}
			if len(lines) != 1+len(strings.Split(tc.sources, ",")) {
				t.Fatalf("EXPUNGEs %q, want one per moved message", lines[1:])
			}
		})
	}
}

// checklist: IMAP-MOVE-05
func TestMoveBackendFailure(t *testing.T) {
	for _, uidPlus := range []bool{false, true} {
		for _, command := range []string{"MOVE 2:4 Archive", "UID MOVE 304:320 Archive"} {
			for _, fail := range []uint32{304, 319} {
				t.Run(fmt.Sprintf("%v/%s/%d", uidPlus, command, fail), func(t *testing.T) {
					m := newMoveMailbox()
					m.fail = fail
					var box imap.Mailbox = m
					if uidPlus {
						box = &uidMoveMailbox{moveMailbox: m, validity: 38505}
					}
					c := expungeClient(t, box)
					lines, tagged := c.command("M1", "%s", command)
					if !strings.HasPrefix(tagged, "M1 NO ") {
						t.Fatalf("refused move: %q, want NO", tagged)
					}
					// Only messages the backend moved leave the source view.
					if fail == 304 {
						requireExactly(t, lines)
						requireCopies(t, m.copyMailbox, "Archive", 304)
						requireRemaining(t, c, 300, 304, 319, 320)
					} else {
						want := []string{"* 2 EXPUNGE"}
						if uidPlus {
							want = append([]string{"* OK [COPYUID 38505 304 1202] Moved"}, want...)
						}
						requireExactly(t, lines, want...)
						requireCopies(t, m.copyMailbox, "Archive", 304, 319)
						requireRemaining(t, c, 300, 319, 320)
					}
					// Error is command-local. Retry the refused message successfully.
					m.fail = 0
					m.calls = nil
					_, tagged = c.command("M2", "UID MOVE %d Archive", fail)
					requireOK(t, tagged, "M2")
					requireCopies(t, m.copyMailbox, "Archive", fail)
				})
			}
		}
	}
}

// checklist: IMAP-MOVE-06
func TestMoveReadOnly(t *testing.T) {
	for _, command := range []string{"MOVE 2 Archive", "UID MOVE 304 Archive"} {
		t.Run(command, func(t *testing.T) {
			m := newMoveMailbox()
			c := selectionClient(t, m)
			_, tagged := c.command("S1", "EXAMINE INBOX")
			requireOK(t, tagged, "S1")
			lines, tagged := c.command("M1", "%s", command)
			requireExactly(t, lines)
			if !strings.HasPrefix(tagged, "M1 NO ") {
				t.Fatalf("MOVE on EXAMINE: %q, want NO", tagged)
			}
			requireCopies(t, m.copyMailbox, "Archive")
			requireRemaining(t, c, 300, 304, 319, 320)
		})
	}
}

// checklist: IMAP-MOVE-07
// checklist: IMAP2-CORE-22
func TestMoveRequiresSelection(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		for _, command := range []string{"MOVE 1 Archive", "UID MOVE 300 Archive"} {
			t.Run(fmt.Sprintf("%v/%s", authenticated, command), func(t *testing.T) {
				m := newMoveMailbox()
				c := dialMailbox(t, m)
				c.line()
				if authenticated {
					c.login()
				}
				lines, tagged := c.command("M1", "%s", command)
				requireExactly(t, lines)
				if strings.HasPrefix(tagged, "M1 OK") {
					t.Fatalf("MOVE without selection: %q", tagged)
				}
				requireCopies(t, m.copyMailbox, "Archive")
			})
		}
	}
}

// checklist: IMAP-MOVE-08
func TestMoveNames(t *testing.T) {
	for _, prefix := range []string{"MOVE 2", "UID MOVE 304"} {
		for _, dest := range []string{`"Project Archive"`, "iNbOx"} {
			t.Run(prefix+dest, func(t *testing.T) {
				m := newMoveMailbox()
				c := expungeClient(t, m)
				lines, tagged := c.command("M1", "%s %s", prefix, dest)
				requireOK(t, tagged, "M1")
				requireExactly(t, lines, "* 2 EXPUNGE")
				want := "INBOX"
				if strings.HasPrefix(dest, `"`) {
					want = "Project Archive"
				}
				requireCopies(t, m.copyMailbox, want, 304)
			})
		}
	}
}

// checklist: IMAP-MOVE-09
func TestMoveUIDValidityFallback(t *testing.T) {
	for _, tc := range []struct {
		name     string
		validity uint32
		err      error
	}{
		{"zero", 0, nil}, {"error", 38505, errors.New("metadata unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &uidMoveMailbox{moveMailbox: newMoveMailbox(), validity: tc.validity, validityErr: tc.err}
			c := expungeClient(t, m)
			lines, tagged := c.command("M1", "UID MOVE 304 Archive")
			requireOK(t, tagged, "M1")
			requireExactly(t, lines, "* 2 EXPUNGE")
			requireCopies(t, m.copyMailbox, "Archive", 304)
		})
	}
	// A base backend without Mover reports no COPYUID even with UIDPLUS copy metadata.
	m := newMoveMailbox()
	c := expungeClient(t, &uidCopyOnlyMoveMailbox{moveMailbox: m})
	lines, tagged := c.command("M2", "UID MOVE 304 Archive")
	requireOK(t, tagged, "M2")
	requireExactly(t, lines, "* 2 EXPUNGE")
}

// uidCopyOnlyMoveMailbox implements UIDPLUS but not [imap.Mover].
type uidCopyOnlyMoveMailbox struct{ *moveMailbox }

func (*uidCopyOnlyMoveMailbox) UIDValidity(string) (uint32, error) { return 38505, nil }
func (*uidCopyOnlyMoveMailbox) AppendUID(string, imap.FlagUpdate, []byte) (uint32, error) {
	return 0, errors.New("unused")
}
func (*uidCopyOnlyMoveMailbox) CopyUID(uint32, string) (uint32, error) {
	return 0, errors.New("unused")
}

// TestMoveInvalidArguments proves malformed sets and destinations get BAD
// before any move or EXPUNGE, and leave the mailbox intact.
// checklist: IMAP-MOVE-12
func TestMoveInvalidArguments(t *testing.T) {
	for _, command := range []string{"MOVE", "UID MOVE"} {
		for _, args := range invalidSetMailboxArgs {
			if command == "UID MOVE" {
				args = strings.Replace(args, "2", "304", 1)
			}
			t.Run(command+" "+args, func(t *testing.T) {
				m := newMoveMailbox()
				c := expungeClient(t, m)
				lines, tagged := c.command("M1", "%s %s", command, args)
				requireExactly(t, lines)
				if !strings.HasPrefix(tagged, "M1 BAD ") {
					t.Fatalf("%s %s: %q, want BAD", command, args, tagged)
				}
				requireCopies(t, m.copyMailbox, "")
				requireRemaining(t, c, 300, 304, 319, 320)
			})
		}
	}
	m := newMoveMailbox()
	c := expungeClient(t, m)
	lines, tagged := c.command("M1", `UID MOVE 304 "Arch\\ive"`)
	requireOK(t, tagged, "M1")
	requireExactly(t, lines, "* 2 EXPUNGE")
	requireCopies(t, m.copyMailbox, `Arch\ive`, 304)
}

// TestMoveUIDInvalidBackendUIDs omits COPYUID when MoveUID reports a zero UID
// (allowed when the backend cannot report one) or repeats a UID. The move
// still succeeds and the EXPUNGEs are still sent.
// checklist: IMAP-MOVE-14
func TestMoveUIDInvalidBackendUIDs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		assigned []uint32
		copyUID  bool
	}{
		{"valid", []uint32{1202, 1203, 1204}, true},
		{"unreported", []uint32{0, 0, 0}, false},
		{"zero middle", []uint32{1202, 0, 1204}, false},
		{"repeated", []uint32{1202, 1203, 1202}, false},
	} {
		for _, command := range []string{"MOVE 2:4 Archive", "UID MOVE 304:320 Archive"} {
			t.Run(tc.name+"/"+command, func(t *testing.T) {
				m := &uidMoveMailbox{moveMailbox: newMoveMailbox(), validity: 38505, assigned: tc.assigned}
				c := expungeClient(t, m)
				lines, tagged := c.command("M1", "%s", command)
				requireOK(t, tagged, "M1")
				want := []string{"* 4 EXPUNGE", "* 3 EXPUNGE", "* 2 EXPUNGE"}
				if tc.copyUID {
					want = append([]string{"* OK [COPYUID 38505 304,319,320 1202,1203,1204] Moved"}, want...)
				}
				requireExactly(t, lines, want...)
				requireCopies(t, m.copyMailbox, "Archive", 304, 319, 320)
				requireRemaining(t, c, 300)
			})
		}
	}

	// A partial move quotes only the UIDs that moved, and validates only those.
	m := &uidMoveMailbox{moveMailbox: newMoveMailbox(), validity: 38505, assigned: []uint32{0, 1203}}
	m.fail = 319
	c := expungeClient(t, m)
	lines, tagged := c.command("M1", "MOVE 2:4 Archive")
	if !strings.HasPrefix(tagged, "M1 NO ") {
		t.Fatalf("refused move: %q, want NO", tagged)
	}
	requireExactly(t, lines, "* 2 EXPUNGE")
	requireRemaining(t, c, 300, 319, 320)
}
