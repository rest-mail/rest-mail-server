package server

import (
	"bytes"
	"io"
	"os"

	"github.com/rest-mail/go-imap/pkg/imap/internal/message"
)

// appendSpoolChunk is how much of an APPEND literal is held at once on its way to the spool.
const appendSpoolChunk = 32 << 10

// appendSpool is an APPEND literal on its way to the store: its octets in a temporary file,
// and what the server must know of them, found as they were copied, so that no step holds
// the message whole (#2107).
type appendSpool struct {
	file *os.File
	size int64
	// nul is whether the literal holds a NUL, which CHAR8 excludes (RFC 3501 §9).
	nul bool
	// header finds 8-bit octets in the message's header section (RFC 6855 §4).
	header message.Header8bitScanner
	// diskErr is the server's own failure to keep the literal. The literal is still read to
	// its end, so the next command's boundary is kept and the APPEND is answered NO.
	diskErr error
}

// spoolLiteral copies the literal's size octets from the client to a temporary file. An error
// is the client's stream failing before the literal ended; the stream cannot be reused. The
// spool it answers is never nil, and the caller removes it however the command ends.
func (s *Session) spoolLiteral(size int64) (*appendSpool, error) {
	spool := &appendSpool{size: size}
	spool.file, spool.diskErr = os.CreateTemp("", "imap-append-*")
	if spool.diskErr != nil {
		spool.file = nil
	}
	buf := make([]byte, min(size, appendSpoolChunk))
	for remaining := size; remaining > 0; {
		chunk := buf[:min(remaining, int64(len(buf)))]
		if _, err := io.ReadFull(s.reader, chunk); err != nil {
			return spool, err
		}
		remaining -= int64(len(chunk))
		if !spool.nul && bytes.IndexByte(chunk, 0) >= 0 {
			spool.nul = true
		}
		_, _ = spool.header.Write(chunk)
		if spool.diskErr == nil {
			_, spool.diskErr = spool.file.Write(chunk)
		}
	}
	return spool, nil
}

// bytes reads the spooled literal back into memory, for a Mailbox that takes it only whole.
func (a *appendSpool) bytes() ([]byte, error) {
	data := make([]byte, a.size)
	if _, err := io.ReadFull(io.NewSectionReader(a.file, 0, a.size), data); err != nil {
		return nil, err
	}
	return data, nil
}

// remove closes and deletes the spool file. It is safe to call on a spool with no file.
func (a *appendSpool) remove() {
	if a.file == nil {
		return
	}
	_ = a.file.Close()
	_ = os.Remove(a.file.Name())
}
