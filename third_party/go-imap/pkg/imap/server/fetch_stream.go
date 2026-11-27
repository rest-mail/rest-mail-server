package server

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/message"
)

// fetchStreamChunk is how much of a streamed message is read at once to scan it.
const fetchStreamChunk = 32 << 10

// errFetchStreamBroken is a literal that could not be sent whole after its length
// was: the connection is closed, because the client would read the next response
// as the rest of it.
var errFetchStreamBroken = errors.New("imap: streamed literal ended early")

// fetchStream is a message a [imap.StreamFetcher] opened for one FETCH response,
// with what the scan of it found.
type fetchStream struct {
	msg  imap.FetchedMessage
	size int64
	scan *message.Scan
	buf  []byte
}

// streamSpans is whether a response to tokens can be sent from a stream: the
// selected Mailbox can open one, at least one item is a span of the whole
// message, and every other item is one fetchResponse answers without loading
// the message. When it is false the response loads the message as before.
func (s *Session) streamSpans(tokens []string) bool {
	if _, ok := s.mailbox.(imap.StreamFetcher); !ok {
		return false
	}
	spans := 0
	for _, item := range tokens {
		switch up := strings.ToUpper(item); up {
		case "FLAGS", "UID", "RFC822.SIZE", "INTERNALDATE":
		case "ENVELOPE", "BODYSTRUCTURE", "BODY":
			return false
		default:
			if _, ok := message.ParseSpanItem(item); ok {
				spans++
				continue
			}
			if strings.HasPrefix(up, "BODY[") || strings.HasPrefix(up, "BODY.PEEK[") ||
				strings.HasPrefix(up, "BINARY") || strings.HasPrefix(up, "RFC822") {
				return false
			}
			// Any other item fetchResponse ignores, and so does a span response.
		}
	}
	return spans > 0
}

// openFetchStream opens uid's message and scans it once, in pieces, so the
// response knows its header block's end and whether it is 7-bit before sending.
// The caller closes it.
func (s *Session) openFetchStream(uid uint32) (*fetchStream, error) {
	msg, err := s.mailbox.(imap.StreamFetcher).FetchStream(uid)
	if err != nil {
		return nil, err
	}
	st := &fetchStream{msg: msg, size: msg.Size(), scan: message.NewScan()}
	st.buf = make([]byte, min(max(st.size, 1), fetchStreamChunk))
	if _, err := io.CopyBuffer(st.scan, io.NewSectionReader(msg, 0, st.size), st.buf); err != nil {
		st.close()
		return nil, err
	}
	if st.scan.Size() != st.size {
		st.close()
		return nil, io.ErrUnexpectedEOF
	}
	return st, nil
}

func (st *fetchStream) close() { _ = st.msg.Close() }

// whole is the message read back into memory, for a response the spans cannot
// answer: one allocation of its size.
func (st *fetchStream) whole() (string, bool) {
	var b strings.Builder
	b.Grow(int(st.size))
	n, err := io.CopyBuffer(&b, io.NewSectionReader(st.msg, 0, st.size), st.buf)
	if err != nil || n != st.size {
		return "", false
	}
	return b.String(), true
}

// streamable is whether every span of tokens sends stored octets: always for
// BINARY, and for the others only when FETCH sends the message as stored.
func (st *fetchStream) streamable(tokens []string) bool {
	if !st.scan.EightBit() {
		return true
	}
	for _, item := range tokens {
		if it, ok := message.ParseSpanItem(item); ok && !it.Binary {
			return false
		}
	}
	return true
}

// spanLit is a literal a streamed response copies from the message.
type spanLit struct {
	name         string
	start, count int64
	literal8     bool
}

// streamedFetchResponse is fetchResponse for a response streamSpans planned and
// the scan of st allows: the same items, in the same order, with each literal
// copied from st's run of the message instead of a string.
func (s *Session) streamedFetchResponse(seq int, msg imap.Message, tokens []string, st *fetchStream) error {
	plain := []string{fmt.Sprintf("UID %d", msg.UID), ""}
	marksSeen := false
	var lits []spanLit
	size, headerEnd := st.size, st.scan.HeaderEnd()
	for _, item := range tokens {
		switch up := strings.ToUpper(item); up {
		case "FLAGS", "UID":
		case "RFC822.SIZE":
			plain = append(plain, fmt.Sprintf("RFC822.SIZE %d", msg.Size))
		case "INTERNALDATE":
			plain = append(plain, fmt.Sprintf("INTERNALDATE %q", msg.Date.Format("02-Jan-2006 15:04:05 -0700")))
		default:
			it, ok := message.ParseSpanItem(item)
			if !ok {
				continue
			}
			name, start, count := it.Range(size, headerEnd)
			if it.Size {
				plain = append(plain, fmt.Sprintf("%s %d", name, count))
				continue
			}
			lit := spanLit{name: name, start: start, count: count}
			if it.Binary && st.scan.NUL() {
				nul, err := message.HasNUL(st.msg, start, count, st.buf)
				if err != nil {
					return err
				}
				lit.literal8 = nul
			}
			lits = append(lits, lit)
			if !it.Peek {
				marksSeen = true
			}
		}
	}
	head, err := s.fetchHead(seq, msg, plain, marksSeen)
	if err != nil {
		return err
	}
	if len(lits) == 0 {
		s.writeString(head + ")\r\n")
		return nil
	}
	s.writeString(head)
	for _, lit := range lits {
		eight := ""
		if lit.literal8 {
			eight = "~"
		}
		s.writeString(fmt.Sprintf(" %s %s{%d}\r\n", lit.name, eight, lit.count))
		n, err := io.Copy(s.writer, io.NewSectionReader(st.msg, lit.start, lit.count))
		if err == nil && n != lit.count {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			_ = s.conn.Close()
			return fmt.Errorf("%w: %w", errFetchStreamBroken, err)
		}
		_ = s.writer.Flush()
	}
	s.writeString(")\r\n")
	return nil
}
