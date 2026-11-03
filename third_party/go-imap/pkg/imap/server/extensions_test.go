package server

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// ── UIDPLUS / Mover test backend ──────────────────────────────────────────
//
// uidPlusStore is an in-memory [Mailbox] that ALSO implements [UIDPlusMailbox]
// and [Mover]. It exists to exercise the optional-interface paths: APPENDUID,
// COPYUID, atomic MOVE and UID EXPUNGE. The base mockMailbox deliberately does
// not implement these, so the two together prove the capability is advertised
// (and the resp-codes emitted) only when the concrete backend supports it.
type uidPlusStore struct {
	mu          sync.Mutex
	byFolder    map[string][]imap.Message
	raws        map[uint32][]byte
	uidValidity map[string]uint32
	nextUID     uint32
	deletes     []uint32
	moves       []moveOp
}

type uidPlusBackend struct {
	user, pass string
	store      *uidPlusStore
}

func newUIDPlusBackend() *uidPlusBackend {
	s := &uidPlusStore{
		byFolder:    map[string][]imap.Message{},
		raws:        map[uint32][]byte{},
		uidValidity: map[string]uint32{"INBOX": 42, "Archive": 99, "Trash": 7},
		nextUID:     1000,
	}
	seed := func(folder string, uid uint32, raw string) {
		s.byFolder[folder] = append(s.byFolder[folder], imap.Message{
			UID: uid, Size: len(raw),
			Subject: "Msg " + strconv.FormatUint(uint64(uid), 10),
			From:    imap.Address{Name: "Sender", Email: "sender@example.com"},
			Date:    time.Date(2025, 3, 15, 10, 0, 0, 0, time.UTC),
		})
		s.raws[uid] = []byte(raw)
	}
	seed("INBOX", 5, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	seed("INBOX", 9, "Subject: Msg 9\r\n\r\nbody 9\r\n")
	seed("INBOX", 20, "Subject: Msg 20\r\n\r\nbody 20\r\n")
	return &uidPlusBackend{user: "alice@example.com", pass: "s3cret", store: s}
}

func (b *uidPlusBackend) Authenticate(user, pass string) (imap.Mailbox, error) {
	if user != b.user || pass != b.pass {
		return nil, fmt.Errorf("invalid credentials")
	}
	return b.store, nil
}

func (m *uidPlusStore) assignUID() uint32 {
	uid := m.nextUID
	m.nextUID++
	return uid
}

func (m *uidPlusStore) findLocked(uid uint32) (folder string, idx int, ok bool) {
	for f, msgs := range m.byFolder {
		for i := range msgs {
			if msgs[i].UID == uid {
				return f, i, true
			}
		}
	}
	return "", 0, false
}

func applyFlagUpdate(msg *imap.Message, f imap.FlagUpdate) {
	if f.Seen != nil {
		msg.Seen = *f.Seen
	}
	if f.Flagged != nil {
		msg.Flagged = *f.Flagged
	}
	if f.Draft != nil {
		msg.Draft = *f.Draft
	}
}

// ── base Mailbox interface ──

func (m *uidPlusStore) Folders() ([]imap.Folder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return []imap.Folder{{Name: "INBOX"}, {Name: "Archive"}, {Name: "Trash"}}, nil
}

func (m *uidPlusStore) Messages(folder string) ([]imap.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]imap.Message, len(m.byFolder[folder]))
	copy(out, m.byFolder[folder])
	return out, nil
}

func (m *uidPlusStore) Fetch(uid uint32) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.raws[uid]
	if !ok {
		return nil, fmt.Errorf("no such message %d", uid)
	}
	return raw, nil
}

func (m *uidPlusStore) Store(uid uint32, f imap.FlagUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if folder, idx, ok := m.findLocked(uid); ok {
		applyFlagUpdate(&m.byFolder[folder][idx], f)
	}
	return nil
}

func (m *uidPlusStore) Move(uid uint32, dest string) error {
	_, err := m.MoveUID(uid, dest)
	return err
}

func (m *uidPlusStore) Delete(uid uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes = append(m.deletes, uid)
	if folder, idx, ok := m.findLocked(uid); ok {
		m.byFolder[folder] = append(m.byFolder[folder][:idx], m.byFolder[folder][idx+1:]...)
		delete(m.raws, uid)
	}
	return nil
}

func (m *uidPlusStore) Copy(uid uint32, dest string) error {
	_, err := m.CopyUID(uid, dest)
	return err
}

func (m *uidPlusStore) Append(dest string, f imap.FlagUpdate, raw []byte) error {
	_, err := m.AppendUID(dest, f, raw)
	return err
}

func (m *uidPlusStore) Quota() (used, limit int64, err error) { return 0, 0, nil }

