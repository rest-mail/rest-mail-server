package server

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// CONDSTORE (RFC 7162 §3.1). A Mailbox that implements [imap.ModSeqMailbox]
// keeps mod-sequences; the session turns CONDSTORE on at the first "CONDSTORE
// enabling command" (§3.1) and from then on puts MODSEQ in every untagged
// FETCH.

// modSeqStore returns the Mailbox as an [imap.ModSeqMailbox], when it is one.
func (s *Session) modSeqStore() (imap.ModSeqMailbox, bool) {
	ms, ok := s.mailbox.(imap.ModSeqMailbox)
	return ms, ok
}

// vanishedStore returns the Mailbox as an [imap.VanishedMailbox] when it also
// keeps mod-sequences, which is what QRESYNC needs (RFC 7162 §3.2).
func (s *Session) vanishedStore() (imap.VanishedMailbox, bool) {
	if _, ok := s.modSeqStore(); !ok {
		return nil, false
	}
	vs, ok := s.mailbox.(imap.VanishedMailbox)
	return vs, ok
}

// modSeqs reports whether MODSEQ may be asked of the selected mailbox: the
// store keeps mod-sequences and did not answer NOMODSEQ for it. A command
// that needs them otherwise is BAD (RFC 7162 §3.1.2.2).
func (s *Session) modSeqs() bool {
	_, ok := s.modSeqStore()
	return ok && !s.noModSeq
}

// enableCondstore records a CONDSTORE enabling command (RFC 7162 §3.1). The
// first one issued with a mailbox selected reports that mailbox's
// HIGHESTMODSEQ, unless SELECT answered NOMODSEQ.
func (s *Session) enableCondstore() {
	if s.condstore {
		return
	}
	s.condstore = true
	if s.selected == nil || s.noModSeq {
		return
	}
	if v, err := s.highestModSeq(s.selected.name); err == nil && v > 0 {
		s.send("* OK [HIGHESTMODSEQ %d] Highest", v)
	}
}

// highestModSeq is folder's HIGHESTMODSEQ: what the store reports, never
// below the largest mod-sequence of the messages this session holds for it.
// 0 means the folder keeps none.
func (s *Session) highestModSeq(folder string) (uint64, error) {
	ms, ok := s.modSeqStore()
	if !ok {
		return 0, nil
	}
	v, err := ms.HighestModSeq(folder)
	if err != nil || v == 0 {
		return v, err
	}
	if s.selected != nil && s.selected.name == folder {
		for _, m := range s.messages {
			v = max(v, m.ModSeq)
		}
	}
	return v, nil
}

// maxModSeq is the largest mod-sequence RFC 7162 allows: 63 bits (§7,
// mod-sequence-value).
const maxModSeq = 1<<63 - 1

// parseModSeq parses a mod-sequence-value (RFC 7162 §7): 1*DIGIT up to
// 2^63-1. With nonZero it is a mod-sequence-value proper; without, it is a
// mod-sequence-valzer, which may be 0.
func parseModSeq(s string, nonZero bool) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || v > maxModSeq || (nonZero && v == 0) {
		return 0, false
	}
	return v, true
}

// fetchModifiers holds a FETCH command's RFC 4466 modifiers: CHANGEDSINCE
// (RFC 7162 §3.1.4.1) and QRESYNC's VANISHED (§3.2.6).
type fetchModifiers struct {
	changedSince uint64 // 0 when absent
	vanished     bool
}

// splitFetchModifiers splits a FETCH's trailing "(CHANGEDSINCE n [VANISHED])"
// off its data items. ok is false for a malformed or unknown modifier; present
// is false when there is none.
func splitFetchModifiers(rest string) (items string, mods fetchModifiers, present, ok bool) {
	i := strings.LastIndex(rest, " (")
	if i < 0 || !strings.HasSuffix(rest, ")") {
		return rest, mods, false, true
	}
	inner := rest[i+2 : len(rest)-1]
	fields := strings.Split(inner, " ")
	if !strings.EqualFold(fields[0], "CHANGEDSINCE") && !strings.EqualFold(fields[0], "VANISHED") {
		// Not a modifier list: a parenthesized item such as
		// BODY[HEADER.FIELDS (A B)] ends the same way.
		return rest, mods, false, true
	}
	seen := map[string]bool{}
	for j := 0; j < len(fields); j++ {
		name := strings.ToUpper(fields[j])
		if seen[name] {
			return "", mods, true, false
		}
		seen[name] = true
		switch name {
		case "CHANGEDSINCE":
			if j+1 >= len(fields) {
				return "", mods, true, false
			}
			v, valid := parseModSeq(fields[j+1], true)
			if !valid {
				return "", mods, true, false
			}
			mods.changedSince = v
			j++
		case "VANISHED":
			mods.vanished = true
		default:
			return "", mods, true, false
		}
	}
	return rest[:i], mods, true, true
}

