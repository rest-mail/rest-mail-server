package imap

import (
	"bytes"
	"errors"
	"io"
	"os"

	goimap "github.com/rest-mail/go-imap/pkg/imap"

	"github.com/rest-mail/rest-mail-server/internal/gateway/rawmsg"
	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
)

// FETCH reads a stored source from a spool file instead of memory (StreamFetcher).
var _ goimap.StreamFetcher = (*mailbox)(nil)

// createFetchSpool makes the file a fetched source is spooled to. Tests replace it.
var createFetchSpool = func() (*os.File, error) { return os.CreateTemp("", "imap-fetch-*") }

// fetchSpoolChunk is how much of a spooled source is read at once to find its header.
const fetchSpoolChunk = 32 << 10

// FetchStream opens what Fetch returns for uid without holding it (#2107): the stored
// source is copied from the API to a temporary file as it arrives, under the client's
// read ceiling, and FETCH reads it from there. A session that has not enabled UTF-8 gets
// Fetch's downgraded header block, the only part held in memory, ahead of the file's
// body. A message with no stored source is rebuilt in memory, as Fetch rebuilds it; so is
// one the gateway cannot spool. The file is removed when the server closes the message,
// or here when FetchStream fails.
func (m *mailbox) FetchStream(uid uint32) (goimap.FetchedMessage, error) {
	file, err := createFetchSpool()
	if err != nil {
		return m.fetchedWhole(uid)
	}
	spool := &spooledMessage{file: file}
	keep := false
	defer func() {
		if !keep {
			_ = spool.Close()
		}
	}()
	w := &diskWriter{w: file}
	found, n, err := m.api.CopyRawMessage(m.token, uint(uid), w)
	switch {
	case w.err != nil:
		return m.fetchedWhole(uid)
	case errors.Is(err, apiclient.ErrRawMessageTooLarge):
		return nil, err
	case err != nil || !found || n == 0:
		// As rawMessage: no stored source, or one the API could not send, is rebuilt.
		return m.rebuilt(uid)
	}
	spool.bodyAt, spool.bodyLen = 0, n
	if !m.utf8 {
		if err := spool.downgrade(); err != nil {
			return nil, err
		}
	}
	keep = true
	return spool, nil
}

// fetchedWhole is Fetch's octets in memory.
func (m *mailbox) fetchedWhole(uid uint32) (goimap.FetchedMessage, error) {
	raw, err := m.Fetch(uid)
	if err != nil {
		return nil, err
	}
	return &spooledMessage{head: raw}, nil
}

// rebuilt is Fetch of a message whose stored source the API did not send.
func (m *mailbox) rebuilt(uid uint32) (goimap.FetchedMessage, error) {
	detail, err := m.api.GetMessage(m.token, uint(uid))
	if err != nil {
		return nil, err
	}
	raw := buildRawMessage(detail.Data)
	if !m.utf8 {
		raw = rawmsg.Downgrade(raw)
	}
	return &spooledMessage{head: []byte(raw)}, nil
}

// diskWriter remembers the spool file's own failure, which is the gateway's, not the API's.
type diskWriter struct {
	w   io.Writer
	err error
}

func (d *diskWriter) Write(p []byte) (int, error) {
	n, err := d.w.Write(p)
	if err != nil {
		d.err = err
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

func (s *spooledMessage) Size() int64 { return int64(len(s.head)) + s.bodyLen }

func (s *spooledMessage) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("imap: negative offset")
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

// downgrade makes the spooled source what Fetch sends a session that has not enabled
// UTF-8, rawmsg.Downgrade of it, holding only its header block. Downgrade rewrites the
// header block alone and keeps the body as it is, so the block it makes of the source's
// header, up to and including the line that ends it, goes ahead of the rest of the file.
// The header ends where Downgrade's own split ends it: at the first CRLF CRLF, or failing
// one the first LF LF; a source with neither is all header and is downgraded whole.
func (s *spooledMessage) downgrade() error {
	end, found, err := s.headerEnd([]byte("\r\n\r\n"))
	if err == nil && !found {
		end, found, err = s.headerEnd([]byte("\n\n"))
	}
	if err != nil {
		return err
	}
	if !found {
		end = s.bodyLen
	}
	header := make([]byte, end)
	if _, err := s.file.ReadAt(header, 0); err != nil && !(err == io.EOF && end == s.bodyLen) {
		return err
	}
	block := rawmsg.Downgrade(string(header))
	if block == string(header) {
		return nil
	}
	s.head = []byte(block)
	s.bodyAt, s.bodyLen = end, s.bodyLen-end
	return nil
}

// headerEnd is the offset just past the first sep in the spooled source.
func (s *spooledMessage) headerEnd(sep []byte) (int64, bool, error) {
	buf := make([]byte, fetchSpoolChunk+len(sep)-1)
	kept := 0
	for at := int64(0); at < s.bodyLen; {
		n, err := s.file.ReadAt(buf[kept:kept+int(min(fetchSpoolChunk, s.bodyLen-at))], at)
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