// ── UIDPlusMailbox ──

func (m *uidPlusStore) UIDValidity(folder string) (uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.uidValidity[folder]; ok {
		return v, nil
	}
	return 1, nil
}

func (m *uidPlusStore) AppendUID(dest string, f imap.FlagUpdate, raw []byte) (uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	uid := m.assignUID()
	msg := imap.Message{UID: uid, Size: len(raw)}
	applyFlagUpdate(&msg, f)
	m.byFolder[dest] = append(m.byFolder[dest], msg)
	m.raws[uid] = raw
	return uid, nil
}

func (m *uidPlusStore) CopyUID(srcUID uint32, dest string) (uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	folder, idx, ok := m.findLocked(srcUID)
	if !ok {
		return 0, fmt.Errorf("no such message %d", srcUID)
	}
	uid := m.assignUID()
	cp := m.byFolder[folder][idx]
	cp.UID = uid
	m.byFolder[dest] = append(m.byFolder[dest], cp)
	if raw, ok := m.raws[srcUID]; ok {
		m.raws[uid] = raw
	}
	return uid, nil
}

// ── Mover ──

func (m *uidPlusStore) MoveUID(srcUID uint32, dest string) (uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	folder, idx, ok := m.findLocked(srcUID)
	if !ok {
		return 0, fmt.Errorf("no such message %d", srcUID)
	}
	src := m.byFolder[folder][idx]
	m.byFolder[folder] = append(m.byFolder[folder][:idx], m.byFolder[folder][idx+1:]...)
	uid := m.assignUID()
	moved := src
	moved.UID = uid
	m.byFolder[dest] = append(m.byFolder[dest], moved)
	if raw, ok := m.raws[srcUID]; ok {
		m.raws[uid] = raw
		delete(m.raws, srcUID)
	}
	m.moves = append(m.moves, moveOp{srcUID, dest})
	return uid, nil
}

func (m *uidPlusStore) deleteCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.deletes)
}

// ── helpers ────────────────────────────────────────────────────────────────

// appendMsg drives an APPEND, handling the "+" continuation and sending the
// literal payload, then returns the tagged status. flags may be "".
func (h *imapHarness) appendMsg(tag, folder, flags, body string) (untagged []string, status string) {
	h.t.Helper()
	if flags != "" {
		h.send("%s APPEND %s (%s) {%d}", tag, folder, flags, len(body))
	} else {
		h.send("%s APPEND %s {%d}", tag, folder, len(body))
	}
	if got := h.readLine(); !strings.HasPrefix(got, "+") {
		h.t.Fatalf("APPEND continuation = %q, want +...", got)
	}
	h.send("%s", body) // send() appends the trailing CRLF after the literal
	for {
		line := h.readLine()
		if strings.HasPrefix(line, tag+" ") {
			return untagged, line
		}
		untagged = append(untagged, line)
	}
}

func mustContain(t *testing.T, got, sub, what string) {
	t.Helper()
	if !strings.Contains(got, sub) {
		t.Errorf("%s = %q, want it to contain %q", what, got, sub)
	}
}

func mustNotContain(t *testing.T, got, sub, what string) {
	t.Helper()
	if strings.Contains(got, sub) {
		t.Errorf("%s = %q, want it to NOT contain %q", what, got, sub)
	}
}

// ── UIDPLUS capability advertisement ─────────────────────────────────────────

func TestUIDPlus_CapabilityAdvertisedOnlyWhenSupported(t *testing.T) {
	b := newUIDPlusBackend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)

	// Pre-auth CAPABILITY cannot see the mailbox, so UIDPLUS is not yet listed.
	pre, status := h.command("c1", "CAPABILITY")
	if !strings.Contains(status, " OK") {
		t.Fatalf("CAPABILITY status = %q", status)
	}
	mustNotContain(t, strings.Join(pre, "\n"), "UIDPLUS", "pre-auth CAPABILITY")

	// LOGIN's tagged OK carries the post-auth capability list including UIDPLUS.
	_, loginStatus := h.command("c2", "LOGIN %s %s", b.user, b.pass)
	mustContain(t, loginStatus, "[CAPABILITY", "LOGIN status")
	mustContain(t, loginStatus, "UIDPLUS", "LOGIN status")

	// A fresh CAPABILITY post-auth lists UIDPLUS (and the always-on extensions).
	post, _ := h.command("c3", "CAPABILITY")
	joined := strings.Join(post, "\n")
	for _, want := range []string{"UIDPLUS", "MOVE", "UNSELECT", "ENABLE", "IDLE", "QUOTA"} {
		mustContain(t, joined, want, "post-auth CAPABILITY")
	}
}

