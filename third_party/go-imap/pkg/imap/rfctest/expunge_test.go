package rfctest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// expungeMailbox records permanent removals and can refuse selected UIDs.
// Deletion marks belong to the session: Message has no persistent Deleted field.
type expungeMailbox struct {
	mailbox
	messages []imap.Message
	attempts []uint32
	fail     map[uint32]bool
}

func newExpungeMailbox(count int) *expungeMailbox {
	m := &expungeMailbox{fail: map[uint32]bool{}}
	for i := range count {
		m.messages = append(m.messages, imap.Message{UID: uint32(100 + i*10)})
	}
	return m
}
func (m *expungeMailbox) Messages(string) ([]imap.Message, error) {
	return slices.Clone(m.messages), nil
}
func (m *expungeMailbox) Delete(uid uint32) error {
	m.attempts = append(m.attempts, uid)
	if m.fail[uid] {
		return errors.New("permission denied")
	}
	for i, msg := range m.messages {
		if msg.UID == uid {
			m.messages = slices.Delete(m.messages, i, i+1)
			return nil
		}
	}
	return errors.New("unknown UID")
}

type uidExpungeMailbox struct{ *expungeMailbox }

func (*uidExpungeMailbox) UIDValidity(string) (uint32, error) { return 42, nil }
func (*uidExpungeMailbox) AppendUID(string, imap.FlagUpdate, []byte) (uint32, error) {
	return 0, errors.New("unused")
}
func (*uidExpungeMailbox) CopyUID(uint32, string) (uint32, error) { return 0, errors.New("unused") }

func expungeClient(t *testing.T, m imap.Mailbox) *client {
	t.Helper()
	c := selectionClient(t, m)
	_, tagged := c.command("S1", "SELECT INBOX")
	requireOK(t, tagged, "S1")
	return c
}
func markDeleted(t *testing.T, c *client, set string) {
	t.Helper()
	lines, tagged := c.command("D1", `STORE %s +FLAGS.SILENT (\Deleted)`, set)
	requireOK(t, tagged, "D1")
	requireExactly(t, lines)
}
func requireUIDs(t *testing.T, m *expungeMailbox, want ...uint32) {
	t.Helper()
	var got []uint32
	for _, msg := range m.messages {
		got = append(got, msg.UID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("backend UIDs %v, want %v", got, want)
	}
}

// TestExpungeExample uses RFC 3501 §6.4.3's deleted positions 3,4,7,11.
// §7.4.1 permits descending responses; unlike the ascending RFC transcript,
// this engine reports 11,7,4,3. Each number names the message at removal time.
//
// checklist: IMAP-EXPUNGE-01 IMAP-EXPUNGE-02
func TestExpungeExample(t *testing.T) {
	m := newExpungeMailbox(11)
	c := expungeClient(t, m)
	markDeleted(t, c, "3,4,7,11")
	lines, tagged := c.command("A202", "EXPUNGE")
	requireOK(t, tagged, "A202")
	requireExactly(t, lines, "* 11 EXPUNGE", "* 7 EXPUNGE", "* 4 EXPUNGE", "* 3 EXPUNGE")
	if !slices.Equal(m.attempts, []uint32{200, 160, 130, 120}) {
		t.Fatalf("Delete calls %v", m.attempts)
	}
	requireUIDs(t, m, 100, 110, 140, 150, 170, 180, 190)
	lines, tagged = c.command("F1", "FETCH 1:* (UID FLAGS)")
	requireOK(t, tagged, "F1")
	requireExactly(t, lines, "* 1 FETCH (UID 100 FLAGS ())", "* 2 FETCH (UID 110 FLAGS ())", "* 3 FETCH (UID 140 FLAGS ())", "* 4 FETCH (UID 150 FLAGS ())", "* 5 FETCH (UID 170 FLAGS ())", "* 6 FETCH (UID 180 FLAGS ())", "* 7 FETCH (UID 190 FLAGS ())")
	// A second EXPUNGE proves deletion marks did not migrate to survivors.
	lines, tagged = c.command("E2", "EXPUNGE")
	requireOK(t, tagged, "E2")
	requireExactly(t, lines)
	if len(m.attempts) != 4 {
		t.Fatalf("unexpected second deletion: %v", m.attempts)
	}
}

// TestExpungeNoMatches checks empty mailboxes and nondeleted messages.
//
// checklist: IMAP-EXPUNGE-01
func TestExpungeNoMatches(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			m := newExpungeMailbox(count)
			c := expungeClient(t, m)
			lines, tagged := c.command("E1", "EXPUNGE")
			requireOK(t, tagged, "E1")
			requireExactly(t, lines)
			if len(m.attempts) != 0 || len(m.messages) != count {
				t.Fatal("expunged an unmarked message")
			}
		})
	}
}

