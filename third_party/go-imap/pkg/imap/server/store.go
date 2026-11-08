package server

import (
	"errors"
	"log/slog"
	"slices"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

func (s *Session) handleStore(tag, args string) {
	if s.selected == nil {
		s.tagged(tag, "NO", "No mailbox selected")
		return
	}

	// Parse: STORE <seq> (FLAGS|+FLAGS|-FLAGS)[.SILENT] (<flags>)
	parts := strings.SplitN(args, " ", 3)
	if len(parts) < 3 {
		s.tagged(tag, "BAD", "STORE requires sequence, action, and flags")
		return
	}

	if !wire.ValidSequenceSet(parts[0]) {
		s.tagged(tag, "BAD", "Invalid sequence set")
		return
	}

	mode, silent, ok := parseStoreAction(parts[1])
	if !ok {
		s.tagged(tag, "BAD", "STORE requires FLAGS, +FLAGS or -FLAGS")
		return
	}

	if !wire.ValidStoreFlags(parts[2]) {
		s.tagged(tag, "BAD", "Invalid flag list")
		return
	}

	flags := wire.ParseFlags(parts[2])
	if namesRecent(flags) {
		s.tagged(tag, "BAD", recentRefusal)
		return
	}
	// After the syntax checks: a malformed STORE is BAD on any selection
	// (RFC 3501 §2.2.2).
	if s.refuseReadOnly(tag) {
		return
	}

	flags, ok = s.storeKeywords(tag, mode, flags)
	if !ok {
		return
	}

	seqNums := s.seqNumbers(parts[0])

	for _, seq := range seqNums {
		if seq < 1 || seq > len(s.messages) {
			continue
		}
		if err := s.applyStore(seq, mode, flags, silent, false); err != nil {
			// The error is the host's: private, and not known to fit response
			// text (RFC 3501 §9). UID STORE gives no more either. Only the
			// engine's own keyword limit is named, as [LIMIT].
			if !errors.Is(err, imap.ErrTooManyKeywords) {
				slog.Warn("imap: STORE failed", "error", err)
			}
			s.tagged(tag, "NO", storeRefusal(err, "STORE failed"))
			return
		}
	}

	s.tagged(tag, "OK", "STORE completed")
}

// storeKeywords is the flags a STORE applies once the store's keyword support
// is taken into account. A store that keeps no keywords ignores them (RFC 3501
// §6.4.6; PERMANENTFLAGS never offered \* for them). A STORE that would take
// the selected folder past the store's keyword limit is answered NO [LIMIT]
// (RFC 5530 §3) and ok is false.
func (s *Session) storeKeywords(tag string, mode storeMode, flags []string) (_ []string, ok bool) {
	if stores, _ := s.keywordStore(); !stores {
		return withoutKeywords(flags), true
	}
	if mode != storeRemove && s.overKeywordLimit(flags) {
		s.tagged(tag, "NO", keywordLimitRefusal)
		return nil, false
	}
	return flags, true
}

// storeRefusal is the NO text for a STORE the message refused: a message over
// [imap.MaxKeywords] keywords carries RFC 5530's LIMIT code; any other failure
// is other.
func storeRefusal(err error, other string) string {
	if errors.Is(err, imap.ErrTooManyKeywords) {
		return keywordLimitRefusal + ": " + err.Error()
	}
	return other
}

// recentRefusal is the response text for a STORE that names \Recent.
const recentRefusal = `STORE cannot name \Recent`

// namesRecent reports whether a STORE flag list names \Recent, which RFC 3501
// §9's flag production excludes: "\Recent can not be used as an argument in a
// STORE or APPEND command" (§2.3.2). Flag names are case-insensitive, and a
// keyword spelled Recent is a different flag because it has no backslash.
func namesRecent(flags []string) bool {
	return slices.ContainsFunc(flags, func(flag string) bool {
		return strings.EqualFold(flag, `\Recent`)
	})
}

// storeMode is the STORE operation selected by the data-item name: replace
// (bare FLAGS), add (+FLAGS) or remove (-FLAGS), per RFC 3501 §6.4.6.
type storeMode int

const (
	storeReplace storeMode = iota
	storeAdd
	storeRemove
)

// parseStoreAction parses a STORE flags data-item name such as "FLAGS",
// "+FLAGS" or "-FLAGS.SILENT" (case-insensitive). It returns the mode, whether
// the .SILENT suffix was present, and ok=false if the token is not a recognised
// FLAGS store item.
func parseStoreAction(action string) (mode storeMode, silent bool, ok bool) {
	a := strings.ToUpper(strings.TrimSpace(action))
	if strings.HasSuffix(a, ".SILENT") {
		silent = true
		a = strings.TrimSuffix(a, ".SILENT")
	}
	switch a {
	case "FLAGS":
		return storeReplace, silent, true
	case "+FLAGS":
		return storeAdd, silent, true
	case "-FLAGS":
		return storeRemove, silent, true
	default:
		return 0, false, false
	}
}

// applyStore mutates the message at the given sequence number per RFC 3501
// §6.4.6 and, unless silent, emits the untagged FETCH carrying the resulting
// flags. When withUID is true the FETCH also reports the UID (UID STORE). If
// the backend refuses the write, the message's session state is restored and
// the error returned, so the caller can answer NO (RFC 3501 §6.4.6).
func (s *Session) applyStore(seq int, mode storeMode, flags []string, silent, withUID bool) error {
	msg := &s.messages[seq-1]
	before, wasDeleted := *msg, s.deleted[msg.UID]
	var requestedKeywords []string
	for _, flag := range flags {
		if !strings.HasPrefix(flag, `\`) {
			requestedKeywords = append(requestedKeywords, flag)
		}
	}
	if _, err := imap.NormalizeImportFlags(requestedKeywords); err != nil {
		return err
	}

	update := s.applyStoreFlags(msg, mode, flags)
	if stores, _ := s.keywordStore(); !stores {
		// A store that keeps no keywords is never asked to change them, even
		// by a FLAGS replace that lists none.
		msg.Keywords = before.Keywords
		update.Keywords, update.KeywordsSet = nil, false
	}
	if update.KeywordsSet {
		normalized, err := imap.NormalizeImportFlags(update.Keywords)
		if err != nil {
			*msg = before
			s.applyDeleted(msg.UID, storeReplace, wasDeleted)
			return err
		}
		msg.Keywords, update.Keywords = normalized, normalized
	}

	if update.Seen != nil || update.Answered != nil || update.Flagged != nil || update.Draft != nil || update.Deleted != nil || update.KeywordsSet {
		if err := s.mailbox.Store(msg.UID, update); err != nil {
			*msg = before
			s.applyDeleted(msg.UID, storeReplace, wasDeleted)
			return err
		}
	}

	if silent {
		return nil
	}
	if withUID {
		s.send("* %d FETCH (UID %d FLAGS (%s))", seq, msg.UID, s.flagString(*msg))
	} else {
		s.send("* %d FETCH (FLAGS (%s))", seq, s.flagString(*msg))
	}
	return nil
}

// applyStoreFlags applies the flag change to msg (and the session's \Deleted
// set) and returns the FlagUpdate to persist. In replace mode every settable
// system flag is forced to its membership in flags; in add/remove mode only the
// listed flags are toggled. Keywords are validated and stored case-insensitively.
func (s *Session) applyStoreFlags(msg *imap.Message, mode storeMode, flags []string) imap.FlagUpdate {
	listed := func(name string) bool {
		for _, f := range flags {
			if strings.EqualFold(f, name) {
				return true
			}
		}
		return false
	}
	apply := func(name string, cur *bool, out **bool) {
		switch mode {
		case storeReplace:
			v := listed(name)
			*cur = v
			*out = boolPtr(v)
		case storeAdd:
			if listed(name) {
				*cur = true
				*out = boolPtr(true)
			}
		case storeRemove:
			if listed(name) {
				*cur = false
				*out = boolPtr(false)
			}
		}
	}

	var update imap.FlagUpdate
	keywords := make([]string, 0, len(msg.Keywords))
	if mode != storeReplace {
		keywords = append(keywords, msg.Keywords...)
	}
	for _, keyword := range flags {
		if strings.HasPrefix(keyword, `\`) {
			continue
		}
		found := false
		for _, current := range keywords {
			if strings.EqualFold(keyword, current) {
				found = true
			}
		}
		if mode != storeRemove && !found {
			keywords = append(keywords, keyword)
		}
	}
	if mode == storeRemove {
		kept := keywords[:0]
		for _, keyword := range keywords {
			if !listed(keyword) {
				kept = append(kept, keyword)
			}
		}
		keywords = kept
	}
	if !slices.Equal(msg.Keywords, keywords) {
		msg.Keywords = keywords
		update.Keywords = keywords
		update.KeywordsSet = true
	}

	apply(`\Seen`, &msg.Seen, &update.Seen)
	apply(`\Answered`, &msg.Answered, &update.Answered)
	apply(`\Flagged`, &msg.Flagged, &update.Flagged)
	apply(`\Draft`, &msg.Draft, &update.Draft)

	// Keep the existing EXPUNGE set in sync with the persistent flag.
	// SELECT seeds this same set from the backend's message metadata.
	s.applyDeleted(msg.UID, mode, listed(`\Deleted`))
	msg.Deleted = s.deleted[msg.UID]
	if mode == storeReplace || listed(`\Deleted`) {
		update.Deleted = boolPtr(s.deleted[msg.UID])
	}

	return update
}

// applyDeleted updates the session's \Deleted set for uid. In replace mode the
// flag is set to its membership in the STORE list; in add/remove mode it changes
// only when \Deleted appears in the list.
func (s *Session) applyDeleted(uid uint32, mode storeMode, listed bool) {
	var target bool
	switch mode {
	case storeReplace:
		target = listed
	case storeAdd:
		if !listed {
			return
		}
		target = true
	case storeRemove:
		if !listed {
			return
		}
		target = false
	}
	if target {
		if s.deleted == nil {
			s.deleted = make(map[uint32]bool)
		}
		s.deleted[uid] = true
	} else {
		delete(s.deleted, uid)
	}
}

// flagString renders msg's flags for a FETCH response, adding the session-local
// \Deleted state that buildFlags (which sees only the Message) cannot know.
func (s *Session) flagString(msg imap.Message) string {
	flags := buildFlags(msg)
	if s.deleted[msg.UID] {
		if flags == "" {
			return `\Deleted`
		}
		return flags + ` \Deleted`
	}
	return flags
}

func (s *Session) handleUIDStore(tag, args string) {
	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 2 {
		s.tagged(tag, "BAD", "UID STORE requires uid set and flags")
		return
	}

	if !wire.ValidSequenceSet(parts[0]) {
		s.tagged(tag, "BAD", "Invalid uid set")
		return
	}

	// Parse the flags data-item once (it is the same for every message in the set).
	flagParts := strings.SplitN(parts[1], " ", 2)
	if len(flagParts) < 2 {
		s.tagged(tag, "BAD", "UID STORE requires an action and flags")
		return
	}
	mode, silent, ok := parseStoreAction(flagParts[0])
	if !ok {
		s.tagged(tag, "BAD", "UID STORE requires FLAGS, +FLAGS or -FLAGS")
		return
	}
	if !wire.ValidStoreFlags(flagParts[1]) {
		s.tagged(tag, "BAD", "Invalid flag list")
		return
	}
	flags := wire.ParseFlags(flagParts[1])
	if namesRecent(flags) {
		s.tagged(tag, "BAD", recentRefusal)
		return
	}
	// After the syntax checks, as in handleStore.
	if s.refuseReadOnly(tag) {
		return
	}

	flags, ok = s.storeKeywords(tag, mode, flags)
	if !ok {
		return
	}

	seqNums := s.parseUIDSet(parts[0])
	for _, seq := range seqNums {
		if seq < 1 || seq > len(s.messages) {
			continue
		}
		if err := s.applyStore(seq, mode, flags, silent, true); err != nil {
			s.tagged(tag, "NO", storeRefusal(err, "UID STORE failed"))
			return
		}
	}

	s.tagged(tag, "OK", "UID STORE completed")
}
