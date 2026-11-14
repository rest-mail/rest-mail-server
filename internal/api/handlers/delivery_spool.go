package handlers

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// A delivery's request and the message in it go to files, and are read from there (#1437), as an
// import's do (message_import_spool.go). What can refuse a delivery without holding the message
// does so, and most of it without reading more than the headers. Past that the pipeline and the
// row need it whole, so it is read once, and nothing else keeps a copy of its own.
//
// Some forms of request are still held whole, within the route's cap, because only the binder can
// answer them and it reads from memory: raw_message or a large text field sent more than once, and
// raw_message as an array of numbers. So are the other fields, which no client sends at any size.

const (
	deliveryBodyText = "body_text"
	deliveryBodyHTML = "body_html"
)

// deliverySpool is a delivery's message and large text fields, in files.
type deliverySpool struct {
	raw   *importSource
	texts map[string]*importSource
	// whole says the binder read the whole request, so the bound text fields are the real ones.
	whole bool
	// section is the header section once it's been read, and sectionSize how long it is in the file,
	// which is more than section when a field was removed from it.
	section     string
	sectionSize int64
	sectionRead bool
	// parsed is the section as readHeaders parsed it, nil if it didn't.
	parsed *rmime.HeaderSection
}

// spoolDelivery reads the request's body to files and binds dst, the route's request type, with
// a stand-in for what is in them. boundRaw is raw_message as bound, for a form of it only the
// binder can read. Every file is gone if it fails, a panic included; otherwise they are the
// caller's to remove.
func spoolDelivery(r *http.Request, dst any, spec spooledBodySpec, boundRaw func() []byte) (*deliverySpool, error) {
	raw, err := newSpooledText()
	if err != nil {
		return nil, err
	}
	spec.source = raw
	spec.texts = map[string]*importSource{deliveryBodyText: nil, deliveryBodyHTML: nil}
	spool := &deliverySpool{raw: raw, texts: spec.texts}
	bound := false
	defer func() {
		if !bound {
			spool.remove()
		}
	}()

	scanned, err := readSpooledBody(r, &spec)
	if err != nil {
		return nil, err
	}
	if err := endpoint.BindRequest(r, dst, scanned.standIn); err != nil {
		return nil, err
	}
	spool.whole = scanned.bindWhole
	if !scanned.streamed {
		if err := raw.replace(boundRaw()); err != nil {
			return nil, err
		}
	}
	bound = true
	return spool, nil
}

func (s *deliverySpool) remove() {
	s.raw.remove()
	for _, text := range s.texts {
		if text != nil {
			text.remove()
		}
	}
}

// text is a text field: what the binder read, or its file, which isn't needed after this.
func (s *deliverySpool) text(name, bound string) (string, error) {
	field := s.texts[name]
	if s.whole || field == nil {
		return bound, nil
	}
	defer field.remove()
	return field.text()
}

// message reads the whole message, which is what the pipeline and the row take, without the
// Authentication-Results claiming to be this server's (RFC 8601 §5). A caller holds the answer:
// the file is gone after this.
func (s *deliverySpool) message() (string, error) {
	defer s.raw.remove()
	whole, err := s.raw.text()
	if err != nil {
		return "", err
	}
	return rmime.StripAuthResults(whole, pipeline.AuthServID), nil
}

// headerSection reads the message up to and including the empty line that ends its header section,
// without the Authentication-Results claiming to be this server's, for what can be decided from
// that alone. It is read once, however often it's asked for.
func (s *deliverySpool) headerSection() (string, error) {
	if s.sectionRead {
		return s.section, nil
	}
	file, err := s.raw.reader()
	if err != nil {
		return "", err
	}
	size, err := headerSectionSize(file)
	if err != nil {
		return "", err
	}
	file, err = s.raw.reader()
	if err != nil {
		return "", err
	}
	var section strings.Builder
	section.Grow(int(size))
	if _, err := io.CopyN(&section, file, size); err != nil {
		return "", err
	}
	s.section, s.sectionSize, s.sectionRead = rmime.StripAuthResults(section.String(), pipeline.AuthServID), size, true
	return s.section, nil
}

// headerSectionSize passes over a header section and answers how long it is, with the empty line
// that ends it. Knowing that first is what lets a sender's long section cost its own size to hold,
// where growing a buffer as it's read costs several times that.
func headerSectionSize(source io.Reader) (int64, error) {
	reader := bufio.NewReader(source)
	var size int64
	wholeLine := true
	for {
		line, err := reader.ReadSlice('\n')
		size += int64(len(line))
		if errors.Is(err, io.EOF) || (wholeLine && (string(line) == "\n" || string(line) == "\r\n")) {
			return size, nil
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return 0, err
		}
		wholeLine = err == nil
	}
}

// readHeaders parses the header section once. It answers an error wrapping
// rmime.ErrStructureLimit when the message is one the parser will refuse, which it finds from the
// file, keeping none of it. readErr says the section didn't parse, for which there's nothing to check.
func (s *deliverySpool) readHeaders() (parsed *rmime.HeaderSection, parseErr, readErr error) {
	section, err := s.headerSection()
	if err != nil {
		return nil, nil, err
	}
	parsed, parseErr = rmime.ReadHeaderSection(section)
	if parseErr != nil {
		return nil, parseErr, nil
	}
	file, err := s.raw.reader()
	if err != nil {
		return nil, nil, err
	}
	if _, err := file.Seek(s.sectionSize, io.SeekStart); err != nil {
		return nil, nil, err
	}
	if err := parsed.CheckStructure(file, s.raw.size-s.sectionSize); err != nil {
		return nil, nil, err
	}
	s.parsed = parsed
	return parsed, nil, nil
}
