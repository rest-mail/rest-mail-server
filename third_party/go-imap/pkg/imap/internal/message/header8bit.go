package message

// Header8bitScanner is [HeaderHas8bit] for a message that arrives in pieces: Write each piece
// in order, and Found answers what HeaderHas8bit would for all of them together. It keeps
// three values, not the message.
type Header8bitScanner struct {
	done, found bool
	// line is how many octets of the current line have been seen, and lineCR whether they
	// are exactly one CR, which with the LF that follows is still the empty line.
	line   int
	lineCR bool
}

// Write scans p and never fails.
func (s *Header8bitScanner) Write(p []byte) (int, error) {
	for _, c := range p {
		if s.done {
			break
		}
		switch {
		case c == '\n':
			if s.line == 0 || s.lineCR {
				s.done = true // the empty line that ends the header section
			}
			s.line, s.lineCR = 0, false
		case c >= 0x80:
			s.found, s.done = true, true
		default:
			s.line++
			s.lineCR = s.line == 1 && c == '\r'
		}
	}
	return len(p), nil
}

// Found reports whether the header section seen so far carries an octet with the high bit set.
func (s *Header8bitScanner) Found() bool { return s.found }
