package wire

import (
	"strconv"
	"strings"
)

// ValidSequenceSet reports whether set matches the RFC 3501 §9 sequence-set
// grammar: comma-separated seq-number or seq-range items, where a seq-number is
// "*" or a nonzero 32-bit nz-number without leading zeros.
func ValidSequenceSet(set string) bool {
	if set == "" {
		return false
	}
	// "$", the saved search result, stands alone as a whole sequence-set
	// (RFC 5182 §3).
	if set == "$" {
		return true
	}
	for _, item := range strings.Split(set, ",") {
		bounds := strings.Split(item, ":")
		if len(bounds) > 2 {
			return false
		}
		for _, n := range bounds {
			if !validSeqNumber(n) {
				return false
			}
		}
	}
	return true
}

func validSeqNumber(n string) bool {
	if n == "*" {
		return true
	}
	if n == "" || n[0] < '1' || n[0] > '9' {
		return false
	}
	for i := 1; i < len(n); i++ {
		if n[i] < '0' || n[i] > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(n, 10, 32)
	return err == nil
}

// ParseSetMailbox splits COPY/MOVE arguments into a sequence-set and a
// destination mailbox (RFC 3501 §§6.4.7, 9; RFC 6851 §3.1). problem is empty
// on success, otherwise the reason for a BAD: a malformed set, a malformed
// astring, or text after the mailbox. Literal mailboxes arrive here already
// spliced in as quoted-strings by QuoteLiteral.
func ParseSetMailbox(args string) (set, mailbox, problem string) {
	set, rest, _ := strings.Cut(args, " ")
	if !ValidSequenceSet(set) {
		return "", "", "Invalid sequence set"
	}
	mailbox, after, ok := ParseAstring(rest)
	if !ok || !ValidAstring(rest[:len(rest)-len(after)]) {
		return "", "", "Invalid mailbox name"
	}
	if after != "" {
		return "", "", "Unexpected arguments after mailbox name"
	}
	return set, mailbox, ""
}

// ParseSequenceSet parses an IMAP sequence set like "1", "1:5", "1,3,5", "1:*".
func ParseSequenceSet(seqStr string, total int) []int {
	if total <= 0 {
		return nil
	}

	var result []int
	seen := make(map[int]bool)

	for _, part := range strings.Split(seqStr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if strings.Contains(part, ":") {
			// Range
			rangeParts := strings.SplitN(part, ":", 2)
			start := resolveSeqNum(rangeParts[0], total)
			end := resolveSeqNum(rangeParts[1], total)

			if start > end {
				start, end = end, start
			}
			// Clamp to [1, total] BEFORE iterating. A crafted set such as
			// "1:4294967295" against a 3-message mailbox would otherwise loop
			// ~4.3 billion times to yield the same three sequence numbers — an
			// authenticated CPU DoS (issue #8). Clamping bounds the loop to the
			// messages that can actually exist.
			if start < 1 {
				start = 1
			}
			if end > uint64(total) {
				end = uint64(total)
			}
			for i := start; i <= end; i++ {
				// Only snapshot-bounded values may be converted to int.
				n := int(i)
				if !seen[n] {
					result = append(result, n)
					seen[n] = true
				}
			}
		} else {
			// Single number
			n := resolveSeqNum(part, total)
			if n >= 1 && n <= uint64(total) && !seen[int(n)] {
				result = append(result, int(n))
				seen[int(n)] = true
			}
		}
	}

	return result
}

// resolveSeqNum resolves a sequence number, handling "*" as the total count.
// RFC 3501 numbers are unsigned 32-bit even when Go's int is signed 32-bit.
// Retain wider historical helper inputs too; callers validate wire syntax.
func resolveSeqNum(s string, total int) uint64 {
	s = strings.TrimSpace(s)
	if s == "*" {
		return uint64(total)
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// SeqRange is a normalized, inclusive [start, end] span from an IMAP sequence
// set. It records the bounds only — it is never expanded into individual
// numbers, so membership testing is O(1) regardless of how wide the declared
// span is.
type SeqRange struct {
	Start, End uint32
}

// ParseUIDRanges parses an IMAP UID set (e.g. "1,3:5,100:*") into normalized
// inclusive ranges WITHOUT expanding them. A pathological span such as
// "1:4294967295" therefore costs O(number of comma parts) rather than
// O(span) — the fix for the quadratic UID SEARCH DoS in issue #8, where the set
// was previously re-expanded once per message. "*" resolves to maxUID (the
// largest UID currently in the mailbox); a part whose "*" endpoint has no UID to
// resolve to (empty mailbox) or that fails to parse is dropped.
func ParseUIDRanges(setStr string, maxUID uint32) []SeqRange {
	var ranges []SeqRange
	for _, part := range strings.Split(setStr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, ":") {
			rangeParts := strings.SplitN(part, ":", 2)
			start, ok1 := parseUIDBound(rangeParts[0], maxUID)
			end, ok2 := parseUIDBound(rangeParts[1], maxUID)
			if !ok1 || !ok2 {
				continue
			}
			if start > end {
				start, end = end, start
			}
			ranges = append(ranges, SeqRange{Start: start, End: end})
		} else {
			v, ok := parseUIDBound(part, maxUID)
			if !ok {
				continue
			}
			ranges = append(ranges, SeqRange{Start: v, End: v})
		}
	}
	return ranges
}

// parseUIDBound resolves one endpoint of a UID range. "*" becomes maxUID (valid
// only when the mailbox holds at least one message); a non-numeric or
// out-of-uint32 token is rejected.
func parseUIDBound(s string, maxUID uint32) (uint32, bool) {
	s = strings.TrimSpace(s)
	if s == "*" {
		return maxUID, maxUID > 0
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// UIDInRanges reports whether uid falls within any of the given ranges. It is
// the membership test that replaces per-message range expansion.
func UIDInRanges(uid uint32, ranges []SeqRange) bool {
	for _, r := range ranges {
		if uid >= r.Start && uid <= r.End {
			return true
		}
	}
	return false
}
