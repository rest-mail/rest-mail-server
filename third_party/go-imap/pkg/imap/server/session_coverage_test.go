package server

import (
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// Refused commands must leave the unauthenticated session usable without
// consulting a mailbox. A successful LOGIN then proves recovery on the wire.
func TestCoverageUnauthenticatedCommands(t *testing.T) {
	h := newIMAPHarness(t, newMockBackend())
	for i, cmd := range []string{"CREATE Archive", "DELETE Archive", "RENAME Archive Old", `GETQUOTA ""`, "GETQUOTAROOT INBOX", "IDLE", "ENABLE UTF8=ACCEPT", "UNSELECT"} {
		tag := fmt.Sprintf("r%d", i)
		lines, st := h.command(tag, "%s", cmd)
		wantStatus := " NO "
		if cmd == "UNSELECT" {
			wantStatus = " BAD "
		}
		if len(lines) != 0 || !strings.HasPrefix(st, tag+wantStatus) {
			t.Fatalf("%s: %v / %s", cmd, lines, st)
		}
	}
	h.login("login")
	for i, cmd := range []string{"IDLE", "UID FETCH 1 FLAGS"} {
		tag := fmt.Sprintf("n%d", i)
		_, st := h.command(tag, "%s", cmd)
		if st != tag+" NO No mailbox selected" {
			t.Fatalf("%s: %s", cmd, st)
		}
	}
	h.selectInbox("select")
}

func TestCoverageMalformedSelectedCommands(t *testing.T) {
	b := newMockBackend()
	b.seed("INBOX", 7, 10, "Subject: test\r\n\r\nbody")
	h := newIMAPHarness(t, b)
	h.login("login")
	h.selectInbox("select")
	for i, cmd := range []string{"FETCH 1", "STORE 1", `STORE 1 BOGUS (\Seen)`, "UID FETCH", "UID STORE", "UID STORE 7", "UID STORE 7 FLAGS", `UID STORE 7 BOGUS (\Seen)`, "UID INVALID"} {
		tag := fmt.Sprintf("b%d", i)
		lines, st := h.command(tag, "%s", cmd)
		if len(lines) != 0 || !strings.HasPrefix(st, tag+" BAD ") {
			t.Fatalf("%s: %v / %s", cmd, lines, st)
		}
	}
	if _, ok := b.mbox.lastStore(7); ok {
		t.Fatal("malformed STORE reached backend")
	}
	lines, st := h.command("recover", "FETCH 1 (UID FLAGS)")
	if st != "recover OK FETCH completed" || len(lines) != 1 || !strings.Contains(lines[0], "UID 7 FLAGS ()") {
		t.Fatalf("recovery: %v / %s", lines, st)
	}
}

func TestCoverageMailboxManagement(t *testing.T) {
	b := newMockBackend()
	b.seed("Archive", 7, 10, "")
	b.seed("Archive", 12, 10, "")
	b.seed("Old", 20, 10, "")
	h := newIMAPHarness(t, b)
	h.login("login")
	for i, tc := range []struct{ cmd, reply string }{
		{`CREATE ""`, "NO Missing folder name"},
		{"CREATE " + strings.Repeat("x", 201), "NO Folder name too long"},
		{"CREATE bad\x00name", "NO Invalid folder name"},
		{`DELETE ""`, "NO Missing folder name"},
		{"RENAME Old", "BAD RENAME requires old and new name"},
		{`RENAME "" New`, "BAD RENAME requires old and new name"},
		{`RENAME Old ""`, "BAD RENAME requires old and new name"},
		{"CREATE New", "OK CREATE completed"},
		{"DELETE Archive", "OK DELETE completed"},
		{"RENAME Old New", "OK RENAME completed"},
	} {
		tag := fmt.Sprintf("m%d", i)
		lines, st := h.command(tag, "%s", tc.cmd)
		if len(lines) != 0 || st != tag+" "+tc.reply {
			t.Fatalf("%q: %v / %s; want %s", tc.cmd, lines, st, tc.reply)
		}
	}
	b.mbox.mu.Lock()
	got := append([]moveOp(nil), b.mbox.moves...)
	b.mbox.mu.Unlock()
	if want := []moveOp{{7, "Trash"}, {12, "Trash"}, {20, "New"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("moves = %v, want %v", got, want)
	}
}

type coverageQuotaMailbox struct {
	*mockMailbox
	used, limit int64
	err         error
	calls       int
}

func (m *coverageQuotaMailbox) Quota() (int64, int64, error) {
	m.calls++
	return m.used, m.limit, m.err
}

func TestCoverageQuotaReplies(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("backendError=%v", fail), func(t *testing.T) {
			m := &coverageQuotaMailbox{mockMailbox: newMockBackend().mbox, used: 3073, limit: 10249}
			m.folders = append(m.folders, imap.Folder{Name: "Archive Box"})
			if fail {
				m.err = errors.New("quota unavailable")
			}
			h := newIMAPHarnessWith(t, panicTestBackend{mbox: m}, "alice@example.com", "s3cret")
			h.login("login")
			if lines, st := h.command("missing", `GETQUOTAROOT ""`); len(lines) != 0 || st != "missing BAD Missing mailbox name" {
				t.Fatalf("missing mailbox: %v / %s", lines, st)
			}
			for i, cmd := range []string{`GETQUOTA ""`, `GETQUOTAROOT "Archive Box"`} {
				tag := fmt.Sprintf("q%d", i)
				lines, st := h.command(tag, "%s", cmd)
				if fail {
					if len(lines) != 0 || st != tag+" NO Failed to get quota" {
						t.Fatalf("%s: %v / %s", cmd, lines, st)
					}
				} else {
					want := []string{`* QUOTA "" (STORAGE 3 10)`}
					completion := "GETQUOTA"
					if i == 1 {
						want = append([]string{`* QUOTAROOT "Archive Box" ""`}, want...)
						completion = "GETQUOTAROOT"
					}
					if !reflect.DeepEqual(lines, want) || st != tag+" OK "+completion+" completed" {
						t.Fatalf("%s: %v / %s; want %v", cmd, lines, st, want)
					}
				}
			}
			// NOOP's response synchronizes access to backend call accounting.
			if _, st := h.command("noop", "NOOP"); st != "noop OK NOOP completed" {
				t.Fatal(st)
			}
			if m.calls != 2 {
				t.Fatalf("Quota calls=%d, want 2 (missing mailbox must not call backend)", m.calls)
			}
		})
	}
}

