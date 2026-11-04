package server

import (
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/message"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

type searchCriterion struct {
	kind string // "all", "seen", "unseen", "recent", "flagged", "unflagged", "deleted", "undeleted",
	// "answered", "unanswered", "draft", "undraft", "larger", "smaller",
	// "from", "to", "subject", "body", "text", "header", "since", "before", "on",
	// "sentsince", "sentbefore", "senton", "uid", "seqset", "not", "or", "and"
	size   uint64            // unsigned 32-bit SEARCH number, compared without narrowing to int
	field  string            // header: the header field name to search
	value  string            // for string/date/uid/seqset criteria
	date   time.Time         // parsed date for since/before/on
	sub    []searchCriterion // for NOT (1 element), OR (2 elements) or AND (a "(...)" group)
	ranges []wire.SeqRange   // uid/seqset: pre-parsed ranges, tested by membership (never expanded)
}

// searchError carries the tagged response that an unparseable SEARCH must
// produce instead of silently matching every message (RFC 3501 §6.4.4): status
// is "BAD" (malformed criteria) or "NO" (an unsupported CHARSET), and text is
// the response text — for BADCHARSET it begins with the "[BADCHARSET (...)]"
// response code.
type searchError struct {
	status string
	text   string
}

// badSearch builds a tagged-BAD searchError for a malformed search key.
func badSearch(text string) *searchError {
	return &searchError{status: "BAD", text: text}
}

func (s *Session) handleSearch(tag, args string) {
	if s.selected == nil {
		s.tagged(tag, "NO", "No mailbox selected")
		return
	}
	s.search(tag, args, false)
}

// search runs SEARCH or UID SEARCH: the matching sequence numbers, or UIDs, in
// an untagged SEARCH, or with RETURN result options in one untagged ESEARCH
// (RFC 4731 §3.1).
func (s *Session) search(tag, args string, uid bool) {
	returns, args, ok := parseSearchReturn(args)
	if !ok {
		s.tagged(tag, "BAD", "Invalid SEARCH RETURN options")
		return
	}
	criteria, serr := s.parseSearchCriteria(args)
	if serr != nil {
		// A SEARCH with SAVE that fails with NO empties the saved result; a BAD
		// leaves it as it was (RFC 5182 §2.1).
		if serr.status == "NO" && returns["SAVE"] {
			s.saved = nil
		}
		s.tagged(tag, serr.status, serr.text)
		return
	}

	var found, uids []uint32
	var modSeqs []uint64 // of each found message, when MODSEQ was a key
	withModSeq := usesModSeq(criteria)
	for i, msg := range s.messages {
		if s.matchesCriteria(msg, criteria, s.rawLoader(msg)) {
			uids = append(uids, msg.UID)
			modSeqs = append(modSeqs, msg.ModSeq)
			if uid {
				found = append(found, msg.UID)
			} else {
				found = append(found, uint32(i+1))
			}
		}
	}
	if returns["SAVE"] {
		s.saved = savedResult(returns, uids)
	}

	completed := "SEARCH completed"
	if uid {
		completed = "UID SEARCH completed"
	}
	if returns == nil {
		var b strings.Builder
		b.WriteString("* SEARCH")
		for _, n := range found {
			b.WriteString(" " + strconv.FormatUint(uint64(n), 10))
		}
		// With a MODSEQ key, a non-empty result ends with the highest
		// mod-sequence of the messages returned (RFC 7162 §3.1.6).
		if withModSeq && len(found) > 0 {
			b.WriteString(" (MODSEQ " + strconv.FormatUint(slices.Max(modSeqs), 10) + ")")
		}
		s.send("%s", b.String())
		s.tagged(tag, "OK", completed)
		return
	}
	if esearchWanted(returns) {
		response := esearchResponse(tag, uid, returns, found)
		if withModSeq && len(found) > 0 {
			response += " MODSEQ " + strconv.FormatUint(esearchModSeq(returns, modSeqs), 10)
		}
		s.send("%s", response)
	}
	s.tagged(tag, "OK", completed)
}

// searchCharsets are the character sets SEARCH accepts (RFC 3501 §6.4.4). Any
// other CHARSET is answered with a tagged NO [BADCHARSET (...)].
var searchCharsets = map[string]bool{"US-ASCII": true, "UTF-8": true}

// parseSearchCriteria tokenizes the IMAP SEARCH arguments and builds criteria.
// It returns a *searchError when the arguments are unparseable — an unknown key,
// a malformed date, unbalanced parentheses (BAD) or an unsupported CHARSET
// (NO [BADCHARSET ...]) — so the caller answers with a tagged failure instead of
// silently matching every message (RFC 3501 §6.4.4).
//
// Any UID set is parsed into ranges here — once, not once per message — so the
// per-message match is a cheap membership test (issue #8).
func (s *Session) parseSearchCriteria(args string) ([]searchCriterion, *searchError) {
	tokens, ok := tokenizeSearch(args)
	if !ok {
		return nil, badSearch("invalid quoted string in SEARCH")
	}

	// An optional leading "CHARSET <name>" selects the charset the string keys
	// are encoded in. Only US-ASCII and UTF-8 are supported; any other name is a
	// tagged NO [BADCHARSET (...)] rather than being ignored.
	if len(tokens) > 0 && strings.ToUpper(tokens[0]) == "CHARSET" {
		// Once UTF-8 is enabled every string is UTF-8, so a charset label
		// conflicts with it and is BAD (RFC 6855 §3).
		if s.utf8 {
			return nil, badSearch("CHARSET is not allowed after ENABLE UTF8=ACCEPT")
		}
		if len(tokens) < 2 {
			return nil, badSearch("CHARSET requires an argument")
		}
		name := wire.Unquote(tokens[1])
		if !wire.IsASCII(name) || !searchCharsets[strings.ToUpper(name)] {
			// A literal charset may contain arbitrary octets, including CR/LF.
			// Keep it out of response text and use ASCII-only name matching.
			return nil, &searchError{status: "NO", text: "[BADCHARSET (US-ASCII UTF-8)] unsupported charset"}
		}
		tokens = tokens[2:]
	}

	if len(tokens) == 0 {
		return nil, badSearch("SEARCH requires at least one search key")
	}

	maxUID := s.maxUID()
	var criteria []searchCriterion
	idx := 0
	for idx < len(tokens) {
		c, newIdx, err := parseSingleCriterion(tokens, idx, maxUID)
		if err != nil {
			return nil, err
		}
		criteria = append(criteria, c)
		idx = newIdx
	}

	// MODSEQ needs the selected mailbox's mod-sequences and turns CONDSTORE
	// on (RFC 7162 §§3.1, 3.1.2.2).
	if usesModSeq(criteria) {
		if !s.modSeqs() {
			return nil, badSearch("MODSEQ needs a mailbox with mod-sequences")
		}
		s.enableCondstore()
	}

	// Resolve any bare sequence-set keys against the current message list once,
	// mapping matched sequence numbers to their UIDs so the per-message test is
	// the same O(1) membership check UID sets use (never expanded — issue #8).
	s.resolveSeqSets(criteria)
	return criteria, nil
}

// resolveSeqSets walks the criteria tree and turns every "seqset" node's raw set
// string into UID ranges, one entry per matched message. Sequence numbers are
// resolved against the live message list (bounded by the mailbox size), so a
// pathological set such as "1:4294967295" costs O(messages), not O(span).
func (s *Session) resolveSeqSets(criteria []searchCriterion) {
	for i := range criteria {
		s.resolveSeqSetNode(&criteria[i])
	}
}

func (s *Session) resolveSeqSetNode(c *searchCriterion) {
	if c.kind == "seqset" && c.value == "$" {
		c.ranges = s.savedRanges()
	} else if c.kind == "seqset" {
		total := len(s.messages)
		for _, seq := range wire.ParseSequenceSet(c.value, total) {
			if seq >= 1 && seq <= total {
				uid := s.messages[seq-1].UID
				c.ranges = append(c.ranges, wire.SeqRange{Start: uid, End: uid})
			}
		}
	}
	for i := range c.sub {
		s.resolveSeqSetNode(&c.sub[i])
	}
}

// maxUID returns the largest UID among the currently selected messages, or 0
// when the mailbox is empty. It resolves "*" in a UID set to the real high-water
// mark instead of an arbitrary large constant.
func (s *Session) maxUID() uint32 {
	var max uint32
	for _, m := range s.messages {
		if m.UID > max {
			max = m.UID
		}
	}
	return max
}

// tokenizeSearch splits the search arguments into tokens, respecting quoted
// strings and emitting "(" and ")" as their own tokens so parenthesized key
// groups (RFC 3501 §6.4.4) parse correctly — "(FROM x)" yields ["(","FROM","x",")"]
// rather than the mis-split ["(FROM","x)"] that the parens-blind tokenizer produced.
func tokenizeSearch(args string) ([]string, bool) {
	var tokens []string
	i := 0
	for i < len(args) {
		// Skip whitespace
		for i < len(args) && args[i] == ' ' {
			i++
		}
		if i >= len(args) {
			break
		}
		switch args[i] {
		case '"':
			// Escaped quotes belong to the value, not the token boundary.
			end := wire.QuotedStringEnd(args[i:])
			if end < 0 {
				return nil, false
			}
			j := i + end + 1
			// readLiteralArgs also uses this representation for literal bytes;
			// wire.ValidAstring preserves their CR/LF and 8-bit content while
			// validating quote/escape structure and excluding NUL.
			if !wire.ValidAstring(args[i:j]) || (j < len(args) && args[j] != ' ' && args[j] != ')') {
				return nil, false
			}
			tokens = append(tokens, args[i:j])
			i = j
		case '(', ')':
			tokens = append(tokens, string(args[i]))
			i++
		default:
			// Unquoted token — runs to the next space or parenthesis.
			j := i
			for j < len(args) && args[j] != ' ' && args[j] != '(' && args[j] != ')' {
				j++
			}
			if strings.ContainsRune(args[i:j], '"') {
				return nil, false
			}
			tokens = append(tokens, args[i:j])
			i = j
		}
	}
	return tokens, true
}

// parseSingleCriterion parses one criterion from the token list starting at idx.
// maxUID resolves "*" in a UID set to the mailbox's high-water mark. It returns a
// *searchError for a malformed key (unknown keyword, missing argument, bad date
// or unbalanced parenthesis) rather than degrading to a match-ALL criterion.
func parseSingleCriterion(tokens []string, idx int, maxUID uint32) (searchCriterion, int, *searchError) {
	if idx >= len(tokens) {
		return searchCriterion{}, idx, badSearch("missing search key")
	}

	tok := tokens[idx]
	keyword := strings.ToUpper(tok)

	switch keyword {
	case "(":
		return parseSearchGroup(tokens, idx, maxUID)
	case ")":
		return searchCriterion{}, idx, badSearch("unbalanced parentheses in SEARCH")
	case "ALL":
		return searchCriterion{kind: "all"}, idx + 1, nil
	case "ANSWERED", "UNANSWERED", "DRAFT", "UNDRAFT":
		return searchCriterion{kind: strings.ToLower(keyword)}, idx + 1, nil
	case "SEEN":
		return searchCriterion{kind: "seen"}, idx + 1, nil
	case "UNSEEN":
		return searchCriterion{kind: "unseen"}, idx + 1, nil
	case "FLAGGED":
		return searchCriterion{kind: "flagged"}, idx + 1, nil
	case "UNFLAGGED":
		return searchCriterion{kind: "unflagged"}, idx + 1, nil
	case "DELETED":
		return searchCriterion{kind: "deleted"}, idx + 1, nil
	case "UNDELETED":
		return searchCriterion{kind: "undeleted"}, idx + 1, nil
	case "RECENT":
		// \Recent has no distinct model in this engine, which reports the unseen
		// set as RECENT in SELECT/EXAMINE and STATUS; SEARCH RECENT matches that
		// same set (RFC 3501 §6.4.4).
		return searchCriterion{kind: "recent"}, idx + 1, nil
	case "NEW":
		// NEW "is functionally equivalent to (RECENT UNSEEN)" and OLD to "NOT
		// RECENT" (RFC 3501 §6.4.4), so both follow whatever RECENT matches.
		recent, unseen := searchCriterion{kind: "recent"}, searchCriterion{kind: "unseen"}
		return searchCriterion{kind: "and", sub: []searchCriterion{recent, unseen}}, idx + 1, nil
	case "OLD":
		return searchCriterion{kind: "not", sub: []searchCriterion{{kind: "recent"}}}, idx + 1, nil
	case "KEYWORD", "UNKEYWORD":
		// KEYWORD and UNKEYWORD match stored keywords case-insensitively.
		// The operand must be a flag-keyword atom (RFC 3501 §6.4.4).
		if idx+1 >= len(tokens) || !wire.ValidFlagKeyword(tokens[idx+1]) {
			return searchCriterion{}, idx, badSearch(keyword + " requires a flag keyword")
		}
		return searchCriterion{kind: strings.ToLower(keyword), value: tokens[idx+1]}, idx + 2, nil
	case "FROM", "TO", "SUBJECT", "BODY", "TEXT":
		if idx+1 >= len(tokens) {
			return searchCriterion{}, idx, badSearch(keyword + " requires a string argument")
		}
		return searchCriterion{kind: strings.ToLower(keyword), value: wire.Unquote(tokens[idx+1])}, idx + 2, nil
	case "CC", "BCC":
		// CC/BCC search the message's own Cc/Bcc header fields, the source of
		// the envelope's cc and bcc members (RFC 3501 §§6.4.4, 7.4.2).
		if idx+1 >= len(tokens) {
			return searchCriterion{}, idx, badSearch(keyword + " requires a string argument")
		}
		return searchCriterion{kind: "header", field: keyword, value: wire.Unquote(tokens[idx+1])}, idx + 2, nil
	case "HEADER":
		if idx+2 >= len(tokens) {
			return searchCriterion{}, idx, badSearch("HEADER requires a field name and a string")
		}
		field := wire.Unquote(tokens[idx+1])
		if field == "" || field == "(" || field == ")" {
			return searchCriterion{}, idx, badSearch("HEADER requires a field name")
		}
		return searchCriterion{kind: "header", field: field, value: wire.Unquote(tokens[idx+2])}, idx + 3, nil
	case "MODSEQ":
		return parseModSeqCriterion(tokens, idx)
	case "LARGER", "SMALLER":
		if idx+1 >= len(tokens) {
			return searchCriterion{}, idx, badSearch(keyword + " requires a number")
		}
		// RFC 3501 §9 number is 1*DIGIT, unsigned 32-bit; ParseUint
		// rejects signs, quoted strings and overflow, but permits leading zeroes.
		n, err := strconv.ParseUint(tokens[idx+1], 10, 32)
		if err != nil {
			return searchCriterion{}, idx, badSearch("invalid " + keyword + " number")
		}
		return searchCriterion{kind: strings.ToLower(keyword), size: n}, idx + 2, nil
	case "SINCE", "BEFORE", "ON", "SENTSINCE", "SENTBEFORE", "SENTON":
		if idx+1 >= len(tokens) {
			return searchCriterion{}, idx, badSearch(keyword + " requires a date")
		}
		d, ok := wire.ParseSearchDate(tokens[idx+1])
		if !ok {
			return searchCriterion{}, idx, badSearch("invalid " + keyword + " date")
		}
		return searchCriterion{kind: strings.ToLower(keyword), value: tokens[idx+1], date: d}, idx + 2, nil
	case "UID":
		if idx+1 >= len(tokens) || !wire.ValidSequenceSet(tokens[idx+1]) {
			return searchCriterion{}, idx, badSearch("UID requires a valid sequence set")
		}
		// UID $ is the saved result, as $ alone is (RFC 5182 §2.2, example 3);
		// both resolve with the other sequence sets.
		if tokens[idx+1] == "$" {
			return searchCriterion{kind: "seqset", value: "$"}, idx + 2, nil
		}
		return searchCriterion{kind: "uid", value: tokens[idx+1], ranges: wire.ParseUIDRanges(tokens[idx+1], maxUID)}, idx + 2, nil
	case "NOT":
		if idx+1 >= len(tokens) {
			return searchCriterion{}, idx, badSearch("NOT requires a search key")
		}
		sub, newIdx, err := parseSingleCriterion(tokens, idx+1, maxUID)
		if err != nil {
			return searchCriterion{}, idx, err
		}
		return searchCriterion{kind: "not", sub: []searchCriterion{sub}}, newIdx, nil
	case "OR":
		sub1, newIdx1, err := parseSingleCriterion(tokens, idx+1, maxUID)
		if err != nil {
			return searchCriterion{}, idx, err
		}
		sub2, newIdx2, err := parseSingleCriterion(tokens, newIdx1, maxUID)
		if err != nil {
			return searchCriterion{}, idx, err
		}
		return searchCriterion{kind: "or", sub: []searchCriterion{sub1, sub2}}, newIdx2, nil
	default:
		// A bare message sequence set is a valid search key (RFC 3501 §6.4.4);
		// resolve it later against the message list. Anything else is unparseable
		// and must be a tagged BAD, not a silent match-ALL.
		if wire.ValidSequenceSet(tok) {
			return searchCriterion{kind: "seqset", value: tok}, idx + 1, nil
		}
		return searchCriterion{}, idx, badSearch("unknown search criterion")
	}
}

// parseModSeqCriterion parses RFC 7162 §3.1.5's search key, tokens[idx]
// being "MODSEQ": MODSEQ [entry-name entry-type-req] mod-sequence-valzer.
// The store keeps one mod-sequence per message, so the entry name and type
// are checked and then ignored, as the RFC requires.
func parseModSeqCriterion(tokens []string, idx int) (searchCriterion, int, *searchError) {
	next := idx + 1
	if next < len(tokens) && strings.HasPrefix(tokens[next], `"`) {
		if next+2 >= len(tokens) {
			return searchCriterion{}, idx, badSearch("MODSEQ requires a mod-sequence")
		}
		entry := wire.Unquote(tokens[next])
		if !strings.HasPrefix(entry, "/flags/") || len(entry) == len("/flags/") {
			return searchCriterion{}, idx, badSearch("invalid MODSEQ entry name")
		}
		switch strings.ToLower(tokens[next+1]) {
		case "priv", "shared", "all":
		default:
			return searchCriterion{}, idx, badSearch("invalid MODSEQ entry type")
		}
		next += 2
	}
	if next >= len(tokens) {
		return searchCriterion{}, idx, badSearch("MODSEQ requires a mod-sequence")
	}
	v, ok := parseModSeq(tokens[next], false)
	if !ok {
		return searchCriterion{}, idx, badSearch("invalid MODSEQ mod-sequence")
	}
	return searchCriterion{kind: "modseq", size: v}, next + 1, nil
}

// usesModSeq reports whether any criterion, however nested, is MODSEQ.
func usesModSeq(criteria []searchCriterion) bool {
	for _, c := range criteria {
		if c.kind == "modseq" || usesModSeq(c.sub) {
			return true
		}
	}
	return false
}

// parseSearchGroup parses a parenthesized key group "( key key ... )", which is
// the AND of the enclosed keys (RFC 3501 §6.4.4). tokens[idx] is the "(". A
// missing closing ")" is a tagged BAD.
func parseSearchGroup(tokens []string, idx int, maxUID uint32) (searchCriterion, int, *searchError) {
	var group []searchCriterion
	j := idx + 1
	for j < len(tokens) && tokens[j] != ")" {
		sub, newIdx, err := parseSingleCriterion(tokens, j, maxUID)
		if err != nil {
			return searchCriterion{}, idx, err
		}
		group = append(group, sub)
		j = newIdx
	}
	if j >= len(tokens) {
		return searchCriterion{}, idx, badSearch("unbalanced parentheses in SEARCH")
	}
	if len(group) == 0 {
		return searchCriterion{}, idx, badSearch("SEARCH group requires at least one search key")
	}
	return searchCriterion{kind: "and", sub: group}, j + 1, nil
}

// compareSearchDate applies a SINCE, BEFORE or ON comparison (kind is
// "since", "before" or "on") of t's calendar date, as written in t's own
// location, against the SEARCH date d.
func compareSearchDate(kind string, t, d time.Time) bool {
	year, month, day := t.Date()
	date := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	switch kind {
	case "since":
		return !date.Before(d)
	case "before":
		return date.Before(d)
	default: // on
		return date.Equal(d)
	}
}

// matchesCriteria reports whether msg satisfies every criterion (the criteria are
// ANDed). loadRaw lazily supplies msg's raw RFC 5322 bytes for the body/text keys;
// it is called at most once per message and only when such a key is present, so a
// metadata-only search never pulls a body.
func (s *Session) matchesCriteria(msg imap.Message, criteria []searchCriterion, loadRaw func() (string, bool)) bool {
	for _, c := range criteria {
		if !s.matchOne(msg, c, loadRaw) {
			return false
		}
	}
	return true
}

func (s *Session) matchOne(msg imap.Message, c searchCriterion, loadRaw func() (string, bool)) bool {
	switch c.kind {
	case "all":
		return true
	case "modseq":
		// "equal to or greater than" (RFC 7162 §3.1.5).
		return msg.ModSeq >= c.size
	case "answered":
		return msg.Answered
	case "unanswered":
		return !msg.Answered
	case "draft":
		return msg.Draft
	case "undraft":
		return !msg.Draft
	case "seen":
		return msg.Seen
	case "unseen":
		return !msg.Seen
	case "recent":
		// The engine models \Recent as the unseen set (see parseSingleCriterion),
		// matching the RECENT count reported by SELECT/EXAMINE and STATUS.
		return !msg.Seen
	case "keyword", "unkeyword":
		found := false
		for _, keyword := range msg.Keywords {
			if strings.EqualFold(keyword, c.value) {
				found = true
			}
		}
		if c.kind == "unkeyword" {
			return !found
		}
		return found
	case "flagged":
		return msg.Flagged
	case "unflagged":
		return !msg.Flagged
	case "deleted":
		return s.deleted[msg.UID]
	case "undeleted":
		return !s.deleted[msg.UID]
	case "larger", "smaller":
		// Size is the host's full RFC822.SIZE octet count, also used by
		// FETCH. Avoid narrowing the operand to int on 32-bit hosts.
		if c.kind == "larger" {
			return msg.Size >= 0 && uint64(msg.Size) > c.size
		}
		return msg.Size >= 0 && uint64(msg.Size) < c.size
	case "from":
		// FROM matches the envelope's FROM field (RFC 3501 §6.4.4): the
		// sender's display name as well as the address, as one mailbox.
		return strings.Contains(strings.ToLower(message.SearchMailbox(msg.From)), strings.ToLower(c.value))
	case "to":
		return strings.Contains(strings.ToLower(message.DecodeSearchHeader(msg.To)), strings.ToLower(c.value))
	case "subject":
		return strings.Contains(strings.ToLower(message.DecodeSearchHeader(msg.Subject)), strings.ToLower(c.value))
	case "body":
		// BODY <string>: case-insensitive substring in the message body (the text
		// after the header block) once transfer encodings and charsets are
		// decoded, RFC 3501 §6.4.4. Missing body ⇒ no match.
		raw, ok := loadRaw()
		if !ok {
			return false
		}
		return strings.Contains(strings.ToLower(message.SearchBody(raw)), strings.ToLower(c.value))
	case "text":
		// TEXT <string>: case-insensitive substring in the header OR the body —
		// i.e. anywhere in the decoded message (RFC 3501 §6.4.4). Missing raw ⇒ no match.
		raw, ok := loadRaw()
		if !ok {
			return false
		}
		return strings.Contains(strings.ToLower(message.SearchMessageText(raw)), strings.ToLower(c.value))
	case "header":
		// HEADER <field-name> <string> (and CC/BCC): a header with that field
		// name whose text after the colon contains the string, case-insensitive;
		// an empty string matches any message that has the field (RFC 3501
		// §6.4.4). Missing raw ⇒ no match.
		raw, ok := loadRaw()
		if !ok {
			return false
		}
		needle := strings.ToLower(c.value)
		for _, v := range message.HeaderFieldValues(raw, c.field) {
			if strings.Contains(strings.ToLower(message.DecodeSearchHeader(v)), needle) {
				return true
			}
		}
		return false
	case "since", "before", "on":
		// RFC 3501 §6.4.4 compares the internal calendar date, ignoring
		// both the time of day and timezone. Converting the instant to UTC
		// first would move messages near midnight onto the wrong day.
		return compareSearchDate(c.kind, msg.Date, c.date)
	case "sentsince", "sentbefore", "senton":
		// The SENT keys compare the Date: header's calendar date, again
		// disregarding time and timezone (RFC 3501 §6.4.4). A message with no
		// parseable Date: header has no sent date and matches none of them.
		raw, ok := loadRaw()
		if !ok {
			return false
		}
		values := message.HeaderFieldValues(raw, "Date")
		if len(values) == 0 {
			return false
		}
		sent, err := mail.ParseDate(strings.TrimSpace(values[0]))
		if err != nil {
			return false
		}
		return compareSearchDate(strings.TrimPrefix(c.kind, "sent"), sent, c.date)
	case "uid", "seqset":
		// Test membership against the pre-parsed ranges (built once in
		// parseSearchCriteria; a seqset is resolved to its messages' UIDs). Never
		// expand the set: a crafted "1:4294967295" must not iterate billions of
		// times per message (issue #8).
		return wire.UIDInRanges(msg.UID, c.ranges)
	case "not":
		if len(c.sub) > 0 {
			return !s.matchOne(msg, c.sub[0], loadRaw)
		}
		return true
	case "or":
		if len(c.sub) >= 2 {
			return s.matchOne(msg, c.sub[0], loadRaw) || s.matchOne(msg, c.sub[1], loadRaw)
		}
		return true
	case "and":
		// A parenthesized group matches when every enclosed key matches.
		for _, sub := range c.sub {
			if !s.matchOne(msg, sub, loadRaw) {
				return false
			}
		}
		return true
	default:
		return true // unknown criteria: don't filter
	}
}

func (s *Session) handleUIDSearch(tag, args string) {
	// UID SEARCH answers UIDs instead of sequence numbers.
	s.search(tag, args, true)
}
