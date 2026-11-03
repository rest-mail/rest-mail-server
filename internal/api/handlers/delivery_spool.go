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
// that alone.
func (s *deliverySpool) headerSection() (string, error) {
	section, _, err := s.splitHeaders()
	return section, err
}

// checkStructure answers an error wrapping rmime.ErrStructureLimit when the message is one the
// parser will refuse. It reads what the parser will be given, from the file, and keeps none of it.
func (s *deliverySpool) checkStructure() error {
	section, rest, err := s.splitHeaders()
	if err != nil {
		return err
	}
	return rmime.CheckStructure(io.MultiReader(strings.NewReader(section), rest))
}

// splitHeaders answers the header section, and a reader of what comes after it.
func (s *deliverySpool) splitHeaders() (string, io.Reader, error) {
	file, err := s.raw.reader()
	if err != nil {
		return "", nil, err
	}
	reader := bufio.NewReader(file)
	var section strings.Builder
	for {
		line, err := reader.ReadString('\n')
		section.WriteString(line)
		if errors.Is(err, io.EOF) || line == "\n" || line == "\r\n" {
			return rmime.StripAuthResults(section.String(), pipeline.AuthServID), reader, nil
		}
		if err != nil {
			return "", nil, err
		}
	}
}
