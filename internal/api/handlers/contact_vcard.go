package handlers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// contactVCardMedia is the media type of an address-book file (RFC 6350 section 10.1).
const contactVCardMedia = "text/vcard"

const (
	// vcardReadBuffer is how much of one physical line is looked at in one read. A longer
	// line, such as an unfolded photo, is read past without being held.
	vcardReadBuffer = 64 << 10
	// maxVCardProperty is the most of one property kept. It is well past what a stored
	// name or address can be once escaped, so only a value that could not be stored, or
	// one nobody reads here, is cut.
	maxVCardProperty = 16 << 10
	// maxVCardAddresses is how many addresses one card may give, so that a card's
	// addresses, the only thing held until its end, stay small.
	maxVCardAddresses = 100
)

// vcardProperty is one unfolded content line.
type vcardProperty struct {
	name    string // upper case, without its group; empty on a line that is no property
	params  []string
	value   string
	tooLong bool
}

// vcardCard is what has been read of the card being read.
type vcardCard struct {
	index     int
	name      string
	addresses []string
	contact   models.Contact
	problems  map[string]string
}

// eachVCardContact walks an address-book file one property at a time, giving each with one
// contact per address on each card. Properties REST Mail does not store are read past and
// never held, whatever their size. problems is what is wrong with the contact, by field; a
// file that is not a sequence of cards is refused outright. It answers how many contacts the
// file gave, counting a card with no address as one that cannot be added.
func eachVCardContact(file io.Reader, each func(models.Contact, map[string]string) error) (int, error) {
	in := bufio.NewReaderSize(file, vcardReadBuffer)
	if start, err := in.Peek(3); err == nil && bytes.Equal(start, []byte("\xef\xbb\xbf")) {
		if _, err := in.Discard(3); err != nil {
			return 0, err
		}
	}
	notCards := func(problem string) error {
		return endpoint.Invalid(map[string]string{"body": problem})
	}
	total, cards := 0, 0
	var card *vcardCard
	for {
		property, err := nextVCardProperty(in)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		value := strings.TrimSpace(property.value)
		switch {
		case property.name == "BEGIN" && strings.EqualFold(value, "VCARD"):
			if card != nil {
				return 0, notCards(fmt.Sprintf("card %d begins inside card %d", cards+1, card.index+1))
			}
			card = &vcardCard{index: cards, problems: map[string]string{},
				contact: models.Contact{TrustLevel: "auto", Source: "import"}}
			cards++
		case card == nil:
			return 0, notCards("the body is not a sequence of vCards")
		case property.name == "END" && strings.EqualFold(value, "VCARD"):
			count, err := card.finish(each)
			if err != nil {
				return 0, err
			}
			total += count
			card = nil
		default:
			card.read(property)
		}
	}
	if card != nil {
		return 0, notCards(fmt.Sprintf("card %d has no END:VCARD", card.index+1))
	}
	return total, nil
}