type coverageListMailbox struct {
	*mockMailbox
	err error
}

func (m *coverageListMailbox) Folders() ([]imap.Folder, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.mockMailbox.Folders()
}

func TestCoverageListReplies(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("backendError=%v", fail), func(t *testing.T) {
			b := newMockBackend()
			b.mbox.folders = []imap.Folder{{Name: "INBOX"}, {Name: "Sent", SpecialUse: imap.SpecialUseSent}, {Name: "Drafts", SpecialUse: imap.SpecialUseDrafts},
				{Name: "Trash", SpecialUse: imap.SpecialUseTrash}, {Name: "Junk", SpecialUse: imap.SpecialUseJunk}, {Name: "Archive"}}
			m := &coverageListMailbox{mockMailbox: b.mbox}
			if fail {
				m.err = errors.New("folders unavailable")
			}
			h := newIMAPHarnessWith(t, panicTestBackend{mbox: m}, b.user, b.pass)
			h.login("login")
			lines, st := h.command("delimiter", `LIST "" ""`)
			if !reflect.DeepEqual(lines, []string{`* LIST (\Noselect) "/" ""`}) || st != "delimiter OK LIST completed" {
				t.Fatalf("delimiter: %v / %s", lines, st)
			}
			for i, cmd := range []string{"LIST", `LIST "*"`, `LIST "" "*"`} {
				tag := fmt.Sprintf("l%d", i)
				lines, st = h.command(tag, "%s", cmd)
				if fail {
					if len(lines) != 0 || st != tag+" NO Failed to list folders" {
						t.Fatalf("%s: %v / %s", cmd, lines, st)
					}
					continue
				}
				want := []string{`* LIST () "/" "INBOX"`, `* LIST (\Sent) "/" "Sent"`, `* LIST (\Drafts) "/" "Drafts"`, `* LIST (\Trash) "/" "Trash"`, `* LIST (\Junk) "/" "Junk"`, `* LIST () "/" "Archive"`}
				if !reflect.DeepEqual(lines, want) || st != tag+" OK LIST completed" {
					t.Fatalf("%s: %v / %s; want %v", cmd, lines, st, want)
				}
			}
		})
	}
}

func TestCoverageAuthenticationSyntaxRecovery(t *testing.T) {
	h := newIMAPHarness(t, newMockBackend())
	for i, tc := range []struct{ cmd, reply string }{{"LOGIN alice", "BAD LOGIN requires username and password"}, {"AUTHENTICATE UNKNOWN", "NO Unsupported mechanism"}} {
		tag := fmt.Sprintf("a%d", i)
		if lines, st := h.command(tag, "%s", tc.cmd); len(lines) != 0 || st != tag+" "+tc.reply {
			t.Fatalf("%s: %v / %s", tc.cmd, lines, st)
		}
	}
	h.send("plain AUTHENTICATE PLAIN")
	if got := h.readLine(); got != "+ " {
		t.Fatal(got)
	}
	h.send("%s", base64.StdEncoding.EncodeToString([]byte("missing NUL separators")))
	if got := h.readLine(); got != "plain NO Invalid PLAIN data" {
		t.Fatal(got)
	}
	if _, st := h.command("refused", "SELECT INBOX"); st != "refused NO Not authenticated" {
		t.Fatal(st)
	}
	h.login("recover")
}