func TestUIDPlus_SelectReportsRealUIDValidity(t *testing.T) {
	b := newUIDPlusBackend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")

	untagged, status := h.command("a2", "SELECT INBOX")
	if !strings.Contains(status, " OK") {
		t.Fatalf("SELECT status = %q", status)
	}
	mustContain(t, strings.Join(untagged, "\n"), "[UIDVALIDITY 42]", "SELECT response")
}

// ── APPENDUID (RFC 4315) ─────────────────────────────────────────────────────

func TestUIDPlus_AppendUID(t *testing.T) {
	b := newUIDPlusBackend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")

	_, status := h.appendMsg("a2", "INBOX", `\Seen`, "Subject: New\r\n\r\nhi\r\n")
	// INBOX UIDVALIDITY is 42; the first assigned UID is 1000.
	mustContain(t, status, "[APPENDUID 42 1000]", "APPEND status")
	mustContain(t, status, "OK", "APPEND status")
}

// ── COPYUID (RFC 4315) on COPY and UID COPY ─────────────────────────────────

func TestUIDPlus_CopyUID(t *testing.T) {
	b := newUIDPlusBackend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")
	h.selectInbox("a2")

	// COPY seq 1 (UID 5) into Archive (UIDVALIDITY 99) -> new UID 1000.
	_, status := h.command("a3", "COPY 1 Archive")
	mustContain(t, status, "[COPYUID 99 5 1000]", "COPY status")

	// UID COPY 9 into Archive -> new UID 1001.
	_, uidStatus := h.command("a4", "UID COPY 9 Archive")
	mustContain(t, uidStatus, "[COPYUID 99 9 1001]", "UID COPY status")
}

// ── Atomic MOVE (RFC 6851): COPYUID first, then EXPUNGE ──────────────────────

func TestUIDPlus_MoveEmitsCopyUIDBeforeExpunge(t *testing.T) {
	b := newUIDPlusBackend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")
	h.selectInbox("a2")

	// MOVE seq 1 (UID 5) into Archive (UIDVALIDITY 99) -> new UID 1000.
	untagged, status := h.command("a3", "MOVE 1 Archive")
	if !strings.Contains(status, " OK") {
		t.Fatalf("MOVE status = %q", status)
	}
	if len(untagged) < 2 {
		t.Fatalf("MOVE untagged = %v, want COPYUID then EXPUNGE", untagged)
	}
	// RFC 6851: the untagged COPYUID must precede the EXPUNGE.
	mustContain(t, untagged[0], "OK [COPYUID 99 5 1000]", "MOVE first untagged")
	mustContain(t, untagged[1], "1 EXPUNGE", "MOVE second untagged")

	// The message actually moved out of INBOX (now 9, 20 remain).
	fetched, _ := h.command("a4", "UID FETCH 1:* (FLAGS)")
	got := sortedUint(uidsIn(fetched))
	if len(got) != 2 || got[0] != 9 || got[1] != 20 {
		t.Errorf("after MOVE, INBOX UIDs = %v, want [9 20]", got)
	}
}

// ── UID EXPUNGE (RFC 4315): only \Deleted messages in the UID set ───────────

func TestUIDPlus_UIDExpunge_OnlyDeletedInSet(t *testing.T) {
	b := newUIDPlusBackend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")
	h.selectInbox("a2")

	// Flag UIDs 5 and 20 \Deleted, but UID EXPUNGE only UID 5.
	if _, st := h.command("a3", `UID STORE 5 +FLAGS (\Deleted)`); !strings.Contains(st, " OK") {
		t.Fatalf("UID STORE 5 status = %q", st)
	}
	if _, st := h.command("a4", `UID STORE 20 +FLAGS (\Deleted)`); !strings.Contains(st, " OK") {
		t.Fatalf("UID STORE 20 status = %q", st)
	}

	untagged, status := h.command("a5", "UID EXPUNGE 5")
	if !strings.Contains(status, " OK") {
		t.Fatalf("UID EXPUNGE status = %q", status)
	}
	// Exactly one EXPUNGE (for UID 5 at seq 1); UID 20 stays despite \Deleted.
	expunges := 0
	for _, l := range untagged {
		if strings.Contains(l, "EXPUNGE") {
			expunges++
		}
	}
	if expunges != 1 {
		t.Errorf("UID EXPUNGE emitted %d EXPUNGE responses, want 1: %v", expunges, untagged)
	}
	if n := b.store.deleteCount(); n != 1 {
		t.Errorf("backend Delete called %d times, want 1 (only UID 5)", n)
	}

	// UID 20 (still \Deleted, not in the set) and UID 9 remain.
	fetched, _ := h.command("a6", "UID FETCH 1:* (FLAGS)")
	got := sortedUint(uidsIn(fetched))
	if len(got) != 2 || got[0] != 9 || got[1] != 20 {
		t.Errorf("after UID EXPUNGE 5, INBOX UIDs = %v, want [9 20]", got)
	}
}

