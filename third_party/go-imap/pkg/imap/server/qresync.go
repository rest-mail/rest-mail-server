package server

import (
	"strconv"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// QRESYNC (RFC 7162 §3.2). A Mailbox that implements [imap.ModSeqMailbox]
// and [imap.VanishedMailbox] offers it; once a client has sent ENABLE
// QRESYNC, expunges are reported with VANISHED and SELECT, EXAMINE and UID
// FETCH can resynchronise a cached mailbox.

// removeMessages takes the messages at the given sequence numbers out of the
// session's view and tells the client: one "* n EXPUNGE" each (RFC 3501
// §7.4.1), or, once QRESYNC is enabled, one "* VANISHED" listing their UIDs
// (RFC 7162 §3.2.10). seqs must be in descending order, so each EXPUNGE
// carries the number the message still holds.
func (s *Session) removeMessages(seqs []int) {
	var uids []uint32
	for _, seq := range seqs {
		if seq < 1 || seq > len(s.messages) {
			continue
		}
		uid := s.messages[seq-1].UID
		if s.qresync {
			uids = append(uids, uid)
		} else {
			s.send("* %d EXPUNGE", seq)
		}
		delete(s.deleted, uid)
		s.messages = append(s.messages[:seq-1], s.messages[seq:]...)
	}
	if len(uids) > 0 {
		s.send("* VANISHED %s", sequenceSet(sortedUnique(uids)))
	}
	if s.selected != nil {
		s.selected.total = int64(len(s.messages))
	}
}

// selectParams are the parameters of SELECT and EXAMINE (RFC 4466 §2.1)
// this server knows: CONDSTORE (RFC 7162 §3.1.8) and QRESYNC (§3.2.5).
type selectParams struct {
	condstore bool
	qresync   *qresyncParams
}

// qresyncParams is SELECT's QRESYNC parameter: the client's last known
// UIDVALIDITY and mod-sequence and, optionally, the UIDs it knows.
type qresyncParams struct {
	uidValidity uint32
	modSeq      uint64
	knownUIDs   string // a UID set, or "" for all
}

// parseSelectParams parses " (param ...)" after the mailbox name. ok is
// false when it is malformed or names a parameter the server does not know.
func parseSelectParams(rest string) (p selectParams, ok bool) {
	if rest == "" {
		return p, true
	}
	if !strings.HasPrefix(rest, " (") || !strings.HasSuffix(rest, ")") {
		return p, false
	}
	inner := rest[2 : len(rest)-1]
	if inner == "" {
		return p, false // at least one select-param (RFC 4466 §2.1)
	}
	for inner != "" {
		var name string
		name, inner, _ = strings.Cut(inner, " ")
		switch strings.ToUpper(name) {
		case "CONDSTORE":
			if p.condstore {
				return p, false
			}
			p.condstore = true
		case "QRESYNC":
			if p.qresync != nil || !strings.HasPrefix(inner, "(") {
				return p, false
			}
			end := matchingParen(inner)
			if end < 0 {
				return p, false
			}
			q, valid := parseQresync(inner[1:end])
			if !valid {
				return p, false
			}
			p.qresync = &q
			inner = inner[end+1:]
			if inner != "" {
				if inner[0] != ' ' {
					return p, false
				}
				inner = inner[1:]
				if inner == "" {
					return p, false
				}
			}
		default:
			return p, false
		}
	}
	return p, true
}

// matchingParen is the index of the ")" closing the "(" s starts with, or -1.
func matchingParen(s string) int {
	depth := 0
	for i := range len(s) {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// parseQresync parses the inside of QRESYNC's parentheses (RFC 7162 §7):
// uidvalidity SP mod-sequence-value [SP known-uids] [SP seq-match-data].
// seq-match-data is checked but not used: the store remembers every expunged
// UID, so it never has to guess.
func parseQresync(s string) (q qresyncParams, ok bool) {
	head, matchData, hasMatch := strings.Cut(s, " (")
	fields := strings.Split(head, " ")
	if len(fields) < 2 || len(fields) > 3 {
		return q, false
	}
	v, err := strconv.ParseUint(fields[0], 10, 32)
	if err != nil || v == 0 || fields[0][0] == '+' {
		return q, false
	}
	q.uidValidity = uint32(v)
	if q.modSeq, ok = parseModSeq(fields[1], true); !ok {
		return q, false
	}
	if len(fields) == 3 {
		if !validUIDSet(fields[2]) {
			return q, false
		}
		q.knownUIDs = fields[2]
	}
	if hasMatch {
		// seq-match-data = "(" known-sequence-set SP known-uid-set ")"
		if !strings.HasSuffix(matchData, ")") {
			return q, false
		}
		seqs, uids, found := strings.Cut(matchData[:len(matchData)-1], " ")
		if !found || !validUIDSet(seqs) || !validUIDSet(uids) {
			return q, false
		}
	}
	return q, true
}

// validUIDSet reports whether s is a sequence-set without "*", as
// known-uids and the seq-match-data sets are (RFC 7162 §7, sequence-set
// whose "*" makes no sense there; it is accepted as RFC 3501 allows it).
func validUIDSet(s string) bool {
	return s != "" && s != "$" && wire.ValidSequenceSet(s)
}

// resync answers SELECT's QRESYNC parameter (RFC 7162 §3.2.5.1) once the
// mailbox is open: "* VANISHED (EARLIER)" for the known UIDs expunged since
// the client's mod-sequence, then a FETCH with UID, FLAGS and MODSEQ for each
// known message changed since. A UIDVALIDITY that does not match, or a
// mailbox without mod-sequences, is answered as if nothing changed.
func (s *Session) resync(q *qresyncParams, uidNext uint32) {
	if s.noModSeq || q.uidValidity != s.resolveUIDValidity(s.selected.name) {
		return
	}
	known := q.knownUIDs
	if known == "" {
		if uidNext <= 1 {
			return // never held a message: no known UIDs (§3.2.5.1)
		}
		known = "1:" + strconv.FormatUint(uint64(uidNext-1), 10)
	}
	s.sendVanishedEarlier(known, q.modSeq, uidNext)
	ranges := wire.ParseUIDRanges(known, s.maxUID())
	for i, m := range s.messages {
		if m.ModSeq > q.modSeq && wire.UIDInRanges(m.UID, ranges) {
			s.send("* %d FETCH (UID %d FLAGS (%s) MODSEQ (%d))", i+1, m.UID, s.flagString(m), m.ModSeq)
		}
	}
}

// sendVanishedEarlier sends "* VANISHED (EARLIER)" with the UIDs in uidSet
// that left the selected mailbox at a mod-sequence above since (RFC 7162
// §§3.2.6, 3.2.10), when there are any. "*" in uidSet counts every UID the
// mailbox has handed out, expunged or not.
func (s *Session) sendVanishedEarlier(uidSet string, since uint64, uidNext uint32) {
	vs, ok := s.vanishedStore()
	if !ok {
		return
	}
	gone, err := vs.Vanished(s.selected.name, since)
	if err != nil || len(gone) == 0 {
		return
	}
	ranges := wire.ParseUIDRanges(uidSet, max(s.maxUID(), uidNext-1))
	var report []uint32
	for _, uid := range gone {
		if wire.UIDInRanges(uid, ranges) && s.uidToSeq(uid) == 0 {
			report = append(report, uid)
		}
	}
	if len(report) > 0 {
		s.send("* VANISHED (EARLIER) %s", sequenceSet(sortedUnique(report)))
	}
}
