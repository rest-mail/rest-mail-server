package queue

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/messagestore"
)

// smtpContent uses the existing inline DATA path or streams an already-prepared
// source. The same writer handles dot-stuffing for both.
type smtpContent struct {
	inline, headers string
	eightBit        bool
	source          *smtpSource
}

func (w *Worker) smtpContent(ctx context.Context, item models.OutboundQueue, host string, supports8Bit bool) (smtpContent, error) {
	if item.SourceRef == nil {
		return smtpContent{inline: item.RawMessage, headers: item.RawMessage,
			eightBit: messageRequires8BitTransport(item.BodyType, item.RawMessage)}, nil
	}
	content := smtpContent{headers: item.SourceHeaders, eightBit: messageRequires8BitTransport(item.BodyType, item.SourceHeaders)}
	source, err := w.openSMTPSource(ctx, item)
	if err != nil {
		return smtpContent{}, err
	}
	if source.size <= dataBlockSize {
		defer source.close()
		// Preserve MIME-aware conversion for small, non-canonical sources
		// using the same bounded buffer as the streaming DATA path.
		block := make([]byte, dataBlockSize+1)
		n, err := source.read(block)
		if err != io.EOF {
			if err == nil {
				err = messagestore.ErrCorruptSource
			}
			return smtpContent{}, err
		}
		content.inline, err = forData(string(block[:n]), host)
		if err != nil {
			return smtpContent{}, err
		}
		content.headers = content.inline
		content.eightBit = messageRequires8BitTransport(item.BodyType, content.inline)
		return content, nil
	}
	if !supports8Bit && !content.eightBit {
		// A 7-bit peer requires a decision before MAIL. Scan the exact bytes
		// with bounded reads and reopen if the message can go over this hop.
		eightBit, err := source.scan8Bit()
		source.close()
		if err != nil {
			return smtpContent{}, err
		}
		content.eightBit = eightBit
		if eightBit {
			return content, nil
		}
		source, err = w.openSMTPSource(ctx, item)
		if err != nil {
			return smtpContent{}, err
		}
	}
	content.source = source
	source.allowBareLF = canNormalizeSourceLF(item.SourceHeaders)
	return content, nil
}

// Let the dot writer normalize LF for plain identity-encoded text. Other
// large sources are prepared with CRLF before signing and publishing.
func canNormalizeSourceLF(headers string) bool {
	mediaType := "text/plain"
	encoding := ""
	for field := range rmail.HeadFields(headers) {
		switch {
		case field.Is("Content-Type"):
			parsed, _, err := mime.ParseMediaType(field.Value())
			if err != nil {
				return false
			}
			mediaType = parsed
		case field.Is("Content-Transfer-Encoding"):
			encoding = strings.ToLower(strings.TrimSpace(field.Value()))
		}
	}
	return strings.HasPrefix(mediaType, "text/") && (encoding == "" || encoding == "7bit" || encoding == "8bit")
}

func (c smtpContent) close() {
	if c.source != nil {
		c.source.close()
	}
}

type smtpSource struct {
	ctx         context.Context
	cancel      context.CancelFunc
	raw         io.ReadCloser
	size        int64
	readOctets  int64
	timeout     time.Duration
	allowBareLF bool
}

func (w *Worker) openSMTPSource(ctx context.Context, item models.OutboundQueue) (*smtpSource, error) {
	ctx, cancel := context.WithCancel(ctx)
	timeout := w.timeouts().DataBlock
	timer := time.AfterFunc(timeout, cancel)
	raw, size, err := w.openSource(ctx, item)
	if !timer.Stop() {
		err = context.DeadlineExceeded
	}
	if err != nil {
		cancel()
		if raw != nil {
			_ = raw.Close()
		}
		return nil, err
	}
	return &smtpSource{ctx: ctx, cancel: cancel, raw: raw, size: size, timeout: timeout}, nil
}

func (s *smtpSource) close() {
	s.cancel()
	_ = s.raw.Close()
}

// Each source read has a block deadline too. Cancelling the open context
// interrupts a stalled object-store request without a concurrent body Close.
func (s *smtpSource) read(p []byte) (int, error) {
	timer := time.AfterFunc(s.timeout, s.cancel)
	n, err := io.ReadFull(s.raw, p)
	if !timer.Stop() {
		return 0, context.DeadlineExceeded
	}
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	s.readOctets += int64(n)
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	if s.readOctets > s.size || err == io.EOF && s.readOctets != s.size {
		return 0, messagestore.ErrCorruptSource
	}
	return n, err
}

func (s *smtpSource) scan8Bit() (bool, error) {
	block := make([]byte, dataBlockSize)
	for {
		n, err := s.read(block)
		if err != nil && err != io.EOF {
			return false, err
		}
		for _, b := range block[:n] {
			if b >= 0x80 {
				return true, nil
			}
		}
		if err == io.EOF {
			return false, nil
		}
	}
}

// write streams CRLF sources and safe bare LF text. The DATA writer performs
// LF normalization and dot-stuffing once the block has been checked.
func (s *smtpSource) write(w io.Writer, flush func() error, conn net.Conn, host string) error {
	block := make([]byte, dataBlockSize)
	pendingCR := false
	lineLength := 0
	var bareLF, nul bool
	for {
		armStep(conn, s.timeout)
		n, err := s.read(block)
		if err != nil && err != io.EOF {
			return err
		}
		data := block[:n]
		nul = nul || bytes.IndexByte(data, 0) >= 0
		for len(data) != 0 {
			if pendingCR && data[0] != '\n' {
				return errDataCannotCarry(host)
			}
			pos := bytes.IndexAny(data, "\r\n")
			if pos < 0 {
				lineLength += len(data)
				break
			}
			lineLength += pos
			switch data[pos] {
			case '\r':
				pendingCR = true
			case '\n':
				if !pendingCR && (!s.allowBareLF || lineLength == 999) {
					return errDataCannotCarry(host)
				}
				bareLF = bareLF || !pendingCR
				pendingCR = false
				lineLength = 0
			}
			data = data[pos+1:]
		}
		if bareLF && nul {
			return errDataCannotCarry(host)
		}
		if written, err := w.Write(block[:n]); err != nil {
			return err
		} else if written != n {
			return io.ErrShortWrite
		}
		if err == io.EOF {
			break
		}
	}
	if pendingCR {
		return errDataCannotCarry(host)
	}
	return flush()
}

func writeSMTPContent(client *smtp.Client, conn net.Conn, data io.Writer, timeouts config.OutboundSMTPTimeouts, host string, content smtpContent) error {
	var err error
	if content.source != nil {
		err = content.source.write(data, client.Text.W.Flush, conn, host)
	} else {
		err = writeDataBlocks(data, client.Text.W.Flush, content.inline, func() { armStep(conn, timeouts.DataBlock) })
	}
	if err != nil {
		// Closing the DATA writer emits the terminator. Close the underlying
		// connection instead, so the peer cannot accept the partial message.
		_ = conn.Close()
		return fmt.Errorf("write message to %s: %w", host, err)
	}
	return nil
}
