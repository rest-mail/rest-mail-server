package wire

import (
	"bufio"
	"io"
)

// streamChunk is how many stored octets a streamed RETR or TOP reads at a time.
const streamChunk = 32 << 10

// CountMessage is the RFC 1939 §11 octet count of the stored message r yields:
// len(WireMessage(raw)), counted as it is read, so nothing that grows with the
// message is held.
func CountMessage(r io.Reader) (int64, error) {
	f := framer{atStart: true, body: -1}
	err := f.feed(r)
	f.finish()
	return f.octets, err
}

// SendMessage writes to w the lines RETR sends for the stored message r yields:
// the lines of WireMessage(raw), each dot-stuffed (DotStuff) and ended in CRLF,
// exactly the octets SplitMessageLines and DotStuff make, without the "."
// terminator. It returns the octets it sent before dot-stuffing, which is
// CountMessage of the same octets. A read or write error stops it.
func SendMessage(w *bufio.Writer, r io.Reader) (int64, error) {
	f := framer{w: w, atStart: true, body: -1}
	err := f.feed(r)
	if err == nil {
		f.finish()
		err = f.err
	}
	return f.octets, err
}

// SendTop writes to w the lines TOP sends for the stored message r yields with
// n (n >= 0) body lines: the lines of TopLines(CanonicalCRLF(raw), n), each
// dot-stuffed and ended in CRLF, without the "." terminator. It stops reading
// once the last line it sends has ended. A read or write error stops it.
func SendTop(w *bufio.Writer, r io.Reader, n int) error {
	f := framer{w: w, atStart: true, top: true, body: n}
	err := f.feed(r)
	if err == nil {
		f.finish()
		err = f.err
	}
	return err
}

// framer turns stored octets into the lines of a RETR or TOP response one octet
// at a time. A stored line ends at an LF, and a CR just before that LF is part
// of its CRLF; any other CR is an octet of the line (CanonicalCRLF). A last line
// with no LF still ends in CRLF on the wire (WireMessage). With no writer it only
// counts.
type framer struct {
	w *bufio.Writer
	// top makes the header end at the first empty line, after which at most body
	// lines are sent (TopLines); without it body is -1 and every line is sent.
	top    bool
	inBody bool
	body   int

	atStart   bool // the next octet begins a line
	pendingCR bool // a CR whose LF, if it is one, is yet to come
	done      bool // TOP has sent its last line

	octets int64 // octets sent, before dot-stuffing
	err    error // the first write error
}

func (f *framer) feed(r io.Reader) error {
	buf := make([]byte, streamChunk)
	for !f.done && f.err == nil {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			f.octet(b)
			if f.done || f.err != nil {
				break
			}
		}
		if err == io.EOF {
			return f.err
		}
		if err != nil {
			return err
		}
	}
	return f.err
}

func (f *framer) octet(b byte) {
	if f.pendingCR {
		f.pendingCR = false
		if b == '\n' {
			f.endLine()
			return
		}
		f.content('\r')
		if f.done {
			return
		}
	}
	switch b {
	case '\r':
		f.pendingCR = true
	case '\n':
		f.endLine()
	default:
		f.content(b)
	}
}

// startLine reports whether the line about to begin is sent: TOP sends no more
// body lines than it was asked for.
func (f *framer) startLine() bool {
	if f.inBody && f.body == 0 {
		f.done = true
		return false
	}
	return true
}

func (f *framer) content(b byte) {
	if f.atStart {
		if !f.startLine() {
			return
		}
		if b == '.' {
			f.write('.') // RFC 1939 §3 byte-stuffing, not counted
		}
		f.atStart = false
	}
	f.write(b)
	f.octets++
}

func (f *framer) endLine() {
	empty := f.atStart
	if empty && !f.startLine() {
		return
	}
	f.write('\r')
	f.write('\n')
	f.octets += 2
	f.atStart = true
	switch {
	case f.inBody:
		f.body--
	case f.top && empty:
		// The first empty line ends the header and is sent as the separator.
		f.inBody = true
	}
}

// finish ends what the stored octets left open: a last CR, a last line with no
// LF, and for TOP a header that no empty line ended, after which TopLines sends
// the separator all the same.
func (f *framer) finish() {
	if f.done {
		return
	}
	if f.pendingCR {
		f.pendingCR = false
		f.content('\r')
	}
	if !f.atStart && !f.done {
		f.endLine()
	}
	if f.top && !f.inBody {
		f.endLine()
	}
}

func (f *framer) write(b byte) {
	if f.w == nil || f.err != nil {
		return
	}
	f.err = f.w.WriteByte(b)
}
