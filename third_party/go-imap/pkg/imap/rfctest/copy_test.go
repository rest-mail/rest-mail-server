package rfctest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

type copyCall struct {
	uid  uint32
	dest string
}

// copyMailbox records dispatch, not storage semantics: preservation of raw
// bytes, dates and destination flags is the backend's responsibility.
type copyMailbox struct {
	mailbox
	messages []imap.Message
	calls    []copyCall
	fail     uint32
}

func newCopyMailbox() *copyMailbox {
	return &copyMailbox{messages: []imap.Message{{UID: 300}, {UID: 304, Seen: true}, {UID: 319, Flagged: true}, {UID: 320}}}
}
func (m *copyMailbox) Messages(string) ([]imap.Message, error) { return slices.Clone(m.messages), nil }
func (m *copyMailbox) Copy(uid uint32, dest string) error {
	m.calls = append(m.calls, copyCall{uid, dest})
	if uid == m.fail {
		return errors.New("copy refused")
	}
	return nil
}

type uidCopyMailbox struct {
	*copyMailbox
	validity    uint32
	validityErr error
	assigned    []uint32 // destination UIDs to report in call order; nil counts from 3956
}

func (m *uidCopyMailbox) UIDValidity(string) (uint32, error) { return m.validity, m.validityErr }
func (*uidCopyMailbox) AppendUID(string, imap.FlagUpdate, []byte) (uint32, error) {
	return 0, errors.New("unused")
}
func (m *uidCopyMailbox) CopyUID(uid uint32, dest string) (uint32, error) {
	if err := m.Copy(uid, dest); err != nil {
		return 0, err
	}
	if m.assigned != nil {
		return m.assigned[len(m.calls)-1], nil
	}
	return 3955 + uint32(len(m.calls)), nil
}
func requireCopies(t *testing.T, m *copyMailbox, dest string, uids ...uint32) {
	t.Helper()
	want := make([]copyCall, len(uids))
	for i, uid := range uids {
		want[i] = copyCall{uid, dest}
	}
	if !slices.Equal(m.calls, want) {
		t.Fatalf("Copy calls %v, want %v", m.calls, want)
	}
}

// TestCopyExample replays RFC 3501 §6.4.7's A003 COPY command.
// checklist: IMAP-COPY-01 IMAP-COPY-02
func TestCopyExample(t *testing.T) {
	m := newCopyMailbox()
	c := expungeClient(t, m)
	lines, tagged := c.command("A003", "COPY 2:4 MEETING")
	requireOK(t, tagged, "A003")
	requireExactly(t, lines)
	if strings.Contains(tagged, "COPYUID") {
		t.Fatalf("base backend leaked COPYUID: %q", tagged)
	}
	requireCopies(t, m, "MEETING", 304, 319, 320)
	lines, tagged = c.command("F1", "FETCH 1:* (UID FLAGS)")
	requireOK(t, tagged, "F1")
	requireExactly(t, lines, `* 1 FETCH (UID 300 FLAGS ())`, `* 2 FETCH (UID 304 FLAGS (\Seen))`, `* 3 FETCH (UID 319 FLAGS (\Flagged))`, `* 4 FETCH (UID 320 FLAGS ())`)
}

// TestCopyUIDExample uses RFC 4315 §3's A004/A005 source/destination UIDs.
// Comma lists and ranges denote the same UID set.
// checklist: IMAP-COPY-03 IMAP-COPY-04
func TestCopyUIDExample(t *testing.T) {
	m := &uidCopyMailbox{copyMailbox: newCopyMailbox(), validity: 38505}
	c := expungeClient(t, m)
	lines, tagged := c.command("A004", "COPY 2:4 meeting")
	requireExactly(t, lines)
	if tagged != "A004 OK [COPYUID 38505 304,319,320 3956,3957,3958] COPY completed" {
		t.Fatalf("COPYUID: %q", tagged)
	}
	requireCopies(t, m.copyMailbox, "meeting", 304, 319, 320)
	m.calls = nil
	lines, tagged = c.command("A005", "UID COPY 305:310 meeting")
	requireExactly(t, lines)
	requireOK(t, tagged, "A005")
	if strings.Contains(tagged, "COPYUID") {
		t.Fatalf("empty copy has COPYUID: %q", tagged)
	}
	requireCopies(t, m.copyMailbox, "meeting")
}

