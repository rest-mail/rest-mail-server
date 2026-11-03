package server

import (
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

func (s *Session) handleCopy(tag, args string) {
	if s.selected == nil {
		s.tagged(tag, "NO", "No mailbox selected")
		return
	}

	seqStr, dest, problem := wire.ParseSetMailbox(args)
	if problem != "" {
		s.tagged(tag, "BAD", problem)
		return
	}

	seqNums := s.seqNumbers(seqStr)

	code, err := s.copyMessages(seqNums, dest)
	if err != nil {
		s.tagged(tag, "NO", failText(err, "COPY failed"))
		return
	}
	if code != "" {
		s.tagged(tag, "OK", "["+code+"] COPY completed")
		return
	}
	s.tagged(tag, "OK", "COPY completed")
}

func (s *Session) handleMove(tag, args string) {
	if s.selected == nil {
		s.tagged(tag, "NO", "No mailbox selected")
		return
	}

	seqStr, dest, problem := wire.ParseSetMailbox(args)
	if problem != "" {
		s.tagged(tag, "BAD", problem)
		return
	}

	seqNums := s.seqNumbers(seqStr)
	s.moveMessages(tag, "MOVE", seqNums, dest)
}

func (s *Session) handleUIDCopy(tag, args string) {
	uidSetStr, dest, problem := wire.ParseSetMailbox(args)
	if problem != "" {
		s.tagged(tag, "BAD", problem)
		return
	}

	seqNums := s.parseUIDSet(uidSetStr)
	code, err := s.copyMessages(seqNums, dest)
	if err != nil {
		s.tagged(tag, "NO", failText(err, "UID COPY failed"))
		return
	}
	if code != "" {
		s.tagged(tag, "OK", "["+code+"] UID COPY completed")
		return
	}
	s.tagged(tag, "OK", "UID COPY completed")
}

func (s *Session) handleUIDMove(tag, args string) {
	uidSetStr, dest, problem := wire.ParseSetMailbox(args)
	if problem != "" {
		s.tagged(tag, "BAD", problem)
		return
	}

	seqNums := s.parseUIDSet(uidSetStr)
	s.moveMessages(tag, "UID MOVE", seqNums, dest)
}

// copyMessages copies the messages at the given sequence numbers into dest and
// returns a COPYUID response code body (without the surrounding brackets) when
// the mailbox implements [UIDPlusMailbox], or "" otherwise. It is shared by COPY
// and UID COPY. It stops at the first backend failure and returns no COPYUID
// on failure. The per-message backend API cannot roll back earlier copies.
func (s *Session) copyMessages(seqNums []int, dest string) (string, error) {
	// "INBOX" is case-insensitive (RFC 3501 §5.1): fold an any-case destination to
	// the one real INBOX. Shared by COPY and UID COPY.
	dest, ok := s.folderName(dest)
	if !ok {
		return "", errInvalidMailboxName
	}
	up, uidPlus := s.mailbox.(imap.UIDPlusMailbox)
	var srcUIDs, dstUIDs []uint32
	seen := make(map[int]bool)

	for _, seq := range seqNums {
		if seq < 1 || seq > len(s.messages) || seen[seq] {
			continue
		}
		seen[seq] = true
		msg := s.messages[seq-1]
		if uidPlus {
			newUID, err := up.CopyUID(msg.UID, dest)
			if err != nil {
				slog.Warn("imap: copy failed", "uid", msg.UID, "error", err)
				return "", err
			}
			srcUIDs = append(srcUIDs, msg.UID)
			dstUIDs = append(dstUIDs, newUID)
			continue
		}
		if err := s.mailbox.Copy(msg.UID, dest); err != nil {
			slog.Warn("imap: copy failed", "uid", msg.UID, "error", err)
			return "", err
		}
	}

	if uidPlus && len(dstUIDs) > 0 && validCopyUIDs(dstUIDs) {
		if v, ok := s.uidValidity(dest); ok {
			return "COPYUID " + strconv.FormatUint(uint64(v), 10) + " " +
				joinUIDs(srcUIDs) + " " + joinUIDs(dstUIDs), nil
		}
	}
	return "", nil
}

// failText is the tagged NO text for a failed APPEND, COPY or MOVE. A backend
// reporting [ErrNoSuchMailbox] gets the TRYCREATE response code, which tells the
// client to CREATE the destination and retry (RFC 3501 §§6.3.11, 6.4.7).
func failText(err error, text string) string {
	if errors.Is(err, imap.ErrNoSuchMailbox) {
		return "[TRYCREATE] " + text
	}
	return text
}

// validCopyUIDs reports whether the destination UIDs a backend returned can be
// quoted in a COPYUID response code: each must be a nonzero UID and none may
// repeat (RFC 4315 §§3, 4). [Mover.MoveUID] may return 0 when the backend cannot
// report the new UID; then COPYUID is omitted rather than sent malformed.
func validCopyUIDs(uids []uint32) bool {
	seen := make(map[uint32]bool, len(uids))
	for _, uid := range uids {
		if uid == 0 || seen[uid] {
			return false
		}
		seen[uid] = true
	}
	return true
}

// moveMessages relocates the messages at the given sequence numbers to dest and
// writes the full MOVE response: an untagged COPYUID resp-code first (RFC 6851,
// when the mailbox supports UIDPLUS and the atomic [Mover]), then an untagged
// EXPUNGE per moved message, then the tagged completion: NO if the backend
// refused a message, which stops the move there. It is shared by MOVE
// and UID MOVE. A backend that implements [Mover] moves atomically; otherwise
// the move falls back to [Mailbox.Move] per message.
func (s *Session) moveMessages(tag, cmdName string, seqNums []int, dest string) {
	// MOVE expunges the source messages, so it changes the mailbox's permanent
	// state and is refused on a read-only (EXAMINE) selection (RFC 3501 §6.3.2).
	if s.refuseReadOnly(tag) {
		return
	}
	// "INBOX" is case-insensitive (RFC 3501 §5.1): fold an any-case destination to
	// the one real INBOX. Shared by MOVE and UID MOVE.
	dest, ok := s.folderName(dest)
	if !ok {
		s.tagged(tag, "NO", "No such mailbox")
		return
	}
	mover, hasMover := s.mailbox.(imap.Mover)

	var srcUIDs, dstUIDs []uint32
	var doneSeq []int
	var moveErr error
	seen := make(map[int]bool)
	for _, seq := range seqNums {
		if seq < 1 || seq > len(s.messages) || seen[seq] {
			continue
		}
		seen[seq] = true
		msg := s.messages[seq-1]
		var newUID uint32
		var err error
		if hasMover {
			newUID, err = mover.MoveUID(msg.UID, dest)
		} else {
			err = s.mailbox.Move(msg.UID, dest)
		}
		if err != nil {
			// Stop at the first failure (RFC 6851 §3.3): the messages already
			// moved have left the source, so they are still expunged below.
			slog.Warn("imap: move failed", "uid", msg.UID, "error", err)
			moveErr = err
			break
		}
		srcUIDs = append(srcUIDs, msg.UID)
		dstUIDs = append(dstUIDs, newUID)
		doneSeq = append(doneSeq, seq)
	}

	// RFC 6851: send COPYUID in an untagged OK *before* the EXPUNGEs, so a client
	// never sees a message expunged before it learns the copy's UID.
	if hasMover && len(dstUIDs) > 0 && validCopyUIDs(dstUIDs) {
		if v, ok := s.uidValidity(dest); ok {
			s.send("* OK [COPYUID %d %s %s] Moved", v, joinUIDs(srcUIDs), joinUIDs(dstUIDs))
		}
	}

	// Expunge from the cached view in descending order so earlier sequence
	// numbers stay valid as later messages are removed.
	sort.Sort(sort.Reverse(sort.IntSlice(doneSeq)))
	for _, seq := range doneSeq {
		if seq < 1 || seq > len(s.messages) {
			continue
		}
		s.send("* %d EXPUNGE", seq)
		s.messages = append(s.messages[:seq-1], s.messages[seq:]...)
	}
	if s.selected != nil {
		s.selected.total = int64(len(s.messages))
	}
	if moveErr != nil {
		s.tagged(tag, "NO", failText(moveErr, cmdName+" failed"))
		return
	}
	s.tagged(tag, "OK", cmdName+" completed")
}

// joinUIDs renders a UID slice as a comma-separated IMAP sequence set, preserving
// order so the nth source UID lines up with the nth destination UID in COPYUID.
func joinUIDs(uids []uint32) string {
	parts := make([]string, len(uids))
	for i, u := range uids {
		parts[i] = strconv.FormatUint(uint64(u), 10)
	}
	return strings.Join(parts, ",")
}
