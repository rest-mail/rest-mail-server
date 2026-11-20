package handlers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/quotedprintable"
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
	index int
	name  string
	// structuredName is the name the card's first N gives, kept for a card with no FN,
	// which vCard 2.1 allows.
	structuredName string
	addresses      []string
	contact        models.Contact
	problems       map[string]string
	// restoreTrust says whether X-RESTMAIL-TRUST-LEVEL is read; otherwise it is ignored
	// like any other property REST Mail does not take from a file.
	restoreTrust bool
}

// eachVCardContact walks an address-book file one property at a time, giving each with one
// contact per address on each card. Properties REST Mail does not store are read past and
// never held, whatever their size. problems is what is wrong with the contact, by field; a
// file that is not a sequence of cards is refused outright. It answers how many contacts the
// file gave, counting a card with no address as one that cannot be added. restoreTrust says
// whether a card's X-RESTMAIL-TRUST-LEVEL is restored; otherwise every contact is auto.
func eachVCardContact(file io.Reader, restoreTrust bool, each func(models.Contact, map[string]string) error) (int, error) {
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
			card = &vcardCard{index: cards, problems: map[string]string{}, restoreTrust: restoreTrust,
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
	case "N":
		// Family;Given;Additional;Prefixes;Suffixes, written in the order a name is said.
		if c.structuredName == "" {
			if value, problem := vcardStructuredName(property); problem != "" {
				c.problems[field("name")] = problem
			} else {
				c.structuredName = value
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
		if !c.restoreTrust {
			break
		}
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
	name := c.name
	if name == "" {
		name = c.structuredName
	}
	for _, address := range c.addresses {
		entry := importContactEntry{Email: address, Name: name}
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
	raw, problem := vcardPropertyRaw(property)
	if problem != "" {
		return "", problem
	}
	return vcardText(raw)
}

// vcardPropertyRaw is a property's value with any quoted-printable encoding undone but its
// escapes still in place, or what stops it being read as UTF-8 text.
func vcardPropertyRaw(property vcardProperty) (string, string) {
	if property.tooLong {
		return "", "is too long"
	}
	if vcardValueEncoded(property.params) {
		return "", "uses an encoding or character set other than UTF-8 text"
	}
	value := property.value
	if vcardQuotedPrintable(property.params) {
		// The decoder passes a broken escape through as it is; such a value is refused
		// instead of being stored with its = left in.
		if !wellFormedQuotedPrintable(value) {
			return "", "is not valid quoted-printable"
		}
		decoded, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(value)))
		if err != nil {
			return "", "is not valid quoted-printable"
		}
		value = string(decoded)
	}
	if !utf8.ValidString(value) {
		return "", "must be UTF-8 text"
	}
	return value, ""
}

// wellFormedQuotedPrintable says whether every = in a value is an escape of two hexadecimal
// digits or a soft line break (RFC 2045 section 6.7).
func wellFormedQuotedPrintable(value string) bool {
	hex := func(character byte) bool {
		return ('0' <= character && character <= '9') || ('A' <= character && character <= 'F') || ('a' <= character && character <= 'f')
	}
	for index := 0; index < len(value); index++ {
		if value[index] != '=' {
			continue
		}
		rest := value[index+1:]
		switch {
		case rest == "" || strings.HasPrefix(rest, "\r\n"):
		case len(rest) >= 2 && hex(rest[0]) && hex(rest[1]):
			index += 2
		default:
			return false
		}
	}
	return true
}

// vcardText undoes a value's escapes and refuses control characters.
func vcardText(raw string) (string, string) {
	var out strings.Builder
	for index := 0; index < len(raw); index++ {
		character := raw[index]
		if character == '\\' && index+1 < len(raw) {
			index++
			character = raw[index]
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

// vcardStructuredName is the name an N property gives (RFC 6350 section 6.2.2): its
// prefixes, given, additional and family names and suffixes, in that order, separated by
// spaces. A component listing several values, separated by commas, gives each.
func vcardStructuredName(property vcardProperty) (string, string) {
	raw, problem := vcardPropertyRaw(property)
	if problem != "" {
		return "", problem
	}
	components := splitVCardValue(raw, ';')
	for len(components) < 5 {
		components = append(components, "")
	}
	var words []string
	for _, index := range []int{3, 1, 2, 0, 4} {
		for _, part := range splitVCardValue(components[index], ',') {
			word, problem := vcardText(part)
			if problem != "" {
				return "", problem
			}
			if word = strings.TrimSpace(word); word != "" {
				words = append(words, word)
			}
		}
	}
	return strings.Join(words, " "), ""
}

// splitVCardValue splits a raw value at each separator not escaped with a backslash,
// leaving the escapes in place.
func splitVCardValue(raw string, separator byte) []string {
	var parts []string
	start := 0
	for index := 0; index < len(raw); index++ {
		switch raw[index] {
		case '\\':
			index++
		case separator:
			parts = append(parts, raw[start:index])
			start = index + 1
		}
	}
	return append(parts, raw[start:])
}

// vcardValueEncoded says whether parameters put the value in something other than UTF-8
// text that can be read here: base64, another transfer encoding or another character set,
// as vCard 2.1 and 3.0 allow. Quoted-printable is read, and what it gives must be UTF-8.
func vcardValueEncoded(params []string) bool {
	for _, param := range params {
		key, value, named := strings.Cut(strings.ToUpper(param), "=")
		value = strings.Trim(value, `"`)
		switch {
		case !named && key == "BASE64":
			return true
		case key == "ENCODING" && value != "7BIT" && value != "8BIT" && value != "QUOTED-PRINTABLE":
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
	// line is not a property of its own. The soft line break is kept for the decoder.
	for vcardQuotedPrintable(property.params) && bytes.HasSuffix(line, []byte("=")) {
		more, cut, err := readVCardLine(in, nil)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return vcardProperty{}, err
		}
		line = more
		if len(property.value)+2+len(more) > maxVCardProperty {
			property.tooLong = true
		}
		if !property.tooLong {
			property.value += "\r\n" + string(more)
		}
		property.tooLong = property.tooLong || cut
	}
	return property, nil
}

// vcardQuotedPrintable says whether parameters put the value in quoted-printable, read the
// way vcardValueEncoded reads them.
func vcardQuotedPrintable(params []string) bool {
	for _, param := range params {
		key, value, named := strings.Cut(strings.ToUpper(param), "=")
		if (!named && key == "QUOTED-PRINTABLE") || (key == "ENCODING" && strings.Trim(value, `"`) == "QUOTED-PRINTABLE") {
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
