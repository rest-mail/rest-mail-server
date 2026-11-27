package pop3

import (
	"bytes"
	"errors"
	"io"
	"os"

	"github.com/rest-mail/go-imap/pkg/imap/wireform"
	gopop3 "github.com/rest-mail/go-pop3/pkg/pop3"

	"github.com/rest-mail/rest-mail-server/internal/gateway/rawmsg"
	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
)

// RETR and TOP read a stored source from a spool file instead of memory (StreamMailbox).
var _ gopop3.StreamMailbox = (*mailbox)(nil)

// createRetrSpool makes the file a retrieved source is spooled to. Tests replace it.
var createRetrSpool = func() (*os.File, error) { return os.CreateTemp("", "pop3-retr-*") }

// retrSpoolChunk is how much of a spooled source is read at once to find its header.
const retrSpoolChunk = 32 << 10

// OpenMessage opens what Retrieve returns for uid without holding it (#2107): the
// stored source is copied from the API to a temporary file as it arrives, under the
// client's read ceiling, and RETR and TOP read it from there. A session that has not
// enabled UTF-8 gets Retrieve's downgraded header block, the only part held in memory,
// ahead of the file's body.
//
// What Retrieve can only make from the whole message is made whole, as Retrieve makes
// it: a message with no stored source is rebuilt; a source with a NUL is spaced by its
// MIME structure (rawmsg.SpaceNUL); for a session without UTF-8, a source whose
// top-level boundary is 8-bit gets a new boundary on every delimiter line, and one with
// no blank line is all header. So is one the gateway cannot spool. The file is removed
// when the server closes the message, or here when OpenMessage fails.
func (m *mailbox) OpenMessage(uid string) (gopop3.StoredMessage, error) {
	id, err := parseUID(uid)
	if err != nil {
		return nil, err
	}
	detail, err := m.api.GetMessage(m.token, id)
	if err != nil {
		return nil, err
	}
	file, err := createRetrSpool()
	if err != nil {
		return m.retrievedWhole(detail.Data)
	}
	spool := &spooledMessage{file: file}
	keep := false
	defer func() {
		if !keep {
			_ = spool.Close()
		}
	}()
	w := &spoolWriter{w: file}
	found, n, err := m.api.CopyRawMessage(m.token, id, w)
	switch {
	case w.err != nil:
		return m.retrievedWhole(detail.Data)
	case errors.Is(err, apiclient.ErrRawMessageTooLarge):
		return nil, err
	case err != nil || !found || n == 0:
		// As rawMessage: no stored source, or one the API could not send, is rebuilt.
		return heldMessage(m.converted(buildRawMessage(detail.Data))), nil
	}
	spool.bodyLen = n
	whole := w.nul
	if !whole && !m.utf8 {
		if whole, err = spool.downgrade(); err != nil {
			return nil, err
		}
	}
	if whole || bytes.IndexByte(spool.head, 0) >= 0 {
		raw, err := spool.readAll()
		if err != nil {
			return nil, err
		}
		return heldMessage(m.converted(raw)), nil
	}
	keep = true
	return spool, nil
}

// converted is Retrieve's form of raw: downgraded for a session without UTF-8, with
// each NUL in a header or in text a space.
func (m *mailbox) converted(raw string) []byte {
	if !m.utf8 {
		// A client that did not send UTF8 gets headers downgraded to ASCII (RFC
		// 6856 §3, RFC 6857).
		raw = rawmsg.DowngradeForPOP3(raw)
	}
	return rawmsg.SpaceNUL(raw)
}

// retrievedWhole is Retrieve of the message whose detail is given, in memory.
func (m *mailbox) retrievedWhole(detail apiclient.MessageDetail) (gopop3.StoredMessage, error) {
	raw, err := m.rawMessage(detail)
	if err != nil {
		return nil, err
	}
	return heldMessage(m.converted(raw)), nil
}

// spoolWriter remembers the spool file's own failure, which is the gateway's, not the
// API's, and whether the source holds a NUL.
type spoolWriter struct {
	w   io.Writer
	err error
	nul bool
}

