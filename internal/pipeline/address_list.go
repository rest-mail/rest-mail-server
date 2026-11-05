package pipeline

import (
	"encoding/json"
	"mime"

	"github.com/rest-mail/rest-mail-server/internal/mailaddr"
)

// AddressList is what a header says of who a message is for. One from a parse holds only its field
// and reads that when asked: a list of very short addresses would otherwise hold eight times its own
// size as []Address on every route, whether or not anything reads it (#1915).
type AddressList struct {
	field string
	dec   *mime.WordDecoder
	given []Address
}

// Addresses is a list of the addresses given.
func Addresses(addresses ...Address) AddressList { return AddressList{given: addresses} }

// ParsedAddresses is the list a header field says, read as net/mail reads it, which is empty when the
// field doesn't read as a list. field may be a view of a buffer, which has to outlive the list.
func ParsedAddresses(field string, dec *mime.WordDecoder) AddressList {
	return AddressList{field: field, dec: dec}
}

// FieldSize is how long the field a parsed list came from is, or 0.
func (l AddressList) FieldSize() int { return len(l.field) }

// Len is how many addresses there are. It holds nothing for each one.
func (l AddressList) Len() int {
	if l.field == "" {
		return len(l.given)
	}
	count, _ := mailaddr.Valid(l.field, l.dec)
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
	if _, ok := mailaddr.Valid(l.field, l.dec); !ok {
		return
	}
	_ = mailaddr.Each(l.field, l.dec, func(name, address string) {
		visit(Address{Name: sanitizeHeaderValue(name), Address: sanitizeHeaderValue(address)})
	})
}

// Visit is Each for a caller that keeps nothing until it's done, with no second reading of the field.
// It answers false for a field that doesn't read as a list, whose visits so far don't count.
func (l AddressList) Visit(visit func(Address)) bool {
	if l.field == "" {
		l.Each(visit)
		return true
	}
	return mailaddr.Each(l.field, l.dec, func(name, address string) {
		visit(Address{Name: sanitizeHeaderValue(name), Address: sanitizeHeaderValue(address)})
	}) == nil
}

// All is every address, or nil for none.
func (l AddressList) All() []Address {
	if l.field == "" {
		return l.given
	}
	count, ok := mailaddr.Valid(l.field, l.dec)
	if !ok || count == 0 {
		return nil
	}
	all := make([]Address, 0, count)
	_ = mailaddr.Each(l.field, l.dec, func(name, address string) {
		all = append(all, Address{Name: sanitizeHeaderValue(name), Address: sanitizeHeaderValue(address)})
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