// TestExpungeBackendFailure checks total and partial refusal, unchanged flags
// for failed messages, successful removals and retry without another STORE.
//
// checklist: IMAP-EXPUNGE-03 IMAP-UIDEXPUNGE-02 IMAP-RESP-10
func TestExpungeBackendFailure(t *testing.T) {
	for _, uid := range []bool{false, true} {
		for _, allFail := range []bool{false, true} {
			t.Run(fmt.Sprintf("uid=%v/allFail=%v", uid, allFail), func(t *testing.T) {
				m := newExpungeMailbox(3)
				m.fail[110] = true
				if allFail {
					m.fail[100] = true
					m.fail[120] = true
				}
				var box imap.Mailbox = m
				command := "EXPUNGE"
				if uid {
					box = &uidExpungeMailbox{m}
					command = "UID EXPUNGE 100:120"
				}
				c := expungeClient(t, box)
				markDeleted(t, c, "1:3")
				lines, tagged := c.command("E1", "%s", command)
				if !strings.HasPrefix(tagged, "E1 NO ") {
					t.Fatalf("refused deletion: %q %q, want NO", lines, tagged)
				}
				if allFail {
					requireExactly(t, lines)
					requireUIDs(t, m, 100, 110, 120)
				} else {
					requireExactly(t, lines, "* 3 EXPUNGE", "* 1 EXPUNGE")
					requireUIDs(t, m, 110)
				}
				lines, tagged = c.command("F1", "FETCH 1:* (FLAGS)")
				requireOK(t, tagged, "F1")
				if allFail {
					requireExactly(t, lines, `* 1 FETCH (UID 100 FLAGS (\Deleted))`, `* 2 FETCH (UID 110 FLAGS (\Deleted))`, `* 3 FETCH (UID 120 FLAGS (\Deleted))`)
				} else {
					requireExactly(t, lines, `* 1 FETCH (UID 110 FLAGS (\Deleted))`)
				}
				clear(m.fail)
				lines, tagged = c.command("E2", "%s", command)
				requireOK(t, tagged, "E2")
				if allFail {
					requireExactly(t, lines, "* 3 EXPUNGE", "* 2 EXPUNGE", "* 1 EXPUNGE")
				} else {
					requireExactly(t, lines, "* 1 EXPUNGE")
				}
				requireUIDs(t, m)
				lines, tagged = c.command("Q1", "SEARCH ALL")
				requireOK(t, tagged, "Q1")
				requireExactly(t, lines, "* SEARCH")
			})
		}
	}
}

