package endpoint

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
)

// SpoolBody streams a request's body to a temporary file, so a handler can read an
// upload of any permitted size without holding it in memory. The answer is the file
// positioned at its start, and a function that closes and removes it. The refusals are
// a bound body's: 413 past the size cap, 400 for one that can't be read or that isn't in
// a media type the route takes.
func SpoolBody(r *http.Request, media string) (*os.File, func(), error) {
	if declared := r.Header.Get("Content-Type"); declared != "" {
		parsed, _, err := mime.ParseMediaType(declared)
		if err != nil || parsed != media {
			return nil, nil, Refuse(http.StatusBadRequest, "bad_request", "Unsupported media type: "+declared+"; the body is "+media)
		}
	}
	file, err := os.CreateTemp("", "restmail-upload-*")
	if err != nil {
		return nil, nil, err
	}
	done := func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	if r.Body != nil {
		if _, err := io.Copy(file, r.Body); err != nil {
			done()
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return nil, nil, Refuse(http.StatusRequestEntityTooLarge, "request_too_large", "Request body too large")
			}
			return nil, nil, Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		done()
		return nil, nil, err
	}
	return file, done, nil
}
