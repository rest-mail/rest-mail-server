package wire

import (
	"testing"
	"time"
)

func TestParseSequenceSet_Single(t *testing.T) {
	result := ParseSequenceSet("3", 10)
	if len(result) != 1 || result[0] != 3 {
		t.Errorf("got %v, want [3]", result)
	}
}

func TestParseSequenceSet_Range(t *testing.T) {
	result := ParseSequenceSet("2:5", 10)
	expected := []int{2, 3, 4, 5}
	assertIntSlice(t, result, expected)
}

func TestParseSequenceSet_StarInRange(t *testing.T) {
	result := ParseSequenceSet("3:*", 5)
	assertIntSlice(t, result, []int{3, 4, 5})
}

func TestParseSequenceSet_StarAlone(t *testing.T) {
	result := ParseSequenceSet("*", 7)
	if len(result) != 1 || result[0] != 7 {
		t.Errorf("got %v, want [7]", result)
	}
}

func TestParseSequenceSet_CommaSeparated(t *testing.T) {
	result := ParseSequenceSet("1,3,5", 10)
	assertIntSlice(t, result, []int{1, 3, 5})
}

func TestParseSequenceSet_MixedRangesAndSingles(t *testing.T) {
	result := ParseSequenceSet("1,3:5,7", 10)
	assertIntSlice(t, result, []int{1, 3, 4, 5, 7})
}

func TestParseSequenceSet_ReverseRange(t *testing.T) {
	result := ParseSequenceSet("5:2", 10)
	assertIntSlice(t, result, []int{2, 3, 4, 5})
}

func TestParseSequenceSet_ZeroTotal(t *testing.T) {
	if result := ParseSequenceSet("1:5", 0); result != nil {
		t.Errorf("got %v, want nil for zero total", result)
	}
}

func TestParseSequenceSet_OutOfRange(t *testing.T) {
	if result := ParseSequenceSet("15", 10); len(result) != 0 {
		t.Errorf("got %v, want empty for out of range", result)
	}
}

func TestParseSequenceSet_ZeroSeqNum(t *testing.T) {
	if result := ParseSequenceSet("0", 10); len(result) != 0 {
		t.Errorf("got %v, want empty for seq 0", result)
	}
}

func TestParseSequenceSet_NoDuplicates(t *testing.T) {
	if result := ParseSequenceSet("1,1,1", 10); len(result) != 1 {
		t.Errorf("got %v, want [1] (no duplicates)", result)
	}
}

func TestParseSequenceSet_OverlappingRanges(t *testing.T) {
	result := ParseSequenceSet("1:3,2:4", 10)
	assertIntSlice(t, result, []int{1, 2, 3, 4})
}

func TestParseSequenceSet_OneToStar(t *testing.T) {
	result := ParseSequenceSet("1:*", 3)
	assertIntSlice(t, result, []int{1, 2, 3})
}

func TestParseSequenceSet_EmptyString(t *testing.T) {
	if result := ParseSequenceSet("", 10); len(result) != 0 {
		t.Errorf("got %v, want empty for empty string", result)
	}
}

func assertIntSlice(t *testing.T, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("result[%d] = %d, want %d", i, got[i], v)
		}
	}
}

// ---------------------------------------------------------------------------
// Sequence/UID range DoS (issue #8): a set such as "1:4294967295" must NOT be
// expanded by iterating the literal declared span. These regression tests fail
// (time out) against unbounded expansion and pass once the work is bounded to
// the messages that can actually exist.
// ---------------------------------------------------------------------------

// A pathological range must not drive an O(span) loop. We use a span far larger
// than the issue's 1:4294967295 example so an unbounded loop blows past the
// timeout deterministically on any machine, while a clamped implementation
// returns instantly.
func TestParseSequenceSet_HugeRangeIsBounded(t *testing.T) {
	done := make(chan []int, 1)
	go func() {
		done <- ParseSequenceSet("1:4000000000000000000", 3)
	}()
	select {
	case result := <-done:
		// Only sequence numbers that can exist may be produced.
		assertIntSlice(t, result, []int{1, 2, 3})
	case <-time.After(2 * time.Second):
		t.Fatal("ParseSequenceSet did not return within 2s: unbounded range expansion (CPU DoS)")
	}
}

