package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/message"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

func (s *Session) handleFetch(tag, args string) {
	if s.selected == nil {
		s.tagged(tag, "NO", "No mailbox selected")
		return
	}

	// Parse sequence set and data items
	// Simplified: handle "FETCH <n> (FLAGS)" and "FETCH <n> (BODY[])" etc.
	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 2 {
		s.tagged(tag, "BAD", "FETCH requires sequence and data items")
		return
	}

	seqStr := parts[0]
	if !wire.ValidSequenceSet(seqStr) {
		s.tagged(tag, "BAD", "Invalid sequence set")
		return
	}
	tokens, mods, ok := s.fetchArgs(tag, parts[1], false)
	if !ok {
		return
	}

	// Parse sequence numbers
	seqNums := s.seqNumbers(seqStr)

	for _, seq := range seqNums {
		if seq < 1 || seq > len(s.messages) {
			continue
		}
		msg := s.messages[seq-1]
		if mods.changedSince > 0 && msg.ModSeq <= mods.changedSince {
			continue
		}

		if err := s.fetchResponse(seq, msg, tokens); err != nil {
			s.tagged(tag, "NO", fetchFailure(err, "FETCH failed"))
			return
		}
	}

	s.tagged(tag, "OK", "FETCH completed")
}

// fetchResponse composes and sends a single untagged FETCH response for msg,
// answering requested data items (RFC 3501 §7.4.2) plus UID and FLAGS. It loads
// all requested data and persists any implicit \Seen update before emitting the
// response, so a backend error cannot produce a successful-looking partial
// response or phantom flags. Read-only selections never set or echo new \Seen.
func (s *Session) fetchResponse(seq int, msg imap.Message, tokens []string) error {
	marksSeen := false
	// The message is loaded lazily: a metadata-only fetch (FLAGS, RFC822.SIZE, …)
	// must never pull the body — that is the entire point of answering RFC822.SIZE
	// from the stored size rather than the message content.
	var stored, sent string
	storedLoaded, storedOK, sentLoaded, sentOK := false, false, false, false
	loadStored := func() (string, bool) {
		if !storedLoaded {
			storedLoaded = true
			if b, err := s.mailbox.Fetch(msg.UID); err == nil {
				stored, storedOK = string(b), true
			}
		}
		return stored, storedOK
	}
	// loadSent is the message as this response sends it (RFC 3501 §7.4.2): every
	// section, partial offset and described size is taken from these octets. Only
	// BINARY reads the stored ones (RFC 3516). A NUL that conversion left behind,
	// in a header or in text, cannot be sent at all.
	loadSent := func() (string, bool) {
		if !sentLoaded {
			sentLoaded = true
			if raw, ok := loadStored(); ok {
				sent = message.WireForm(raw, s.utf8)
				sentOK = strings.IndexByte(sent, 0) < 0
			}
		}
		return sent, sentOK
	}

	// plain holds non-literal "name value" fragments; lits holds body-content
	// fragments emitted as IMAP literals after the plain items. The FLAGS slot is
	// a placeholder filled in after the item loop: a non-peek body fetch of a
	// previously-unseen message sets \Seen as a side effect, and the FLAGS echoed
	// in THIS response must reflect that new state (RFC 3501 §6.4.5) — otherwise
	// the client is never told the message became \Seen. FLAGS is always index 1.
	plain := []string{
		fmt.Sprintf("UID %d", msg.UID),
		"", // FLAGS — set after the loop, once marksSeen is known
	}
	// binary marks a BINARY section, sent as a literal8 when it holds a NUL
	// (RFC 3516 §4.3); otherwise it is a plain literal, as the RFC prefers.
	type litFrag struct {
		name, payload string
		binary        bool
	}
	var lits []litFrag

	for _, item := range tokens {
		switch up := strings.ToUpper(item); {
		case up == "FLAGS", up == "UID":
			// Always included above.
		case up == "RFC822.SIZE":
			plain = append(plain, fmt.Sprintf("RFC822.SIZE %d", msg.Size))
		case up == "INTERNALDATE":
			plain = append(plain, fmt.Sprintf("INTERNALDATE %q", msg.Date.Format("02-Jan-2006 15:04:05 -0700")))
		case up == "ENVELOPE":
			// ENVELOPE's recipient/reference fields and its date come from the
			// message's own headers (RFC 3501 §7.4.2), so load the message. This
			// is still metadata: loadSent never sets \Seen. A load failure is
			// reported before sending this message's response.
			r, ok := loadSent()
			plain = append(plain, "ENVELOPE "+message.BuildEnvelope(msg, r, ok, s.utf8))
		case up == "BODYSTRUCTURE":
			// Body structure is metadata (RFC 3501 §7.4.2): it must never set
			// \Seen. It needs the raw message to describe the MIME layout.
			if r, ok := loadSent(); ok {
				plain = append(plain, "BODYSTRUCTURE "+message.BuildBodyStructure(r, true, s.utf8))
			}
		case up == "BODY":
			// The bare BODY item is the non-extensible form of BODYSTRUCTURE
			// (no extension fields); likewise metadata, so no \Seen.
			if r, ok := loadSent(); ok {
				plain = append(plain, "BODY "+message.BuildBodyStructure(r, false, s.utf8))
			}
		case up == "RFC822":
			if r, ok := loadSent(); ok {
				lits = append(lits, litFrag{name: "RFC822", payload: r})
				marksSeen = true
			}
		case up == "RFC822.HEADER": // defined as BODY.PEEK[HEADER]: no \Seen.
			if r, ok := loadSent(); ok {
				lits = append(lits, litFrag{name: "RFC822.HEADER", payload: message.HeaderSection(r)})
			}
		case up == "RFC822.TEXT":
			if r, ok := loadSent(); ok {
				lits = append(lits, litFrag{name: "RFC822.TEXT", payload: message.TextSection(r)})
				marksSeen = true
			}
		case wire.BinaryItem(up):
			name, data, size, peek, ok, err := message.BinarySection(item, loadStored)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if size {
				plain = append(plain, fmt.Sprintf("%s %d", name, len(data)))
				continue
			}
			lits = append(lits, litFrag{name: name, payload: data, binary: true})
			if !peek {
				marksSeen = true
			}
		case strings.HasPrefix(up, "BODY[") || strings.HasPrefix(up, "BODY.PEEK["):
			name, payload, peek, ok := message.BodySection(item, loadSent)
			if !ok {
				continue
			}
			lits = append(lits, litFrag{name: name, payload: payload})
			if !peek {
				marksSeen = true
			}
		default:
			// Unknown / unsupported data item — ignore.
		}
	}

	if (storedLoaded && !storedOK) || (sentLoaded && !sentOK) {
		return errors.New("cannot load message data")
	}

	// RFC 3501 CHAR8 excludes NUL. MIME/encoded-word decoders can produce
	// one even when the raw message has none. Validate decoded metadata
	// before any Seen write or response bytes, including nested envelopes.
	// Body literals were already checked in loadSent, and BINARY sends a NUL as a
	// literal8.
	for _, field := range plain {
		if strings.IndexByte(field, 0) >= 0 {
			return errors.New("NUL in message metadata")
		}
	}

	// Only advertise a new \Seen after the backend accepted it. Earlier
	// messages in a multi-message FETCH may already have succeeded; an error
	// stops this command without rolling their completed writes back.
	if marksSeen && !msg.Seen && !s.readOnly {
		if err := s.storeFlags(seq, imap.FlagUpdate{Seen: boolPtr(true)}, storeCondition{}); err != nil {
			return err
		}
		msg.Seen = true
		s.messages[seq-1].Seen = true
	}
	plain[1] = fmt.Sprintf("FLAGS (%s)", s.flagString(msg))
	// Once CONDSTORE is on, every FETCH carries MODSEQ (RFC 7162 §3.1); a
	// FETCH that asks for MODSEQ or CHANGEDSINCE has turned it on.
	if s.condstore && s.modSeqs() {
		plain = append(plain, fmt.Sprintf("MODSEQ (%d)", s.messages[seq-1].ModSeq))
	}

	head := fmt.Sprintf("* %d FETCH (%s", seq, strings.Join(plain, " "))
	if len(lits) == 0 {
		s.writeString(head + ")\r\n")
		return nil
	}
	s.writeString(head)
	for _, lf := range lits {
		eight := ""
		if lf.binary && strings.IndexByte(lf.payload, 0) >= 0 {
			eight = "~"
		}
		s.writeString(fmt.Sprintf(" %s %s{%d}\r\n", lf.name, eight, len(lf.payload)))
		s.writeString(lf.payload)
	}
	s.writeString(")\r\n")
	return nil
}

