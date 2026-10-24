package endpoint

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// closingReader is content that remembers being closed.
type closingReader struct {
	*strings.Reader
	closed bool
}

func (c *closingReader) Close() error {
	c.closed = true
	return nil
}

// Served content is streamed with http.ServeContent: a whole answer, a range, and a
// conditional request each get ServeContent's answer, with the content's own headers, and
// the content is closed once written. It is described as bytes in its media type.
func TestServedIsServedWithRangesAndConditionalRequests(t *testing.T) {
	modified := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	var opened []*closingReader
	r := chi.NewRouter()
	reg := NewRegistry()
	Register(Routes{Router: r, Registry: reg}, Operation{Method: http.MethodGet, Path: "/file", OperationID: "file",
		MediaType: "application/octet-stream"}, func(*http.Request, struct{}) (Served, error) {
		content := &closingReader{Reader: strings.NewReader("0123456789")}
		opened = append(opened, content)
		return Served{ContentType: "application/octet-stream", Name: "notes.txt", Modified: modified,
			Header:  http.Header{"Content-Disposition": {`attachment; filename="notes.txt"`}},
			Content: content}, nil
	})
	get := func(header ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/file", nil)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}

	whole := get()
	if whole.Code != http.StatusOK || whole.Body.String() != "0123456789" ||
		whole.Header().Get("Content-Type") != "application/octet-stream" ||
		whole.Header().Get("Content-Disposition") != `attachment; filename="notes.txt"` ||
		whole.Header().Get("Content-Length") != "10" || whole.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("whole = %d %q %v", whole.Code, whole.Body, whole.Header())
	}
	part := get("Range", "bytes=2-4")
	if part.Code != http.StatusPartialContent || part.Body.String() != "234" || part.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Fatalf("range = %d %q %v", part.Code, part.Body, part.Header())
	}
	if unchanged := get("If-Modified-Since", modified.Format(http.TimeFormat)); unchanged.Code != http.StatusNotModified {
		t.Fatalf("conditional = %d", unchanged.Code)
	}
	for i, content := range opened {
		if !content.closed {
			t.Errorf("content %d was not closed", i)
		}
	}

	ok := reg.Endpoints()[0].Responses[0]
	if ok.Status != http.StatusOK || ok.MediaType != "application/octet-stream" || ok.Type != reflect.TypeOf([]byte(nil)) {
		t.Fatalf("described as %+v", ok)
	}
}

// Like a Document, Served content needs the operation to name its media type.
func TestServedNeedsAMediaType(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registered without complaint")
		}
	}()
	Register(Routes{Router: chi.NewRouter(), Registry: NewRegistry()}, Operation{Method: http.MethodGet, Path: "/a", OperationID: "a"},
		func(*http.Request, struct{}) (Served, error) { return Served{}, nil })
}
