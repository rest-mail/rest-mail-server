package server

import (
	"log/slog"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

func (s *Session) handleExpunge(tag string) {
	if s.selected == nil {
		s.tagged(tag, "BAD", "EXPUNGE valid only in selected state")
		return
	}
	if s.refuseReadOnly(tag) {
		return
	}
	s.expungeMessages(tag, "EXPUNGE", nil)
}

// expungeMessages removes marked messages, optionally restricted to the given
// sequence numbers. Descending order keeps every reported sequence valid. A
// refused delete leaves the message and its mark intact for a later retry;
// successful removals are still reported before the final NO (RFC 3501 §6.4.3,
// RFC 4315 §2.1). Backends need not support an atomic multi-message deletion.
func (s *Session) expungeMessages(tag, command string, targets map[int]bool) {
	failed := false
	for i := len(s.messages) - 1; i >= 0; i-- {
		msg := s.messages[i]
		if !s.deleted[msg.UID] || (targets != nil && !targets[i+1]) {
			continue
		}
		if err := s.mailbox.Delete(msg.UID); err != nil {
			slog.Warn("imap: expunge failed", "command", command, "uid", msg.UID, "error", err)
			failed = true
			continue
		}
		s.send("* %d EXPUNGE", i+1)
		s.messages = append(s.messages[:i], s.messages[i+1:]...)
		delete(s.deleted, msg.UID)
	}

	s.selected.total = int64(len(s.messages))
	if failed {
		s.tagged(tag, "NO", command+" failed")
		return
	}
	s.tagged(tag, "OK", command+" completed")
}

// closeExpunge removes the marked messages for CLOSE and reports whether every
// removal succeeded. A successful CLOSE sends no untagged EXPUNGE (RFC 3501
// §6.4.2). A refused delete keeps the mailbox selected and answers NO: the
// removals that did succeed are then reported as EXPUNGE so the client's
// sequence numbers stay valid, and failed messages keep their marks for a retry,
// as with EXPUNGE (§6.4.3).
func (s *Session) closeExpunge(tag string) bool {
	var removed []int
	failed := false
	for i := len(s.messages) - 1; i >= 0; i-- {
		msg := s.messages[i]
		if !s.deleted[msg.UID] {
			continue
		}
		if err := s.mailbox.Delete(msg.UID); err != nil {
			slog.Warn("imap: expunge failed", "command", "CLOSE", "uid", msg.UID, "error", err)
			failed = true
			continue
		}
		removed = append(removed, i)
	}
	if !failed {
		return true
	}
	// Descending order keeps every reported sequence number valid.
	for _, i := range removed {
		s.send("* %d EXPUNGE", i+1)
		delete(s.deleted, s.messages[i].UID)
		s.messages = append(s.messages[:i], s.messages[i+1:]...)
	}
	s.selected.total = int64(len(s.messages))
	s.tagged(tag, "NO", "CLOSE failed")
	return false
}

// handleUIDExpunge implements UID EXPUNGE (RFC 4315 §2.1): it permanently removes
// only the messages that both carry the \Deleted flag and fall in the given UID
// set, leaving other \Deleted messages in place. It is only reachable when the
// mailbox implements [UIDPlusMailbox].
func (s *Session) handleUIDExpunge(tag, args string) {
	if s.refuseReadOnly(tag) {
		return
	}
	if _, ok := s.mailbox.(imap.UIDPlusMailbox); !ok {
		s.tagged(tag, "BAD", "UID EXPUNGE requires UIDPLUS")
		return
	}
	uidSetStr := strings.TrimSpace(args)
	if uidSetStr == "" {
		s.tagged(tag, "BAD", "UID EXPUNGE requires a uid set")
		return
	}
	if !wire.ValidSequenceSet(uidSetStr) {
		s.tagged(tag, "BAD", "Invalid uid set")
		return
	}

	targets := make(map[int]bool)
	for _, seq := range s.parseUIDSet(uidSetStr) {
		targets[seq] = true
	}

	s.expungeMessages(tag, "UID EXPUNGE", targets)
}