// rawLoader returns a lazy, memoized loader of msg's raw RFC 5322 bytes for the
// SEARCH body/text keys. The backend Fetch runs at most once, and only when a
// body/text criterion actually calls it — so a metadata-only SEARCH never pulls a
// message body. A Fetch error yields ("", false), which those keys treat as no
// match.
func (s *Session) rawLoader(msg imap.Message) func() (string, bool) {
	var raw string
	loaded, ok := false, false
	return func() (string, bool) {
		if !loaded {
			loaded = true
			if b, err := s.mailbox.Fetch(msg.UID); err == nil {
				raw, ok = string(b), true
			}
		}
		return raw, ok
	}
}

func (s *Session) handleUIDFetch(tag, args string) {
	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 2 {
		s.tagged(tag, "BAD", "UID FETCH requires uid set and data items")
		return
	}

	uidSetStr := parts[0]
	if !wire.ValidSequenceSet(uidSetStr) {
		s.tagged(tag, "BAD", "Invalid uid set")
		return
	}
	tokens, mods, ok := s.fetchArgs(tag, parts[1], true)
	if !ok {
		return
	}

	// VANISHED (EARLIER) comes before any FETCH (RFC 7162 §3.2.6).
	if mods.vanished {
		uidNext, _ := s.uidNext(s.selected.name, s.messages)
		s.sendVanishedEarlier(uidSetStr, mods.changedSince, uidNext)
	}

	seqNums := s.parseUIDSet(uidSetStr)

	for _, seq := range seqNums {
		if seq < 1 || seq > len(s.messages) {
			continue
		}
		msg := s.messages[seq-1]
		if mods.changedSince > 0 && msg.ModSeq <= mods.changedSince {
			continue
		}

		if err := s.fetchResponse(seq, msg, tokens); err != nil {
			s.tagged(tag, "NO", fetchFailure(err, "UID FETCH failed"))
			return
		}
	}

	s.tagged(tag, "OK", "UID FETCH completed")
}