// read takes what a card's property says about the contact.
func (c *vcardCard) read(property vcardProperty) {
	field := func(name string) string { return fmt.Sprintf("cards[%d].%s", c.index, name) }
	text := func(name string) (string, bool) {
		value, problem := vcardPropertyText(property)
		if problem != "" {
			c.problems[field(name)] = problem
			return "", false
		}
		return value, true
	}
	switch property.name {
	case "VERSION":
		switch strings.TrimSpace(property.value) {
		case "2.1", "3.0", "4.0":
		default:
			c.problems[field("version")] = "must be 2.1, 3.0 or 4.0"
		}
	case "FN":
		// A card may name a person several ways; the first is the one kept.
		if c.name == "" {
			if value, ok := text("name"); ok {
				c.name = value
			}
		}
	case "EMAIL":
		value, ok := text("email")
		value = strings.TrimSpace(value)
		switch {
		case !ok || value == "":
		case len(c.addresses) == maxVCardAddresses:
			c.problems[field("email")] = fmt.Sprintf("must be at most %d addresses", maxVCardAddresses)
		default:
			c.addresses = append(c.addresses, value)
		}
	case "X-RESTMAIL-TRUST-LEVEL":
		if value, ok := text("trust_level"); ok {
			switch value {
			case "auto", "trusted", "blocked":
				c.contact.TrustLevel = value
			default:
				c.problems[field("trust_level")] = "must be auto, trusted, or blocked"
			}
		}
	case "X-RESTMAIL-SOURCE":
		if value, ok := text("source"); ok {
			switch value {
			case "sent", "manual", "import":
				c.contact.Source = value
			default:
				c.problems[field("source")] = "must be sent, manual, or import"
			}
		}
	case "X-RESTMAIL-CREATED-AT":
		if value, ok := text("created_at"); ok {
			c.readTime(value, field("created_at"), &c.contact.CreatedAt)
		}
	case "X-RESTMAIL-UPDATED-AT":
		if value, ok := text("updated_at"); ok {
			c.readTime(value, field("updated_at"), &c.contact.UpdatedAt)
		}
	}
}

// readTime keeps a time the API can give back: the JSON form of a time has a four-digit
// year, and PostgreSQL has no year zero.
func (c *vcardCard) readTime(value, field string, into *time.Time) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if year := parsed.UTC().Year(); err != nil || year < 1 || year > 9999 {
		c.problems[field] = "must be an RFC 3339 time"
		return
	}
	*into = parsed.UTC()
}

// finish gives the card's contacts and answers how many it counts for.
func (c *vcardCard) finish(each func(models.Contact, map[string]string) error) (int, error) {
	if len(c.addresses) == 0 {
		// Nothing can be stored for a card without an address, such as one with only a
		// telephone number. It is counted as skipped, unless it was also wrong.
		if len(c.problems) > 0 {
			return 1, each(models.Contact{}, c.problems)
		}
		return 1, nil
	}
	prefix := fmt.Sprintf("cards[%d]", c.index)
	for _, address := range c.addresses {
		entry := importContactEntry{Email: address, Name: c.name}
		// An exported contact with no name carries its address as FN, which vCard requires.
		if entry.Name == address {
			entry.Name = ""
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			return 0, err
		}
		_, found := checkImportEntry(raw, prefix)
		problems := make(map[string]string, len(c.problems)+len(found))
		for field, problem := range c.problems {
			problems[field] = problem
		}
		for field, problem := range found {
			problems[field] = problem
		}
		contact := c.contact
		contact.Email, contact.Name = entry.Email, entry.Name
		if err := each(contact, problems); err != nil {
			return 0, err
		}
	}
	return len(c.addresses), nil
}

// vcardPropertyText is a property's value as text, or what stops it being read as text.
func vcardPropertyText(property vcardProperty) (string, string) {
	if property.tooLong {
		return "", "is too long"
	}
	if vcardValueEncoded(property.params) {
		return "", "uses an encoding or character set other than UTF-8 text"
	}
	if !utf8.ValidString(property.value) {
		return "", "must be UTF-8 text"
	}
	var out strings.Builder
	for index := 0; index < len(property.value); index++ {
		character := property.value[index]
		if character == '\\' && index+1 < len(property.value) {
			index++
			character = property.value[index]
			if character == 'n' || character == 'N' {
				character = '\n'
			}
		}
		out.WriteByte(character)
	}
	value := out.String()
	if strings.ContainsFunc(value, func(character rune) bool {
		return (character < 0x20 && character != '\t' && character != '\n') || character == 0x7f
	}) {
		return "", "must not contain control characters"
	}
	return value, ""
}

