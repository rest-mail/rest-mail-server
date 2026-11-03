package server

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// seedSearchCorpus loads a small, deterministic corpus so SEARCH assertions can
// distinguish "matched the right subset" from "matched everything" (the issue's
// core defect). Sequence numbers map to UIDs 10/20/30.
//
//	seq1 UID10  anne@…  "quarterly report"  2025-01-10  \Seen
//	seq2 UID20  bob@…   "lunch plans"        2025-06-15  \Flagged
//	seq3 UID30  anne@…  "vacation photos"    2025-06-20
func seedSearchCorpus(m *mockBackend) {
	m.mbox.byFolder["INBOX"] = []imap.Message{
		{UID: 10, Size: 100, Seen: true, Subject: "quarterly report", From: imap.Address{Email: "anne@example.com"}, Date: time.Date(2025, 1, 10, 9, 0, 0, 0, time.UTC)},
		{UID: 20, Size: 100, Flagged: true, Subject: "lunch plans", From: imap.Address{Email: "bob@example.com"}, Date: time.Date(2025, 6, 15, 9, 0, 0, 0, time.UTC)},
		{UID: 30, Size: 100, Subject: "vacation photos", From: imap.Address{Email: "anne@example.com"}, Date: time.Date(2025, 6, 20, 9, 0, 0, 0, time.UTC)},
	}
}

// searchSeqs extracts the sequence numbers from a "* SEARCH ..." untagged line.
// The second return reports whether a "* SEARCH" line was present at all.
func searchSeqs(untagged []string) ([]int, bool) {
	for _, l := range untagged {
		if !strings.HasPrefix(l, "* SEARCH") {
			continue
		}
		var nums []int
		for _, f := range strings.Fields(strings.TrimPrefix(l, "* SEARCH")) {
			if n, err := strconv.Atoi(f); err == nil {
				nums = append(nums, n)
			}
		}
		return nums, true
	}
	return nil, false
}

func newSearchHarness(t *testing.T) *imapHarness {
	t.Helper()
	m := newMockBackend()
	seedSearchCorpus(m)
	h := newIMAPHarness(t, m)
	h.login("l")
	h.selectInbox("s")
	return h
}

// Sub-item 1: an unrecognized search key must be a tagged BAD, not match ALL.
// On the pre-fix code "FLURB" falls to the default arm and returns every
// message (* SEARCH 1 2 3, tagged OK) — a correctness/security defect.
func TestIMAP_Search_UnknownKeyIsBad(t *testing.T) {
	h := newSearchHarness(t)

	untagged, status := h.command("t1", "SEARCH FLURB")
	if seqs, ok := searchSeqs(untagged); ok {
		t.Errorf("unknown key produced a SEARCH result %v; want none (matched ALL is the bug)", seqs)
	}
	if !strings.Contains(status, " BAD") {
		t.Errorf("SEARCH FLURB status = %q, want a tagged BAD", status)
	}
}

// Sub-item 2: an unsupported CHARSET must be tagged NO [BADCHARSET ...], not
// silently ignored (which the old tokenizer did → matched ALL).
func TestIMAP_Search_UnsupportedCharsetIsBadCharset(t *testing.T) {
	h := newSearchHarness(t)

	untagged, status := h.command("t1", "SEARCH CHARSET ISO-8859-1 FROM \"anne\"")
	if seqs, ok := searchSeqs(untagged); ok {
		t.Errorf("unsupported CHARSET produced a SEARCH result %v; want none", seqs)
	}
	if !strings.Contains(status, " NO ") || !strings.Contains(status, "BADCHARSET") {
		t.Errorf("SEARCH CHARSET ISO-8859-1 status = %q, want NO [BADCHARSET ...]", status)
	}
}

// Sub-item 2 (accept path): US-ASCII and UTF-8 are supported and the search
// after the charset must be honoured normally.
func TestIMAP_Search_SupportedCharsetsAccepted(t *testing.T) {
	h := newSearchHarness(t)

	for i, cs := range []string{"US-ASCII", "UTF-8", "utf-8"} {
		tag := "c" + strconv.Itoa(i)
		untagged, status := h.command(tag, "SEARCH CHARSET %s FROM \"anne\"", cs)
		if !strings.Contains(status, " OK") {
			t.Errorf("CHARSET %s status = %q, want OK", cs, status)
		}
		seqs, _ := searchSeqs(untagged)
		if want := []int{1, 3}; !reflect.DeepEqual(seqs, want) {
			t.Errorf("CHARSET %s FROM anne = %v, want %v", cs, seqs, want)
		}
	}
}

