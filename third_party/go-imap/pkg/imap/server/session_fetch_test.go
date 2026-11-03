package server

import (
	"strings"
	"testing"
)

// parensBalanced reports whether every '(' in s is matched by a ')' and no ')'
// ever precedes its opener — a cheap well-formedness check for the parenthesized
// BODYSTRUCTURE / ENVELOPE structures RFC 3501 §7.4.2 requires.
func parensBalanced(s string) bool {
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// The FETCH macros ALL/FAST/FULL (RFC 3501 §6.4.5) must expand to their
// component data items. ALL = FLAGS INTERNALDATE RFC822.SIZE ENVELOPE.
func TestIMAP_FetchMacro_ALL(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("m1")
	h.selectInbox("m2")

	untagged, status := h.command("m3", "FETCH 1 ALL")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH 1 ALL status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	for _, want := range []string{"FLAGS", "INTERNALDATE", "RFC822.SIZE 100", "ENVELOPE ("} {
		if !strings.Contains(resp, want) {
			t.Errorf("FETCH 1 ALL missing %q; got: %q", want, resp)
		}
	}
	if strings.Contains(resp, "BODY") {
		t.Errorf("FETCH 1 ALL must not include BODY; got: %q", resp)
	}
}

// FAST = FLAGS INTERNALDATE RFC822.SIZE (no ENVELOPE, no BODY).
func TestIMAP_FetchMacro_FAST(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("f1")
	h.selectInbox("f2")

	untagged, status := h.command("f3", "FETCH 1 FAST")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH 1 FAST status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	for _, want := range []string{"FLAGS", "INTERNALDATE", "RFC822.SIZE 100"} {
		if !strings.Contains(resp, want) {
			t.Errorf("FETCH 1 FAST missing %q; got: %q", want, resp)
		}
	}
	if strings.Contains(resp, "ENVELOPE") {
		t.Errorf("FETCH 1 FAST must not include ENVELOPE; got: %q", resp)
	}
	if strings.Contains(resp, "BODY") {
		t.Errorf("FETCH 1 FAST must not include BODY; got: %q", resp)
	}
}

// FULL = FLAGS INTERNALDATE RFC822.SIZE ENVELOPE BODY (the non-extensible
// BODYSTRUCTURE).
func TestIMAP_FetchMacro_FULL(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("l1")
	h.selectInbox("l2")

	untagged, status := h.command("l3", "FETCH 1 FULL")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH 1 FULL status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	for _, want := range []string{"FLAGS", "INTERNALDATE", "RFC822.SIZE 100", "ENVELOPE (", "BODY ("} {
		if !strings.Contains(resp, want) {
			t.Errorf("FETCH 1 FULL missing %q; got: %q", want, resp)
		}
	}
	if !parensBalanced(resp) {
		t.Errorf("FETCH 1 FULL response has unbalanced parens: %q", resp)
	}
}

// FETCH BODYSTRUCTURE must answer with a correctly-formed parenthesized body
// structure — not silently fall through to FLAGS+UID — and must not set \Seen.
func TestIMAP_FetchBodystructure_WellFormed_NoSeen(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("bs1")
	h.selectInbox("bs2")

	untagged, status := h.command("bs3", "FETCH 1 BODYSTRUCTURE")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH 1 BODYSTRUCTURE status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	if !strings.Contains(resp, `BODYSTRUCTURE ("TEXT" "PLAIN"`) {
		t.Errorf("BODYSTRUCTURE missing or malformed; got: %q", resp)
	}
	if !parensBalanced(resp) {
		t.Errorf("BODYSTRUCTURE response has unbalanced parens: %q", resp)
	}
	if _, st := h.command("bs4", "NOOP"); !strings.Contains(st, " OK") {
		t.Fatalf("NOOP status = %q", st)
	}
	if m.mbox.wasMarkedRead(5) {
		t.Errorf("FETCH BODYSTRUCTURE must NOT mark \\Seen, but UID 5 was marked read")
	}
}

// The bare BODY item is the non-extensible BODYSTRUCTURE and must likewise be a
// well-formed parenthesized structure that does not set \Seen (distinct from
// BODY[...] section fetches).
func TestIMAP_FetchBody_Structure_NoSeen(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("bb1")
	h.selectInbox("bb2")

	untagged, status := h.command("bb3", "FETCH 1 BODY")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH 1 BODY status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	if !strings.Contains(resp, `BODY ("TEXT" "PLAIN"`) {
		t.Errorf("bare BODY structure missing or malformed; got: %q", resp)
	}
	if !parensBalanced(resp) {
		t.Errorf("BODY structure response has unbalanced parens: %q", resp)
	}
	if _, st := h.command("bb4", "NOOP"); !strings.Contains(st, " OK") {
		t.Fatalf("NOOP status = %q", st)
	}
	if m.mbox.wasMarkedRead(5) {
		t.Errorf("FETCH BODY (structure) must NOT mark \\Seen, but UID 5 was marked read")
	}
}

// BODY.PEEK[HEADER] must return the message's header block — the lines up to and
// including the blank line that ends the header — labelled as BODY[HEADER] (the
// .PEEK is not echoed, RFC 3501 §7.4.2), and must NOT set \Seen (§6.4.5). The
// buggy dispatch reported in rest-mail/go-imap#16 matched only the non-peek
// spelling "BODY[HEADER]", so "BODY.PEEK[HEADER]" fell through to the FLAGS+UID
// catch-all and returned no body at all. Seq 1 is UID 5, raw
// "Subject: Msg 5\r\n\r\nbody 5\r\n": its header is "Subject: Msg 5\r\n\r\n".
func TestIMAP_BodyPeekHeader_ReturnsHeaderBlock_NoSeen(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("ph1")
	h.selectInbox("ph2")

	untagged, status := h.command("ph3", "FETCH 1 BODY.PEEK[HEADER]")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH BODY.PEEK[HEADER] status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	if !strings.Contains(resp, "BODY[HEADER] {") {
		t.Errorf("BODY.PEEK[HEADER] must answer a non-empty BODY[HEADER] literal, not fall through; got: %q", resp)
	}
	if !strings.Contains(h.lastLiteral, "Subject: Msg 5") {
		t.Errorf("BODY.PEEK[HEADER] literal must contain the header block; got: %q", h.lastLiteral)
	}
	if strings.Contains(h.lastLiteral, "body 5") {
		t.Errorf("BODY.PEEK[HEADER] literal must not contain the body text; got: %q", h.lastLiteral)
	}
	if _, st := h.command("ph4", "NOOP"); !strings.Contains(st, " OK") {
		t.Fatalf("NOOP status = %q", st)
	}
	if m.mbox.wasMarkedRead(5) {
		t.Errorf("BODY.PEEK[HEADER] must NOT mark \\Seen, but UID 5 was marked read")
	}
}

// BODY.PEEK[TEXT] must return the message body — everything after the
// header-terminating blank line — labelled BODY[TEXT], without setting \Seen.
// This is the [TEXT] twin of the rest-mail/go-imap#16 regression. Seq 1's body
// is "body 5\r\n".
func TestIMAP_BodyPeekText_ReturnsBodyText_NoSeen(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("pt1")
	h.selectInbox("pt2")

	untagged, status := h.command("pt3", "FETCH 1 BODY.PEEK[TEXT]")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH BODY.PEEK[TEXT] status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	if !strings.Contains(resp, "BODY[TEXT] {") {
		t.Errorf("BODY.PEEK[TEXT] must answer a non-empty BODY[TEXT] literal, not fall through; got: %q", resp)
	}
	if !strings.Contains(h.lastLiteral, "body 5") {
		t.Errorf("BODY.PEEK[TEXT] literal must contain the body text; got: %q", h.lastLiteral)
	}
	if strings.Contains(h.lastLiteral, "Subject") {
		t.Errorf("BODY.PEEK[TEXT] literal must not contain header lines; got: %q", h.lastLiteral)
	}
	if _, st := h.command("pt4", "NOOP"); !strings.Contains(st, " OK") {
		t.Fatalf("NOOP status = %q", st)
	}
	if m.mbox.wasMarkedRead(5) {
		t.Errorf("BODY.PEEK[TEXT] must NOT mark \\Seen, but UID 5 was marked read")
	}
}

// The non-peek BODY[HEADER] / BODY[TEXT] spellings return the same sections but,
// being non-peek body-content fetches, DO set \Seen (RFC 3501 §6.4.5). This
// pins the peek/non-peek distinction so a future "normalize .PEEK away" change
// cannot accidentally drop the \Seen side effect of the non-peek form.
func TestIMAP_BodyHeaderText_NonPeek_MarksSeen(t *testing.T) {
	for _, tc := range []struct {
		section, want string
	}{
		{"BODY[HEADER]", "Subject: Msg 5"},
		{"BODY[TEXT]", "body 5"},
	} {
		m := newMockBackend()
		seedThree(m)
		h := newIMAPHarness(t, m)
		h.login("nh1")
		h.selectInbox("nh2")

		_, status := h.command("nh3", "FETCH 1 %s", tc.section)
		if !strings.Contains(status, " OK") {
			t.Fatalf("FETCH %s status = %q", tc.section, status)
		}
		if !strings.Contains(h.lastLiteral, tc.want) {
			t.Errorf("FETCH %s literal missing %q; got: %q", tc.section, tc.want, h.lastLiteral)
		}
		if _, st := h.command("nh4", "NOOP"); !strings.Contains(st, " OK") {
			t.Fatalf("NOOP status = %q", st)
		}
		if !m.mbox.wasMarkedRead(5) {
			t.Errorf("non-peek FETCH %s must mark \\Seen, but UID 5 was not marked read", tc.section)
		}
	}
}

// UID FETCH shares the same section handler, so its BODY.PEEK[HEADER] /
// BODY.PEEK[TEXT] must serve the header/body just as the sequence-number twin
// does — the issue reported the catch-all fall-through for the UID path too.
func TestIMAP_UIDFetch_BodyPeekSections(t *testing.T) {
	for _, tc := range []struct {
		section, want, absent string
	}{
		{"BODY.PEEK[HEADER]", "Subject: Msg 5", "body 5"},
		{"BODY.PEEK[TEXT]", "body 5", "Subject"},
	} {
		m := newMockBackend()
		seedThree(m)
		h := newIMAPHarness(t, m)
		h.login("uh1")
		h.selectInbox("uh2")

		_, status := h.command("uh3", "UID FETCH 5 %s", tc.section)
		if !strings.Contains(status, " OK") {
			t.Fatalf("UID FETCH %s status = %q", tc.section, status)
		}
		if !strings.Contains(h.lastLiteral, tc.want) {
			t.Errorf("UID FETCH %s literal missing %q; got: %q", tc.section, tc.want, h.lastLiteral)
		}
		if strings.Contains(h.lastLiteral, tc.absent) {
			t.Errorf("UID FETCH %s literal must not contain %q; got: %q", tc.section, tc.absent, h.lastLiteral)
		}
		if _, st := h.command("uh4", "NOOP"); !strings.Contains(st, " OK") {
			t.Fatalf("NOOP status = %q", st)
		}
		if m.mbox.wasMarkedRead(5) {
			t.Errorf("UID FETCH %s (peek) must NOT mark \\Seen, but UID 5 was marked read", tc.section)
		}
	}
}

// A non-peek FETCH BODY[] of a previously-unseen message sets \Seen as a side
// effect (RFC 3501 §6.4.5), and the FLAGS echoed in THAT SAME response must
// reflect the new state — otherwise the client still believes the message is
// unseen (rest-mail/go-imap#28). Seq 1 is UID 5, seeded unseen; the buggy code
// formatted FLAGS from the pre-store message, emitting "FLAGS ()".
func TestIMAP_FetchBody_AutoSeen_ResponseFlagsIncludeSeen(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("as1")
	h.selectInbox("as2")

	untagged, status := h.command("as3", "FETCH 1 BODY[]")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH 1 BODY[] status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	if !strings.Contains(resp, `FLAGS (\Seen)`) {
		t.Errorf("auto-\\Seen FETCH response FLAGS must include \\Seen (reflect the new state); got: %q", resp)
	}
	// The side-effect store must actually have happened.
	if _, st := h.command("as4", "NOOP"); !strings.Contains(st, " OK") {
		t.Fatalf("NOOP status = %q", st)
	}
	if !m.mbox.wasMarkedRead(5) {
		t.Errorf("FETCH BODY[] must mark UID 5 \\Seen")
	}
}

// The peek form BODY.PEEK[] must not set \Seen, and so must NOT add \Seen to the
// echoed FLAGS: an unseen message stays "FLAGS ()". Pins that the #28 fix keys
// off the same non-peek/peek distinction as the \Seen store itself.
func TestIMAP_FetchBodyPeek_ResponseFlagsOmitSeen(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("pk1")
	h.selectInbox("pk2")

	untagged, status := h.command("pk3", "FETCH 1 BODY.PEEK[]")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH 1 BODY.PEEK[] status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	if strings.Contains(resp, `\Seen`) {
		t.Errorf("BODY.PEEK[] must not add \\Seen to the FETCH FLAGS; got: %q", resp)
	}
	if !strings.Contains(resp, "FLAGS ()") {
		t.Errorf("unseen BODY.PEEK[] should echo empty FLAGS; got: %q", resp)
	}
	if _, st := h.command("pk4", "NOOP"); !strings.Contains(st, " OK") {
		t.Fatalf("NOOP status = %q", st)
	}
	if m.mbox.wasMarkedRead(5) {
		t.Errorf("BODY.PEEK[] must NOT mark UID 5 \\Seen")
	}
}

// An already-\Seen message's non-peek FETCH BODY[] is unchanged: the FLAGS still
// report \Seen (from real state, not a side effect), and the flag did not change
// so no redundant \Seen Store is issued.
func TestIMAP_FetchBody_AlreadySeen_Unchanged(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	m.mbox.byFolder["INBOX"][0].Seen = true // UID 5 already seen (set before the session reads it)
	h := newIMAPHarness(t, m)
	h.login("se1")
	h.selectInbox("se2")

	untagged, status := h.command("se3", "FETCH 1 BODY[]")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH 1 BODY[] status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	if !strings.Contains(resp, `FLAGS (\Seen)`) {
		t.Errorf("already-seen message must still echo \\Seen; got: %q", resp)
	}
	if _, st := h.command("se4", "NOOP"); !strings.Contains(st, " OK") {
		t.Fatalf("NOOP status = %q", st)
	}
	if m.mbox.wasMarkedRead(5) {
		t.Errorf("already-\\Seen message must not trigger a redundant \\Seen Store")
	}
}
