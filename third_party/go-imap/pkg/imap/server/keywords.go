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
		s.send("* OK [PERMANENTFLAGS (%s \\*)] Limited", flags)
	}
}