// TestUIDExpungeSubset uses §2.1's UID range with sparse surrounding UIDs.
// Only marked messages within that range are removed; other marks survive.
//
// checklist: IMAP-UIDEXPUNGE-01 IMAP-EXPUNGE-02
// checklist: IMAP2-CMD-166
func TestUIDExpungeSubset(t *testing.T) {
	m := newExpungeMailbox(0)
	for _, uid := range []uint32{10, 20, 3000, 3001, 3002, 4000} {
		m.messages = append(m.messages, imap.Message{UID: uid})
	}
	c := expungeClient(t, &uidExpungeMailbox{m})
	markDeleted(t, c, "1,3:5")
	lines, tagged := c.command("A003", "UID EXPUNGE 3000:3002")
	requireOK(t, tagged, "A003")
	requireExactly(t, lines, "* 5 EXPUNGE", "* 4 EXPUNGE", "* 3 EXPUNGE")
	requireUIDs(t, m, 10, 20, 4000)
	// An in-range but unmarked UID, and absent UIDs, are unaffected.
	lines, tagged = c.command("E2", "UID EXPUNGE 20,9999")
	requireOK(t, tagged, "E2")
	requireExactly(t, lines)
	requireUIDs(t, m, 10, 20, 4000)
	lines, tagged = c.command("E2b", "UID EXPUNGE 9999")
	requireOK(t, tagged, "E2b")
	requireExactly(t, lines)
	requireUIDs(t, m, 10, 20, 4000)
	lines, tagged = c.command("F1", "FETCH 1:* (FLAGS)")
	requireOK(t, tagged, "F1")
	requireExactly(t, lines, `* 1 FETCH (UID 10 FLAGS (\Deleted))`, `* 2 FETCH (UID 20 FLAGS ())`, `* 3 FETCH (UID 4000 FLAGS ())`)
	lines, tagged = c.command("E3", "EXPUNGE")
	requireOK(t, tagged, "E3")
	requireExactly(t, lines, "* 1 EXPUNGE")
	requireUIDs(t, m, 20, 4000)
}

// TestExpungeRequiresSelection checks both unauthenticated and unselected
// states, and verifies CLOSE has the same selected-state requirement.
//
// checklist: IMAP-EXPUNGE-04 IMAP-CLOSE-01
// checklist: IMAP2-CORE-22
func TestExpungeRequiresSelection(t *testing.T) {
	for _, login := range []bool{false, true} {
		t.Run(fmt.Sprint(login), func(t *testing.T) {
			c := dial(t)
			c.line()
			if login {
				c.login()
			}
			for i, cmd := range []string{"EXPUNGE", "CLOSE"} {
				tag := fmt.Sprintf("X%d", i)
				lines, tagged := c.command(tag, "%s", cmd)
				requireExactly(t, lines)
				if !strings.HasPrefix(tagged, tag+" BAD ") {
					t.Fatalf("login=%v %s: %q, want BAD", login, cmd, tagged)
				}
			}
		})
	}
}

// TestExpungeAndCloseArguments proves invalid arguments are rejected before
// any deletion, and CLOSE's rejection preserves the current selection.
//
// checklist: IMAP-EXPUNGE-05 IMAP-CLOSE-02
func TestExpungeAndCloseArguments(t *testing.T) {
	for _, cmd := range []string{"EXPUNGE 1", "EXPUNGE garbage", "CLOSE INBOX", "CLOSE garbage"} {
		t.Run(cmd, func(t *testing.T) {
			m := newExpungeMailbox(1)
			c := expungeClient(t, m)
			markDeleted(t, c, "1")
			lines, tagged := c.command("X1", "%s", cmd)
			requireExactly(t, lines)
			if !strings.HasPrefix(tagged, "X1 BAD ") {
				t.Fatalf("%s: %q, want BAD", cmd, tagged)
			}
			if len(m.attempts) != 0 {
				t.Fatalf("bad command deleted %v", m.attempts)
			}
			lines, tagged = c.command("F1", "FETCH 1 (FLAGS)")
			requireOK(t, tagged, "F1")
			requireExactly(t, lines, `* 1 FETCH (UID 100 FLAGS (\Deleted))`)
		})
	}
}

// TestCloseExample checks §6.4.2's A341 exchange with marked messages: silent
// removal, deselection and subsequent reselection without stale deletion marks.
//
// checklist: IMAP-CLOSE-03
func TestCloseExample(t *testing.T) {
	m := newExpungeMailbox(3)
	c := expungeClient(t, m)
	markDeleted(t, c, "1,3")
	lines, tagged := c.command("A341", "CLOSE")
	requireOK(t, tagged, "A341")
	requireExactly(t, lines)
	requireUIDs(t, m, 110)
	lines, tagged = c.command("F1", "FETCH 1 (UID)")
	requireExactly(t, lines)
	if strings.HasPrefix(tagged, "F1 OK ") {
		t.Fatalf("still selected: %q", tagged)
	}
	_, tagged = c.command("S2", "SELECT INBOX")
	requireOK(t, tagged, "S2")
	lines, tagged = c.command("E1", "EXPUNGE")
	requireOK(t, tagged, "E1")
	requireExactly(t, lines)
	requireUIDs(t, m, 110)
}