// checklist: IMAP-COPY-04 IMAP-COPY-05 IMAP-COPY-06
func TestCopySets(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []uint32
	}{
		{"COPY 4:2 Archive", []uint32{304, 319, 320}},
		{"COPY 2,2:3,3:4 Archive", []uint32{304, 319, 320}},
		{"UID COPY 320:304 Archive", []uint32{304, 319, 320}},
		{"UID COPY 304,304:319,319:320 Archive", []uint32{304, 319, 320}},
		{"UID COPY 999:* Archive", []uint32{320}},
		{"UID COPY 1,304,305,320 Archive", []uint32{304, 320}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			m := newCopyMailbox()
			c := expungeClient(t, m)
			lines, tagged := c.command("C1", "%s", tc.command)
			requireOK(t, tagged, "C1")
			requireExactly(t, lines)
			requireCopies(t, m, "Archive", tc.want...)
		})
	}
}

// checklist: IMAP-COPY-07
func TestCopyBackendFailure(t *testing.T) {
	for _, uidPlus := range []bool{false, true} {
		for _, command := range []string{"COPY 2:4 Archive", "UID COPY 304:320 Archive"} {
			for _, fail := range []uint32{304, 319} {
				t.Run(fmt.Sprintf("%v/%s/%d", uidPlus, command, fail), func(t *testing.T) {
					m := newCopyMailbox()
					m.fail = fail
					var box imap.Mailbox = m
					if uidPlus {
						box = &uidCopyMailbox{copyMailbox: m, validity: 38505}
					}
					c := expungeClient(t, box)
					lines, tagged := c.command("C1", "%s", command)
					requireExactly(t, lines)
					if !strings.HasPrefix(tagged, "C1 NO ") || strings.Contains(tagged, "COPYUID") {
						t.Fatalf("refused copy: %q, want NO without COPYUID", tagged)
					}
					if fail == 304 {
						requireCopies(t, m, "Archive", 304)
					} else {
						requireCopies(t, m, "Archive", 304, 319)
					}
					// Error is command-local. Retry a single refused message successfully.
					m.fail = 0
					m.calls = nil
					_, tagged = c.command("C2", "UID COPY %d Archive", fail)
					requireOK(t, tagged, "C2")
					requireCopies(t, m, "Archive", fail)
				})
			}
		}
	}
}

// checklist: IMAP-COPY-08
func TestCopyReadOnlyAndNames(t *testing.T) {
	for _, prefix := range []string{"COPY 2", "UID COPY 304"} {
		for _, dest := range []string{`"Project Archive"`, "iNbOx"} {
			t.Run(prefix+dest, func(t *testing.T) {
				m := newCopyMailbox()
				c := selectionClient(t, m)
				_, tagged := c.command("S1", "EXAMINE INBOX")
				requireOK(t, tagged, "S1")
				lines, tagged := c.command("C1", "%s %s", prefix, dest)
				requireOK(t, tagged, "C1")
				requireExactly(t, lines)
				want := "INBOX"
				if strings.HasPrefix(dest, `"`) {
					want = "Project Archive"
				}
				requireCopies(t, m, want, 304)
			})
		}
	}
}

// checklist: IMAP-COPY-09
// checklist: IMAP2-CORE-22
func TestCopyRequiresSelection(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		for _, command := range []string{"COPY 1 Archive", "UID COPY 300 Archive"} {
			t.Run(fmt.Sprintf("%v/%s", authenticated, command), func(t *testing.T) {
				m := newCopyMailbox()
				c := dialMailbox(t, m)
				c.line()
				if authenticated {
					c.login()
				}
				lines, tagged := c.command("C1", "%s", command)
				requireExactly(t, lines)
				if strings.HasPrefix(tagged, "C1 OK") {
					t.Fatalf("COPY without selection: %q", tagged)
				}
				requireCopies(t, m, "Archive")
			})
		}
	}
}

// checklist: IMAP-COPY-10
func TestCopyUIDValidityFallback(t *testing.T) {
	for _, tc := range []struct {
		name     string
		validity uint32
		err      error
	}{
		{"zero", 0, nil}, {"error", 38505, errors.New("metadata unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &uidCopyMailbox{copyMailbox: newCopyMailbox(), validity: tc.validity, validityErr: tc.err}
			c := expungeClient(t, m)
			lines, tagged := c.command("C1", "UID COPY 304 Archive")
			requireExactly(t, lines)
			requireOK(t, tagged, "C1")
			if strings.Contains(tagged, "COPYUID") {
				t.Fatalf("unavailable UIDVALIDITY: %q", tagged)
			}
			requireCopies(t, m.copyMailbox, "Archive", 304)
		})
	}
}

// checklist: IMAP-COPY-03 IMAP-COPY-06
func TestCopyUIDMapping(t *testing.T) {
	for _, tc := range []struct{ set, sources string }{
		{"304,304:319,319:320", "304,319,320"},
		{"320,304", "320,304"},
	} {
		t.Run(tc.set, func(t *testing.T) {
			m := &uidCopyMailbox{copyMailbox: newCopyMailbox(), validity: 38505}
			c := expungeClient(t, m)
			lines, tagged := c.command("C1", "UID COPY %s Archive", tc.set)
			requireExactly(t, lines)
			destinations := "3956,3957"
			if len(strings.Split(tc.sources, ",")) == 3 {
				destinations += ",3958"
			}
			want := "C1 OK [COPYUID 38505 " + tc.sources + " " + destinations + "] UID COPY completed"
			if tagged != want {
				t.Fatalf("COPYUID %q, want %q", tagged, want)
			}
		})
	}
}