// The reverse form of the same pathological input (huge start clamps to total,
// so the range collapses to empty) must also be bounded.
func TestParseSequenceSet_HugeReverseRangeIsBounded(t *testing.T) {
	done := make(chan []int, 1)
	go func() {
		done <- ParseSequenceSet("4000000000000000000:1", 3)
	}()
	select {
	case result := <-done:
		assertIntSlice(t, result, []int{1, 2, 3})
	case <-time.After(2 * time.Second):
		t.Fatal("ParseSequenceSet did not return within 2s: unbounded range expansion (CPU DoS)")
	}
}

func TestParseUIDRanges_SingleAndRangeAndStar(t *testing.T) {
	// "100:*" with maxUID 42 resolves * to 42, then normalizes 100 > 42 to {42,100}.
	ranges := ParseUIDRanges("2,5:7,100:*", 42)
	want := []SeqRange{{2, 2}, {5, 7}, {42, 100}}
	if len(ranges) != len(want) {
		t.Fatalf("got %v, want %v", ranges, want)
	}
	for i, r := range ranges {
		if r != want[i] {
			t.Errorf("range[%d] = %v, want %v", i, r, want[i])
		}
	}
}

func TestParseUIDRanges_StarOnEmptyMailboxDropped(t *testing.T) {
	if ranges := ParseUIDRanges("*", 0); len(ranges) != 0 {
		t.Errorf("got %v, want empty ('*' unresolvable on empty mailbox)", ranges)
	}
	if ranges := ParseUIDRanges("1:*", 0); len(ranges) != 0 {
		t.Errorf("got %v, want empty ('*' unresolvable on empty mailbox)", ranges)
	}
}

func TestParseUIDRanges_OutOfUint32Dropped(t *testing.T) {
	// An endpoint beyond the 32-bit UID space cannot name a real message.
	if ranges := ParseUIDRanges("1:99999999999", 3); len(ranges) != 0 {
		t.Errorf("got %v, want empty (endpoint exceeds uint32)", ranges)
	}
}

func TestParseUIDRanges_InvalidTokenDropped(t *testing.T) {
	ranges := ParseUIDRanges("abc,3:5", 10)
	if len(ranges) != 1 || ranges[0] != (SeqRange{3, 5}) {
		t.Errorf("got %v, want [{3 5}]", ranges)
	}
}

func TestUIDInRanges(t *testing.T) {
	ranges := []SeqRange{{2, 2}, {5, 7}}
	cases := map[uint32]bool{1: false, 2: true, 3: false, 5: true, 6: true, 7: true, 8: false}
	for uid, want := range cases {
		if got := UIDInRanges(uid, ranges); got != want {
			t.Errorf("UIDInRanges(%d) = %v, want %v", uid, got, want)
		}
	}
	if UIDInRanges(5, nil) {
		t.Error("UIDInRanges against nil ranges should be false")
	}
}

func TestResolveSeqNum_Star(t *testing.T) {
	if result := resolveSeqNum("*", 10); result != 10 {
		t.Errorf("got %d, want 10", result)
	}
}

func TestResolveSeqNum_Number(t *testing.T) {
	if result := resolveSeqNum("5", 10); result != 5 {
		t.Errorf("got %d, want 5", result)
	}
}

func TestResolveSeqNum_Invalid(t *testing.T) {
	if result := resolveSeqNum("abc", 10); result != 0 {
		t.Errorf("got %d, want 0 for invalid input", result)
	}
}

func TestResolveSeqNum_Whitespace(t *testing.T) {
	if result := resolveSeqNum("  3  ", 10); result != 3 {
		t.Errorf("got %d, want 3 (trimmed)", result)
	}
}

func TestResolveSeqNum_StarWithWhitespace(t *testing.T) {
	if result := resolveSeqNum("  *  ", 10); result != 10 {
		t.Errorf("got %d, want 10 (trimmed star)", result)
	}
}
