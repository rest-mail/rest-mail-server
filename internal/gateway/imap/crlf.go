package imap

import "io"

// crlfChunk is how much of the source a crlfReader holds at once.
const crlfChunk = 16 << 10

// crlfReader reads src with every LF that no CR precedes made CRLF, leaving existing CRLF
// and bare CR as they are, so CRLF input reads unchanged. It is applied to the message an
// APPEND stores, so the stored message is CRLF-framed: an LF-only message would otherwise
// break POP3 RETR/TOP framing (go-pop3 splits on CRLF, and bare-LF dot-lines are not
// dot-stuffed, risking premature termination) and diverge from the CRLF wire form every
// other stored message uses. It holds a chunk of the message, never the whole (#2107).
type crlfReader struct {
	src io.Reader
	buf []byte
	// prevCR is whether the last octet read from src was CR.
	prevCR bool
	// pendingLF is the LF of an inserted CRLF that did not fit the last Read.
	pendingLF bool
	err       error
}

func newCRLFReader(src io.Reader) *crlfReader {
	return &crlfReader{src: src}
}

func (r *crlfReader) Read(p []byte) (int, error) {
	n := 0
	if r.pendingLF && len(p) > 0 {
		p[0] = '\n'
		n, r.pendingLF = 1, false
	}
	if n == len(p) || r.err != nil {
		if n == 0 && r.err != nil {
			return 0, r.err
		}
		return n, nil
	}
	if r.buf == nil {
		r.buf = make([]byte, crlfChunk)
	}
	// Each octet read takes at most two places in p; only the last one's LF can fail to
	// fit, and pendingLF carries it to the next Read.
	read, err := r.src.Read(r.buf[:min((len(p)-n+1)/2, len(r.buf))])
	r.err = err
	for _, c := range r.buf[:read] {
		if c == '\n' && !r.prevCR {
			p[n] = '\r'
			n++
			if n == len(p) {
				r.pendingLF = true
				break
			}
		}
		p[n] = c
		n++
		r.prevCR = c == '\r'
	}
	if n == 0 && r.err != nil {
		return 0, r.err
	}
	return n, nil
}