// invalidSetMailboxArgs are COPY/MOVE argument strings the RFC 3501 §9 grammar
// refuses: malformed sequence-sets, malformed astrings and trailing text.
var invalidSetMailboxArgs = []string{
	"0 Archive", "0:2 Archive", "02 Archive", "+2 Archive", "a Archive",
	"4294967296 Archive", "2, Archive", ",2 Archive", "2,,3 Archive", "2: Archive",
	":2 Archive", "2:3:4 Archive", "** Archive", "2", "2 ", "2  Archive",
	`2 "Archive`, `2 "Arch\ive"`, `2 "Archive"x`, "2 Arch%", "2 Arch*", `2 Arch"ive`,
	`2 Arch\ive`, "2 Arch)ive", "2 Arch(ive", "2 Archive extra", "2 \"Arch\x00ive\"",
	"2 Arch\x7five", "2 Arch\xc3\xa9",
}

// TestCopyInvalidArguments proves malformed sets and destinations get BAD
// before any copy, while astring forms the grammar allows still work.
// checklist: IMAP-COPY-14
func TestCopyInvalidArguments(t *testing.T) {
	for _, command := range []string{"COPY", "UID COPY"} {
		for _, args := range invalidSetMailboxArgs {
			if command == "UID COPY" {
				args = strings.Replace(args, "2", "304", 1)
			}
			t.Run(command+" "+args, func(t *testing.T) {
				m := newCopyMailbox()
				c := expungeClient(t, m)
				lines, tagged := c.command("C1", "%s %s", command, args)
				requireExactly(t, lines)
				if !strings.HasPrefix(tagged, "C1 BAD ") {
					t.Fatalf("%s %s: %q, want BAD", command, args, tagged)
				}
				requireCopies(t, m, "")
			})
		}
	}
	for _, tc := range []struct{ dest, want string }{
		{`"Arch\"ive"`, `Arch"ive`},
		{`"Arch\\ive"`, `Arch\ive`},
		{`""`, ""},
		{"Arch]ive", "Arch]ive"},
		{"{7+}\r\nArchive", "Archive"},
		{"{8+}\r\nArch ive", "Arch ive"},
	} {
		t.Run("valid "+tc.dest, func(t *testing.T) {
			m := newCopyMailbox()
			c := expungeClient(t, m)
			_, tagged := c.command("C1", "COPY 4294967295,2 %s", tc.dest)
			requireOK(t, tagged, "C1")
			requireCopies(t, m, tc.want, 304)
		})
	}
}

// TestCopyUIDInvalidBackendUIDs gives COPYUID only valid nonzero destination
// UIDs with no repeats: a backend result that breaks either rule leaves the
// copy successful but omits the response code instead of quoting it.
// checklist: IMAP-COPY-16
func TestCopyUIDInvalidBackendUIDs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		assigned []uint32
		want     string
	}{
		{"valid", []uint32{3956, 3957, 3958}, "C1 OK [COPYUID 38505 304,319,320 3956,3957,3958] "},
		{"zero first", []uint32{0, 3957, 3958}, "C1 OK "},
		{"zero last", []uint32{3956, 3957, 0}, "C1 OK "},
		{"repeated", []uint32{3956, 3957, 3956}, "C1 OK "},
		{"all repeated", []uint32{7, 7, 7}, "C1 OK "},
	} {
		for _, cmd := range []struct{ name, args string }{{"COPY", "2:4"}, {"UID COPY", "304:320"}} {
			t.Run(tc.name+"/"+cmd.name, func(t *testing.T) {
				m := &uidCopyMailbox{copyMailbox: newCopyMailbox(), validity: 38505, assigned: tc.assigned}
				c := expungeClient(t, m)
				lines, tagged := c.command("C1", "%s %s Archive", cmd.name, cmd.args)
				requireExactly(t, lines)
				if want := tc.want + cmd.name + " completed"; tagged != want {
					t.Fatalf("tagged %q, want %q", tagged, want)
				}
				requireCopies(t, m.copyMailbox, "Archive", 304, 319, 320)
				// The omitted code is command-local: a later valid copy reports it.
				m.calls, m.assigned = nil, nil
				_, tagged = c.command("C2", "UID COPY 300 Archive")
				if tagged != "C2 OK [COPYUID 38505 300 3956] UID COPY completed" {
					t.Fatalf("follow-up copy %q", tagged)
				}
			})
		}
	}
}
