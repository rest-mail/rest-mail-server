package server

import (
	"fmt"
	"math"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// resolveUIDValidity picks the UIDVALIDITY reported for folder in SELECT/EXAMINE:
// the mailbox's real value when it implements [UIDPlusMailbox], else the legacy
// constant 1 (unchanged pre-UIDPLUS behaviour).
func (s *Session) resolveUIDValidity(folder string) uint32 {
	if v, ok := s.uidValidity(folder); ok {
		return v
	}
	return 1
}

// handleSelect implements SELECT (readOnly=false) and EXAMINE (readOnly=true).
// EXAMINE opens the mailbox read-only (RFC 3501 §6.3.2): the tagged OK carries
// [READ-ONLY] and s.readOnly is set so state-mutating commands are refused.
// deselect leaves the selected state, as a SELECT or EXAMINE does once its
// arguments parse and it attempts the new selection: "if a SELECT command that
// fails is attempted, no mailbox is selected" (RFC 3501 §6.3.1). Without it a
// later FETCH, STORE or SEARCH would keep operating on the previously selected
// mailbox. A command refused with BAD has attempted nothing, and "never changes
// the state of the connection or of the selected mailbox" (RFC 3501 §6). \Deleted
// marks are keyed by folder-scoped UIDs, so they must not carry over either
// (otherwise EXPUNGE could delete a same-numbered UID in the new mailbox).
func (s *Session) deselect() {
	s.selected = nil
	s.readOnly = false
	s.messages = nil
	s.saved = nil
	s.deleted = make(map[uint32]bool)
	s.keywordNames = make(map[string]string)
}

func (s *Session) handleSelect(tag, args string, readOnly bool) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	// SELECT and EXAMINE take exactly one mailbox astring (RFC 3501 §9).
	// Literal arguments were materialized by readLiteralArgs; retain their
	// decoded value, including significant spaces, instead of trimming it.
	folder, rest, ok := wire.ParseAstring(args)
	if !ok || rest != "" || !wire.ValidAstring(args) {
		s.tagged(tag, "BAD", "SELECT/EXAMINE requires one mailbox name")
		return
	}

	// A well-formed SELECT/EXAMINE begins a new selection (RFC 3501 §6.3.1/§6.3.2):
	// drop any prior selection now so that a failure below leaves no mailbox
	// selected.
	hadSelection := s.selected != nil
	s.deselect()
	// "When deselecting a selected mailbox, the server MUST return an untagged
	// OK response with the "[CLOSED]" response code when the currently selected
	// mailbox is closed" (RFC 9051 §6.3.2), before any response about the new
	// mailbox: CLOSED "serves as a boundary" between the two (RFC 9051 §7.1).
	// It is sent even when the new selection then fails, because the old one is
	// closed all the same. A rev1 session is not sent it; see closedCode.
	if hadSelection && s.closedCode() {
		s.send("* OK [CLOSED] Previous mailbox is now closed")
	}

	folder, ok = s.folderName(folder)
	if !ok {
		s.tagged(tag, "NO", "No such mailbox")
		return
	}

	// Fetch messages for this folder
	messages, err := s.mailbox.Messages(folder)
	if err != nil {
		s.tagged(tag, "NO", "Failed to select folder")
		return
	}

	s.readOnly = readOnly
	s.messages = messages
	for _, message := range messages {
		if message.Deleted {
			s.deleted[message.UID] = true
		}
	}
	total := int64(len(s.messages))
	var unread int64
	var firstUnseen int
	for i, m := range s.messages {
		if !m.Seen {
			unread++
			if firstUnseen == 0 {
				firstUnseen = i + 1
			}
		}
	}

	s.selected = &selectedMailbox{
		name:   folder,
		total:  total,
		unread: unread,
	}

	s.send("* %d EXISTS", total)
	s.send("* %d RECENT", unread)
	// Each response code below is followed by text: RFC 3501 §9's resp-text is
	// ["[" resp-text-code "]" SP] text, and text is one or more characters.
	// SELECT/EXAMINE UNSEEN is the first unseen sequence number, unlike
	// STATUS UNSEEN's count (RFC 3501 §§6.3.1, 6.3.10). Omit it when none
	// exists: zero is not a valid message sequence number.
	if firstUnseen != 0 {
		s.send("* OK [UNSEEN %d] Message %d is first unseen", firstUnseen, firstUnseen)
	}
	s.send("* OK [UIDVALIDITY %d] UIDs valid", s.resolveUIDValidity(folder))
	// UIDNEXT (RFC 3501 §2.3.1.1) is the UID that will be assigned to the next
	// message: it must be strictly greater than every existing UID, i.e.
	// highest-UID + 1 — not oldest-UID + 1. maxUID() returns 0 for an empty
	// mailbox, so this yields 1 there.
	s.send("* OK [UIDNEXT %d] Predicted next UID", s.maxUID()+1)
	available := s.availableFlags(messages)
	s.send("* FLAGS (%s)", available)
	if readOnly {
		// A read-only mailbox permits no permanent flag changes (RFC 3501 §6.3.2).
		s.send("* OK [PERMANENTFLAGS ()] No permanent flags permitted")
		s.tagged(tag, "OK", "[READ-ONLY] EXAMINE completed")
		return
	}
	// Keyword creation is required for imapsync to retain source keywords.
	s.send("* OK [PERMANENTFLAGS (%s \\*)] Limited", available)
	s.tagged(tag, "OK", "[READ-WRITE] SELECT completed")
}

