package endpoint

import "net/http"

// Document is an answer in a media type of its own, written byte for byte with the
// operation's status: a page, a YAML document, a metrics exposition. The operation names the
// media type it is described with (Operation.MediaType); ContentType is the header written,
// which may add parameters such as a charset. Header holds any other headers to write.
type Document struct {
	ContentType string
	Header      http.Header
	Body        []byte
}

func (d Document) write(w http.ResponseWriter, status int) {
	for name, values := range d.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.Header().Set("Content-Type", d.ContentType)
	w.WriteHeader(status)
	_, _ = w.Write(d.Body)
}
