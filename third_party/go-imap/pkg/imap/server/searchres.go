package server

import "github.com/rest-mail/go-imap/pkg/imap/internal/wire"

// SEARCHRES (RFC 5182): SEARCH RETURN (SAVE) keeps its result in the search
// result variable, which "$" names wherever a sequence-set may stand.

// seqNumbers resolves a command's sequence-set to message numbers in the
// current view; "$" is the saved search result.
func (s *Session) seqNumbers(set string) []int {
	if set == "$" {
		return s.savedSeqNumbers()
	}
	return wire.ParseSequenceSet(set, len(s.messages))
}

// savedSeqNumbers are the message numbers of the saved result's messages
// still in the view, ascending. An empty result is "a valid, but non-matching
// list of messages" (RFC 5182 §2.1).
func (s *Session) savedSeqNumbers() []int {
	in := make(map[uint32]bool, len(s.saved))
	for _, uid := range s.saved {
		in[uid] = true
	}
	var seqs []int
	for i, msg := range s.messages {
		if in[msg.UID] {
			seqs = append(seqs, i+1)
		}
	}
	return seqs
}

// savedRanges is "$" as a search key, alone or after UID: the saved result's
// messages as UID ranges (RFC 5182 §2.2, example 3).
func (s *Session) savedRanges() []wire.SeqRange {
	ranges := make([]wire.SeqRange, 0, len(s.saved))
	for _, uid := range s.saved {
		ranges = append(ranges, wire.SeqRange{Start: uid, End: uid})
	}
	return ranges
}

// savedResult is what SAVE keeps from a search that found uids, ascending
// (RFC 5182 §2.4): with MIN and/or MAX and neither ALL nor COUNT, only those
// messages; otherwise every one found.
func savedResult(returns map[string]bool, uids []uint32) []uint32 {
	if returns["ALL"] || returns["COUNT"] || !returns["MIN"] && !returns["MAX"] || len(uids) == 0 {
		return append([]uint32(nil), uids...)
	}
	var saved []uint32
	if returns["MIN"] {
		saved = append(saved, uids[0])
	}
	if last := uids[len(uids)-1]; returns["MAX"] && (len(saved) == 0 || saved[0] != last) {
		saved = append(saved, last)
	}
	return saved
}