func (s *Session) handleStatus(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	// Parse the complete RFC 3501 §9 STATUS grammar before touching the
	// backend. wire.ParseArgs is permissive and can discard trailing arguments.
	// Literal mailbox arguments have already been spliced as quoted strings.
	folder, rest, ok := wire.ParseAstring(args)
	if !ok || !wire.ValidAstring(args[:len(args)-len(rest)]) {
		s.tagged(tag, "BAD", "Invalid STATUS mailbox name")
		return
	}
	rest, ok = wire.CutSingleSP(rest)
	if !ok {
		s.tagged(tag, "BAD", "STATUS requires SP before the item list")
		return
	}
	items, ok := wire.ParseStatusItems(rest)
	if !ok {
		s.tagged(tag, "BAD", "STATUS requires a non-empty parenthesized item list")
		return
	}
	// Validate every requested item up front so an unknown one is a BAD syntax
	// error before any untagged response is emitted.
	for _, item := range items {
		if !statusItems[item] {
			s.tagged(tag, "BAD", "Unknown STATUS item: "+item)
			return
		}
	}

	// INBOX is case-insensitive; other names retain their spelling.
	folder, ok = s.folderName(folder)
	if !ok {
		s.tagged(tag, "NO", "No such mailbox")
		return
	}

	data, err := s.statusData(folder, items)
	if err != nil {
		s.tagged(tag, "NO", "Failed to get status")
		return
	}
	s.send(`* STATUS %s (%s)`, wire.EncodeMailboxName(s.wireName(folder)), data)
	s.tagged(tag, "OK", "STATUS completed")
}

// closedCode reports whether this session is sent the CLOSED response code.
// RFC 3501 has no CLOSED, and asks that a response code outside the protocol
// "SHOULD be prefixed with an "X" until they are added to a revision of this
// protocol" (§7.1). For an IMAP4rev1 client it comes from RFC 7162 §3.2.11,
// which ties it to the server that "advertises "QRESYNC" or "CONDSTORE""; this
// engine advertises neither. So only an IMAP4rev2 session gets it, and no
// command turns rev2 on yet: ENABLE IMAP4rev2 (RFC 9051 Appendix A) is
// rest-mail/rest-mail-server#718.
func (s *Session) closedCode() bool {
	return s.imap4rev2
}

// statusItems are the STATUS data items (RFC 3501 §6.3.10), with RFC 8438's
// SIZE and RFC 9051's DELETED.
var statusItems = map[string]bool{"MESSAGES": true, "RECENT": true, "UNSEEN": true, "UIDNEXT": true, "UIDVALIDITY": true, "SIZE": true, "DELETED": true}