// Sub-item 3: a parenthesized key group is the AND of the enclosed keys. anne's
// messages are seq1,seq3; the unseen ones are seq2,seq3; the AND is seq3 only.
// The old tokenizer split "(FROM"/"UNSEEN)" into unknown tokens that all matched
// ALL, so the whole group collapsed to every message (1 2 3). The first key must
// be load-bearing (FROM here narrows beyond UNSEEN) for the test to discriminate.
func TestIMAP_Search_ParenthesizedGroupIsAnd(t *testing.T) {
	h := newSearchHarness(t)

	untagged, status := h.command("t1", "SEARCH (FROM \"anne\" UNSEEN)")
	if !strings.Contains(status, " OK") {
		t.Fatalf("status = %q, want OK", status)
	}
	if seqs, _ := searchSeqs(untagged); !reflect.DeepEqual(seqs, []int{3}) {
		t.Errorf("SEARCH (FROM anne UNSEEN) = %v, want [3] (AND, not ALL)", seqs)
	}

	// A group as an operand of OR: (FROM bob) OR (SUBJECT report) = seq2 or seq1.
	untagged, status = h.command("t2", "SEARCH OR (FROM \"bob\") (SUBJECT \"report\")")
	if !strings.Contains(status, " OK") {
		t.Fatalf("OR-of-groups status = %q, want OK", status)
	}
	if seqs, _ := searchSeqs(untagged); !reflect.DeepEqual(seqs, []int{1, 2}) {
		t.Errorf("SEARCH OR (FROM bob) (SUBJECT report) = %v, want [1 2]", seqs)
	}
}

// Sub-item 4: a malformed date in SINCE/BEFORE/ON must be a tagged BAD. The old
// wire.ParseSearchDate returned the zero time, so SINCE <garbage> matched everything.
func TestIMAP_Search_MalformedDateIsBad(t *testing.T) {
	for _, key := range []string{"SINCE 99-Foo-2020", "BEFORE 32-Jan-2020", "ON not-a-date"} {
		h := newSearchHarness(t)
		untagged, status := h.command("t1", "SEARCH %s", key)
		if seqs, ok := searchSeqs(untagged); ok {
			t.Errorf("SEARCH %s produced a result %v; want none (matched ALL is the bug)", key, seqs)
		}
		if !strings.Contains(status, " BAD") {
			t.Errorf("SEARCH %s status = %q, want a tagged BAD", key, status)
		}
	}
}

// A bare message sequence set is a valid RFC 3501 search key. Making unknown
// keys BAD must not regress it into a BAD; it must match by sequence number and
// AND correctly with other keys.
func TestIMAP_Search_SequenceSetKey(t *testing.T) {
	h := newSearchHarness(t)

	untagged, status := h.command("t1", "SEARCH 1,3")
	if !strings.Contains(status, " OK") {
		t.Fatalf("SEARCH 1,3 status = %q, want OK", status)
	}
	if seqs, _ := searchSeqs(untagged); !reflect.DeepEqual(seqs, []int{1, 3}) {
		t.Errorf("SEARCH 1,3 = %v, want [1 3]", seqs)
	}

	untagged, status = h.command("t2", "SEARCH 1:2 FROM \"anne\"")
	if !strings.Contains(status, " OK") {
		t.Fatalf("SEARCH 1:2 FROM anne status = %q, want OK", status)
	}
	if seqs, _ := searchSeqs(untagged); !reflect.DeepEqual(seqs, []int{1}) {
		t.Errorf("SEARCH 1:2 FROM anne = %v, want [1]", seqs)
	}
}

// Regression guard: the standard keys must keep working unchanged.
func TestIMAP_Search_ValidCriteriaStillWork(t *testing.T) {
	cases := []struct {
		query string
		want  []int
	}{
		{"FROM \"anne\"", []int{1, 3}},
		{"SUBJECT \"report\"", []int{1}},
		{"SINCE 1-Jun-2025", []int{2, 3}},
		{"BEFORE 1-Jun-2025", []int{1}},
		{"SEEN", []int{1}},
		{"UNSEEN", []int{2, 3}},
		{"FLAGGED", []int{2}},
		{"NOT SEEN", []int{2, 3}},
		{"OR FROM \"bob\" SUBJECT \"report\"", []int{1, 2}},
		{"FROM \"anne\" UNSEEN", []int{3}},
	}
	for i, tc := range cases {
		h := newSearchHarness(t)
		untagged, status := h.command("v"+strconv.Itoa(i), "SEARCH %s", tc.query)
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

// UID SEARCH shares the parser: an unsupported CHARSET must fail there too.
func TestIMAP_UIDSearch_UnsupportedCharsetIsBadCharset(t *testing.T) {
	h := newSearchHarness(t)

	_, status := h.command("t1", "UID SEARCH CHARSET ISO-8859-1 FROM \"anne\"")
	if !strings.Contains(status, " NO ") || !strings.Contains(status, "BADCHARSET") {
		t.Errorf("UID SEARCH CHARSET ISO-8859-1 status = %q, want NO [BADCHARSET ...]", status)
	}
}
