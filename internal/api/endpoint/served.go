package endpoint

import (
	"io"
	"net/http"
	"time"
)

// Served is stored content answered with http.ServeContent: streamed rather than held in
// memory, with Range and conditional requests answered as ServeContent answers them (206,
// 304, 412, 416). The operation names the media type it is described with
// (Operation.MediaType); ContentType is the header written. Header holds any other headers,
// Name and Modified are what ServeContent is given, and Content is closed once it is written
// when it is an io.Closer.
type Served struct {
	ContentType string
	Header      http.Header
	Name        string
	Modified    time.Time
	Content     io.ReadSeeker
}

func (s Served) serve(w http.ResponseWriter, r *http.Request) {
	if closer, ok := s.Content.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}
	for name, values := range s.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.Header().Set("Content-Type", s.ContentType)
	http.ServeContent(w, r, s.Name, s.Modified, s.Content)
}