// TestCloseBackendFailure refuses one or all deletions during CLOSE. CLOSE must
// not report success until every marked message is gone, so it answers NO and
// keeps the mailbox selected. Removals that did succeed are reported as
// EXPUNGE so later sequence numbers stay valid; failed messages keep their
// marks, and a retried CLOSE removes them silently and deselects.
//
// checklist: IMAP-CLOSE-06
func TestCloseBackendFailure(t *testing.T) {
	for _, allFail := range []bool{false, true} {
		t.Run(fmt.Sprintf("allFail=%v", allFail), func(t *testing.T) {
			m := newExpungeMailbox(4)
			m.fail[110] = true
			if allFail {
				m.fail[100] = true
				m.fail[120] = true
			}
			c := expungeClient(t, m)
			markDeleted(t, c, "1:3")
			lines, tagged := c.command("C1", "CLOSE")
			if !strings.HasPrefix(tagged, "C1 NO ") {
				t.Fatalf("refused deletion: %q %q, want NO", lines, tagged)
			}
			if allFail {
				requireExactly(t, lines)
				requireUIDs(t, m, 100, 110, 120, 130)
			} else {
				requireExactly(t, lines, "* 3 EXPUNGE", "* 1 EXPUNGE")
				requireUIDs(t, m, 110, 130)
			}
			lines, tagged = c.command("F1", "FETCH 1:* (FLAGS)")
			requireOK(t, tagged, "F1")
			if allFail {
				requireExactly(t, lines, `* 1 FETCH (UID 100 FLAGS (\Deleted))`, `* 2 FETCH (UID 110 FLAGS (\Deleted))`, `* 3 FETCH (UID 120 FLAGS (\Deleted))`, `* 4 FETCH (UID 130 FLAGS ())`)
			} else {
				requireExactly(t, lines, `* 1 FETCH (UID 110 FLAGS (\Deleted))`, `* 2 FETCH (UID 130 FLAGS ())`)
			}
			clear(m.fail)
			lines, tagged = c.command("C2", "CLOSE")
			requireOK(t, tagged, "C2")
			requireExactly(t, lines)
			requireUIDs(t, m, 130)
			_, tagged = c.command("F2", "FETCH 1 (UID)")
			if strings.HasPrefix(tagged, "F2 OK ") {
				t.Fatalf("CLOSE kept selection: %q", tagged)
			}
		})
	}
}

// TestReadOnlyCloseAndExpunge checks EXAMINE: explicit EXPUNGE is refused,
// but CLOSE succeeds silently and leaves all backend messages intact.
//
// checklist: IMAP-CLOSE-04 IMAP-EXPUNGE-06 IMAP-UIDEXPUNGE-03
func TestReadOnlyCloseAndExpunge(t *testing.T) {
	m := newExpungeMailbox(2)
	c := selectionClient(t, &uidExpungeMailbox{m})
	_, tagged := c.command("S1", "EXAMINE INBOX")
	requireOK(t, tagged, "S1")
	for i, cmd := range []string{"EXPUNGE", "UID EXPUNGE 100:110"} {
		tag := fmt.Sprintf("E%d", i)
		lines, tagged := c.command(tag, "%s", cmd)
		requireExactly(t, lines)
		if !strings.HasPrefix(tagged, tag+" NO ") {
			t.Fatalf("%s: %q, want NO", cmd, tagged)
		}
	}
	lines, tagged := c.command("A341", "CLOSE")
	requireOK(t, tagged, "A341")
	requireExactly(t, lines)
	if len(m.attempts) != 0 {
		t.Fatalf("read-only deletion %v", m.attempts)
	}
	requireUIDs(t, m, 100, 110)
	_, tagged = c.command("F1", "FETCH 1 (UID)")
	if strings.HasPrefix(tagged, "F1 OK ") {
		t.Fatalf("CLOSE kept selection: %q", tagged)
	}
}

