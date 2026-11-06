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
// sequence numbers. Descending order keeps every reported sequence valid; a
// session that enabled QRESYNC is told with one VANISHED instead (RFC 7162
// §3.2.10). A
// refused delete leaves the message and its mark intact for a later retry;
// successful removals are still reported before the final NO (RFC 3501 §6.4.3,
// RFC 4315 §2.1). Backends need not support an atomic multi-message deletion.
func (s *Session) expungeMessages(tag, command string, targets map[int]bool) {
	marks, err := s.expungeMarks()
	if err != nil {
		slog.Warn("imap: reading deleted marks failed", "command", command, "error", err)
		s.tagged(tag, "NO", command+" failed")
		return
	}
	failed := false
	var removed []int
	for i := len(s.messages) - 1; i >= 0; i-- {
		msg := s.messages[i]
		if !marks[msg.UID] || (targets != nil && !targets[i+1]) {
			continue
		}
		if err := s.mailbox.Delete(msg.UID); err != nil {
			slog.Warn("imap: expunge failed", "command", command, "uid", msg.UID, "error", err)
			failed = true
			continue
		}
		removed = append(removed, i+1)
	}
	s.removeMessages(removed)

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
	marks, err := s.expungeMarks()
	if err != nil {
		slog.Warn("imap: reading deleted marks failed", "command", "CLOSE", "error", err)
		s.tagged(tag, "NO", "CLOSE failed")
		return false
	}
	var removed []int
	failed := false
	for i := len(s.messages) - 1; i >= 0; i-- {
		msg := s.messages[i]
		if !marks[msg.UID] {
			continue
		}
		if err := s.mailbox.Delete(msg.UID); err != nil {
			slog.Warn("imap: expunge failed", "command", "CLOSE", "uid", msg.UID, "error", err)
			failed = true
			continue
		}
		removed = append(removed, i+1)
	}
	if !failed {
		return true
	}
	// Descending order keeps every reported sequence number valid.
	s.removeMessages(removed)
	s.tagged(tag, "NO", "CLOSE failed")
	return false
}

// expungeMarks returns the UIDs EXPUNGE and CLOSE remove: the store's \Deleted
// marks as they are now when the mailbox keeps the flag ([imap.DeletedMailbox]),
// so a mark another session set or cleared counts (RFC 3501 §2.3.2), and
// otherwise the marks this session holds. Only messages in this session's view
// are ever removed, so a mark on one it has not been told about waits.
func (s *Session) expungeMarks() (map[uint32]bool, error) {
	store, ok := s.mailbox.(imap.DeletedMailbox)
	if !ok {
		return s.deleted, nil
	}
	uids, err := store.DeletedUIDs(s.selected.name)
	if err != nil {
		return nil, err
	}
	marks := make(map[uint32]bool, len(uids))
	for _, uid := range uids {
		marks[uid] = true
	}
	return marks, nil
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
