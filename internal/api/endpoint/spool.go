package endpoint

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"reflect"
	"unicode"
)

// Spool streams src to a temporary file. The answer is the file positioned at its start, and
// a function that closes and removes it. When it fails, no file remains.
func Spool(src io.Reader) (*os.File, func(), error) {
	file, err := os.CreateTemp("", "restmail-upload-*")
	if err != nil {
		return nil, nil, err
	}
	done := func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	if _, err := io.Copy(file, src); err != nil {
		done()
		return nil, nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		done()
		return nil, nil, err
	}
	return file, done, nil
}

// SpoolBody streams a request's body to a temporary file, so a handler can read an
// upload of any permitted size without holding it in memory. The answer is the file
// positioned at its start, and a function that closes and removes it. The refusals are
// a bound body's: 413 past the size cap, 400 for one that can't be read or that isn't in
// a media type the route takes. A route that takes several media types passes an empty
// media, reads the file, and holds the type to its forms with CheckSpooled. The server's
// own disk failing is not a refusal: it is the error it was.
func SpoolBody(r *http.Request, media string) (*os.File, func(), error) {
	if declared := r.Header.Get("Content-Type"); declared != "" && media != "" {
		parsed, _, err := mime.ParseMediaType(declared)
		if err != nil || parsed != media {
			return nil, nil, Refuse(http.StatusBadRequest, "bad_request", "Unsupported media type: "+declared+"; the body is "+media)
		}
	}
	src := io.Reader(http.NoBody)
	if r.Body != nil {
		src = r.Body
	}
	file, done, err := Spool(src)
	if err == nil {
		return file, done, nil
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, nil, Refuse(http.StatusRequestEntityTooLarge, "request_too_large", "Request body too large")
	}
	var disk *fs.PathError
	if errors.As(err, &disk) {
		return nil, nil, err
	}
	return nil, nil, Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
}

// CheckSpooled holds a spooled body to what a bound body is held to before its content is
// read, with the same refusals, in the same order: a body with content must be in a media
// type one of request's forms takes, and a request with several forms must have a body that
// is more than whitespace. request points at the type declaring the forms. The file is left
// at its start.
func CheckSpooled(r *http.Request, request any, file *os.File) error {
	t := reflect.TypeOf(request).Elem()
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if size > 0 {
		if err := checkBodyMedia(r, t); err != nil {
			return err
		}
	}
	blank, err := onlyWhitespace(file)
	if err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if !blank {
		return nil
	}
	// Whitespace reads as no body at all, so the binder can answer for it without the file.
	bare := r.WithContext(r.Context())
	bare.Body = http.NoBody
	return bind(bare, request, requestShape(t))
}

// onlyWhitespace says whether src holds nothing but what bytes.TrimSpace would remove,
// reading no further than the first thing that isn't.
func onlyWhitespace(src io.Reader) (bool, error) {
	buffered := bufio.NewReader(src)
	for {
		char, _, err := buffered.ReadRune()
		if errors.Is(err, io.EOF) {
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