// vcardValueEncoded says whether parameters put the value in something other than UTF-8
// text: a transfer encoding or another character set, as vCard 2.1 and 3.0 allow.
func vcardValueEncoded(params []string) bool {
	for _, param := range params {
		key, value, named := strings.Cut(strings.ToUpper(param), "=")
		value = strings.Trim(value, `"`)
		switch {
		case !named && (key == "QUOTED-PRINTABLE" || key == "BASE64"):
			return true
		case key == "ENCODING" && value != "7BIT" && value != "8BIT":
			return true
		case key == "CHARSET" && value != "UTF-8" && value != "US-ASCII":
			return true
		}
	}
	return false
}

// nextVCardProperty reads one content line with its folded continuations, skipping blank
// lines. It answers io.EOF when the file has no more.
func nextVCardProperty(in *bufio.Reader) (vcardProperty, error) {
	var line []byte
	over := false
	for len(line) == 0 && !over {
		var err error
		line, over, err = readVCardLine(in, nil)
		if err != nil {
			return vcardProperty{}, err
		}
	}
	for {
		// A line that starts with a space or tab continues the one before it.
		next, err := in.Peek(1)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return vcardProperty{}, err
		}
		if next[0] != ' ' && next[0] != '\t' {
			break
		}
		if _, err := in.Discard(1); err != nil {
			return vcardProperty{}, err
		}
		more, cut, err := readVCardLine(in, line)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return vcardProperty{}, err
		}
		line, over = more, over || cut
	}
	property := parseVCardProperty(line, over)
	// A quoted-printable value ending in = goes on to the next line, unfolded, and that
	// line is not a property of its own.
	for vcardQuotedPrintable(property.params) && bytes.HasSuffix(line, []byte("=")) {
		more, cut, err := readVCardLine(in, nil)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return vcardProperty{}, err
		}
		line = more
		property.tooLong = property.tooLong || cut
	}
	return property, nil
}

func vcardQuotedPrintable(params []string) bool {
	for _, param := range params {
		upper := strings.ToUpper(param)
		if upper == "QUOTED-PRINTABLE" || upper == "ENCODING=QUOTED-PRINTABLE" {
			return true
		}
	}
	return false
}

// readVCardLine adds the next physical line, without its line ending, to line. Past
// maxVCardProperty the rest is read and dropped, and over says so. It answers io.EOF only
// when no line was left.
func readVCardLine(in *bufio.Reader, line []byte) ([]byte, bool, error) {
	over, read := false, false
	for {
		chunk, err := in.ReadSlice('\n')
		read = read || len(chunk) > 0
		if room := maxVCardProperty - len(line); len(chunk) > room {
			line, over = append(line, chunk[:max(room, 0)]...), true
		} else {
			line = append(line, chunk...)
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && !read:
			return line, over, io.EOF
		case err != nil && !errors.Is(err, io.EOF):
			return line, over, err
		}
		if !over {
			line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		}
		return line, over, nil
	}
}

// parseVCardProperty splits an unfolded line into its name, parameters and value. A colon or
// semicolon inside a quoted parameter value is part of that value.
func parseVCardProperty(line []byte, over bool) vcardProperty {
	quoted, colon := false, -1
	var separators []int
	for index, character := range line {
		switch {
		case character == '"':
			quoted = !quoted
		case quoted:
		case character == ';':
			separators = append(separators, index)
		case character == ':':
			colon = index
		}
		if colon >= 0 {
			break
		}
	}
	nameEnd := len(line)
	if colon >= 0 {
		nameEnd = colon
	}
	if len(separators) > 0 {
		nameEnd = separators[0]
	}
	name := string(line[:nameEnd])
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		name = name[dot+1:]
	}
	property := vcardProperty{name: strings.ToUpper(name), tooLong: over}
	if colon < 0 {
		if !over {
			property.name = ""
		}
		return property
	}
	for index, start := range separators {
		end := colon
		if index+1 < len(separators) {
			end = separators[index+1]
		}
		property.params = append(property.params, string(line[start+1:end]))
	}
	property.value = string(line[colon+1:])
	return property
}
