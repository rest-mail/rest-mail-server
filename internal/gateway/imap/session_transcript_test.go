package imap

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

func uidsIn(lines []string) []uint {
	var out []uint
	for _, l := range lines {
		if !strings.Contains(l, "FETCH") {
			continue
		}
		if mm := uidRe.FindStringSubmatch(l); mm != nil {
			v, _ := strconv.ParseUint(mm[1], 10, 32)
			out = append(out, uint(v))
		}
	}
	return out
}

func sortedUint(in []uint) []uint {
	out := append([]uint(nil), in...)
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
	if want := []uint{5, 9, 20}; !reflect.DeepEqual(sortedUint(got), want) {
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
	if want := []uint{5, 9, 20}; !reflect.DeepEqual(sortedUint(got), want) {
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
	if !reflect.DeepEqual(got, []uint{20}) {
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
	if m.wasMarkedRead(9) {
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
	if !m.wasMarkedRead(9) {
		t.Errorf("BODY[] must mark \\Seen, but message 9 was not marked read")
	}
}

// IDLE start/stop under -race guards the goroutine lifecycle fix (#48): the poll
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
