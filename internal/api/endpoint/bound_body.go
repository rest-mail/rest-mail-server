package endpoint

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"reflect"
)

// boundBodyInMemory is the most of a bound body read in memory before the rest goes to a file
// (#1437). Most bodies are far smaller and never touch the disk.
const boundBodyInMemory = 64 << 10

// readBoundBody reads a request's body for the binder, which needs it whole, holding no more
// of it than that and answering as reading it whole did.
//
//   - A body past the size cap is refused as it arrives: past the first part, the body goes
//     to a file, so a body over the cap never sits in memory.
//   - A body in a media type the route doesn't take is refused once its first part shows it
//     isn't empty, and none of the rest is kept. Its length known, the rest isn't read: it
//     can't pass the cap, which refused it before the handler if it declared more. Sent in
//     chunks, the rest is read and dropped, so a body past the cap is still the 413.
//   - A body the binder will read is read from its file into a buffer of its size, once,
//     where reading it whole grew a buffer to it a step at a time.
//
// An empty body is nil. The server's own disk failing is not a refusal: it is the error it was.
func readBoundBody(r *http.Request, t reflect.Type) ([]byte, error) {
	head, err := io.ReadAll(io.LimitReader(r.Body, boundBodyInMemory+1))
	if err != nil {
		return nil, boundBodyReadFailure(err)
	}
	if len(head) == 0 {
		return nil, nil
	}
	if err := checkBodyMedia(r, t); err != nil {
		if r.ContentLength < 0 {
			if _, drained := io.Copy(io.Discard, r.Body); drained != nil {
				return nil, boundBodyReadFailure(drained)
			}
		}
		return nil, err
	}
	if len(head) <= boundBodyInMemory {
		return head, nil
	}

	file, done, err := Spool(io.MultiReader(bytes.NewReader(head), r.Body))
	if err != nil {
		return nil, boundBodyReadFailure(err)
	}
	defer done()
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(file, body); err != nil {
		return nil, err
	}
	return body, nil
}

// boundBodyReadFailure is a bound body's refusal for what failed reading it: 413 past the cap,
// 400 for anything else wrong with the body, and the server's own disk failing as it failed.
func boundBodyReadFailure(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return Refuse(http.StatusRequestEntityTooLarge, "request_too_large", "Request body too large")
	}
	var disk *fs.PathError
	if errors.As(err, &disk) {
		return err
	}
	return Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
}
