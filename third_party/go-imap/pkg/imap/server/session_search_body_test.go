package server

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// SEARCH BODY, TEXT and RECENT are known RFC 3501 §6.4.4 keys that must match a
// real subset, not every message. BODY <s> matches a case-insensitive substring
// in the message body (text); TEXT <s> matches header OR body; RECENT matches the
// \Recent messages — which this engine models as the unseen set, exactly as the
// RECENT count reported by SELECT/EXAMINE and STATUS does.
//
// Each corpus message carries a distinct header-only term (the Subject) and a
// distinct body-only term, so a match can be attributed to the header or the body
// and "matched the right subset" is distinguishable from "matched everything".
//
//	seq1 UID10  Subject "alpha-subj"   body "beta-body quarterly"  \Seen
//	seq2 UID20  Subject "gamma-subj"   body "delta-body lunch"     (unseen)
//	seq3 UID30  Subject "epsilon-subj" body "zeta-body photos"     (unseen)
func seedBodyCorpus(m *mockBackend) {
	rows := []struct {
		uid  uint32
		seen bool
		raw  string
	}{
		{10, true, "Subject: alpha-subj\r\nFrom: a@example.com\r\n\r\nbeta-body quarterly report\r\n"},
		{20, false, "Subject: gamma-subj\r\nFrom: b@example.com\r\n\r\ndelta-body lunch plans\r\n"},
		{30, false, "Subject: epsilon-subj\r\nFrom: c@example.com\r\n\r\nzeta-body vacation photos\r\n"},
	}
	for _, r := range rows {
		m.mbox.byFolder["INBOX"] = append(m.mbox.byFolder["INBOX"],
			imap.Message{UID: r.uid, Size: len(r.raw), Seen: r.seen})
		m.mbox.raws[r.uid] = []byte(r.raw)
	}
}

func newBodySearchHarness(t *testing.T) *imapHarness {
	t.Helper()
	m := newMockBackend()
	seedBodyCorpus(m)
	h := newIMAPHarness(t, m)
	h.login("l")
	h.selectInbox("s")
	return h
}

// BODY matches a substring in the body text only — not headers — and not ALL.
func TestIMAP_Search_Body(t *testing.T) {
	cases := []struct {
		query string
		want  []int
	}{
		{`BODY "beta-body"`, []int{1}},   // body of seq1
		{`BODY "delta-body"`, []int{2}},  // body of seq2
		{`BODY "BETA-BODY"`, []int{1}},   // case-insensitive
		{`BODY "alpha-subj"`, nil},       // a header term: must NOT match via BODY
		{`BODY "not-present"`, nil},      // absent everywhere
		{`BODY "-body"`, []int{1, 2, 3}}, // common body substring, all three
	}
	for i, tc := range cases {
		h := newBodySearchHarness(t)
		untagged, status := h.command("b"+strconv.Itoa(i), "SEARCH %s", tc.query)
		if !strings.Contains(status, " OK") {
			t.Errorf("SEARCH %s status = %q, want OK", tc.query, status)
			continue
		}
		seqs, _ := searchSeqs(untagged)
		if !reflect.DeepEqual(seqs, tc.want) {
			t.Errorf("SEARCH %s = %v, want %v", tc.query, seqs, tc.want)
		}
	}
}

// TEXT matches a substring in the header OR the body — and not ALL.
func TestIMAP_Search_Text(t *testing.T) {
	cases := []struct {
		query string
		want  []int
	}{
		{`TEXT "alpha-subj"`, []int{1}},   // header (Subject) of seq1
		{`TEXT "delta-body"`, []int{2}},   // body of seq2
		{`TEXT "epsilon-subj"`, []int{3}}, // header of seq3
		{`TEXT "quarterly"`, []int{1}},    // body of seq1
		{`TEXT "not-present"`, nil},       // absent everywhere
	}
	for i, tc := range cases {
		h := newBodySearchHarness(t)
		untagged, status := h.command("x"+strconv.Itoa(i), "SEARCH %s", tc.query)
		if !strings.Contains(status, " OK") {
			t.Errorf("SEARCH %s status = %q, want OK", tc.query, status)
			continue
		}
		seqs, _ := searchSeqs(untagged)
		if !reflect.DeepEqual(seqs, tc.want) {
			t.Errorf("SEARCH %s = %v, want %v", tc.query, seqs, tc.want)
		}
	}
}

// RECENT matches only the \Recent (here: unseen) messages, not ALL. seq1 is
// \Seen, so RECENT is seq2,seq3.
func TestIMAP_Search_Recent(t *testing.T) {
	h := newBodySearchHarness(t)
	untagged, status := h.command("r1", "SEARCH RECENT")
	if !strings.Contains(status, " OK") {
		t.Fatalf("SEARCH RECENT status = %q, want OK", status)
	}
	if seqs, _ := searchSeqs(untagged); !reflect.DeepEqual(seqs, []int{2, 3}) {
		t.Errorf("SEARCH RECENT = %v, want [2 3] (unseen), not ALL", seqs)
	}

	// AND with another key: RECENT SEEN is empty, since RECENT excludes \Seen.
	untagged, _ = h.command("r2", "SEARCH RECENT SEEN")
	if seqs, _ := searchSeqs(untagged); len(seqs) != 0 {
		t.Errorf("SEARCH RECENT SEEN = %v, want none (RECENT excludes \\Seen)", seqs)
	}
}

// UID SEARCH shares the criteria matcher, so BODY works there too and returns
// UIDs. Proves the raw-loading path is wired for both SEARCH and UID SEARCH.
func TestIMAP_UIDSearch_Body(t *testing.T) {
	h := newBodySearchHarness(t)
	untagged, status := h.command("u1", `UID SEARCH BODY "delta-body"`)
	if !strings.Contains(status, " OK") {
		t.Fatalf("UID SEARCH BODY status = %q, want OK", status)
	}
	if seqs, _ := searchSeqs(untagged); !reflect.DeepEqual(seqs, []int{20}) {
		t.Errorf("UID SEARCH BODY delta-body = %v, want [20]", seqs)
	}
}
