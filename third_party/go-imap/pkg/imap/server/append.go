package server

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/message"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// handleAppend returns true when the command stream cannot safely be reused.
func (s *Session) handleAppend(tag, args string) (closeConn bool) {
	// A refused non-synchronizing literal may already be buffered behind this
	// line. Until we commit to reading it, every refusal must end the session
	// rather than dispatching those octets as commands (RFC 7888 §§3–4).
	_, _, synchronizing, literal := wire.SplitTrailingLiteral(args)
	rejectNonSync := literal && !synchronizing
	bye := "APPEND rejected with unread non-synchronizing literal"
	defer func() {
		if rejectNonSync {
			s.send("* BYE %s", bye)
			closeConn = true
		}
	}()
	// tooBig refuses a non-synchronizing literal over LITERAL-'s limit: the
	// tagged BAD MUST carry TOOBIG on APPEND, and the BYE that ends the session,
	// since its octets are in flight, SHOULD (RFC 7888 §5).
	tooBig := func() {
		s.tagged(tag, "BAD", nonSyncTooBig)
		bye = nonSyncTooBig
	}

	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}
	// The mailbox name may be a UTF-8 quoted-string on a UTF8=ACCEPT server;
	// any other 8-bit octets in the typed line are BAD (RFC 6855 §3).
	utf8OK := s.utf8Accept()
	if reason := s.commandTextError(args); reason != "" {
		s.tagged(tag, "BAD", reason)
		return
	}

	// APPEND mailbox [flag-list] [date-time] literal  (RFC 3501 §6.3.11). The
	// mailbox is an astring: an atom (unquoted), a quoted-string, or a literal.
	// Parse it as an astring so an unquoted name is honoured rather than ignored,
	// which previously left folder at "INBOX" and misdelivered the message.
	// Arguments are separated by exactly one SP (RFC 3501 §9): a doubled, missing
	// or leading separator is BAD before the continuation, not skipped.
	if strings.HasPrefix(args, " ") {
		s.tagged(tag, "BAD", "Invalid argument separator")
		return
	}
	var folder, rest string
	var ok bool
	if prefix, size, sync, isLit := wire.SplitTrailingLiteral(args); isLit && prefix == "" {
		// A line that is only a literal marker carries the mailbox name as a
		// literal astring (RFC 3501 §§4.3, 9); the rest of the command, ending
		// in the message literal, follows its octets.
		if !sync && size > maxNonSyncLiteral {
			tooBig()
			return
		}
		if size > MaxCommandLineLength {
			s.tagged(tag, "BAD", "Invalid mailbox name")
			return
		}
		if sync {
			s.send("+ Ready for literal data")
		}
		name := make([]byte, size)
		if _, err := io.ReadFull(s.reader, name); err != nil {
			rejectNonSync = false
			return true
		}
		cont, tooLong, err := s.readLine()
		if err != nil {
			rejectNonSync = false
			return true
		}
		if tooLong {
			// readLine discarded the suffix, including the message literal marker.
			rejectNonSync = false
			s.tagged(tag, "BAD", fmt.Sprintf("Command line too long (limit %d bytes)", MaxCommandLineLength))
			s.send("* BYE Command literal rejected; stream cannot be reused")
			return true
		}
		_, _, synchronizing, literal = wire.SplitTrailingLiteral(cont)
		rejectNonSync = literal && !synchronizing
		if reason := s.commandTextError(cont); reason != "" {
			s.tagged(tag, "BAD", reason)
			return
		}
		// A literal is CHAR8, which excludes NUL (RFC 3501 §9).
		if bytes.IndexByte(name, 0) >= 0 {
			s.tagged(tag, "BAD", "Invalid mailbox name")
			return
		}
		folder, rest = string(name), cont
	} else {
		folder, rest, ok = wire.ParseAstring(args)
		token := args[:len(args)-len(rest)]
		// An unquoted mailbox uses ASTRING-CHAR, not arbitrary non-space
		// octets. In particular, controls, wildcards and quoted-specials
		// require a string; ']' remains legal in an unquoted astring.
		mailboxOK := wire.ValidAstring(token)
		if strings.HasPrefix(token, `"`) {
			mailboxOK = wire.ValidAppendQuotedMailbox(token, utf8OK)
		}
		if !ok || !mailboxOK {
			s.tagged(tag, "BAD", "Invalid mailbox name")
			return
		}
	}
	if rest, ok = wire.CutSingleSP(rest); !ok {
		s.tagged(tag, "BAD", "Invalid argument separator")
		return
	}
	// "INBOX" is case-insensitive (RFC 3501 §5.1): fold to canonical so an any-case
	// destination delivers to the one real INBOX, not a phantom "inbox" mailbox.
	folder, ok = s.folderName(folder)
	if !ok {
		s.tagged(tag, "NO", "No such mailbox")
		return false
	}

	// Optional flag-list: (\Seen \Draft ...) immediately after the mailbox. Taking
	// it positionally — rather than scanning for the first '(' — avoids matching a
	// parenthesis inside a quoted mailbox name.
	var appendFlags []string
	if strings.HasPrefix(rest, "(") {
		// No flag contains ')', so the first one closes the list. The list must be
		// well formed and followed by SP (RFC 3501 §9); a malformed list is BAD,
		// not an unsupported flag, and is refused before the continuation.
		end := strings.IndexByte(rest, ')')
		if end < 0 || !wire.ValidFlagList(rest[:end+1]) || !strings.HasPrefix(rest[end+1:], " ") {
			s.tagged(tag, "BAD", "Malformed flag list")
			return
		}
		appendFlags = wire.ParseFlags(rest[:end+1])
		if rest, ok = wire.CutSingleSP(rest[end+1:]); !ok {
			s.tagged(tag, "BAD", "Invalid argument separator")
			return
		}
	}

	// Translate the flags into a neutral FlagUpdate, rejecting any flag the store
	// cannot honour so the client is not misled into thinking it took effect. This
	// runs before the continuation: a rejected APPEND must not read the literal.
	normalized, flagErr := imap.NormalizeImportFlags(appendFlags)
	if flagErr != nil {
		s.tagged(tag, "NO", flagErr.Error())
		return
	}
	appendFlags = normalized
	var flags imap.FlagUpdate
	for _, flag := range appendFlags {
		switch strings.ToLower(flag) {
		case `\seen`:
			flags.Seen = boolPtr(true)
		case `\answered`:
			flags.Answered = boolPtr(true)
		case `\flagged`:
			flags.Flagged = boolPtr(true)
		case `\draft`:
			flags.Draft = boolPtr(true)
		case `\deleted`:
			flags.Deleted = boolPtr(true)
		default:
			flags.Keywords = append(flags.Keywords, flag)
		}
	}

	// Optional date-time: a quoted "dd-Mon-yyyy HH:MM:SS +ZZZZ" that sets the
	// message's INTERNALDATE. Parsed and validated here; it is stored only if the
	// backend implements DateAppender (otherwise it is dropped, best-effort).
	var internalDate time.Time
	var hasInternalDate bool
	if strings.HasPrefix(rest, `"`) {
		dateStr, after, dok := wire.ParseAstring(rest)
		// date-time is its own DQUOTE-delimited grammar, not a quoted-string:
		// backslash escapes must not turn malformed wire text into a valid date.
		if !dok || rest[:len(rest)-len(after)] != `"`+dateStr+`"` {
			s.tagged(tag, "BAD", "Malformed date-time")
			return
		}
		t, tok := wire.ParseDateTime(dateStr)
		if !tok {
			s.tagged(tag, "BAD", "Invalid date-time")
			return
		}
		internalDate = t
		hasInternalDate = true
		if rest, ok = wire.CutSingleSP(after); !ok {
			s.tagged(tag, "BAD", "Invalid argument separator")
			return
		}
	}

	// The terminal argument must be exactly one literal marker, with no
	// ignored arguments before it or suffix after it. Reuse the unsigned
	// literal grammar used by ordinary command arguments. A UTF8=ACCEPT server
	// also takes the message as RFC 6855 §4's utf8-literal, "UTF8 (" literal8
	// ")", whose literal8 is the same marker after a "~" (RFC 4466 §2.2).
	prefix, size, sync, isLiteral := wire.SplitTrailingLiteral(rest)
	// A literal8 message (RFC 3516 §4.4) may hold NUL, which the store cannot
	// keep (CHAR8 messages only), so it fails with UNKNOWN-CTE before any
	// octet is invited.
	if isLiteral && prefix == "~" {
		s.tagged(tag, "NO", "[UNKNOWN-CTE] Binary content cannot be stored")
		return
	}
	utf8Data := isLiteral && utf8OK && strings.EqualFold(prefix, "UTF8 (~")
	if !isLiteral || (prefix != "" && !utf8Data) {
		s.tagged(tag, "BAD", "Invalid message literal")
		return
	}
	if !sync && size > maxNonSyncLiteral {
		tooBig()
		return
	}
	if size > 10*1024*1024 {
		s.tagged(tag, "NO", "Message too large")
		return
	}

	// All early refusals are past. Reading the payload and command terminator
	// now establishes the next command boundary, even if the backend refuses it.
	rejectNonSync = false

	// A non-synchronizing {N+} already has its octets in flight and must not
	// receive a continuation (RFC 7888 §3). Synchronizing literals wait here.
	if sync {
		s.send("+ Ready for literal data")
	}

	data := make([]byte, size)
	if _, err := io.ReadFull(s.reader, data); err != nil {
		// Incomplete commands must never reach the backend. The stream cannot
		// be reused after a partial literal or timeout.
		return true
	}

	// The command's CRLF is separate from the literal's counted octets. Do
	// not discard arbitrary trailing text or ignore EOF: neither completes a
	// valid APPEND, and a buffered next command must remain untouched. A
	// utf8-literal closes its parenthesis first.
	want := "\r\n"
	if utf8Data {
		want = ")\r\n"
	}
	terminator := make([]byte, len(want))
	if _, err := io.ReadFull(s.reader, terminator); err != nil {
		return true
	}
	if string(terminator) != want {
		s.tagged(tag, "BAD", "Expected CRLF after message literal")
		return true
	}
	// RFC 3501 §9 literal payloads are CHAR8 (%x01-ff). After the counted
	// octets and terminator have been consumed, this refusal leaves the next
	// command boundary intact, including for a non-synchronizing literal.
	if bytes.IndexByte(data, 0) >= 0 {
		s.tagged(tag, "BAD", "NUL in message literal")
		return false
	}
	// Until the client has enabled UTF-8, a UTF8=ACCEPT server must refuse a
	// message whose header fields carry 8-bit octets (RFC 6855 §4).
	if utf8OK && !s.utf8 && message.HeaderHas8bit(data) {
		s.tagged(tag, "NO", "8-bit header fields need ENABLE UTF8=ACCEPT")
		return false
	}

	var err error
	// Deliver. Precedence: a client date-time with a DateAppender backend honours
	// INTERNALDATE; otherwise a UIDPlusMailbox reports the assigned UID; otherwise
	// the base Append. AppendWithDate and AppendUID both return the UID for the
	// APPENDUID (RFC 4315) response code.
	up, isUIDPlus := s.mailbox.(imap.UIDPlusMailbox)
	da, isDater := s.mailbox.(imap.DateAppender)
	var uid uint32
	switch {
	case hasInternalDate && isDater:
		uid, err = da.AppendWithDate(folder, flags, internalDate, data)
	case isUIDPlus:
		uid, err = up.AppendUID(folder, flags, data)
	default:
		err = s.mailbox.Append(folder, flags, data)
	}
	if err != nil {
		slog.Warn("imap: append failed", "error", err)
		s.tagged(tag, "NO", failText(err, "APPEND failed"))
		return
	}

	// RFC 3501 §6.3.11: when the message is appended to the mailbox this session
	// currently has selected, notify the session of the new message count with an
	// untagged EXISTS (and refresh the cached message set). Appends to any other
	// mailbox are invisible to the current selection. This runs before the tagged
	// completion — including the APPENDUID branch below — so the EXISTS precedes
	// the OK, as untagged responses must (RFC 3501 §7).
	s.notifyAppendToSelected(folder)

	// APPENDUID (RFC 4315) is emitted only for a UIDPLUS backend that reported a
	// real UID, whether the store handled the append via AppendUID or the
	// date-aware AppendWithDate.
	if isUIDPlus && uid != 0 {
		if v, ok := s.uidValidity(folder); ok {
			s.tagged(tag, "OK", fmt.Sprintf("[APPENDUID %d %d] APPEND completed", v, uid))
			return
		}
	}
	s.tagged(tag, "OK", "APPEND completed")
	return
}

// notifyAppendToSelected sends an untagged "* n EXISTS" (RFC 3501 §6.3.11) when a
// just-completed APPEND targeted the mailbox this session has selected, so the
// client learns a message arrived. It re-reads the mailbox so the cached message
// slice — which backs sequence-number resolution for later FETCH/STORE/SEARCH —
// reflects the new message rather than going stale. folder is already canonical
// (INBOX-folded), as is s.selected.name, so the comparison is exact. A read error
// leaves the prior cache and count in place; the next SELECT or IDLE poll
// reconciles.
func (s *Session) notifyAppendToSelected(folder string) {
	if s.selected == nil || s.selected.name != folder {
		return
	}
	messages, err := s.mailbox.Messages(folder)
	if err != nil {
		return
	}
	for _, message := range messages {
		if message.Deleted {
			s.deleted[message.UID] = true
		}
	}
	s.announceKeywords(messages)
	s.messages = messages
	s.selected.total = int64(len(messages))
	s.send("* %d EXISTS", len(messages))
}
