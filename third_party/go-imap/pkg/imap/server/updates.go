package server

import "github.com/rest-mail/go-imap/pkg/imap"

// pollSelected re-reads the selected mailbox and reports what other sources
// changed since the session last looked (RFC 3501 §5.2): a server "MUST send
// mailbox size updates automatically if a mailbox size change is observed" and
// "SHOULD send message flag updates automatically". A failed read reports
// nothing and keeps the cached set. NOOP and CHECK call it; FETCH, STORE and
// SEARCH never do, since no EXPUNGE may be sent while they run (§7.4.1).
func (s *Session) pollSelected() {
	if s.selected == nil {
		return
	}
	messages, err := s.mailbox.Messages(s.selected.name)
	if err != nil {
		return
	}
	s.reconcile(messages)
}

// reconcile diffs a freshly-read message set against the cached one and pushes
// the untagged responses that report other sources' changes (RFC 3501 §§5.2,
// 7; RFC 2177 while idling): "* n EXPUNGE" for messages removed from the
// mailbox by another session, "* n EXISTS" for new arrivals, and
// "* n FETCH (FLAGS ...)" when a surviving message's flags changed. The pre-fix
// poll only ever announced growth, so an external expunge was never reported and
// the cached message set (which backs sequence-number resolution for every later
// command) went stale.
//
// During IDLE it runs in the poll goroutine, which owns s.writer, s.messages
// and s.selected for the duration of IDLE — the main goroutine only reads DONE
// and does not touch them until the poll goroutine has stopped. Otherwise NOOP
// and CHECK run it on the main goroutine. Either way these mutations need no
// further synchronisation.
func (s *Session) reconcile(messages []imap.Message) {
	if s.selected == nil {
		return
	}

	// Index the fresh set by UID for membership tests and flag comparison. UIDs
	// are stable and ascend with arrival order (see Message.UID), so new messages
	// only ever append after the existing ones.
	fresh := make(map[uint32]imap.Message, len(messages))
	for _, m := range messages {
		fresh[m.UID] = m
	}

	// 1) EXPUNGE removed messages. Walk the cached set highest-sequence-first so
	// each "* n EXPUNGE" carries the sequence number the message still holds at the
	// instant it is removed; lower sequence numbers are unaffected by the splice
	// (RFC 3501 §7.4.1 renumbering rule).
	// A session that enabled QRESYNC is told with one VANISHED instead (RFC
	// 7162 §3.2.10).
	var gone []int
	for i := len(s.messages) - 1; i >= 0; i-- {
		if _, ok := fresh[s.messages[i].UID]; !ok {
			gone = append(gone, i+1)
		}
	}
	s.removeMessages(gone)
	survivors := s.messages

	// 2) FLAGS changes on messages that survived. Their sequence numbers are the
	// post-expunge indices; arrivals reported below only append, so they do not
	// shift these.
	for _, message := range messages {
		if message.Deleted {
			s.deleted[message.UID] = true
		} else {
			delete(s.deleted, message.UID)
		}
	}
	// Once CONDSTORE is on, a FETCH also carries UID and MODSEQ, and a
	// changed mod-sequence is reported too (RFC 7162 §§3.1, 3.2.4).
	for i, old := range survivors {
		nm := fresh[old.UID]
		if buildFlags(nm) != buildFlags(old) || nm.Deleted != old.Deleted || (s.condstore && nm.ModSeq != old.ModSeq) {
			s.send("* %d FETCH (%s)", i+1, s.storeFetch(nm, true, false))
		}
	}

	// 3) EXISTS for new arrivals — any growth beyond the surviving count. An
	// expunge on its own never triggers EXISTS: the EXPUNGE responses already
	// convey the smaller mailbox.
	if len(messages) > len(survivors) {
		s.send("* %d EXISTS", len(messages))
		s.noteArrivals(messages)
	}

	// Replace the cache with the authoritative fresh set.
	s.announceKeywords(messages)
	s.messages = messages
	s.selected.total = int64(len(messages))
}

// noteArrivals adds the messages this session has just been told of to its
// \Recent set, and sends the new "* n RECENT" (RFC 3501 §7.3.2) when any of
// them is recent to it. A read-write session is the first told of an arrival
// unless another claimed it first, so it claims them; a read-only one only
// reads the mark (§2.3.2). When the store cannot answer, the arrivals are
// recent: "that message SHOULD be considered recent".
func (s *Session) noteArrivals(messages []imap.Message) {
	known := make(map[uint32]bool, len(s.messages))
	for _, m := range s.messages {
		known[m.UID] = true
	}
	var arrivals []imap.Message
	for _, m := range messages {
		if !known[m.UID] {
			arrivals = append(arrivals, m)
		}
	}
	recent, err := s.recentOf(s.selected.name, arrivals, !s.readOnly)
	if s.recent == nil {
		s.recent = make(map[uint32]bool)
	}
	added := false
	for _, m := range arrivals {
		if err != nil || recent[m.UID] {
			s.recent[m.UID] = true
			added = true
		}
	}
	// Arrivals another session already claimed leave the count as it was.
	if !added {
		return
	}
	count := 0
	for _, m := range messages {
		if s.recent[m.UID] {
			count++
		}
	}
	s.send("* %d RECENT", count)
}
