package server

import (
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// literalArgumentAllowed checks a literal's source type before replacing it
// with an internal quoted string. That representation alone cannot distinguish
// a legal string from a date whose grammar permits quotes but not literals.
func literalArgumentAllowed(command, prefix string) bool {
	if wire.NoArguments[command] || command == "ENABLE" || command == "AUTHENTICATE" {
		return false
	}
	if command == "UID" {
		subcommand, rest, found := strings.Cut(prefix, " ")
		if !found || subcommand == "" {
			return false // the UID subcommand itself is an atom
		}
		if !strings.EqualFold(subcommand, "SEARCH") {
			return true // other UID grammars validate their materialized arguments
		}
		command, prefix = "SEARCH", rest
	}
	if command != "SEARCH" {
		return true
	}
	// Probe the real criterion parser with a valid, nonempty string token.
	// Only string-operand branches mark it accepted. Later missing criteria or
	// closing parentheses do not invalidate a legal literal at this position:
	// the client supplies those in the continuation after its literal data.
	_, query, ok := parseSearchReturn(prefix + `"literal-type-probe"`)
	if !ok {
		return false
	}
	tokens, ok := tokenizeSearch(query)
	if !ok || len(tokens) == 0 {
		return false
	}
	probe := &searchLiteralProbe{index: len(tokens) - 1}
	if strings.EqualFold(tokens[0], "CHARSET") {
		if probe.index == 1 {
			return true // RFC3501 SEARCH's charset is an astring
		}
		if len(tokens) < 2 {
			return false
		}
		tokens = tokens[2:]
		probe.index -= 2
	}
	for i := 0; i < len(tokens); {
		_, next, err := parseSearchCriterion(tokens, i, 0, probe)
		if err != nil {
			return probe.allowed
		}
		if next <= i {
			return false
		}
		i = next
	}
	return probe.allowed
}

type searchLiteralProbe struct {
	index   int
	allowed bool
}

func (p *searchLiteralProbe) stringOperand(indices ...int) {
	if p == nil {
		return
	}
	for _, index := range indices {
		if p.index == index {
			p.allowed = true
		}
	}
}

// mailboxArgumentCounts are the commands whose arguments are only mailbox
// astrings, each with how many it takes (RFC 3501 §9).
var mailboxArgumentCounts = map[string]int{
	"CREATE": 1, "DELETE": 1, "SUBSCRIBE": 1, "UNSUBSCRIBE": 1, "RENAME": 2,
}

// mailboxArguments consumes exactly count mailbox astrings (RFC 3501 §9).
// Validate the original tokens before decoding; ParseAstring alone is lenient
// about separators and escapes. Counted literals have already been materialized
// as quoted strings, so their spaces and quoted-specials remain name data.
func mailboxArguments(args string, count int) ([]string, bool) {
	names := make([]string, 0, count)
	for i := 0; i < count; i++ {
		name, rest, ok := wire.ParseAstring(args)
		if !ok || !wire.ValidAstring(args[:len(args)-len(rest)]) {
			return nil, false
		}
		names = append(names, name)
		if i == count-1 {
			return names, rest == ""
		}
		args, ok = wire.CutSingleSP(rest)
		if !ok {
			return nil, false
		}
	}
	return names, args == ""
}

// refuseMailboxArguments answers BAD for a mailbox-management command whose
// arguments are not exactly its mailbox astrings, so a malformed one never runs
// (RFC 3501 §2.2.2). A session that has not authenticated gets its NO from the
// command's own handler.
func (s *Session) refuseMailboxArguments(tag, command, args string) bool {
	count, ok := mailboxArgumentCounts[command]
	if !ok || !s.auth.authenticated {
		return false
	}
	if _, ok := mailboxArguments(args, count); ok {
		return false
	}
	reason := command + " requires one mailbox name"
	if count == 2 {
		reason = command + " requires old and new name"
	}
	s.tagged(tag, "BAD", reason)
	return true
}

// refuseLineEnding answers a command line that holds a CR or LF other than its
// CRLF terminator, as a bare LF ending does (RFC 3501 §§2.2, 2.2.2). The BAD is
// tagged when the line starts with a tag, since a client waits for that. It
// reports whether the session must end, because a line that ends in a
// non-synchronizing literal marker has its octets in flight (RFC 7888 §4).
func (s *Session) refuseLineEnding(line string) (closeConn bool) {
	const reason = "A command line ends in CRLF and holds no other CR or LF"
	if tag, _, found := strings.Cut(line, " "); found && wire.ValidTag(tag) {
		s.tagged(tag, "BAD", reason)
	} else {
		s.send("* BAD %s", reason)
	}
	if endsNonSyncLiteral([]byte(line)) {
		s.send("* BYE Command literal rejected; stream cannot be reused")
		return true
	}
	return false
}