// ── UNSELECT (RFC 3691): deselect without expunging ─────────────────────────

func TestUnselect_DoesNotExpunge(t *testing.T) {
	b := newUIDPlusBackend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")
	h.selectInbox("a2")

	if _, st := h.command("a3", `UID STORE 5 +FLAGS (\Deleted)`); !strings.Contains(st, " OK") {
		t.Fatalf("UID STORE status = %q", st)
	}
	if _, st := h.command("a4", "UNSELECT"); !strings.Contains(st, " OK") {
		t.Fatalf("UNSELECT status = %q", st)
	}
	if n := b.store.deleteCount(); n != 0 {
		t.Errorf("UNSELECT expunged %d messages, want 0 (RFC 3691)", n)
	}

	// Re-select: the \Deleted message is still there.
	fetched, _ := h.command("a5", "SELECT INBOX")
	mustContain(t, strings.Join(fetched, "\n"), "3 EXISTS", "re-SELECT after UNSELECT")
}

// ── ENABLE (RFC 5161): acknowledged, unknown caps ignored ───────────────────

func TestEnable_Acknowledged(t *testing.T) {
	b := newUIDPlusBackend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")

	_, status := h.command("a2", "ENABLE CONDSTORE X-UNKNOWN")
	if !strings.Contains(status, " OK") {
		t.Errorf("ENABLE status = %q, want OK (unknown caps ignored, not BAD)", status)
	}
}

// ── Proof: a v0.1.0 backend (no optional interfaces) is unaffected ───────────

// The base mockBackend implements only [Mailbox]. This test proves the server
// advertises none of the UIDPLUS resp-codes for it and rejects UID EXPUNGE,
// while MOVE/COPY/APPEND still work exactly as in v0.1.0.
func TestBaseBackend_NoUIDPlusCapabilitiesOrRespCodes(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)

	// Capability list must not include UIDPLUS (it isn't supported).
	pre, _ := h.command("b1", "CAPABILITY")
	mustNotContain(t, strings.Join(pre, "\n"), "UIDPLUS", "pre-auth CAPABILITY")

	_, loginStatus := h.command("b2", "LOGIN %s %s", m.user, m.pass)
	mustNotContain(t, loginStatus, "UIDPLUS", "LOGIN status")

	post, _ := h.command("b3", "CAPABILITY")
	joined := strings.Join(post, "\n")
	mustNotContain(t, joined, "UIDPLUS", "post-auth CAPABILITY")
	// MOVE is still advertised — it works via the base Mailbox.Move.
	mustContain(t, joined, "MOVE", "post-auth CAPABILITY")

	if _, st := h.command("b4", "SELECT INBOX"); !strings.Contains(st, " OK") {
		t.Fatalf("SELECT status = %q", st)
	}

	// COPY: no COPYUID resp-code.
	_, copyStatus := h.command("b5", "COPY 1 Archive")
	mustContain(t, copyStatus, "OK", "COPY status")
	mustNotContain(t, copyStatus, "COPYUID", "COPY status")
	if len(m.mbox.copies) != 1 {
		t.Errorf("base COPY recorded %d copies, want 1", len(m.mbox.copies))
	}

	// MOVE: still emits EXPUNGE, but no untagged COPYUID.
	moveUntagged, moveStatus := h.command("b6", "MOVE 1 Archive")
	mustContain(t, moveStatus, "OK", "MOVE status")
	joinedMove := strings.Join(moveUntagged, "\n")
	mustContain(t, joinedMove, "EXPUNGE", "MOVE untagged")
	mustNotContain(t, joinedMove, "COPYUID", "MOVE untagged")

	// UID EXPUNGE: rejected because UIDPLUS is unsupported.
	_, uidExpStatus := h.command("b7", "UID EXPUNGE 1:*")
	mustContain(t, uidExpStatus, "BAD", "UID EXPUNGE status")

	// APPEND: no APPENDUID resp-code.
	_, appendStatus := h.appendMsg("b8", "INBOX", "", "Subject: X\r\n\r\nhi\r\n")
	mustContain(t, appendStatus, "OK", "APPEND status")
	mustNotContain(t, appendStatus, "APPENDUID", "APPEND status")

	// UNSELECT and ENABLE remain available (they need no backend support).
	if _, st := h.command("b9", "UNSELECT"); !strings.Contains(st, " OK") {
		t.Errorf("UNSELECT status = %q, want OK", st)
	}
	if _, st := h.command("b10", "ENABLE CONDSTORE"); !strings.Contains(st, " OK") {
		t.Errorf("ENABLE status = %q, want OK", st)
	}
}
