package handlers

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// endlessObject is storage that says it holds size octets and then streams without end,
// as a file written to after it was measured, or an object replaced between its HEAD and
// its GET, would.
type endlessObject struct {
	size   int64
	closed bool
}

func (o *endlessObject) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func (o *endlessObject) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		return offset, nil
	case io.SeekEnd:
		return o.size + offset, nil
	}
	return 0, errors.New("seek from current is not used here")
}

func (o *endlessObject) Close() error { o.closed = true; return nil }

// boundSize is not a multiple of any copy buffer, so a bound that only stops between
// reads shows.
const boundSize = 1<<20 + 7

// A download of an attachment whose storage holds its recorded size is served whole, and
// never past that size however long the stream runs, with little allocated.
func TestAttachmentDownloadIsBoundedToTheRecordedSize(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/attachments/1", nil)
	att := &models.Attachment{ID: 1, SizeBytes: boundSize}
	content, err := boundAttachment(r, att, &endlessObject{size: boundSize}, boundSize)
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	n, err := io.Copy(io.Discard, io.LimitReader(content, 64<<20))
	runtime.ReadMemStats(&after)
	if err != nil || n != boundSize {
		t.Fatalf("read %d octets (%v) from a stream that does not end, want the %d recorded", n, err, boundSize)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > boundSize/4 {
		t.Fatalf("allocated %d octets streaming %d", allocated, boundSize)
	}

	// Through ServeContent, as the route serves it: the length and the body are the size.
	content, _ = boundAttachment(r, att, &endlessObject{size: boundSize}, boundSize)
	w := httptest.NewRecorder()
	http.ServeContent(w, r, "a.bin", att.CreatedAt, content)
	if w.Code != http.StatusOK || w.Body.Len() != boundSize || w.Header().Get("Content-Length") != "1048583" ||
		!bytes.Equal(w.Body.Bytes(), bytes.Repeat([]byte("a"), boundSize)) {
		t.Fatalf("served %d %d octets, Content-Length %q", w.Code, w.Body.Len(), w.Header().Get("Content-Length"))
	}
}

// Storage that says it holds one octet more than the attachment's row records is refused,
// and closed, before anything is read.
func TestAttachmentStorageLargerThanRecordedIsRefused(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/attachments/1", nil)
	object := &endlessObject{size: boundSize + 1}
	content, err := boundAttachment(r, &models.Attachment{ID: 1, SizeBytes: boundSize}, object, boundSize+1)
	if !errors.Is(err, errAttachmentStorageLarger) || content != nil || !object.closed {
		t.Fatalf("got %v, %v, closed %v; want the refusal and the object closed", content, err, object.closed)
	}
}
