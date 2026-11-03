package server

import (
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// ---------------------------------------------------------------------------
// Sequence/UID range DoS (issue #8): a set such as "1:4294967295" must NOT be
// expanded by iterating the literal declared span. These regression tests fail
// (time out) against unbounded expansion and pass once the work is bounded to
// the messages that can actually exist.
// ---------------------------------------------------------------------------

// UID SEARCH must not expand a giant UID range per message (the quadratic path
// in matchOne). It must complete quickly and still match every UID inside the
// range. 4294967295 is the issue's example and a valid 32-bit UID upper bound.
func TestUIDSearch_HugeRangeIsBounded(t *testing.T) {
	s := &Session{
		messages: []imap.Message{{UID: 1}, {UID: 2}, {UID: 3}},
		deleted:  map[uint32]bool{},
	}
	type result struct{ matched []uint32 }
	done := make(chan result, 1)
	go func() {
		criteria, _ := s.parseSearchCriteria("UID 1:4294967295")
		var r result
		for _, msg := range s.messages {
			if s.matchesCriteria(msg, criteria, func() (string, bool) { return "", false }) {
				r.matched = append(r.matched, msg.UID)
			}
		}
		done <- r
	}()
	select {
	case r := <-done:
		if len(r.matched) != 3 {
			t.Fatalf("UID 1:4294967295 matched %v, want all of [1 2 3]", r.matched)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("UID SEARCH did not return within 2s: per-message range expansion (CPU DoS)")
	}
}

// UID "*" must resolve to the mailbox high-water mark, so "UID *" matches only
// the highest-UID message. (The previous implementation matched nothing.)
func TestUIDSearch_StarResolvesToHighestUID(t *testing.T) {
	s := &Session{
		messages: []imap.Message{{UID: 10}, {UID: 20}, {UID: 30}},
		deleted:  map[uint32]bool{},
	}
	criteria, _ := s.parseSearchCriteria("UID *")
	var matched []uint32
	for _, msg := range s.messages {
		if s.matchesCriteria(msg, criteria, func() (string, bool) { return "", false }) {
			matched = append(matched, msg.UID)
		}
	}
	if len(matched) != 1 || matched[0] != 30 {
		t.Errorf("UID * matched %v, want [30]", matched)
	}
}
