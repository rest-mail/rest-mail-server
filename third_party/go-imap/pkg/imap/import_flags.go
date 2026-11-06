package imap

import (
	"fmt"
	"strings"
)

// MaxKeywordLength is the longest keyword the server accepts, in bytes.
const MaxKeywordLength = 255

// MaxKeywords is the most keywords one message may carry. A STORE or APPEND
// that would give a message more is refused, so a client cannot grow one
// message's flag list, and every FETCH FLAGS of it, without bound.
const MaxKeywords = 100

// ErrTooManyKeywords is the error NormalizeImportFlags returns for a flag list
// over [MaxKeywords] keywords.
var ErrTooManyKeywords = fmt.Errorf("at most %d keywords per message", MaxKeywords)

func NormalizeImportFlags(flags []string) ([]string, error) {
	result := make([]string, 0, len(flags))
	seen := make(map[string]bool)
	keywords := 0
	for _, flag := range flags {
		key := strings.ToLower(flag)
		switch key {
		case `\seen`:
			flag = `\Seen`
		case `\answered`:
			flag = `\Answered`
		case `\flagged`:
			flag = `\Flagged`
		case `\draft`:
			flag = `\Draft`
		case `\deleted`:
			flag = `\Deleted`
		default:
			if len(flag) == 0 || len(flag) > MaxKeywordLength {
				return nil, fmt.Errorf("keyword must contain 1 to %d ASCII bytes", MaxKeywordLength)
			}
			for _, character := range []byte(flag) {
				if character <= 32 || character >= 127 || strings.ContainsRune("(){%*\"\\]", rune(character)) {
					return nil, fmt.Errorf("invalid keyword: expected an RFC 3501 atom without backslash")
				}
			}
			if !seen[key] {
				keywords++
			}
			if keywords > MaxKeywords {
				return nil, ErrTooManyKeywords
			}
		}
		if !seen[key] {
			result = append(result, flag)
			seen[key] = true
		}
	}
	return result, nil
}
