package server

import (
	"sort"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
)

func (s *Session) availableFlags(messages []imap.Message) string {
	if s.keywordNames == nil {
		s.keywordNames = make(map[string]string)
	}
	for _, message := range messages {
		for _, keyword := range message.Keywords {
			key := strings.ToLower(keyword)
			if _, exists := s.keywordNames[key]; !exists {
				s.keywordNames[key] = keyword
			}
		}
	}
	names := make([]string, 0, len(s.keywordNames))
	for _, keyword := range s.keywordNames {
		names = append(names, keyword)
	}
	sort.Strings(names)
	return strings.Join(append([]string{`\Seen`, `\Answered`, `\Flagged`, `\Deleted`, `\Draft`}, names...), " ")
}

func (s *Session) announceKeywords(messages []imap.Message) {
	before := len(s.keywordNames)
	flags := s.availableFlags(messages)
	if len(s.keywordNames) == before {
		return
	}
	s.send("* FLAGS (%s)", flags)
	if !s.readOnly {
		s.send("* OK [PERMANENTFLAGS (%s)] Limited", s.permanentFlags(flags, messages))
	}
}

// keywordStore reports whether the mailbox keeps keywords
// ([imap.KeywordMailbox]) and the most distinct keywords it keeps in one
// folder, 0 for no limit.
func (s *Session) keywordStore() (stores bool, limit int) {
	store, ok := s.mailbox.(imap.KeywordMailbox)
	if !ok {
		return false, 0
	}
	return true, max(store.KeywordLimit(), 0)
}

// folderKeywords is the set of keywords messages carry, compared without case.
func folderKeywords(messages []imap.Message) map[string]bool {
	set := make(map[string]bool)
	for _, message := range messages {
		for _, keyword := range message.Keywords {
			set[strings.ToLower(keyword)] = true
		}
	}
	return set
}

// permanentFlags is the PERMANENTFLAGS list for a folder whose messages are
// messages and whose FLAGS are available: \* is offered only when the store
// keeps keywords and the folder is under the store's keyword limit (RFC 3501
// §7.1, RFC 9051 §7.1).
func (s *Session) permanentFlags(available string, messages []imap.Message) string {
	stores, limit := s.keywordStore()
	if !stores || (limit > 0 && len(folderKeywords(messages)) >= limit) {
		return available
	}
	return available + ` \*`
}

// overKeywordLimit reports whether setting keywords on the selected folder's
// messages would take the folder past the store's keyword limit.
func (s *Session) overKeywordLimit(keywords []string) bool {
	stores, limit := s.keywordStore()
	if !stores || limit == 0 {
		return false
	}
	known := folderKeywords(s.messages)
	for _, keyword := range keywords {
		if !strings.HasPrefix(keyword, `\`) {
			known[strings.ToLower(keyword)] = true
		}
	}
	return len(known) > limit
}

// keywordLimitRefusal is the NO text for a STORE or APPEND over a keyword
// limit, with RFC 5530 §3's LIMIT response code.
const keywordLimitRefusal = "[LIMIT] Too many keywords"

// withoutKeywords is flags with every keyword left out, for a store that does
// not keep them.
func withoutKeywords(flags []string) []string {
	kept := make([]string, 0, len(flags))
	for _, flag := range flags {
		if strings.HasPrefix(flag, `\`) {
			kept = append(kept, flag)
		}
	}
	return kept
}
