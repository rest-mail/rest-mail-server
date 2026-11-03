package server

import (
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Non-contiguous UIDs are the crux: they prove FETCH ranges expand across the
// whole selection (regression guard for "1:* returned only one message") and
// that "*" resolves to the newest message, not seq 1.
func seedThree(m *mockBackend) {
	m.seed("INBOX", 5, 100, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	m.seed("INBOX", 9, 200, "Subject: Msg 9\r\n\r\nbody 9\r\n")
	m.seed("INBOX", 20, 300, "Subject: Msg 20\r\n\r\nbody 20\r\n")
}

var uidRe = regexp.MustCompile(`UID (\d+)`)

func uidsIn(lines []string) []uint32 {
	var out []uint32
	for _, l := range lines {
		if !strings.Contains(l, "FETCH") {
			continue
		}
		if mm := uidRe.FindStringSubmatch(l); mm != nil {
			v, _ := strconv.ParseUint(mm[1], 10, 32)
			out = append(out, uint32(v))
		}
	}
	return out
}

func sortedUint(in []uint32) []uint32 {
	out := append([]uint32(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func TestIMAP_UIDFetchRange_ExpandsAcrossNonContiguousUIDs(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("a1")
	h.selectInbox("a2")

	untagged, status := h.command("a3", "UID FETCH 1:* (FLAGS)")
	if !strings.Contains(status, " OK") {
		t.Fatalf("UID FETCH status = %q", status)
	}
	got := uidsIn(untagged)
	if want := []uint32{5, 9, 20}; !reflect.DeepEqual(sortedUint(got), want) {
		t.Errorf("UID FETCH 1:* returned UIDs %v, want %v (all messages, not one)", got, want)
	}
	if len(got) != 3 {
		t.Errorf("UID FETCH 1:* returned %d messages, want 3", len(got))
	}
}

func TestIMAP_SeqFetchRange_ReturnsAllMessages(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("b1")
	h.selectInbox("b2")

	untagged, status := h.command("b3", "FETCH 1:* (FLAGS)")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH status = %q", status)
	}
	got := uidsIn(untagged)
	if want := []uint32{5, 9, 20}; !reflect.DeepEqual(sortedUint(got), want) {
		t.Errorf("FETCH 1:* returned UIDs %v, want %v", got, want)
	}
}

func TestIMAP_UIDFetchStar_IsNewest(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("c1")
	h.selectInbox("c2")

	untagged, status := h.command("c3", "UID FETCH * (FLAGS)")
	if !strings.Contains(status, " OK") {
		t.Fatalf("UID FETCH * status = %q", status)
	}
	got := uidsIn(untagged)
	if !reflect.DeepEqual(got, []uint32{20}) {
		t.Errorf("UID FETCH * returned %v, want [20] (newest only)", got)
	}
}

// BODY.PEEK[] must NOT set \Seen; BODY[] must. This is an RFC 3501 requirement
// and a class of bug a mock makes cheap to pin down.
func TestIMAP_BodyPeekDoesNotMarkSeen_BodyDoes(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("p1")
	h.selectInbox("p2")

	// BODY.PEEK[] — content is fetched but the message stays unread.
	_, status := h.command("p3", "UID FETCH 9 (BODY.PEEK[])")
	if !strings.Contains(status, " OK") {
		t.Fatalf("UID FETCH BODY.PEEK[] status = %q", status)
	}
	if !strings.Contains(h.lastLiteral, "body 9") {
		t.Errorf("BODY.PEEK[] literal missing body: %q", h.lastLiteral)
	}
	// Synchronize before inspecting recorded side effects.
	if _, st := h.command("p4", "NOOP"); !strings.Contains(st, " OK") {
		t.Fatalf("NOOP status = %q", st)
	}
	if m.mbox.wasMarkedRead(9) {
		t.Errorf("BODY.PEEK[] must NOT mark \\Seen, but message 9 was marked read")
	}

	// BODY[] — the non-peek form marks the message read.
	_, status = h.command("p5", "UID FETCH 9 (BODY[])")
	if !strings.Contains(status, " OK") {
		t.Fatalf("UID FETCH BODY[] status = %q", status)
	}
	if _, st := h.command("p6", "NOOP"); !strings.Contains(st, " OK") {
		t.Fatalf("NOOP status = %q", st)
	}
	if !m.mbox.wasMarkedRead(9) {
		t.Errorf("BODY[] must mark \\Seen, but message 9 was not marked read")
	}
}

// RFC822.SIZE must report the message's octet size (RFC 3501 §6.4.5) without
// returning body content and without setting \Seen. Clients fetch it to build
// mailbox lists, so answering with the body — or marking the message read —
// downloads and "reads" the whole mailbox on every list view.
func TestIMAP_RFC822Size_ReturnsSizeNotBody_AndNoSeen(t *testing.T) {
	m := newMockBackend()
	seedThree(m) // UID 5 has Size 100; its raw is only 26 octets, so the two differ.
	h := newIMAPHarness(t, m)
	h.login("s1")
	h.selectInbox("s2")

	untagged, status := h.command("s3", "FETCH 1 RFC822.SIZE")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH RFC822.SIZE status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	if !strings.Contains(resp, "RFC822.SIZE 100") {
		t.Errorf("RFC822.SIZE must report the stored Size 100, got: %q", resp)
	}
	if strings.Contains(resp, "BODY[") {
		t.Errorf("RFC822.SIZE must not return body content, got: %q", resp)
	}
	// Synchronize, then confirm the message was left unread.
	if _, st := h.command("s4", "NOOP"); !strings.Contains(st, " OK") {
		t.Fatalf("NOOP status = %q", st)
	}
	if m.mbox.wasMarkedRead(5) {
		t.Errorf("FETCH RFC822.SIZE must NOT mark \\Seen, but UID 5 was marked read")
	}
}

// RFC822.HEADER is defined as BODY.PEEK[HEADER] (RFC 3501 §6.4.5): it returns
// only the header octets and must NOT set \Seen.
func TestIMAP_RFC822Header_IsPeekHeadersOnly(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("hh1")
	h.selectInbox("hh2")

	untagged, status := h.command("hh3", "FETCH 1 RFC822.HEADER")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH RFC822.HEADER status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	if !strings.Contains(resp, "RFC822.HEADER") {
		t.Errorf("response must use the RFC822.HEADER item, got: %q", resp)
	}
	if !strings.Contains(h.lastLiteral, "Subject: Msg 5") {
		t.Errorf("RFC822.HEADER literal must contain the headers, got: %q", h.lastLiteral)
	}
	if strings.Contains(h.lastLiteral, "body 5") {
		t.Errorf("RFC822.HEADER must not return the body, got: %q", h.lastLiteral)
	}
	if _, st := h.command("hh4", "NOOP"); !strings.Contains(st, " OK") {
		t.Fatalf("NOOP status = %q", st)
	}
	if m.mbox.wasMarkedRead(5) {
		t.Errorf("FETCH RFC822.HEADER must NOT mark \\Seen, but UID 5 was marked read")
	}
}

// The UID FETCH twin has the same defect: (FLAGS RFC822.SIZE) is the canonical
// mailbox-paging fetch and must not download bodies or set \Seen.
func TestIMAP_UIDFetch_FlagsAndSize_NoBodyNoSeen(t *testing.T) {
	m := newMockBackend()
	seedThree(m) // UID 9 has Size 200.
	h := newIMAPHarness(t, m)
	h.login("u1")
	h.selectInbox("u2")

	untagged, status := h.command("u3", "UID FETCH 9 (FLAGS RFC822.SIZE)")
	if !strings.Contains(status, " OK") {
		t.Fatalf("UID FETCH (FLAGS RFC822.SIZE) status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	if !strings.Contains(resp, "RFC822.SIZE 200") {
		t.Errorf("must report UID 9 Size 200, got: %q", resp)
	}
	if strings.Contains(resp, "BODY[") {
		t.Errorf("(FLAGS RFC822.SIZE) must not return body content, got: %q", resp)
	}
	if _, st := h.command("u4", "NOOP"); !strings.Contains(st, " OK") {
		t.Fatalf("NOOP status = %q", st)
	}
	if m.mbox.wasMarkedRead(9) {
		t.Errorf("(FLAGS RFC822.SIZE) must NOT mark \\Seen, but UID 9 was marked read")
	}
}

// SELECT begins a new selection (RFC 3501 §6.3.1): the set of UIDs flagged
// \Deleted in a prior mailbox must not carry over. UIDs are folder-scoped, so a
// leaked \Deleted mark makes EXPUNGE delete a same-numbered UID in the newly
// selected folder — cross-folder data loss. Here INBOX and Sent both hold UID 5;
// flagging UID 5 \Deleted in INBOX (without expunging) then SELECTing Sent and
// EXPUNGEing must NOT delete Sent's UID 5.
func TestIMAP_DeletedFlag_DoesNotLeakAcrossSelect(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 5, 100, "Subject: Inbox 5\r\n\r\nbody\r\n")
	m.seed("Sent", 5, 100, "Subject: Sent 5\r\n\r\nbody\r\n")
	h := newIMAPHarness(t, m)
	h.login("d1")
	h.selectInbox("d2")

	// Flag INBOX UID 5 \Deleted, but do not expunge it here.
	if _, st := h.command("d3", "STORE 1 +FLAGS (\\Deleted)"); !strings.Contains(st, " OK") {
		t.Fatalf("STORE status = %q", st)
	}

	// Switch to Sent — a fresh selection; no message here is flagged \Deleted.
	if _, st := h.command("d4", "SELECT Sent"); !strings.Contains(st, " OK") {
		t.Fatalf("SELECT Sent status = %q", st)
	}

	// EXPUNGE in Sent must be a no-op: nothing was flagged in this selection.
	untagged, st := h.command("d5", "EXPUNGE")
	if !strings.Contains(st, " OK") {
		t.Fatalf("EXPUNGE status = %q", st)
	}
	for _, l := range untagged {
		if strings.Contains(l, "EXPUNGE") {
			t.Errorf("EXPUNGE in Sent emitted %q; the \\Deleted mark leaked from INBOX", l)
		}
	}
	if m.mbox.wasDeleted(5) {
		t.Errorf("UID 5 was deleted after SELECT Sent; \\Deleted leaked across SELECT (cross-folder data loss)")
	}
}

// IDLE start/stop under -race guards the goroutine lifecycle fix: the poll
// goroutine must be fully stopped before the tagged response is written.
func TestIMAP_Idle_StartAndStop(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 5, 100, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	h := newIMAPHarness(t, m)
	h.login("i1")
	h.selectInbox("i2")

	h.send("i3 IDLE")
	if got := h.readLine(); got != "+ idling" {
		t.Fatalf("IDLE continuation = %q, want %q", got, "+ idling")
	}
	h.send("DONE")
	if got := h.readLine(); !strings.HasPrefix(got, "i3 OK") {
		t.Fatalf("IDLE termination = %q, want i3 OK...", got)
	}
}

// UIDNEXT (RFC 3501 §2.3.1.1) is the UID that will be assigned to the *next*
// message, which must be strictly greater than every existing UID — i.e.
// highest-UID + 1, not oldest-UID + 1. With non-contiguous UIDs {5, 9, 20} the
// next append gets UID 21, so SELECT must report [UIDNEXT 21]; the old code
// reported oldest+1 = 6, which would corrupt a disconnected client's resync.
func TestIMAP_SelectUIDNEXT_IsHighestPlusOne(t *testing.T) {
	m := newMockBackend()
	seedThree(m) // INBOX holds UIDs {5, 9, 20}
	h := newIMAPHarness(t, m)
	h.login("u1")

	untagged, status := h.command("u2", "SELECT INBOX")
	if !strings.Contains(status, " OK") {
		t.Fatalf("SELECT status = %q", status)
	}
	joined := strings.Join(untagged, "\n")
	if strings.Contains(joined, "[UIDNEXT 6]") {
		t.Errorf("SELECT reported oldest-UID+1: [UIDNEXT 6]; want highest-UID+1 [UIDNEXT 21]\n%s", joined)
	}
	if !strings.Contains(joined, "[UIDNEXT 21]") {
		t.Errorf("SELECT UIDNEXT = missing/wrong; want [UIDNEXT 21] (highest UID 20 + 1)\n%s", joined)
	}
}

// An empty mailbox reports [UIDNEXT 1]: with no messages the next UID is 1.
func TestIMAP_SelectUIDNEXT_EmptyMailboxIsOne(t *testing.T) {
	m := newMockBackend()
	h := newIMAPHarness(t, m)
	h.login("e1")

	untagged, status := h.command("e2", "SELECT INBOX")
	if !strings.Contains(status, " OK") {
		t.Fatalf("SELECT status = %q", status)
	}
	if joined := strings.Join(untagged, "\n"); !strings.Contains(joined, "[UIDNEXT 1]") {
		t.Errorf("SELECT UIDNEXT on empty mailbox = missing/wrong; want [UIDNEXT 1]\n%s", joined)
	}
}
