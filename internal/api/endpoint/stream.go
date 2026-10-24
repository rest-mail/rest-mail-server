package endpoint

import (
	"io"
	"net/http"
)

// Stream is an answer written as it happens, a Server-Sent Events stream: its headers and the
// operation's status go out at once, then Send writes the body, calling flush to put what it
// has written on the wire, until it returns. The operation names the media type it is
// described with (Operation.MediaType), described as text; ContentType is the header written
// and Header any others.
//
// Done, when set, is called once the answer is over, whether Send ran or not: a stream holds
// what it streams from (a subscription, a slot) from the handler's return until then. A
// response that cannot be flushed is a 500, never a stream that would arrive all at once.
type Stream struct {
	ContentType string
	Header      http.Header
	Send        func(w io.Writer, flush func())
	Done        func()
}

func (s Stream) stream(w http.ResponseWriter, status int) {
	if s.Done != nil {
		defer s.Done()
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, Refuse(http.StatusInternalServerError, "internal_error", "Streaming not supported"))
		return
	}
	for name, values := range s.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.Header().Set("Content-Type", s.ContentType)
	w.WriteHeader(status)
	flusher.Flush()
	s.Send(w, flusher.Flush)
}