func (s *spoolWriter) Write(p []byte) (int, error) {
	if !s.nul && bytes.IndexByte(p, 0) >= 0 {
		s.nul = true
	}
	n, err := s.w.Write(p)
	if err != nil {
		s.err = err
	}
	return n, err
}

// spooledMessage is head followed by bodyLen octets of file from bodyAt. file is nil for
// a message held whole in head.
type spooledMessage struct {
	head    []byte
	file    *os.File
	bodyAt  int64
	bodyLen int64
}

func heldMessage(raw []byte) *spooledMessage { return &spooledMessage{head: raw} }

func (s *spooledMessage) Size() int64 { return int64(len(s.head)) + s.bodyLen }

func (s *spooledMessage) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("pop3: negative offset")
	}
	n := 0
	if off < int64(len(s.head)) {
		n = copy(p, s.head[off:])
	}
	if n < len(p) && s.file != nil {
		at := off + int64(n) - int64(len(s.head))
		if at < s.bodyLen {
			want := p[n:min(int64(len(p)), int64(n)+s.bodyLen-at)]
			m, err := s.file.ReadAt(want, s.bodyAt+at)
			n += m
			if err != nil && !(err == io.EOF && m == len(want)) {
				return n, err
			}
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Close removes the spool file.
func (s *spooledMessage) Close() error {
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	if rmErr := os.Remove(s.file.Name()); err == nil {
		err = rmErr
	}
	s.file = nil
	return err
}

// readAll is the whole spooled source.
func (s *spooledMessage) readAll() (string, error) {
	raw := make([]byte, s.bodyLen)
	if _, err := s.file.ReadAt(raw, 0); err != nil && !(err == io.EOF && len(raw) == int(s.bodyLen)) {
		return "", err
	}
	return string(raw), nil
}

// downgrade makes the spooled source what Retrieve sends a session that has not enabled
// UTF-8, rawmsg.DowngradeForPOP3 of it, holding only its header block, or reports that
// only the whole source can be: one whose top-level boundary is 8-bit, which takes a new
// spelling on every delimiter line of the body, or one with no blank line, which is all
// header. DowngradeForPOP3 otherwise rewrites the header block alone and keeps the body
// as it is, so the block it makes of the source's header, up to and including the line
// that ends it, goes ahead of the rest of the file. The header ends where its own split
// ends it: at the first CRLF CRLF, or failing one the first LF LF.
func (s *spooledMessage) downgrade() (whole bool, err error) {
	end, found, err := s.headerEnd([]byte("\r\n\r\n"))
	if err == nil && !found {
		end, found, err = s.headerEnd([]byte("\n\n"))
	}
	if err != nil || !found {
		return true, err
	}
	header := make([]byte, end)
	if _, err := s.file.ReadAt(header, 0); err != nil && !(err == io.EOF && end == s.bodyLen) {
		return false, err
	}
	block := rawmsg.DowngradeForPOP3(string(header))
	if block == string(header) {
		return false, nil
	}
	// The boundary decision reads only the header, so a boundary that moves in the
	// header alone moves on the body's delimiter lines too.
	if wireform.SevenBitBoundary(string(header)) != string(header) {
		return true, nil
	}
	s.head = []byte(block)
	s.bodyAt, s.bodyLen = end, s.bodyLen-end
	return false, nil
}

// headerEnd is the offset just past the first sep in the spooled source.
func (s *spooledMessage) headerEnd(sep []byte) (int64, bool, error) {
	buf := make([]byte, retrSpoolChunk+len(sep)-1)
	kept := 0
	for at := int64(0); at < s.bodyLen; {
		n, err := s.file.ReadAt(buf[kept:kept+int(min(retrSpoolChunk, s.bodyLen-at))], at)
		if err != nil && err != io.EOF {
			return 0, false, err
		}
		if n == 0 {
			return 0, false, io.ErrUnexpectedEOF
		}
		window := buf[:kept+n]
		if i := bytes.Index(window, sep); i >= 0 {
			return at - int64(kept) + int64(i+len(sep)), true, nil
		}
		at += int64(n)
		kept = min(len(sep)-1, len(window))
		copy(buf, window[len(window)-kept:])
	}
	return 0, false, nil
}
