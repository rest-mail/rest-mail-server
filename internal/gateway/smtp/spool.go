package smtp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"unicode"

	gosmtp "github.com/rest-mail/go-smtp/pkg/smtp"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// spoolFilePattern names the temporary files a message passes through, in the system's
// temporary directory.
const spoolFilePattern = "restmail-smtp-*"

// spoolFile is a temporary file holding a message, or a part of one. A nil spoolFile is an empty
// one, so a message with no HTML body needs no file for it.
type spoolFile struct {
	file *os.File
	size int64
}

// spool streams src to a new temporary file. When it fails, no file remains.
func spool(src io.Reader) (*spoolFile, error) {
	file, err := os.CreateTemp("", spoolFilePattern)
	if err != nil {
		return nil, err
	}
	spooled := &spoolFile{file: file}
	if spooled.size, err = io.Copy(file, src); err != nil {
		spooled.remove()
		return nil, err
	}
	return spooled, nil
}

// remove closes the file and deletes it.
func (f *spoolFile) remove() {
	if f == nil {
		return
	}
	_ = f.file.Close()
	_ = os.Remove(f.file.Name())
}

// reader answers a new reader at the start of the file. Readers do not share a position.
func (f *spoolFile) reader() io.Reader {
	if f == nil {
		return strings.NewReader("")
	}
	return io.NewSectionReader(f.file, 0, f.size)
}

// whole reads the file into memory, for the steps listed on messageSource.whole.
func (f *spoolFile) whole() (string, error) {
	var text strings.Builder
	if f != nil {
		text.Grow(int(f.size))
	}
	_, err := io.Copy(&text, f.reader())
	return text.String(), err
}

// messageSource is a message as it goes on from this server: the file, with the header field
// this server stamps in front of it.
type messageSource struct {
	head string
	body *spoolFile
}

func (m messageSource) open() io.Reader {
	return io.MultiReader(strings.NewReader(m.head), m.body.reader())
}

// whole reads the message into memory. Only what stores or signs the message whole calls it: the
// outbound queue row, the Sent copy of a submission, a relayed bounce, and DKIM signing, because
// the column and the signer's API each take the whole message.
func (m messageSource) whole() (string, error) {
	var text strings.Builder
	if m.body != nil {
		text.Grow(len(m.head) + int(m.body.size))
	}
	_, err := io.Copy(&text, m.open())
	return text.String(), err
}

// parsedMessage is what a delivery needs from a message. Its text bodies stay on disk.
type parsedMessage struct {
	subject, messageID, senderName, inReplyTo, references, fromAddr string
	toList, ccList                                                  []string
	text, html                                                      *spoolFile
}

// remove deletes the files holding the text bodies.
func (p *parsedMessage) remove() {
	p.text.remove()
	p.html.remove()
}

// bodyTexts keeps the first text/plain and the first text/html part with any content in them,
// as rmime.TextAndHTML picks them from a parsed message.
type bodyTexts struct {
	text, html *spoolFile
	failed     error
}

func (b *bodyTexts) take(mediaType string, part io.Reader) error {
	slot := &b.text
	if mediaType == "text/html" {
		slot = &b.html
	}
	if *slot != nil {
		_, err := io.Copy(io.Discard, part)
		return err
	}
	reading := &sourceRead{source: part}
	// These go in the API's text fields, and a message may hold any octets. Its source keeps them.
	file, err := spool(models.NewReadableReader(reading))
	if err != nil {
		if reading.err == nil {
			b.failed = err
		}
		return err
	}
	if file.size == 0 {
		file.remove()
		return nil
	}
	*slot = file
	return nil
}

// sourceRead remembers that reading its source failed, as distinct from what it was read into
// failing.
type sourceRead struct {
	source io.Reader
	err    error
}

func (r *sourceRead) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

// discardPart reads an attachment and keeps none of it: the gateway needs only the message's
// headers and text from its parse, and the attachment goes on in the raw message.
func discardPart(_ context.Context, part io.Reader) (rmime.StoredPart, error) {
	_, err := io.Copy(io.Discard, part)
	return rmime.StoredPart{}, err
}

