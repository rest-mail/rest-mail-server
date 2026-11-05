package mail

import (
	"iter"
	"strings"
)

// HeadField is one field of a message's head: its first line and any folded lines, with their line
// ends, exactly as the message holds them.
type HeadField struct {
	Text string
}

// name is what comes before the colon of the field's first line. Space before the colon is read
// as obs-optional (RFC 5322 section 4.5.3), and before the name only at the top of a message, where
// it can't be a fold.
func (f HeadField) name() string {
	before, _, found := strings.Cut(f.Text, ":")
	if !found {
		return ""
	}
	return strings.Trim(before, " \t")
}

// Is reports whether the field has this name, in any case (RFC 5322 section 1.2.2).
func (f HeadField) Is(name string) bool {
	return strings.EqualFold(f.name(), name)
}

var lineEndRemover = strings.NewReplacer("\r", "", "\n", "")

// Value is the field's body, unfolded and without the space around it.
func (f HeadField) Value() string {
	_, value, _ := strings.Cut(f.Text, ":")
	return strings.TrimSpace(lineEndRemover.Replace(value))
}

// HeadFields reads the fields of a message's head, for any mix of line ends.
//
// RFC 5322 section 2.2 ends a field at CRLF, but the bytes of a queue row are whatever a client
// sent, and what reads them next may split them another way. Three readings are common:
//
//   - CRLF alone ends a line, as RFC 5322 has it.
//   - LF ends a line, with one CR before it dropped, as net/textproto does.
//   - CR, LF or both end a line, as Python's email package does.
//
// Each reading has its own idea of where a line starts and of which empty line ends the head. A
// field is in this head when any reading finds it there, so what any of them could take for a Bcc,
// a DKIM-Signature or a TLS-Required field is seen here. A line that is no field doesn't end the
// head for any reading: some readers stop there and some don't, and carrying on hides nothing.
//
// The price is on a message the readings disagree about: "From: a\n\nHello\r\nBcc: x\r\n\r\n" has
// a body for an LF reader, and a Bcc field for a CRLF one, so it counts as a field.
//
// A field's Text runs by the broadest reading, CR, LF or both. A Bcc field a CRLF reader sees as
// "Bcc: x\nSubject: s" is two fields here, and removing one leaves the Subject.
func HeadFields(raw string) iter.Seq[HeadField] {
	return func(yield func(HeadField) bool) {
		walkHead(raw, func(f HeadField) walkStep {
			if !yield(f) {
				return walkStop
			}
			return walkKeep
		})
	}
}

// RemoveHeadField answers raw without any field of these names that HeadFields would find, folded
// lines included. Every other byte stays as it was, and with nothing to remove raw itself comes
// back, with no copy made.
//
// All the names go in one walk. Removing them one call at a time can leave a field behind, because
// taking out a field of one name can bring a field of another into view.
func RemoveHeadField(raw string, names ...string) string {
	return walkHead(raw, func(f HeadField) walkStep {
		for _, name := range names {
			if f.Is(name) {
				return walkRemove
			}
		}
		return walkKeep
	})
}

type walkStep int

const (
	walkKeep walkStep = iota
	walkRemove
	walkStop
)

// walkHead offers each field of the head to visit, and answers raw without the fields it removed.
// It runs once over the message, and copies only if a field goes.
func walkHead(raw string, visit func(HeadField) walkStep) string {
	var kept strings.Builder
	copied := 0
	readings := newHeadReadings()
	for pos := 0; pos < len(raw) && readings.open(); {
		end, next, _ := lineAt(raw, pos)
		if !readings.fieldCanStart() || pos > 0 && startsFold(raw[pos:end]) || !strings.Contains(raw[pos:end], ":") {
			readings.keepLine(raw[pos:next], end == pos)
			pos = next
			continue
		}
		fieldEnd := next
		for fieldEnd < len(raw) {
			foldEnd, foldNext, _ := lineAt(raw, fieldEnd)
			if !startsFold(raw[fieldEnd:foldEnd]) {
				break
			}
			fieldEnd = foldNext
		}
		step := visit(HeadField{Text: raw[pos:fieldEnd]})
		if step == walkStop {
			break
		}
		if step == walkRemove {
			if copied == 0 {
				kept.Grow(len(raw))
			}
			kept.WriteString(raw[copied:pos])
			copied = fieldEnd
		} else {
			readings.keepLine(raw[pos:fieldEnd], false)
		}
		pos = fieldEnd
	}
	if copied == 0 {
		return raw
	}
	kept.WriteString(raw[copied:])
	return kept.String()
}

func startsFold(line string) bool {
	return line != "" && (line[0] == ' ' || line[0] == '\t')
}

// headReadings follows the three readings over the bytes that stay. It follows what stays, not
// what was there, so that taking a field out can't bring another into view: without its Bcc line,
// "A\rBcc: x\n\nBcc: y" says A, then CRLF, then a field.
type headReadings struct {
	crlfOpen, lfOpen, anyOpen bool
	// crlfLine and lfLine count the bytes so far on the line each reading is in.
	crlfLine, lfLine int
	last             byte
}

func newHeadReadings() *headReadings {
	return &headReadings{crlfOpen: true, lfOpen: true, anyOpen: true}
}

func (r *headReadings) open() bool {
	return r.crlfOpen || r.lfOpen || r.anyOpen
}

// fieldCanStart says whether a reading still in its head is at the start of a line here. The
// walk stands at the start of a line by the broadest reading, and the others may be mid-line.
func (r *headReadings) fieldCanStart() bool {
	return r.anyOpen || r.lfOpen && r.lfLine == 0 || r.crlfOpen && r.crlfLine == 0
}

// keepLine moves the readings over text, which stays in the message. emptyLine says text is a whole
// line with nothing on it by the broadest reading, which ends that reading's head, unless its line
// end only finishes a CR that stayed before it.
func (r *headReadings) keepLine(text string, emptyLine bool) {
	if emptyLine && (r.last != '\r' || text[len(text)-1] != '\n') {
		r.anyOpen = false
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '\n' {
			if r.last == '\r' {
				if r.crlfLine == 1 {
					r.crlfOpen = false
				}
				r.crlfLine = 0
			} else {
				r.crlfLine++
			}
			if r.lfLine == 0 || r.lfLine == 1 && r.last == '\r' {
				r.lfOpen = false
			}
			r.lfLine = 0
		} else {
			r.crlfLine++
			r.lfLine++
		}
		r.last = c
	}
}

// lineAt finds the line starting at start: its content ends at end, and the next line starts at
// next. A run of CRs before an LF is one line end. eol is false for a last line with no line end.
func lineAt(s string, start int) (end, next int, eol bool) {
	i := strings.IndexAny(s[start:], "\r\n")
	if i < 0 {
		return len(s), len(s), false
	}
	end = start + i
	if s[end] == '\n' {
		return end, end + 1, true
	}
	j := end
	for j < len(s) && s[j] == '\r' {
		j++
	}
	if j < len(s) && s[j] == '\n' {
		return end, j + 1, true
	}
	return end, end + 1, true
}