// statusData is the inside of a STATUS response's parentheses for folder: the
// requested items, in the order asked, read fresh from the store so neither
// the selected mailbox nor any flag changes.
func (s *Session) statusData(folder string, items []string) (string, error) {
	messages, err := s.mailbox.Messages(folder)
	if err != nil {
		return "", err
	}

	total := len(messages)
	var unseen, deleted int
	var maxUID uint32
	var size int64
	sizeValid := true
	for _, m := range messages {
		if !m.Seen {
			unseen++
		}
		if m.Deleted {
			deleted++
		}
		if m.UID > maxUID {
			maxUID = m.UID
		}
		// RFC8438 SIZE is an unsigned 63-bit sum. Do not let invalid host
		// metadata produce a negative value or wrap below the real total.
		if m.Size < 0 || int64(m.Size) > math.MaxInt64-size {
			sizeValid = false
		} else {
			size += int64(m.Size)
		}
	}

	// Emit exactly the requested items, in the order the client asked for them.
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteByte(' ')
		}
		switch item {
		case "MESSAGES":
			fmt.Fprintf(&b, "MESSAGES %d", total)
		case "RECENT":
			// This engine models no \Recent flag; it reports the unseen count as
			// RECENT, consistent with the "* n RECENT" line SELECT/EXAMINE send.
			fmt.Fprintf(&b, "RECENT %d", unseen)
		case "UNSEEN":
			fmt.Fprintf(&b, "UNSEEN %d", unseen)
		case "UIDNEXT":
			// The UID the next message will be assigned: strictly greater than
			// every existing UID, i.e. highest-UID + 1 (RFC 3501 §2.3.1.1). An
			// empty mailbox has maxUID 0, yielding 1.
			fmt.Fprintf(&b, "UIDNEXT %d", maxUID+1)
		case "UIDVALIDITY":
			// The mailbox's real UIDVALIDITY when the backend supports UIDPLUS,
			// else the legacy constant 1 — matching SELECT/EXAMINE.
			fmt.Fprintf(&b, "UIDVALIDITY %d", s.resolveUIDValidity(folder))
		case "DELETED":
			// "The number of messages that have the \Deleted flag set" (RFC
			// 9051 §6.3.11). STORE writes \Deleted through to the backend, so
			// the stored flags are the count, selected mailbox or not.
			fmt.Fprintf(&b, "DELETED %d", deleted)
		case "SIZE":
			if !sizeValid {
				return "", fmt.Errorf("invalid mailbox message sizes")
			}
			// The sum of the messages' RFC822.SIZE, which FETCH gives as the
			// stored size (RFC 8438 §3).
			fmt.Fprintf(&b, "SIZE %d", size)
		}
	}
	return b.String(), nil
}

// uidValidity returns the UIDVALIDITY of folder when the mailbox implements
// [UIDPlusMailbox] and can report it. The bool is false for a base backend, a
// backend error, or a zero value — the caller then omits the UIDPLUS resp-code.
func (s *Session) uidValidity(folder string) (uint32, bool) {
	up, ok := s.mailbox.(imap.UIDPlusMailbox)
	if !ok {
		return 0, false
	}
	v, err := up.UIDValidity(folder)
	if err != nil || v == 0 {
		return 0, false
	}
	return v, true
}

// handleUnselect implements UNSELECT (RFC 3691): it returns the session to the
// authenticated state like CLOSE, but does not expunge \Deleted messages.
func (s *Session) handleUnselect(tag string) {
	if s.selected == nil {
		s.tagged(tag, "BAD", "No mailbox selected")
		return
	}
	s.selected = nil
	s.readOnly = false
	s.messages = nil
	s.saved = nil
	s.deleted = make(map[uint32]bool)
	s.keywordNames = make(map[string]string)
	s.tagged(tag, "OK", "UNSELECT completed")
}