// parseSpooled extracts the fields a delivery needs from a message on disk. fromAddr is the bare
// address of the From header, used on the authenticated submission path to bind the header From
// to an identity the account is authorized to send as.
//
// The parsing itself is delegated to internal/mime, which wraps go-message: RFC 2047 encoded-words,
// charset conversion, transfer-encoding decoding and nested multipart structures all come from
// there, so the same message yields the same fields over SMTP as over REST (issue #298).
//
// A message nested deeper or holding more parts than the MIME limits allow is an error wrapping
// rmime.ErrStructureLimit, and is refused. Any other error is this server failing to read or
// write its own disk. The caller removes what it answers.
func parseSpooled(message *spoolFile) (*parsedMessage, error) {
	parsed := &parsedMessage{}
	// An empty or whitespace-only message has nothing to parse. Handing it over would come back
	// as a default text/plain body holding that whitespace.
	blank, err := onlyWhitespace(message.reader())
	if err != nil {
		return nil, err
	}
	if blank {
		return parsed, nil
	}

	terminated, err := hasBlankLine(message)
	if err != nil {
		return nil, err
	}
	// A message that is nothing but headers has no terminator, and the parser needs one to treat
	// the whole input as a header block rather than a truncated message.
	source := &sourceRead{source: message.reader()}
	var input io.Reader = source
	if !terminated {
		input = io.MultiReader(source, strings.NewReader("\r\n\r\n"))
	}
	texts := &bodyTexts{}
	email, err := rmime.ParseStream(context.Background(), input, rmime.StreamOptions{
		Store:    discardPart,
		MaxText:  1, // Every part worth keeping goes to TextPart.
		TextPart: texts.take,
	})
	parsed.text, parsed.html = texts.text, texts.html
	switch {
	case errors.Is(err, rmime.ErrStructureLimit):
		parsed.remove()
		return nil, err
	case source.err != nil:
		parsed.remove()
		return nil, source.err
	case texts.failed != nil:
		parsed.remove()
		return nil, texts.failed
	case err != nil || email == nil:
		// Nothing parseable: keep whatever body text can be recovered rather than delivering an
		// empty message. Headers stay empty, which is what an unparseable message can honestly
		// report.
		parsed.remove()
		text, err := recoverBody(message)
		if err != nil {
			return nil, err
		}
		return &parsedMessage{text: text}, nil
	}
	parsed.applyHeaders(email.Headers)
	return parsed, nil
}

func (p *parsedMessage) applyHeaders(h pipeline.Headers) {
	p.subject = models.ReadableText(h.Subject)
	// The stored form is the bare id; the angle brackets are re-added wherever the message is
	// rendered back into a header.
	p.messageID = models.ReadableText(strings.Trim(h.MessageID, "<>"))
	p.inReplyTo = models.ReadableText(strings.Trim(h.InReplyTo, "<>"))
	// References stays the whitespace-separated list with brackets intact, matching the
	// historical output contract.
	p.references = models.ReadableText(strings.Join(h.References, " "))
	if from, ok := h.From.First(); ok {
		p.senderName = models.ReadableText(from.Name)
		p.fromAddr = models.ReadableText(from.Address)
	}
	p.toList = readableList(bareAddresses(h.To.All()))
	p.ccList = readableList(bareAddresses(h.Cc.All()))
}

func readableList(list []string) []string {
	for i := range list {
		list[i] = models.ReadableText(list[i])
	}
	return list
}

// onlyWhitespace says whether src holds nothing but what bytes.TrimSpace would remove, reading no
// further than the first thing that isn't.
func onlyWhitespace(src io.Reader) (bool, error) {
	reader := bufio.NewReader(src)
	for {
		char, _, err := reader.ReadRune()
		if err == io.EOF {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if !unicode.IsSpace(char) {
			return false, nil
		}
	}
}

// hasBlankLine says whether the message has a line ending followed by another anywhere in it,
// as a header block has at its end.
func hasBlankLine(message *spoolFile) (bool, error) {
	at, err := blankLineAt(message)
	return at >= 0, err
}

// blankLineAt answers where the first blank line's separator ends, and -1 without one. A CRLF
// separator anywhere comes before an LF one, as it did for the bytes held in memory.
func blankLineAt(message *spoolFile) (int64, error) {
	for _, separator := range []string{"\r\n\r\n", "\n\n"} {
		at, err := indexInFile(message, []byte(separator))
		if err != nil {
			return -1, err
		}
		if at >= 0 {
			return at + int64(len(separator)), nil
		}
	}
	return -1, nil
}

// indexInFile answers where separator first appears in the file, or -1, holding one chunk of it.
func indexInFile(message *spoolFile, separator []byte) (int64, error) {
	const chunk = 64 << 10
	buffer := make([]byte, chunk+len(separator)-1)
	var offset int64
	kept := 0
	reader := message.reader()
	for {
		n, err := reader.Read(buffer[kept:])
		have := buffer[:kept+n]
		if at := bytes.Index(have, separator); at >= 0 {
			return offset + int64(at), nil
		}
		if err == io.EOF {
			return -1, nil
		}
		if err != nil {
			return -1, err
		}
		if len(have) >= len(separator) {
			next := len(have) - (len(separator) - 1)
			offset += int64(next)
			kept = copy(buffer, have[next:])
		} else {
			kept = len(have)
		}
	}
}

// recoverBody spools the body of a message the parser could not read: whatever follows the first
// blank line, or nothing when there is no body at all.
func recoverBody(message *spoolFile) (*spoolFile, error) {
	start, err := blankLineAt(message)
	if err != nil || start < 0 || start >= message.size {
		return nil, err
	}
	return spool(models.NewReadableReader(io.NewSectionReader(message.file, start, message.size-start)))
}

// messageDiskFailure is the answer when this server cannot read or write its own copy of a
// message: nothing is wrong with the message, so the client tries again later.
func messageDiskFailure() error {
	return &gosmtp.SMTPError{
		Code:         451,
		EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
		Message:      "Error reading message data",
	}
}