// splitStoreModifier splits a STORE's "(UNCHANGEDSINCE n)" modifier (RFC 7162
// §3.1.3) off the front of what follows the sequence set. set is false when
// there is none; ok is false for a malformed one.
func splitStoreModifier(rest string) (after string, since uint64, set, ok bool) {
	if !strings.HasPrefix(rest, "(") {
		return rest, 0, false, true
	}
	end := strings.IndexByte(rest, ')')
	if end < 0 || end+1 >= len(rest) || rest[end+1] != ' ' {
		return "", 0, true, false
	}
	fields := strings.Split(rest[1:end], " ")
	if len(fields) != 2 || !strings.EqualFold(fields[0], "UNCHANGEDSINCE") {
		return "", 0, true, false
	}
	v, valid := parseModSeq(fields[1], false)
	if !valid {
		return "", 0, true, false
	}
	return rest[end+2:], v, true, true
}

// storeCondition is a STORE's UNCHANGEDSINCE test, when it has one.
type storeCondition struct {
	set   bool
	since uint64
}

// storeFlags writes update for the message at seq, through StoreModSeq when
// the store keeps mod-sequences so the message's new MODSEQ is known. A
// conditional store that fails its test returns [imap.ErrModified].
func (s *Session) storeFlags(seq int, update imap.FlagUpdate, cond storeCondition) error {
	msg := &s.messages[seq-1]
	ms, ok := s.modSeqStore()
	if !ok {
		return s.mailbox.Store(msg.UID, update)
	}
	since := uint64(0)
	if cond.set {
		// "UNCHANGEDSINCE 0 always fails" (RFC 7162 §3.1.3): every message
		// has a positive mod-sequence.
		if cond.since == 0 {
			return imap.ErrModified
		}
		since = cond.since
	}
	modSeq, err := ms.StoreModSeq(msg.UID, update, since)
	if modSeq > 0 {
		msg.ModSeq = modSeq
	}
	return err
}

// modifiedResponse is the tagged OK of a conditional STORE some of whose
// messages failed the test (RFC 7162 §3.1.3): MODIFIED lists them, as
// sequence numbers or, for UID STORE, UIDs.
func modifiedResponse(failed []uint32) string {
	return "[MODIFIED " + sequenceSet(sortedUnique(failed)) + "] Conditional STORE failed"
}

// sortedUnique returns numbers ascending, each once.
func sortedUnique(numbers []uint32) []uint32 {
	out := slices.Clone(numbers)
	slices.Sort(out)
	return slices.Compact(out)
}

// isModified reports whether err is a failed UNCHANGEDSINCE test.
func isModified(err error) bool { return errors.Is(err, imap.ErrModified) }

// storeFetch is the inside of the untagged FETCH that reports msg's flags
// after a STORE, or a change made elsewhere: FLAGS (unless the STORE was
// .SILENT), and UID when the command was a UID one. Once CONDSTORE is on it
// always carries UID and MODSEQ (RFC 7162 §§3.1, 3.1.3, 3.2.4).
func (s *Session) storeFetch(msg imap.Message, flags, uid bool) string {
	var parts []string
	if uid || s.condstore {
		parts = append(parts, "UID "+strconv.FormatUint(uint64(msg.UID), 10))
	}
	if flags {
		parts = append(parts, "FLAGS ("+s.flagString(msg)+")")
	}
	if s.condstore && s.modSeqs() {
		parts = append(parts, "MODSEQ ("+strconv.FormatUint(msg.ModSeq, 10)+")")
	}
	return strings.Join(parts, " ")
}

// hasModSeqStore reports whether the store keeps mod-sequences, which is
// when CONDSTORE is advertised.
func (s *Session) hasModSeqStore() bool {
	_, ok := s.modSeqStore()
	return ok
}
