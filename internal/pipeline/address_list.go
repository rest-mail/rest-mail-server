package pipeline

import (
	"encoding/json"
	"mime"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap/wireform"
	"github.com/rest-mail/rest-mail-server/internal/mailaddr"
)

// AddressList is what a header says of who a message is for. One from a parse holds only its field
// and reads that when asked: a list of very short addresses would otherwise hold eight times its own
// size as []Address on every route, whether or not anything reads it (#1915).
type AddressList struct {
	field  string
	dec    *mime.WordDecoder
	unread *mailaddr.Unread
	given  []Address
}

// Addresses is a list of the addresses given.
func Addresses(addresses ...Address) AddressList { return AddressList{given: addresses} }

// ParsedAddresses is the list a header field says, read as net/mail reads it, which is empty when the
// field doesn't read as a list. field may be a view of a buffer, which has to outlive the list.
func ParsedAddresses(field string, dec *mime.WordDecoder) AddressList {
	return AddressList{field: field, dec: dec}
}

// ParsedAddressesKeeping is ParsedAddresses for a reader that leaves a word in a charset unread names as
// it was sent, as a mail client is sent it, so that naming one can't hide a field's addresses (#1968).
func ParsedAddressesKeeping(field string, dec *mime.WordDecoder, unread *mailaddr.Unread) AddressList {
	return AddressList{field: field, dec: dec, unread: unread}
}

func (l AddressList) unreadCharset() mailaddr.Unread {
	if l.unread == nil {
		return nil
	}
	return *l.unread
}

// Len is how many addresses there are. It holds nothing for each one.
func (l AddressList) Len() int {
	if l.field == "" {
		return len(l.given)
	}
	count, _ := mailaddr.Valid(l.field, l.dec, l.unreadCharset())
	return count
}

// IsZero reports whether there are no addresses, for encoding/json.
func (l AddressList) IsZero() bool { return l.Len() == 0 }

// First is the first address, or none.
func (l AddressList) First() (first Address, ok bool) {
	if l.field == "" {
		if len(l.given) > 0 {
			return l.given[0], true
		}
		return Address{}, false
	}
	l.Each(func(address Address) {
		if !ok {
			first, ok = address, true
		}
	})
	return first, ok
}

// FirstAddress is the address of the first, or empty.
func (l AddressList) FirstAddress() string {
	first, _ := l.First()
	return first.Address
}

// Each calls visit with every address.
func (l AddressList) Each(visit func(Address)) {
	if l.field == "" {
		for _, address := range l.given {
			visit(address)
		}
		return
	}
	if _, ok := mailaddr.Valid(l.field, l.dec, l.unreadCharset()); !ok {
		return
	}
	_ = mailaddr.Each(l.field, l.dec, l.unreadCharset(), func(name, address string) {
		visit(Address{Name: l.name(name), Address: sanitizeHeaderValue(address)})
	})
}

// Visit is Each for a caller that keeps nothing until it's done, with no second reading of the field.
// It answers false for a field that doesn't read as a list, whose visits so far don't count.
func (l AddressList) Visit(visit func(Address)) bool {
	if l.field == "" {
		l.Each(visit)
		return true
	}
	return mailaddr.Each(l.field, l.dec, l.unreadCharset(), func(name, address string) {
		visit(Address{Name: l.name(name), Address: sanitizeHeaderValue(address)})
	}) == nil
}

// VisitAddresses is Visit with each name left unread, which costs nothing per address.
func (l AddressList) VisitAddresses(visit func(address string)) bool {
	if l.field == "" {
		for _, address := range l.given {
			visit(address.Address)
		}
		return true
	}
	return mailaddr.EachAddress(l.field, l.dec, l.unreadCharset(), func(address string) { visit(sanitizeHeaderValue(address)) }) == nil
}

// All is every address, or nil for none.
func (l AddressList) All() []Address {
	if l.field == "" {
		return l.given
	}
	count, ok := mailaddr.Valid(l.field, l.dec, l.unreadCharset())
	if !ok || count == 0 {
		return nil
	}
	all := make([]Address, 0, count)
	_ = mailaddr.Each(l.field, l.dec, l.unreadCharset(), func(name, address string) {
		all = append(all, Address{Name: l.name(name), Address: sanitizeHeaderValue(address)})
	})
	return all
}

// MarshalJSON writes the list as an array of addresses.
func (l AddressList) MarshalJSON() ([]byte, error) { return json.Marshal(l.All()) }

// UnmarshalJSON reads an array of addresses.
func (l *AddressList) UnmarshalJSON(data []byte) error {
	var given []Address
	if err := json.Unmarshal(data, &given); err != nil {
		return err
	}
	*l = AddressList{given: given}
	return nil
}

// name is a display name as a mail client reads it, which is what a filter gets: a client decodes every
// encoded word it's sent in ENVELOPE, so that includes a word the parser leaves as text because it stands
// in a quoted string, against RFC 2047 §5, or because a word decoded to it. A NUL, spelled or decoded,
// is the space a client is sent for it (#1968).
func (l AddressList) name(name string) string {
	if strings.Contains(name, "=?") {
		name = l.readWords(wireform.SpaceWordNUL(name))
	}
	return sanitizeHeaderValue(strings.ReplaceAll(name, "\x00", " "))
}

// readWords is text with its encoded words decoded. A word in a charset nothing reads stays as it was
// sent, as go-imap sends it, and the words around it are still read.
func (l AddressList) readWords(text string) string {
	if strings.IndexByte(text, ' ') < 0 && l.isKept(text) {
		return text
	}
	if decoded, err := l.dec.DecodeHeader(text); err == nil {
		return decoded
	}
	var out strings.Builder
	out.Grow(len(text))
	run, at := 0, 0
	flush := func(end int) {
		if end > run {
			read := text[run:end]
			if decoded, err := l.dec.DecodeHeader(read); err == nil {
				read = decoded
			}
			out.WriteString(read)
		}
	}
	for at <= len(text) {
		end := len(text)
		if space := strings.IndexByte(text[at:], ' '); space >= 0 {
			end = at + space
		}
		if l.isKept(text[at:end]) {
			flush(at)
			out.WriteString(text[at:end])
			run = end
		}
		at = end + 1
	}
	flush(len(text))
	return out.String()
}

// isKept reports whether word is an encoded word in a charset that isn't read, which takes no decoding
// to find out when the list was told which those are.
func (l AddressList) isKept(word string) bool {
	charset, ok := mailaddr.WordCharset(word)
	if !ok {
		return false
	}
	if unread := l.unreadCharset(); unread != nil {
		return unread(charset)
	}
	_, err := l.dec.Decode(word)
	return err != nil
}

// sanitizeHeaderValue removes CR and LF from a decoded header value. RFC 2047 encoded-words can
// decode to arbitrary bytes, including CR/LF; if such a value is later re-serialized into a header
// (forward/reply/bounce/Sieve) the embedded line break would start a new header line, enabling header
// injection (e.g. a smuggled Bcc). Stripping the line breaks neutralizes the injection while
// preserving the remaining visible text. (OSI-23)
func sanitizeHeaderValue(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' || s[i] == '\n' {
			out := make([]byte, 0, len(s))
			for j := 0; j < len(s); j++ {
				if s[j] != '\r' && s[j] != '\n' {
					out = append(out, s[j])
				}
			}
			return string(out)
		}
	}
	return s
}
