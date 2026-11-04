package mime

import (
	"strings"

	"github.com/emersion/go-message/mail"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// addressList reads a header's address list. Almost every list is plain names and addresses, which
// are read as views of value, the way net/mail would read them. Anything else goes to net/mail, whose
// answer is the one that counts (TestPlainAddressesMatchNetMail).
func addressList(value string) []pipeline.Address {
	if value == "" {
		return nil
	}
	if list, ok := plainAddresses(value); ok {
		return list
	}
	list, err := mail.ParseAddressList(value)
	if err != nil {
		return nil
	}
	var addrs []pipeline.Address
	if len(list) != 0 {
		addrs = make([]pipeline.Address, 0, len(list))
	}
	for _, addr := range list {
		// addr.Name is RFC 2047-decoded; strip any embedded CR/LF so a crafted
		// display name cannot inject a header when the address is re-serialized.
		addrs = append(addrs, pipeline.Address{
			Name:    sanitizeHeaderValue(addr.Name),
			Address: sanitizeHeaderValue(addr.Address),
		})
	}
	return addrs
}

// plainAddresses reads a list made only of a@b.c, <a@b.c>, Name Here <a@b.c> and "Name" <a@b.c>. It
// answers false for anything else, comments and encoded-words among them.
func plainAddresses(value string) ([]pipeline.Address, bool) {
	addrs := make([]pipeline.Address, 0, strings.Count(value, ",")+1)
	for rest := value; ; {
		var addr pipeline.Address
		var ok bool
		if addr, rest, ok = plainAddress(rest); !ok {
			return nil, false
		}
		addrs = append(addrs, addr)
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			return addrs, true
		}
		if rest[0] != ',' {
			return nil, false
		}
		rest = rest[1:]
	}
}

func plainAddress(s string) (pipeline.Address, string, bool) {
	s = strings.TrimLeft(s, " \t")
	if spec, rest, ok := plainSpec(s); ok {
		// A bare address may be followed only by what ends it.
		after := strings.TrimLeft(rest, " \t")
		if after == "" || after[0] == ',' {
			return pipeline.Address{Address: spec}, rest, true
		}
		return pipeline.Address{}, "", false
	}
	name, rest, ok := plainPhrase(s)
	if !ok {
		return pipeline.Address{}, "", false
	}
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" || rest[0] != '<' {
		return pipeline.Address{}, "", false
	}
	spec, rest, ok := plainSpec(rest[1:])
	if !ok || rest == "" || rest[0] != '>' {
		return pipeline.Address{}, "", false
	}
	return pipeline.Address{Name: name, Address: spec}, rest[1:], true
}

// plainSpec reads an addr-spec with a dot-atom on each side of its @.
func plainSpec(s string) (spec, rest string, ok bool) {
	local := plainDotAtom(s)
	if local == 0 || local == len(s) || s[local] != '@' {
		return "", "", false
	}
	domain := plainDotAtom(s[local+1:])
	if domain == 0 {
		return "", "", false
	}
	end := local + 1 + domain
	return s[:end], s[end:], true
}

// plainDotAtom is the length of the dot-atom s starts with, or 0.
func plainDotAtom(s string) int {
	i := 0
	for {
		start := i
		for i < len(s) && isPlainAtext(s[i]) {
			i++
		}
		if i == start {
			return 0
		}
		if i == len(s) || s[i] != '.' {
			return i
		}
		i++
	}
}

func isPlainAtext(c byte) bool {
	if c <= ' ' || c >= 0x7f {
		return false
	}
	return strings.IndexByte(`()<>[]:;@\,."`, c) < 0
}

// plainPhrase reads a display name: one quoted string with nothing to unquote, or atoms that one
// space apart, so that either is a view of s just as net/mail would spell it.
func plainPhrase(s string) (name, rest string, ok bool) {
	if s == "" {
		return "", "", false
	}
	if s[0] == '"' {
		for i := 1; i < len(s); i++ {
			c := s[i]
			if c == '"' {
				return s[1:i], s[i+1:], true
			}
			if c == '\\' || c >= 0x7f || (c < ' ' && c != '\t') {
				return "", "", false
			}
		}
		return "", "", false
	}
	i := 0
	for {
		start := i
		for i < len(s) && (isPlainAtext(s[i]) || s[i] == '.') {
			i++
		}
		if i == start {
			return "", "", false
		}
		if strings.Contains(s[start:i], "=?") {
			return "", "", false
		}
		if i < len(s) && s[i] == ' ' && i+1 < len(s) && s[i+1] != ' ' && s[i+1] != '\t' && s[i+1] != '<' {
			i++
			continue
		}
		return s[:i], s[i:], true
	}
}