// buildFlags returns the IMAP flag string for a message.
func buildFlags(msg imap.Message) string {
	var flags []string
	if msg.Seen {
		flags = append(flags, `\Seen`)
	}
	if msg.Answered {
		flags = append(flags, `\Answered`)
	}
	if msg.Flagged {
		flags = append(flags, `\Flagged`)
	}
	if msg.Draft {
		flags = append(flags, `\Draft`)
	}
	flags = append(flags, msg.Keywords...)
	return strings.Join(flags, " ")
}

// fetchFailure is the text of FETCH's NO for err: a section whose encoding
// cannot be removed carries UNKNOWN-CTE (RFC 3516 §4.3).
func fetchFailure(err error, text string) string {
	if errors.Is(err, message.ErrUnknownCTE) {
		return "[UNKNOWN-CTE] Cannot remove the section's Content-Transfer-Encoding"
	}
	return text
}

// fetchArgs parses a FETCH's data items and modifiers, answering BAD (ok
// false) for any it cannot take. MODSEQ and CHANGEDSINCE need the selected
// mailbox's mod-sequences and turn CONDSTORE on; VANISHED is only for UID
// FETCH, with CHANGEDSINCE, once QRESYNC is enabled (RFC 7162 §§3.1.2.2,
// 3.1.4, 3.2.6).
func (s *Session) fetchArgs(tag, rest string, uid bool) ([]string, fetchModifiers, bool) {
	items, mods, present, ok := splitFetchModifiers(rest)
	if !ok || (present && !s.modSeqs()) {
		s.tagged(tag, "BAD", "Invalid FETCH modifier")
		return nil, mods, false
	}
	if mods.vanished && (!uid || !s.qresync || mods.changedSince == 0) {
		s.tagged(tag, "BAD", "VANISHED needs UID FETCH, CHANGEDSINCE and ENABLE QRESYNC")
		return nil, mods, false
	}
	tokens, ok := wire.ParseFetchItems(items)
	if !ok {
		s.tagged(tag, "BAD", "Invalid FETCH data items")
		return nil, mods, false
	}
	wantModSeq := false
	for _, t := range tokens {
		if strings.EqualFold(t, "MODSEQ") {
			wantModSeq = true
		}
	}
	if wantModSeq && !s.modSeqs() {
		s.tagged(tag, "BAD", "Invalid FETCH data items")
		return nil, mods, false
	}
	if wantModSeq || mods.changedSince > 0 {
		s.enableCondstore()
	}
	return tokens, mods, true
}