// TestLogoutDoesNotExpunge checks LOGOUT implicitly deselects without removal.
//
// checklist: IMAP-CLOSE-05
func TestLogoutDoesNotExpunge(t *testing.T) {
	m := newExpungeMailbox(1)
	c := expungeClient(t, m)
	markDeleted(t, c, "1")
	lines, tagged := c.command("X1", "LOGOUT")
	requireOK(t, tagged, "X1")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "* BYE ") || !c.closed() {
		t.Fatalf("logout %q %q", lines, tagged)
	}
	if len(m.attempts) != 0 {
		t.Fatalf("LOGOUT deleted %v", m.attempts)
	}
	requireUIDs(t, m, 100)
}

// TestUIDExpungeCapabilityAndMissingSet proves unsupported UIDPLUS and a
// missing UID set both fail without deleting a marked message.
//
// checklist: IMAP-UIDEXPUNGE-04
func TestUIDExpungeCapabilityAndMissingSet(t *testing.T) {
	for _, uidplus := range []bool{false, true} {
		t.Run(fmt.Sprint(uidplus), func(t *testing.T) {
			m := newExpungeMailbox(1)
			var box imap.Mailbox = m
			if uidplus {
				box = &uidExpungeMailbox{m}
			}
			c := expungeClient(t, box)
			markDeleted(t, c, "1")
			command := "UID EXPUNGE 100"
			if uidplus {
				command = "UID EXPUNGE"
			}
			lines, tagged := c.command("E1", "%s", command)
			requireExactly(t, lines)
			if !strings.HasPrefix(tagged, "E1 BAD ") {
				t.Fatalf("%s: %q, want BAD", command, tagged)
			}
			if len(m.attempts) != 0 {
				t.Fatalf("invalid command deleted %v", m.attempts)
			}
			lines, tagged = c.command("F1", "FETCH 1 (FLAGS)")
			requireOK(t, tagged, "F1")
			requireExactly(t, lines, `* 1 FETCH (UID 100 FLAGS (\Deleted))`)
		})
	}
}

// TestUIDExpungeInvalidSet proves malformed UID sets fail with BAD before any
// deletion, while boundary-valid sets still expunge their marked messages.
//
// checklist: IMAP-UIDEXPUNGE-05
func TestUIDExpungeInvalidSet(t *testing.T) {
	for _, set := range []string{
		"0", "0:100", "100:0", "00100", "+100", "-100", "abc", "100a",
		"4294967296", "100,", ",100", "100,,110", "100:", ":100",
		"100:110:120", "**", "100 110", "100 (FLAGS)",
	} {
		t.Run(set, func(t *testing.T) {
			m := newExpungeMailbox(2)
			c := expungeClient(t, &uidExpungeMailbox{m})
			markDeleted(t, c, "1:2")
			lines, tagged := c.command("E1", "UID EXPUNGE %s", set)
			requireExactly(t, lines)
			if !strings.HasPrefix(tagged, "E1 BAD ") {
				t.Fatalf("UID EXPUNGE %s: %q, want BAD", set, tagged)
			}
			if len(m.attempts) != 0 {
				t.Fatalf("invalid set deleted %v", m.attempts)
			}
			lines, tagged = c.command("F1", "FETCH 1:2 (FLAGS)")
			requireOK(t, tagged, "F1")
			requireExactly(t, lines,
				`* 1 FETCH (UID 100 FLAGS (\Deleted))`,
				`* 2 FETCH (UID 110 FLAGS (\Deleted))`)
		})
	}
	for _, tc := range []struct {
		set  string
		want []uint32
	}{
		{"4294967295", nil},
		{"110:100", []uint32{100, 110}},
		{"*", []uint32{110}},
		{"*:100,100", []uint32{100, 110}},
	} {
		t.Run("valid "+tc.set, func(t *testing.T) {
			m := newExpungeMailbox(2)
			c := expungeClient(t, &uidExpungeMailbox{m})
			markDeleted(t, c, "1:2")
			_, tagged := c.command("E1", "UID EXPUNGE %s", tc.set)
			requireOK(t, tagged, "E1")
			got := slices.Sorted(slices.Values(m.attempts))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("UID EXPUNGE %s deleted %v, want %v", tc.set, m.attempts, tc.want)
			}
		})
	}
}
